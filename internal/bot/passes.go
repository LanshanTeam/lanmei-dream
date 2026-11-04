package bot

import (
	"fmt"
	"strings"

	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/gateway"
	"github.com/DaWesen/lanmei-dream/internal/topic"
)

const (
	// KeyPlatform 平台标识：OnMessage 经 Extra 注入；命令上下文、话题入参与工具调用身份读取；
	// 缺失或类型不符时 platformFromCtx 回退 "unknown"。
	KeyPlatform = "platform" // string 平台标识（qq/wechat/telegram/...）
	// KeyPlatformUserID 发送者平台用户 ID（string，如 QQ 号）：OnMessage 经 Extra 注入；
	// 命令上下文与工具调用者身份读取；缺失时 platformUserIDFromCtx 回退 ctx.UserID。
	KeyPlatformUserID = "platform_user_id"
	// KeyNickname 发送者昵称（string）：OnMessage 经 Extra 注入；命令上下文、话题发言者标注读取；
	// 缺失时为空串（标注退化为用户 ID）。
	KeyNickname = "nickname"
	// KeyMessageID 平台消息 ID（string）：OnMessage 经 Extra 注入；命令上下文读取；
	// 缺失时为空串（引用等依赖它的能力降级）。
	KeyMessageID = "message_id"
	// KeyConnID 来源网关连接 ID（string）：OnMessage 经 Extra 注入；命令上下文读取；
	// 缺失时为空串。
	KeyConnID = "conn_id"
	// KeySelfID 机器人自身平台 ID（string）：OnMessage 经 Extra 注入；at 目标匹配（话题提及判定）
	// 与命令上下文读取；缺失时为空串（at 匹配不命中）。
	KeySelfID = "self_id"
	// KeyIsSegment 流式段落重入标记（bool）：Bot.streamSegments 在子消息 Extra 中置 true；
	// 条件 IsSegment 直接读 ctx.Extra 判定（不走 conduit.Get）；缺失/false 按普通消息处理。
	KeyIsSegment = "bot.is_segment" // bool 标记流式段落重入消息
	// KeyStreamChannel 流式段落通道（chan string）：RoleplayStreamPass 写入 ctx.data；
	// Bot.streamSegments 读取消费；缺失时无法投递段落，回调回复兜底话术。
	KeyStreamChannel = "bot.stream.ch" // chan string 流式段落通道
	// KeyIsSuperUser 发送者是否超管（bool）：OnMessage 经 Extra 注入（静态配置 + 动态管理员合并）；
	// IsSuperUserFromCtx 与 AdminGuardPass 读取；缺失时视为非超管。
	KeyIsSuperUser = "bot.is_super_user" // bool 当前用户是否为超管（OnMessage 注入）
	// KeySuppressRequesterAt 抑制自动 at 请求者的标记（bool）：命令 handler 调用
	// SuppressRequesterAt 时由 ExecuteCommandPass 写入 ctx.data；Bot.flushOutput 读取；
	// 缺失/false 时按指向性判定正常 at 请求者。
	KeySuppressRequesterAt = "bot.reply.suppress_requester_at"

	// 出站段输出（插件经 conduit.Set 写入，回调读取后按段发送）

	// KeySendSegments 插件出站段列表（[]map[string]any，OneBot 12 原生段 at/text/image）：
	// 插件或 ExecuteCommandPass 写入 ctx.data；Bot.trySendSegments 读取并按段发送
	// （原生段优先于纯文本）；缺失、类型不符或空列表时回退 ctx.Output 纯文本发送。
	KeySendSegments = "bot.send.segments" // []map[string]any OneBot 原生段列表（at/text/image 组合）

	// 事件输入（Extra，由 OnMessage 注入，只读）

	// KeyMessageType 消息类型（gateway.MessageType：message/notice/request）：OnMessage 经 Extra 注入；
	// IsNotice/MessageTypeFromCtx 读取；缺失时为零值（按普通消息处理）。
	KeyMessageType = "bot.message_type" // gateway.MessageType：message/notice/request
	// KeySegments 完整消息段（[]gateway.NormalizedSegment）：OnMessage 经 Extra 注入；
	// IsMedia/SegmentsFromCtx/MediaPass 读取；缺失时为空列表（媒体管线不触发）。
	KeySegments = "bot.segments" // []gateway.NormalizedSegment 完整消息段
	// KeyMimeTypes 去重后的 MIME 类型列表（[]string）：OnMessage 经 Extra 注入；
	// MimeTypesFromCtx 读取；缺失时为 nil。
	KeyMimeTypes = "bot.mime_types" // []string 去重后的 MIME 类型
	// KeyAtTargets at 目标 user_id 列表（[]string）：OnMessage 经 Extra 注入；
	// 命令上下文与话题提及判定（at 精确命中）读取；缺失时为 nil（视为未 at）。
	KeyAtTargets = "bot.at_targets" // []string at 目标 user_id 列表
	// KeyEventType 规范化事件类型（string，普通消息为空串）：OnMessage 经 Extra 注入；
	// EventTypeFromCtx/NoticeGatePass 读取；缺失时为空串。
	KeyEventType = "bot.event.type" // string 规范化事件类型（普通消息为空）
	// KeyEventSubType 事件子类型（string，透传原始 sub_type）：OnMessage 经 Extra 注入；
	// EventSubTypeFromCtx 读取；缺失时为空串。
	KeyEventSubType = "bot.event.sub_type"
	// KeyEventData 事件全字段（map[string]any，普通消息为 nil）：OnMessage 经 Extra 注入；
	// EventDataFromCtx/NoticeGatePass/插件读取；缺失时为 nil。
	KeyEventData = "bot.event.data" // map[string]any 事件全字段
	// KeyImageURLs 消息中图片段 URL 列表（[]string）：OnMessage 经 Extra 注入；
	// 插件零依赖读取（如表情包打标取消息图片）；缺失时为 nil（视为无图）。
	KeyImageURLs = "bot.image_urls" // []string 消息中图片段 url 列表（OnMessage 注入，插件零依赖读取）

	// 媒体处理中间结果（由 MediaPass 写入 ctx.data）

	// KeyImageDesc 图片理解描述（string，形如 "[图片：...]" 的拼接文本）：MediaPass 写入；
	// ImageDescFromCtx/MediaRouterPass 读取；缺失或空串表示无可用描述。
	KeyImageDesc = "bot.image_desc"
	// KeyMediaHandled 媒体已处理标记（bool）：MediaPass 写入；当前引擎内无读取方
	// （路由实际以 KeyImageDesc 是否非空为依据），供插件/后续节点判断；缺失/false 表示未经媒体处理。
	KeyMediaHandled = "bot.media_handled"
	// KeyImageOnly 纯图片消息标记（bool）：MediaPass 写入（原消息无文字、描述由视觉理解生成）；
	// TopicGatePass 读取并置 IncomingMsg.ImageOnly —— 这类消息只入窗、不自动回复，
	// 避免"文字消息回一次 + 截图再答一次"表现为对同一件事回复两遍。
	KeyImageOnly = "bot.image_only"

	// 群聊话题（TopicGatePass 写入 ctx.data，供对话管线消费）

	// KeyTopicID 命中话题 ID（string）：TopicGatePass 写入；RoleplayStreamPass 读取
	// （流式回复完成后据此记录 Bot 回复）；缺失为空串。
	KeyTopicID = "bot.topic.id" // string 命中话题 ID（data）
	// KeyTopicLabel 话题描述（string）：TopicGatePass 写入；供展示与日志消费；缺失为空串。
	KeyTopicLabel = "bot.topic.label" // string 话题描述（data）
	// KeyTopicContext 话题上下文（*llm.TopicContext）：TopicGatePass 写入；RoleplayStreamPass
	// 与 TopicContextFromCtx 读取；缺失/nil 表示私聊或未命中话题。
	KeyTopicContext = "bot.topic.ctx" // *llm.TopicContext 话题上下文（data）
	// KeyMentionMode 提及模式（topic.MentionMode）：TopicGatePass 写入；Bot.isDirected 与
	// TopicMentionModeFromCtx 读取（非 MentionNone 视为明确指向，回复时 at）；缺失时为零值 MentionNone。
	KeyMentionMode = "bot.topic.mention" // topic.MentionMode 提及模式（data）
	// KeyTopicStrong 强提及标记（bool）：TopicGatePass 写入（at 或高置信度语言学提及）；
	// RoleplayStreamPass 读取，作为 RecordBotReply 的 explicit 参数 —— 显式提及会重置
	// 连续自动回复计数与重复提问计数，非显式续聊则受 max_auto_replies 限制。
	KeyTopicStrong = "bot.topic.strong" // bool 强提及（data）
)

