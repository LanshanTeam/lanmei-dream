// Package bot 中群聊话题（Topic）系统的行为树集成节点（设计详见 docs/group-topic-design.md）。
//
// TopicGatePass 是 RouterPass：Execute 对群消息做"是否应回复"的决策 —— 私聊直接放行；
// 群聊用一次 LLM 调用同时得到意图与"是否在跟机器人说话"的提及判断（注入群聊最近对话
// 做指代消解），再交 TopicManager 决策；Route 按决策与意图路由到 roleplay /
// intent_command_exec，或静默保存到 topic_ignore。命中话题时写入黑板 TopicContext。
package bot

import (
	"fmt"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/intent"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"github.com/DaWesen/lanmei-dream/internal/topic"
)

// topicReplyKey 存储 TopicGatePass 的回复决策（data，Route 读取）。
const topicReplyKey = "bot.topic.reply"

// TopicGatePass 实现 conduit.RouterPass。
// Execute：私聊放行；群聊合并调用意图+提及判断（Analyzer）后交 Manager 决策，
// 命中话题时写黑板；Route：私聊 → intent_analysis；群聊按决策与意图动态路由。
type TopicGatePass struct {
	Manager  *topic.Manager // nil 时退化为全放行（topic 系统未启用）
	Analyzer *intent.Analyzer
	Logger   *zap.Logger
}

// Execute 对消息做"是否应回复"的决策：私聊或 topic 系统未启用时直接放行；
// 群聊时构造 IncomingMsg、执行提示词注入检测、合并一次 LLM 调用（意图 + 提及判断），
// 再交 TopicManager 决策，并把话题上下文、提及模式与回复决策写入 ctx.data。
//
// 位置：pipeline.topic_gate 唯一 Pass（群聊与私聊的非命令消息都进入；RouterPass：Execute 只做决策）。
//
// 依赖上下文键：读取 Extra 的 KeyPlatform/KeySelfID/KeyNickname/KeyAtTargets；写入 intentResultKey、
// KeyTopicID/KeyTopicLabel/KeyTopicContext、KeyMentionMode 与私有键 topicReplyKey（均 ctx.data）。
//
// 失败降级：Analyzer 失败时按 IntentChat/0.5 且视为未提及；judge 省略提及置信度时回退用意图置信度；
// Execute 始终返回 nil（错误不中断管线，由 Route 按决策兜底）。
func (p *TopicGatePass) Execute(ctx *conduit.MessageContext) error {
	if p.Manager == nil || !ctx.IsGroup {
		return nil
	}

	msg := &topic.IncomingMsg{
		Platform:  platformFromCtx(ctx),
		SelfID:    SelfIDFromCtx(ctx),
		GroupID:   ctx.GroupID,
		UserID:    ctx.UserID,
		UserName:  nicknameFromCtx(ctx),
		Content:   ctx.RawMsg,
		AtTargets: AtTargetsFromCtx(ctx),
		ImageOnly: imageOnlyFromCtx(ctx),
		SentAt:    time.Now(),
	}

	// 合并 LLM 判断：一次调用同时返回意图与提及判定（at 命中时也走一遍，
	// 以便意图路由；LLM 失败时结果降级为 chat，仅 at 仍可回复）。
	// 防注入观测：消息命中注入模式时记 Warn（LLM 侧由 chat.go 安全规则兜底，这里做硬观测）。
	if inj := ai.DetectInjection(msg.Content); inj != "" {
		p.Logger.Warn("topic: 检测到疑似提示词注入",
			zap.String("group", ctx.GroupID), zap.String("user", ctx.UserID),
			zap.String("pattern", inj), zap.String("msg", truncate(ctx.RawMsg, 40)))
	}
	var judge *topic.LinguisticJudge
	if p.Analyzer != nil && msg.Content != "" {
		result, err := p.Analyzer.Analyze(ctx.Ctx, msg.Content, p.Manager.BuildJudgeContext(msg))
		if err != nil {
			p.Logger.Warn("topic: 意图/提及判断失败，按未提及处理",
				zap.String("group", ctx.GroupID), zap.String("user", ctx.UserID), zap.Error(err))
			result = &intent.Result{Intent: intent.IntentChat, Confidence: 0.5}
		} else {
			p.Logger.Info("intent: result",
				zap.String("msg", truncate(ctx.RawMsg, 20)),
				zap.String("intent", string(result.Intent)),
				zap.String("command", result.CommandName),
				zap.Float64("confidence", result.Confidence),
				zap.Bool("is_talking_to_bot", result.IsTalkingToBot),
				zap.String("mention_role", result.MentionRole),
				zap.Float64("mention_confidence", result.MentionConfidence),
				zap.String("mention_evidence", result.MentionEvidence),
			)
			judge = &topic.LinguisticJudge{
				IsTalkingToBot: result.IsTalkingToBot,
				Role:           topic.MentionRole(result.MentionRole),
				Confidence:     result.MentionConfidence,
				Evidence:       result.MentionEvidence,
			}
			if judge.IsTalkingToBot && judge.Confidence <= 0 {
				// LLM 省略了提及置信度时，回退用意图置信度
				judge.Confidence = result.Confidence
			}
		}
		conduit.Set(ctx, intentResultKey, result)
	} else {
		// 私聊/纯媒体（无文本）/分析器缺失：不注入意图结果，路由走默认回复路径
		conduit.Set(ctx, intentResultKey, &intent.Result{Intent: intent.IntentChat, Confidence: 0.5})
	}

	dec := p.Manager.HandleGroupMessage(ctx.Ctx, msg, judge)
	if dec == nil {
		return nil
	}

	// 命中话题：写入上下文供对话管线消费（排除窗口末尾的当前消息，避免重复）。
	//
	// 只有语义相关（dec.Related）时才注入话题窗口与标签：话题名每轮出现在 prompt 里
	// 会形成"话题锚定"，让模型反复把旧话题拎回来；不相关时连旧窗口一起跳过，
	// 对话改用 L0 近期历史，避免旧话题内容把新话题带偏。
	// KeyTopicID 始终写入（仅用于回复记账），不参与 prompt 组装。
	if dec.Reply && dec.Topic != nil {
		conduit.Set(ctx, KeyTopicID, dec.Topic.ID)
		if dec.Related {
			if tc := p.Manager.BuildTopicContext(dec.Topic, 1); tc != nil {
				conduit.Set(ctx, KeyTopicLabel, tc.Label)
				conduit.Set(ctx, KeyTopicContext, tc)
			}
		}
	}
	conduit.Set(ctx, KeyMentionMode, dec.Mention)
	conduit.Set(ctx, KeyTopicStrong, dec.Strong)
	conduit.Set(ctx, topicReplyKey, dec.Reply)
	return nil
}

