package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// opencodeSessionHeader / opencodeUserAgent OpenCode Go 网关要求的标识请求头。
// 官方文档（https://opencode.ai/docs/go/）要求接入方明确标识自身，
// 并携带 x-opencode-session 以优化提示词缓存；缺失该头的请求会被网关 400 拒绝。
const (
	opencodeSessionHeader = "x-opencode-session"
	opencodeSessionValue  = "lanmei-bot"
	opencodeUserAgent     = "lanmei-bot"
)

// opencodeTransport 为 OpenCode Go 网关请求附加标识头的 http.RoundTripper。
// 覆盖该网关的全部出站请求（对话/流式/视觉），缺失头的请求会被 400 拒绝。
type opencodeTransport struct {
	base http.RoundTripper
}

// RoundTrip 实现 http.RoundTripper：为 OpenCode Go 网关的每个出站请求附加
// x-opencode-session 与 User-Agent 标识头后转发；缺失标识头的请求会被网关 400 拒绝。
// 每次调用先 Clone 请求，避免修改调用方传入的原始 *http.Request。
func (t *opencodeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(opencodeSessionHeader, opencodeSessionValue)
	req.Header.Set("User-Agent", opencodeUserAgent)
	return t.base.RoundTrip(req)
}

// withOpencodeHeaders 若 BaseURL 指向 OpenCode Go 网关，返回注入标识头的 HTTPClient；
// 其他 provider（DeepSeek 官方等）返回 nil，行为不变。
func withOpencodeHeaders(baseURL string) *http.Client {
	if !strings.Contains(baseURL, "opencode.ai") {
		return nil
	}
	return &http.Client{Transport: &opencodeTransport{base: http.DefaultTransport}}
}

// DisableThinkingOption 返回关闭推理模型思考的 eino Option。
// 非流式 Chat 与流式直连路径（chatModel.Stream）共用，
// 经 OpenAI 兼容层扩展字段 thinking={"type":"disabled"} 透传（DeepSeek 等支持）。
func DisableThinkingOption() model.Option {
	return openai.WithExtraFields(map[string]any{
		"thinking": map[string]any{"type": "disabled"},
	})
}

// EinoOptions eino ChatModel 创建参数（provider 无关）
type EinoOptions struct {
	Provider    string // Provider 名称（如 deepseek/qwen/openai），用于计费统计
	BaseURL     string // API 基础地址（OpenAI/DeepSeek/Qwen/Moonshot/Ark/Ollama 通用）
	APIKey      string
	Model       string
	MaxTokens   int
	Temperature float64
	// PresencePenalty / FrequencyPenalty 采样重复惩罚（0~2，0 表示不启用）。
	// 用于抑制模型反复提及同一个话题/关键词；过大会让中文表达生硬，建议 0.2~0.6。
	PresencePenalty  float64
	FrequencyPenalty float64
	// 以下为酒馆（SillyTavern）风格采样参数，0 / 1 表示不启用：
	// TopP 核采样；TopK 只从概率最高的 K 个 token 采样；RepetitionPenalty 重复惩罚
	// （与 presence/frequency 不同，1.0 为中性，>1 抑制重复）；MinP / TopA 为
	// 本地推理后端常见的动态截断参数；Seed 固定采样种子。
	TopP              float64
	TopK              int
	RepetitionPenalty float64
	MinP              float64
	TopA              float64
	Seed              int
}

// UsageHook 用量上报回调（由计费模块注入；nil 表示不上报）。
type UsageHook func(record UsageRecord)

// EinoClient 基于 eino 的 LLMClient 实现。
// 通过 eino-ext 的 OpenAI 兼容层调用，天然支持多 provider。
type EinoClient struct {
	model     model.BaseChatModel
	provider  string
	modelName string
	sampling  SamplingDefaults
	usageHook UsageHook
}