// AdminGuardPass 校验 /admin 命令的发送者是否为超管。
// 非超管：写入拒绝回复并返回错误中断管线（CommandPass 不会执行），
// 不触发超时降级管线（fallback 仅在超时触发）。
type AdminGuardPass struct{}

// Execute 校验发送者超管身份：超管直接放行，非超管写入拒绝回复并返回错误中断管线。
//
// 位置：pipeline.admin 的首个 Pass，在 pass.admin.command（CommandPass）之前执行。
//
// 依赖上下文键：KeyIsSuperUser（Extra，由 OnMessage 注入）。
//
// 失败条件：非超管（含 KeyIsSuperUser 缺失/类型不符，一律按非超管处理）时返回错误，
// CommandPass 不会执行；错误路径不触发超时降级管线（pipeline.fallback 仅在管线超时时执行）。
//
// 注意：写入 ctx.Output 的拒绝回复不会随错误路径发出——Bot.makeResponseCallback 收到错误时
// 只回复统一兜底话术，消息级输出不会被发送。
func (p *AdminGuardPass) Execute(ctx *conduit.MessageContext) error {
	if IsSuperUserFromCtx(ctx) {
		return nil
	}
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: "只有管理员才能执行该命令哦~",
	})
	return fmt.Errorf("admin: 非超管用户 %s 尝试执行管理员命令", ctx.UserID)
}

