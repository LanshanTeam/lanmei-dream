package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
)

// maxReasoningChars 流式生成中推理思考的长度上限（字符）。
// 正文为空而思考超过该值 → 判定模型陷入"只思考不输出"，中止并关思考重试。
const maxReasoningChars = 500

// ChatStream 以流式方式执行对话，将分段结果增量写入 segmentCh。
// 支持流式的客户端走工具调用循环，其余降级为 Chat + StreamSegmenter 分段。
// 返回的 ChatResponse.Content 为完整回复文本（供存储）；segmentCh 由调用方创建并关闭。
//
// 参数：
//   - ctx：流式生成上下文，取消会中止分段投递
//   - req：对话请求；Messages 至少一条
//   - segmentCh：段落通道，函数内按段落边界增量写入
//
// 返回：完整回复（Content 与 toolArgs 供存储/上层消费）；出错时可能已投递部分段落。
func (s *ChatService) ChatStream(ctx context.Context, req *llm.ChatRequest, segmentCh chan<- string) (*llm.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("chat stream: empty messages")
	}
	s.applySamplingDefaults(req)

	queryVec, lastMsgContent, err := s.assembleContext(ctx, req)
	if err != nil {
		return nil, err
	}

	// EinoClient 流式路径（支持工具调用循环）
	if einoClient, isEino := s.client.(llm.EinoCapable); isEino {
		return s.chatStreamWithToolLoop(ctx, req, einoClient, segmentCh, queryVec, lastMsgContent)
	}

	// 降级路径：非流式客户端 → Chat + 逐段投递
	return s.chatStreamFallback(ctx, req, segmentCh, queryVec, lastMsgContent)
}

// chatStreamWithToolLoop 执行带工具调用循环的流式对话，边接收 token 边分段写入 segmentCh。
// 每轮读取首个 chunk 判定轮次类型：带 ToolCalls 即工具轮，带 Content 即文本轮。
//
// 这基于 OpenAI 兼容 API 的行为：工具调用响应的首个 chunk 携带 tool_calls delta 且
// content 为空；纯文本响应的首个 chunk 携带 content delta 且无 tool_calls。
func (s *ChatService) chatStreamWithToolLoop(
	ctx context.Context,
	req *llm.ChatRequest,
	einoClient llm.EinoCapable,
	segmentCh chan<- string,
	queryVec []float32,
	lastMsgContent string,
) (*llm.ChatResponse, error) {
	if s.hasWebSearch() {
		resp, err := s.chatWithSearchTools(ctx, req, einoClient, segmentCh)
		if err == nil {
			s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)
		}
		return resp, err
	}
	// 注入调用者平台身份，工具 handler 通过 tool.CallerFrom 读取
	ctx = s.withCaller(ctx, req)
	chatModel, err := s.getStreamChatModel(einoClient)
	if err != nil {
		return nil, err
	}

	schemaMsgs := llm.ToSchemaMessages(req.Messages)
	segmenter := NewStreamSegmenter()
	totalInput := 0
	totalOutput := 0
	var invokedTools []string
	var lastToolResult string // 最近一次工具结果（LLM 工具轮后无文本时兜底输出）
	// toolArgs 实际执行的工具调用参数（工具名 → 参数 JSON），同名覆盖保留最后一次
	toolArgs := make(map[string]string)
	// disableThinking 本轮流式生成是否关闭推理思考：推理模型可能把输出预算全花在
	// reasoning 上导致正文为空，触发思考超限后置位，下一轮以 thinking=disabled 重试。
	disableThinking := false