// NewEinoClient 创建 eino LLM 客户端（基于 OpenAI 兼容层，仅做配置装配，不发起对话请求）。
//
// 参数：
//   - ctx：初始化上下文
//   - opts：Provider/BaseURL/APIKey/Model 等连接配置，不能为 nil（BaseURL 指向 OpenCode Go
//     网关时自动注入标识头 HTTPClient）
//
// 返回：初始化失败返回错误；成功后返回的客户端同时实现 LLMClient 与 EinoCapable。
func NewEinoClient(ctx context.Context, opts *EinoOptions) (*EinoClient, error) {
	mt := opts.MaxTokens
	t := float32(opts.Temperature)

	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:     opts.BaseURL,
		APIKey:      opts.APIKey,
		Model:       opts.Model,
		MaxTokens:   &mt,
		Temperature: &t,
		// OpenCode Go 网关：注入标识头 HTTPClient（x-opencode-session + UA），
		// 缺失该头的请求会被 400 拒绝；其他 provider 该值为 nil 不生效
		HTTPClient: withOpencodeHeaders(opts.BaseURL),
	})
	if err != nil {
		return nil, fmt.Errorf("llm: eino init: %w", err)
	}
	return &EinoClient{
		model:     cm,
		provider:  opts.Provider,
		modelName: opts.Model,
		sampling: SamplingDefaults{
			TopP:              clampRange(opts.TopP, 0, 1),
			TopK:              max(opts.TopK, 0),
			PresencePenalty:   clampRange(opts.PresencePenalty, 0, 2),
			FrequencyPenalty:  clampRange(opts.FrequencyPenalty, 0, 2),
			RepetitionPenalty: clampRepetitionPenalty(opts.RepetitionPenalty),
			MinP:              clampRange(opts.MinP, 0, 1),
			TopA:              clampRange(opts.TopA, 0, 1),
			Seed:              opts.Seed,
		},
	}, nil
}