// parseSuperUsers 解析超管配置字符串为平台→用户ID 集合。
// 格式：qq:123456,wechat:wxid_xxx → {"qq": {"123456": {}, "wxid_xxx": {}}}
func parseSuperUsers(raw string) map[string]map[string]struct{} {
	result := map[string]map[string]struct{}{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if result[parts[0]] == nil {
			result[parts[0]] = map[string]struct{}{}
		}
		result[parts[0]][parts[1]] = struct{}{}
	}
	return result
}

// IsSuperUserFromCtx 判断当前消息发送者是否为超管（读取 OnMessage 写入黑板的标记）。
// 供插件 Pass 直接使用，插件无需依赖 bot 包。
func IsSuperUserFromCtx(ctx *conduit.MessageContext) bool {
	v, _ := ctx.Extra[KeyIsSuperUser].(bool)
	return v
}

// CommandPass 把 command.System 包装成 Conduit Pass
type CommandPass struct {
	CmdSys *command.System
}

// Execute 用 command.System.Process 处理 ctx.RawMsg，把命令 handler 经 Reply 回调产生的
// 文本回复逐条追加到 ctx.Output。
//
// 位置：pipeline.admin 的第二个 Pass（行为树 IsAdminCommand 条件命中 /admin 前缀后进入）。
//
// 依赖上下文键：ctx.RawMsg 为命令原文；平台/用户/群/昵称/消息 ID/连接 ID/at 目标等参数取自
// Extra 的 Key* 字段（缺失时按 helper 兜底），插件命令重入标记取自 Extra 的 bot.command.reentry。
//
// 返回：Process 的错误原样返回（如未知命令会导致管线中断）；handler 正常执行返回 nil。
func (p *CommandPass) Execute(ctx *conduit.MessageContext) error {
	var replies []string
	err := p.CmdSys.Process(ctx.RawMsg, &command.Context{
		Platform:       platformFromCtx(ctx),
		PlatformUserID: platformUserIDFromCtx(ctx),
		GroupID:        ctx.GroupID,
		IsGroup:        ctx.IsGroup,
		Message:        ctx.RawMsg,
		Reply:          func(s string) { replies = append(replies, s) },
		SelfID:         SelfIDFromCtx(ctx),
		AtTargets:      AtTargetsFromCtx(ctx),
		Nickname:       nicknameFromCtx(ctx),
		MessageID:      messageIDFromCtx(ctx),
		ConnID:         connIDFromCtx(ctx),
		CommandReentry: commandReentryFromCtx(ctx),
		IsSuperUser:    IsSuperUserFromCtx(ctx),
	})

	for _, r := range replies {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID:  ctx.UserID,
			GroupID: ctx.GroupID,
			Content: r,
			IsGroup: ctx.IsGroup,
		})
	}

	return err
}