roundLoop:
	for round := 0; round < maxToolCallRounds; round++ {
		streamOpts := llm.SamplingOptions(req.Sampling, disableThinking)
		reader, streamErr := chatModel.Stream(ctx, schemaMsgs, streamOpts...)
		if streamErr != nil {
			return nil, fmt.Errorf("chat stream: open stream: %w", streamErr)
		}

		firstChunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			reader.Close()
			s.logger.Warn("chat stream: 首 chunk 即 EOF（LLM 空响应）",
				zap.Int("round", round), zap.Int("msgs", len(schemaMsgs)))
			break // 空响应
		}
		if recvErr != nil {
			reader.Close()
			return nil, fmt.Errorf("chat stream: recv first chunk: %w", recvErr)
		}

		// 轮次判定日志：确认 LLM 实际返回的内容形态（文本 / 工具调用 / 仅 reasoning）
		s.logger.Info("chat stream: 轮次判定",
			zap.Int("round", round),
			zap.Int("tool_calls", len(firstChunk.ToolCalls)),
			zap.Int("content_len", len(firstChunk.Content)),
			zap.Int("reasoning_len", len(firstChunk.ReasoningContent)))

		// 工具轮执行闭包：拼接 assistant 消息 + 执行工具 + 回传结果。
		// 推理模型先流 reasoning，工具调用可能出现在后续 chunk，故"首 chunk 初判"与
		// "文本轮中途检测"到的工具调用都走这里。
		runToolRound := func(chunks []*schema.Message) (bool, error) {
			accumulated, concatErr := schema.ConcatMessages(chunks)
			if concatErr != nil {
				return false, fmt.Errorf("chat stream: concat tool message: %w", concatErr)
			}
			if accumulated.ResponseMeta != nil && accumulated.ResponseMeta.Usage != nil {
				totalInput += accumulated.ResponseMeta.Usage.PromptTokens
				totalOutput += accumulated.ResponseMeta.Usage.CompletionTokens
			}
			// DeepSeek 等实现要求 assistant 消息必须携带 content 字段，而 go-openai 序列化
			// 时空 content 会被 omitempty 省略；工具调用类消息补空格占位避免请求被 400 拒绝。
			if accumulated.Content == "" && len(accumulated.ToolCalls) > 0 {
				accumulated.Content = " "
			}
			schemaMsgs = append(schemaMsgs, accumulated)
			for _, tc := range accumulated.ToolCalls {
				s.logger.Info("chat stream: 工具轮触发",
					zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))
				// 记录工具调用参数（同名覆盖，保留最后一次），供上层读取。
				toolArgs[tc.Function.Name] = tc.Function.Arguments
				result, callErr := s.toolReg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
				if callErr != nil {
					result = fmt.Sprintf("工具调用失败: %v", callErr)
				}
				s.logger.Info("chat stream: 工具结果", zap.String("tool", tc.Function.Name),
					zap.Int("result_len", len(result)), zap.String("result", truncateForLog(result, 120)))
				lastToolResult = result
				schemaMsgs = append(schemaMsgs, &schema.Message{
					Role:       schema.Tool,
					ToolCallID: tc.ID,
					Content:    result,
				})
				invokedTools = append(invokedTools, tc.Function.Name)
			}
			return true, nil
		}

		// 初判：首 chunk 即带工具调用 → 纯工具轮
		if len(firstChunk.ToolCalls) > 0 {
			chunks := []*schema.Message{firstChunk}
			for {
				chunk, err := reader.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					reader.Close()
					return nil, fmt.Errorf("chat stream: recv tool chunk: %w", err)
				}
				chunks = append(chunks, chunk)
			}
			reader.Close()
			if _, err := runToolRound(chunks); err != nil {
				return nil, err
			}
			continue // 下一轮
		}

		// reasoning 型模型可能只返回 reasoning_content 而无 content，
		// 收集 reasoning 作为空响应兜底。
		var reasoningBuf strings.Builder
		if firstChunk.ReasoningContent != "" {
			reasoningBuf.WriteString(firstChunk.ReasoningContent)
		}
		if firstChunk.Content != "" {
			for _, seg := range segmenter.Feed(firstChunk.Content) {
				if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
					reader.Close()
					return nil, sendErr
				}
			}
		}
		if firstChunk.ResponseMeta != nil && firstChunk.ResponseMeta.Usage != nil {
			totalInput += firstChunk.ResponseMeta.Usage.PromptTokens
			totalOutput += firstChunk.ResponseMeta.Usage.CompletionTokens
		}

		// 剩余 chunk：推理模型可能在 reasoning 后才发出工具调用（tool_calls 出现在后续 chunk），
		// 一旦检测到即切换为工具轮，避免把工具调用当纯文本轮处理导致"只想不做"。
		switchedToTool := false
	chunkLoop:
		for {
			chunk, err := reader.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				reader.Close()
				return nil, fmt.Errorf("chat stream: recv text chunk: %w", err)
			}
			if chunk.ReasoningContent != "" {
				reasoningBuf.WriteString(chunk.ReasoningContent)
				// 思考超限实时防护：思考远超阈值且正文仍为空，说明输出预算全耗在
				// reasoning 上（表现为"只思考不输出"），继续等待只会拖垮超时预算，
				// 故立即中止本轮流并在下一轮关闭思考重试；仅未关思考的首轮生效，避免重试轮再次中止。
				if reasoningBuf.Len() > maxReasoningChars && segmenter.FullText() == "" && !disableThinking {
					reader.Close()
					s.logger.Warn("chat stream: 思考超限，中止本轮并关思考重试",
						zap.Int("reasoning_len", reasoningBuf.Len()),
						zap.Int("round", round))
					disableThinking = true
					continue roundLoop
				}
			}
			if len(chunk.ToolCalls) > 0 {
				// 中间文本 chunk 已在前面的循环里发送给用户，但未进入工具消息；
				// 若不补回，下一轮 LLM 上下文缺失这段已输出内容，会重复/矛盾输出。
				if chunk.Content != "" {
					firstChunk.Content += chunk.Content
				}
				chunks := []*schema.Message{firstChunk, chunk}
				for {
					c, err := reader.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						reader.Close()
						return nil, fmt.Errorf("chat stream: recv tool chunk: %w", err)
					}
					chunks = append(chunks, c)
				}
				reader.Close()
				if _, err := runToolRound(chunks); err != nil {
					return nil, err
				}
				switchedToTool = true
				break chunkLoop
			}
			if chunk.Content != "" {
				for _, seg := range segmenter.Feed(chunk.Content) {
					if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
						reader.Close()
						return nil, sendErr
					}
				}
			}
			if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
				totalInput += chunk.ResponseMeta.Usage.PromptTokens
				totalOutput += chunk.ResponseMeta.Usage.CompletionTokens
			}
		}
		// 工具切换分支已在内部 Close，此处避免二次 Close（会 panic: close of closed channel）
		if !switchedToTool {
			reader.Close()
		}
		if switchedToTool {
			continue // 本轮已切换为工具轮，进入下一轮生成
		}

		if last := segmenter.Flush(); last != "" {
			if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
				return nil, sendErr
			}
		}

		// 工具已执行但 LLM 未产出最终文本（部分模型认为工具结果即答案）：
		// 用最近一次工具结果兜底输出，避免"调了工具却无响应"。
		if segmenter.FullText() == "" && lastToolResult != "" {
			for _, seg := range segmenter.Feed(lastToolResult) {
				if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
					return nil, sendErr
				}
			}
			if last := segmenter.Flush(); last != "" {
				if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
					return nil, sendErr
				}
			}
		}
		// reasoning（思考内容）绝不能作为回复输出给用户——它含提示词线索、检索内容与
		// 模型内部推理，泄露即事故；空响应统一由上层（roleplay）关思考重试 / 提示语兜底。

		// 异步存记忆 + 触发压缩。
		s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

		// 流式路径绕过 client.Chat 直连 chatModel，需手动上报用量。
		s.reportUsage(req, totalInput, totalOutput)

		return &llm.ChatResponse{
			Content:       segmenter.FullText(),
			TokensUsed:    totalInput + totalOutput,
			InputTokens:   totalInput,
			OutputTokens:  totalOutput,
			InvolvedTools: invokedTools,
			ToolArgs:      toolArgs,
		}, nil
	}

	// 达到最大工具调用轮次，返回已有内容
	s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

	// 同上：工具已执行但未产出文本时，用最近一次工具结果兜底。
	if segmenter.FullText() == "" && lastToolResult != "" {
		for _, seg := range segmenter.Feed(lastToolResult) {
			if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
				return nil, sendErr
			}
		}
	}

	// 流式路径绕过 client.Chat 直连 chatModel，需手动上报用量。
	s.reportUsage(req, totalInput, totalOutput)

	return &llm.ChatResponse{
		Content:       segmenter.FullText(),
		TokensUsed:    totalInput + totalOutput,
		InputTokens:   totalInput,
		OutputTokens:  totalOutput,
		InvolvedTools: invokedTools,
		ToolArgs:      toolArgs,
	}, nil
}