// clampRange 将采样参数限制在 [lo, hi] 区间。
func clampRange(v, lo, hi float64) float64 {
	if v <= 0 {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampRepetitionPenalty 把 repetition_penalty 限制在 [1, 2]：1.0 为中性（不启用），
// 小于 1 会鼓励重复，与本项目用途相反，按 1 处理。
func clampRepetitionPenalty(v float64) float64 {
	if v <= 1 {
		return 1
	}
	if v > 2 {
		return 2
	}
	return v
}

// SamplingOptions 汇总请求级采样参数为 eino Option。
// 标准参数 top_p 走通用 option；非标准参数（top_k / repetition_penalty / min_p /
// top_a / seed）与关闭思考合并为单个 ExtraFields（eino 的 WithExtraFields 是覆盖
// 语义，多次传入会互相覆盖）。这些非标准参数仅在显式配置时下发，
// 避免 OpenAI / DeepSeek 等不支持的端点因未知字段报 400。
func SamplingOptions(s SamplingParams, disableThinking bool) []model.Option {
	var opts []model.Option
	if s.TopP != nil {
		if v := clampRange(*s.TopP, 0, 1); v > 0 {
			opts = append(opts, model.WithTopP(float32(v)))
		}
	}

	extra := make(map[string]any, 6)
	if s.TopK != nil && *s.TopK > 0 {
		extra["top_k"] = *s.TopK
	}
	if s.PresencePenalty != nil {
		if v := clampRange(*s.PresencePenalty, 0, 2); v > 0 {
			extra["presence_penalty"] = v
		}
	}
	if s.FrequencyPenalty != nil {
		if v := clampRange(*s.FrequencyPenalty, 0, 2); v > 0 {
			extra["frequency_penalty"] = v
		}
	}
	if s.RepetitionPenalty != nil {
		if v := clampRepetitionPenalty(*s.RepetitionPenalty); v > 1 {
			extra["repetition_penalty"] = v
		}
	}
	if s.MinP != nil {
		if v := clampRange(*s.MinP, 0, 1); v > 0 {
			extra["min_p"] = v
		}
	}
	if s.TopA != nil {
		if v := clampRange(*s.TopA, 0, 1); v > 0 {
			extra["top_a"] = v
		}
	}
	if s.Seed != nil {
		extra["seed"] = *s.Seed
	}
	if disableThinking {
		extra["thinking"] = map[string]any{"type": "disabled"}
	}
	if len(extra) == 0 {
		return opts
	}
	return append(opts, openai.WithExtraFields(extra))
}

// SamplingDefaults 返回客户端当前配置的采样默认值。
func (c *EinoClient) SamplingDefaults() SamplingDefaults {
	return c.sampling
}

// SetUsageHook 注入用量上报回调（nil 清除）。
func (c *EinoClient) SetUsageHook(hook UsageHook) { c.usageHook = hook }

// ProviderName 返回 Provider 名称
func (c *EinoClient) ProviderName() string { return c.provider }

// ModelName 返回模型名
func (c *EinoClient) ModelName() string { return c.modelName }

// reportUsage 上报一次用量记录（无 hook 或 0 token 时跳过）。
func (c *EinoClient) reportUsage(req *ChatRequest, input, output int) {
	if c.usageHook == nil || (input <= 0 && output <= 0) {
		return
	}
	c.usageHook(UsageRecord{
		Provider:     c.provider,
		Model:        c.modelName,
		Scene:        req.Scene,
		UserID:       req.UserID,
		GroupID:      req.GroupID,
		Platform:     req.Platform,
		InputTokens:  int64(input),
		OutputTokens: int64(output),
		TotalTokens:  int64(input + output),
	})
}

// usageOf 从 eino 响应中提取输入/输出 token 数。
func usageOf(resp *schema.Message) (input, output int) {
	if resp == nil || resp.ResponseMeta == nil || resp.ResponseMeta.Usage == nil {
		return 0, 0
	}
	return resp.ResponseMeta.Usage.PromptTokens, resp.ResponseMeta.Usage.CompletionTokens
}

// SupportsToolCalling 检查底层模型是否支持工具调用
func (c *EinoClient) SupportsToolCalling() bool {
	_, ok := c.model.(model.ToolCallingChatModel)
	return ok
}

// BaseModel 返回底层 eino BaseChatModel，供调用方直接调用 Stream/Generate。
// 用于流式工具调用循环中直接操作 schema.Message 列表。
func (c *EinoClient) BaseModel() model.BaseChatModel {
	return c.model
}

// ChatWithTools 返回绑定了工具的模型实例
func (c *EinoClient) ChatWithTools(tools []*schema.ToolInfo) (model.BaseChatModel, error) {
	tccm, ok := c.model.(model.ToolCallingChatModel)
	if !ok {
		return c.model, nil
	}
	return tccm.WithTools(tools)
}

// Chat 实现 LLMClient 接口
func (c *EinoClient) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	msgs := ToSchemaMessages(req.Messages)

	opts := SamplingOptions(req.Sampling, req.DisableThinking != nil && *req.DisableThinking)
	if req.MaxTokens != nil {
		// 按请求覆盖输出上限（短输出场景需显式设小值）
		opts = append(opts, model.WithMaxTokens(*req.MaxTokens))
	}
	resp, err := c.model.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, fmt.Errorf("llm: eino generate: %w", err)
	}

	input, output := usageOf(resp)
	c.reportUsage(req, input, output)

	return &ChatResponse{
		Content:      resp.Content,
		TokensUsed:   input + output,
		InputTokens:  input,
		OutputTokens: output,
		ToolCalls:    ToToolCallPointers(resp.ToolCalls),
	}, nil
}