// CommandRouterPass 解析斜杠命令并将命令信息写入 MessageContext
type CommandRouterPass struct {
	CmdSys *command.System
}

const (
	commandNameKey    = "bot.command.name"
	commandArgsKey    = "bot.command.args"
	commandHandlerKey = "bot.command.handler"
)

// commandReentryKey 标记插件命令重入消息（插件包 makeCommandHandler 在重入 Extra 中设置）。
const commandReentryKey = "bot.command.reentry"

// commandReentryFromCtx 从 ctx.Extra 读取插件命令重入标记。
func commandReentryFromCtx(ctx *conduit.MessageContext) bool {
	b, _ := ctx.Extra[commandReentryKey].(bool)
	return b
}

// Execute 解析 ctx.RawMsg 中的斜杠命令名与参数，把命令名、参数列表与 handler 写入 ctx.data，
// 供 ExecuteCommandPass 执行。
//
// 位置：pipeline.command 的首个 Pass（行为树 IsCommand 条件命中 / 前缀后进入）。
//
// 依赖上下文键：ctx.RawMsg；写入 commandNameKey/commandArgsKey/commandHandlerKey（私有键）。
//
// 返回：空命令返回错误中断管线；命令不存在时写入未知命令提示并返回 nil
// （不中断管线，后续 ExecuteCommandPass 因无 handler 静默跳过）。
func (p *CommandRouterPass) Execute(ctx *conduit.MessageContext) error {
	name := strings.TrimPrefix(ctx.RawMsg, "/")
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}

	cmdName := parts[0]
	cmd, ok := p.CmdSys.Lookup(cmdName)
	if !ok {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("未知命令: /%s\n输入 /帮助 查看可用命令", cmdName),
		})
		return nil
	}

	args := parts[1:]
	conduit.Set(ctx, commandNameKey, cmdName)
	conduit.Set(ctx, commandArgsKey, args)
	conduit.Set(ctx, commandHandlerKey, cmd.Handler)

	return nil
}

// ExecuteCommandPass 从 MessageContext 读取命令信息并执行
type ExecuteCommandPass struct{}

// Execute 从 ctx.data 读取命令名、参数与 handler 并执行命令。
//
// 位置：pipeline.command 的第二个 Pass；IntentCommandExecPass 也直接复用本 Pass 执行命令。
//
// 依赖上下文键：commandNameKey/commandArgsKey/commandHandlerKey（缺失或 handler 为 nil 时
// 静默返回 nil，不产生输出）；平台/用户/群/at 目标等参数取自 Extra 的 Key* 字段。
//
// 说明：handler 的 Reply 文本追加到 ctx.Output；ReplySegments 写入 KeySendSegments
// （原生段优先于文本，与 Bot.flushOutput 的发送规则一致）；调用 SuppressRequesterAt 时写入
// KeySuppressRequesterAt；handler 返回的错误原样返回，由引擎中断管线。
func (p *ExecuteCommandPass) Execute(ctx *conduit.MessageContext) error {
	nameRaw, _ := conduit.Get[string](ctx, commandNameKey)
	argsRaw, _ := conduit.Get[[]string](ctx, commandArgsKey)
	handlerRaw, _ := conduit.Get[func(*command.Context) error](ctx, commandHandlerKey)

	if handlerRaw == nil {
		return nil
	}

	var replies []string
	var replySegments []map[string]any
	suppressRequesterAt := false
	cmdCtx := &command.Context{
		Platform:       platformFromCtx(ctx),
		PlatformUserID: platformUserIDFromCtx(ctx),
		GroupID:        ctx.GroupID,
		IsGroup:        ctx.IsGroup,
		CommandName:    nameRaw,
		CommandArgs:    argsRaw,
		Message:        ctx.RawMsg,
		Reply:          func(s string) { replies = append(replies, s) },
		ReplySegments:  func(segments []map[string]any) { replySegments = segments },
		SuppressRequesterAt: func() {
			suppressRequesterAt = true
		},
		SelfID:         SelfIDFromCtx(ctx),
		AtTargets:      AtTargetsFromCtx(ctx),
		Nickname:       nicknameFromCtx(ctx),
		MessageID:      messageIDFromCtx(ctx),
		ConnID:         connIDFromCtx(ctx),
		CommandReentry: commandReentryFromCtx(ctx),
		IsSuperUser:    IsSuperUserFromCtx(ctx),
	}

	if err := handlerRaw(cmdCtx); err != nil {
		return err
	}
	if len(replySegments) > 0 {
		// 原生段优先级高于文本输出，与 Bot.flushOutput 的发送规则一致。
		conduit.Set(ctx, KeySendSegments, replySegments)
	}
	if suppressRequesterAt {
		conduit.Set(ctx, KeySuppressRequesterAt, true)
	}

	for _, r := range replies {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: r,
		})
	}

	return nil
}