// Route 根据话题决策与意图结果路由到下游管线。
// 群聊回复时按意图路由（chat/tool → roleplay，command → 命令执行，ignore → 静默）；
// 群聊不回复 → topic_ignore；私聊 → intent_analysis（走原意图分析流程）。
func (p *TopicGatePass) Route(ctx *conduit.MessageContext) (string, error) {
	if p.Manager == nil || !ctx.IsGroup {
		return "pipeline.intent_analysis", nil
	}
	reply, _ := conduit.Get[bool](ctx, topicReplyKey)
	if !reply {
		return "pipeline.topic_ignore", nil
	}
	result, ok := conduit.Get[*intent.Result](ctx, intentResultKey)
	if !ok || result == nil {
		return "pipeline.topic_ignore", nil
	}
	switch result.Intent {
	case intent.IntentCommand:
		return "pipeline.intent_command_exec", nil
	case intent.IntentChat, intent.IntentTool:
		return "pipeline.roleplay", nil
	case intent.IntentIgnore:
		return "pipeline.topic_ignore", nil
	default:
		return "pipeline.topic_ignore", nil
	}
}

// TopicIgnorePass 保存未命中话题的群消息到对话历史（带 group_id），不生成回复。
// 这些记录供未来"群回忆"查询使用，但不进入话题归档链路。
type TopicIgnorePass struct {
	DB *database.DB
}

// Execute 把未命中话题的群消息保存到对话历史（带 group_id，SourceChat），不生成回复；
// 这些记录供后续"群回忆"查询使用，但不进入话题归档链路。
//
// 位置：pipeline.topic_ignore 唯一 Pass。
//
// 依赖上下文键：ctx.Ctx 与 Extra 的 KeyPlatform/KeyPlatformUserID/KeyNickname；DB 为 nil 时
// 直接返回 nil（不落库）。
//
// 失败语义：取用户或写库失败返回 conduit.NewSoftError（引擎记录日志但不中断管线）。
func (p *TopicIgnorePass) Execute(ctx *conduit.MessageContext) error {
	if p.DB == nil {
		return nil
	}
	platform := platformFromCtx(ctx)
	platformUserID := platformUserIDFromCtx(ctx)
	nickname := nicknameFromCtx(ctx)
	user, err := p.DB.GetOrCreateUser(ctx.Ctx, platform, platformUserID, nickname)
	if err != nil {
		return conduit.NewSoftError(fmt.Errorf("topic_ignore: get or create user: %w", err))
	}
	if err := p.DB.SaveConversation(ctx.Ctx, user.ID, ctx.GroupID, "user", ctx.RawMsg, model.SourceChat, ""); err != nil {
		return conduit.NewSoftError(fmt.Errorf("topic_ignore: save conversation: %w", err))
	}
	return nil
}