// getStreamChatModel 获取用于流式对话的 chatModel。
// 若模型支持工具调用且有注册工具，绑定工具后返回；否则返回 base model。
func (s *ChatService) getStreamChatModel(e llm.EinoCapable) (model.BaseChatModel, error) {
	if s.toolReg != nil && len(s.toolReg.ToolInfos()) > 0 && e.SupportsToolCalling() {
		chatModel, err := e.ChatWithTools(s.toolReg.ToolInfos())
		if err != nil {
			s.logger.Error("ai.ChatStream: bind tools failed, using base model", zap.Error(err))
			return e.BaseModel(), nil
		}
		return chatModel, nil
	}
	return e.BaseModel(), nil
}

// chatStreamFallback 非流式客户端的降级路径。
// 调用 Chat 获取完整响应，用 StreamSegmenter 拆分后逐段写入 segmentCh。
func (s *ChatService) chatStreamFallback(
	ctx context.Context,
	req *llm.ChatRequest,
	segmentCh chan<- string,
	queryVec []float32,
	lastMsgContent string,
) (*llm.ChatResponse, error) {
	resp, err := s.client.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("chat stream fallback: llm call: %w", err)
	}

	segmenter := NewStreamSegmenter()
	for _, seg := range segmenter.Feed(resp.Content) {
		if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
			return nil, sendErr
		}
	}
	if last := segmenter.Flush(); last != "" {
		if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
			return nil, sendErr
		}
	}

	s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

	return resp, nil
}

// sendSegment 将一个段落写入 segmentCh，支持 context 取消。
func sendSegment(ctx context.Context, ch chan<- string, seg string) error {
	select {
	case ch <- seg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// truncateForLog 截断过长的日志值（工具结果可能是完整图片 URL，全量打印会刷屏）。
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