// FallbackPass 超时或未匹配时的兜底
type FallbackPass struct{}

// Execute 向 ctx.Output 追加固定的兜底话术，用于管线超时后的降级回复。
//
// 位置：pipeline.fallback 唯一 Pass，由引擎在管线执行超时时（WithFallbackPipeline）调用；
// 正常错误路径不会触发降级管线。
//
// 依赖上下文键：无；不读取任何黑板数据，也不返回错误。
func (p *FallbackPass) Execute(ctx *conduit.MessageContext) error {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		Content: "蓝妹现在有点迷糊，稍等一下...",
		IsGroup: ctx.IsGroup,
	})
	return nil
}

// IsCommand 判断消息是否以 / 开头
func IsCommand(ctx *conduit.MessageContext) bool {
	return strings.HasPrefix(ctx.RawMsg, "/")
}

// IsAdminCommand 判断消息是否以 /admin 开头
func IsAdminCommand(ctx *conduit.MessageContext) bool {
	return strings.HasPrefix(ctx.RawMsg, "/admin")
}

// IsSegment 判断消息是否为流式段落重入消息。
// 由 Bot.streamSegments 在提交子消息时通过 InputMessage.Extra 标记，
// BT 据此路由到段落交付管线。
//
// 注意：KeyIsSegment 设置在 ctx.Extra（来自 InputMessage.Extra），
// 不能用 conduit.Get（从 ctx.data 读取），必须直接读 ctx.Extra。
func IsSegment(ctx *conduit.MessageContext) bool {
	if raw, ok := ctx.Extra[KeyIsSegment]; ok {
		if b, ok := raw.(bool); ok {
			return b
		}
	}
	return false
}

// IsNotice 判断消息是否为互动事件（notice/request 类）。
// 行为树据此将事件路由到 pipeline.notice（预留节点）或插件子树。
func IsNotice(ctx *conduit.MessageContext) bool {
	mt, _ := ctx.Extra[KeyMessageType].(gateway.MessageType)
	return mt == gateway.MessageTypeNotice || mt == gateway.MessageTypeRequest
}

// IsMedia 判断消息是否含多媒体段（image/audio/video/file/record）。
// 行为树据此将消息路由到 pipeline.media 进行下载/缓存/理解。
func IsMedia(ctx *conduit.MessageContext) bool {
	segs, _ := ctx.Extra[KeySegments].([]gateway.NormalizedSegment)
	for _, s := range segs {
		switch s.Type {
		case "image", "audio", "video", "file", "record":
			return true
		}
	}
	return false
}

// SegmentsFromCtx 从 ctx.Extra 读取完整消息段列表。
func SegmentsFromCtx(ctx *conduit.MessageContext) []gateway.NormalizedSegment {
	segs, _ := ctx.Extra[KeySegments].([]gateway.NormalizedSegment)
	return segs
}