// StreamChat 实现 StreamingLLMClient 接口，以流式方式返回聊天补全。
func (c *EinoClient) StreamChat(ctx context.Context, req *ChatRequest) (*schema.StreamReader[*schema.Message], error) {
	msgs := ToSchemaMessages(req.Messages)
	reader, err := c.model.Stream(ctx, msgs, SamplingOptions(req.Sampling, req.DisableThinking != nil && *req.DisableThinking)...)
	if err != nil {
		return nil, fmt.Errorf("llm: eino stream: %w", err)
	}
	return reader, nil
}

// StreamChatWithTools 绑定工具后以流式方式返回聊天补全。
func (c *EinoClient) StreamChatWithTools(ctx context.Context, req *ChatRequest, tools []*schema.ToolInfo) (*schema.StreamReader[*schema.Message], error) {
	chatModel, err := c.ChatWithTools(tools)
	if err != nil {
		return nil, fmt.Errorf("llm: bind tools for stream: %w", err)
	}
	msgs := ToSchemaMessages(req.Messages)
	reader, err := chatModel.Stream(ctx, msgs, SamplingOptions(req.Sampling, req.DisableThinking != nil && *req.DisableThinking)...)
	if err != nil {
		return nil, fmt.Errorf("llm: eino stream with tools: %w", err)
	}
	return reader, nil
}

// ChatWithModel 使用指定模型执行对话（用于工具调用流程中切换绑定了工具的模型）
func (c *EinoClient) ChatWithModel(ctx context.Context, req *ChatRequest, chatModel model.BaseChatModel) (*ChatResponse, error) {
	msgs := ToSchemaMessages(req.Messages)

	resp, err := chatModel.Generate(ctx, msgs, SamplingOptions(req.Sampling, req.DisableThinking != nil && *req.DisableThinking)...)
	if err != nil {
		return nil, fmt.Errorf("llm: eino generate: %w", err)
	}

	input, output := usageOf(resp)
	c.reportUsage(req, input, output)

	return &ChatResponse{
		Content:      resp.Content,
		TokensUsed:   input + output,
		InputTokens:  input,
		OutputTokens: output,
		ToolCalls:    ToToolCallPointers(resp.ToolCalls),
	}, nil
}

// ToSchemaMessages 将内部 llm.Message 列表转为 eino schema.Message 列表。
// 保留 ToolCallID 以支持多轮工具调用场景；
// 带 ImageURLs 的 user 消息转为多模态（text + image_url 分段），纯文本消息保持原 Content 路径。
func ToSchemaMessages(msgs []Message) []*schema.Message {
	result := make([]*schema.Message, len(msgs))
	for i, m := range msgs {
		schemaMsg := &schema.Message{
			Role:    ToSchemaRole(m.Role),
			Content: m.Content,
		}
		if m.ToolCallID != "" {
			schemaMsg.ToolCallID = m.ToolCallID
		}
		if len(m.ImageURLs) > 0 {
			schemaMsg.UserInputMultiContent = buildMultiContent(m.Content, m.ImageURLs)
		}
		result[i] = schemaMsg
	}
	return result
}

// buildMultiContent 构造多模态分段：text 在前，随后按序追加 image_url 分段。
func buildMultiContent(text string, imageURLs []string) []schema.MessageInputPart {
	parts := make([]schema.MessageInputPart, 0, len(imageURLs)+1)
	if text != "" {
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: text,
		})
	}
	for _, u := range imageURLs {
		if u == "" {
			continue
		}
		url := u
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &url},
			},
		})
	}
	return parts
}

// ToSchemaRole 将内部 Role 转为 eino schema.RoleType
func ToSchemaRole(r Role) schema.RoleType {
	switch r {
	case RoleSystem:
		return schema.System
	case RoleAssistant:
		return schema.Assistant
	case RoleTool:
		return schema.Tool
	default:
		return schema.User
	}
}

// ToToolCallPointers 将 []schema.ToolCall 转为 []*schema.ToolCall
func ToToolCallPointers(calls []schema.ToolCall) []*schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	result := make([]*schema.ToolCall, len(calls))
	for i := range calls {
		result[i] = &calls[i]
	}
	return result
}