// MimeTypesFromCtx 从 ctx.Extra 读取去重后的 MIME 类型列表。
func MimeTypesFromCtx(ctx *conduit.MessageContext) []string {
	mimes, _ := ctx.Extra[KeyMimeTypes].([]string)
	return mimes
}

// AtTargetsFromCtx 从 ctx.Extra 读取 at 目标 user_id 列表。
func AtTargetsFromCtx(ctx *conduit.MessageContext) []string {
	ats, _ := ctx.Extra[KeyAtTargets].([]string)
	return ats
}

// MessageTypeFromCtx 从 ctx.Extra 读取事件类型（message/notice/request）。
func MessageTypeFromCtx(ctx *conduit.MessageContext) gateway.MessageType {
	mt, _ := ctx.Extra[KeyMessageType].(gateway.MessageType)
	return mt
}

// EventTypeFromCtx 从 ctx.Extra 读取规范化事件类型（普通消息为空串）。
func EventTypeFromCtx(ctx *conduit.MessageContext) string {
	et, _ := ctx.Extra[KeyEventType].(string)
	return et
}

// EventSubTypeFromCtx 从 ctx.Extra 读取事件子类型（透传原始 sub_type，可为空）。
func EventSubTypeFromCtx(ctx *conduit.MessageContext) string {
	est, _ := ctx.Extra[KeyEventSubType].(string)
	return est
}

// EventDataFromCtx 从 ctx.Extra 读取事件全字段（普通消息为 nil）。
func EventDataFromCtx(ctx *conduit.MessageContext) map[string]any {
	ed, _ := ctx.Extra[KeyEventData].(map[string]any)
	return ed
}

// ImageDescFromCtx 从 ctx.data 读取图片理解描述（由 MediaPass 写入）。
func ImageDescFromCtx(ctx *conduit.MessageContext) string {
	desc, _ := conduit.Get[string](ctx, KeyImageDesc)
	return desc
}

// imageOnlyFromCtx 从 ctx.data 读取"纯图片消息"标记（由 MediaPass 写入）。
// 缺失/false 表示消息含文字或未经媒体处理。
func imageOnlyFromCtx(ctx *conduit.MessageContext) bool {
	v, _ := conduit.Get[bool](ctx, KeyImageOnly)
	return v
}

// topicStrongFromCtx 从 ctx.data 读取强提及标记（由 TopicGatePass 写入）。
// 缺失/false 表示非显式提及触发的回复。
func topicStrongFromCtx(ctx *conduit.MessageContext) bool {
	v, _ := conduit.Get[bool](ctx, KeyTopicStrong)
	return v
}

// TopicContextFromCtx 从 ctx.data 读取群聊话题上下文（由 TopicGatePass 写入）。
// 返回 nil 表示未命中话题（私聊或群聊 SKIP）。
func TopicContextFromCtx(ctx *conduit.MessageContext) *llm.TopicContext {
	tc, _ := conduit.Get[*llm.TopicContext](ctx, KeyTopicContext)
	return tc
}

// TopicMentionModeFromCtx 从 ctx.data 读取提及模式（MentionNone 表示未提及）。
func TopicMentionModeFromCtx(ctx *conduit.MessageContext) topic.MentionMode {
	mode, _ := conduit.Get[topic.MentionMode](ctx, KeyMentionMode)
	return mode
}

// SelfIDFromCtx 从 ctx.Extra 读取机器人自身 ID。
func SelfIDFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeySelfID]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}

func platformFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyPlatform]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return "unknown"
}

func platformUserIDFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyPlatformUserID]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ctx.UserID
}

// nicknameFromCtx 从 ctx.Extra 读取用户昵称。
// KeyNickname 由 OnMessage 通过 InputMessage.Extra 设置，存储在 ctx.Extra 中，
// 不能用 conduit.Get（从 ctx.data 读取）。
func nicknameFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyNickname]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}

// messageIDFromCtx 从 ctx.Extra 读取消息 ID。
func messageIDFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyMessageID]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}

// connIDFromCtx 从 ctx.Extra 读取来源连接 ID。
func connIDFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyConnID]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}
