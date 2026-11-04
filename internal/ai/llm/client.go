package llm

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Role 表示对话消息的角色，取值与 OpenAI 兼容 API 的 role 字段一致；
// 经 ToSchemaRole 映射为 eino schema.RoleType（未知值按 user 处理）。
type Role string

const (
	// RoleSystem 系统提示词角色，承载人设、安全规则与注入的上下文，须位于消息列表最前。
	RoleSystem Role = "system"
	// RoleUser 用户消息角色；带 ImageURLs 时转为多模态（text + image_url 分段）输入。
	RoleUser Role = "user"
	// RoleAssistant 助手回复角色；工具调用轮中 Content 可为空并携带 ToolCalls。
	RoleAssistant Role = "assistant"
	// RoleTool 工具结果角色，必须携带 ToolCallID 以关联对应的工具调用请求。
	RoleTool Role = "tool"
)

// Message 表示一条对话消息
type Message struct {
	Role         Role     `json:"role"`
	Content      string   `json:"content"`
	ImageURLs    []string `json:"image_urls,omitempty"`   // 多模态：图片 URL 列表（OpenAI 兼容 image_url），仅 user 角色使用
	ToolCallID   string   `json:"tool_call_id,omitempty"` // tool 角色消息必须携带
	ToolCallName string   `json:"tool_call_name,omitempty"`
}

// TopicMsg 群聊话题内的一条消息（供上下文注入与归档）
type TopicMsg struct {
	UserID   string    `json:"user_id"`  // Bot 回复时为 bot 自身 ID
	Nickname string    `json:"nickname"` // 上下文注入时用于标注发言者，可能为空
	IsBot    bool      `json:"is_bot"`
	Content  string    `json:"content"`
	At       bool      `json:"at"`
	SentAt   time.Time `json:"sent_at"`
}

// TopicContext 群聊话题上下文（nil = 私聊/无话题）。
// 定义在本包以避免 topic 包与 llm 包循环依赖；topic 包负责填充。
type TopicContext struct {
	TopicID string     `json:"topic_id"`
	Label   string     `json:"label"` // 话题名
	Members []string   `json:"members"`
	Recent  []TopicMsg `json:"recent"` // 话题内近期消息（替代 LOD 的部分片段）
}

// SamplingParams 请求级采样参数（酒馆风格），nil 字段沿用客户端默认值。
// 仅角色扮演对话链路下发；意图分析、记忆压缩等结构化输出不设置，
// 避免 top_k / repetition_penalty 等非标准参数影响 JSON 稳定性。
type SamplingParams struct {
	TopP              *float64 `json:"top_p,omitempty"`
	TopK              *int     `json:"top_k,omitempty"`
	PresencePenalty   *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64 `json:"frequency_penalty,omitempty"`
	RepetitionPenalty *float64 `json:"repetition_penalty,omitempty"`
	MinP              *float64 `json:"min_p,omitempty"`
	TopA              *float64 `json:"top_a,omitempty"`
	Seed              *int     `json:"seed,omitempty"`
}

// SamplingDefaults 客户端级采样默认值（0 / 1 表示不启用对应参数）。
type SamplingDefaults struct {
	TopP              float64
	TopK              int
	PresencePenalty   float64
	FrequencyPenalty  float64
	RepetitionPenalty float64
	MinP              float64
	TopA              float64
	Seed              int
}

// ChatRequest 是一次对话请求的入参
type ChatRequest struct {
	Messages  []Message `json:"messages"`
	UserID    int64     `json:"user_id"`
	UserName  string    `json:"user_name"`          // 用户昵称，供 prompt 组装使用
	GroupName string    `json:"group_name"`         // 群组名称，供 prompt 组装使用
	GroupID   string    `json:"group_id"`           // 来源群（空=私聊），供 LOD 上下文按群隔离
	Platform  string    `json:"platform,omitempty"` // 消息平台（qq/wechat/telegram…），供计费统计
	// PlatformUserID 发送者平台用户 ID 字符串（与 conduit MessageContext.UserID 同源，
	// 如 QQ 号 "123456"）。工具调用循环据此注入 CallerIdentity，供工具查询以平台 ID
	// 为键的业务数据；与 UserID（数据库自增 int64）不同源。
	PlatformUserID string        `json:"platform_user_id,omitempty"`
	Scene          string        `json:"scene,omitempty"`         // 用量场景（chat/intent/compress/topic/vision），供计费统计
	TopicContext   *TopicContext `json:"topic_context,omitempty"` // 群聊话题上下文（nil = 私聊/无话题）
	// MaxTokens 本次请求的输出 token 上限（nil 时沿用 client 全局配置）。
	// 推理型模型上限越大思考越久、响应越慢，短输出场景（意图分类、海龟汤出题/判定）
	// 应显式设小值控制耗时。
	MaxTokens *int `json:"max_tokens,omitempty"`
	// DisableThinking 是否禁用推理模型的"思考"（DeepSeek 等通过 thinking={"type":"disabled"} 支持）。
	// 推理模型可能把输出预算全花在 reasoning 上导致 content 为空，
	// 结构化短输出场景（海龟汤 JSON）应禁用思考，响应可降至秒级。
	DisableThinking *bool `json:"-"`
	// Sampling 采样参数覆盖（nil 字段沿用客户端默认值）。仅角色扮演链路填充。
	Sampling SamplingParams `json:"sampling,omitempty"`
}

// ChatResponse 是对话服务的返回
type ChatResponse struct {
	Content       string             `json:"content"`
	TokensUsed    int                `json:"tokens_used"`   // 兼容旧字段
	InputTokens   int                `json:"input_tokens"`  // 计费用
	OutputTokens  int                `json:"output_tokens"` // 计费用
	ToolCalls     []*schema.ToolCall `json:"tool_calls,omitempty"`
	InvolvedTools []string           `json:"involved_tools,omitempty"`
	// ToolArgs 本次对话中实际执行的工具调用参数（工具名 → 参数 JSON 字符串）。
	// 同一工具多次调用时保留最后一次；供上层（如表情情绪窗口）读取调用参数。
	ToolArgs map[string]string `json:"tool_args,omitempty"`
}

// UsageRecord LLM 用量记录（由各采集点上报给计费模块）。
type UsageRecord struct {
	Provider     string
	Model        string
	Scene        string
	UserID       int64
	GroupID      string
	Platform     string
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// LLMClient 抽象大语言模型的对话能力，是对话编排（ai.ChatService）与意图分析（intent.Analyzer）
// 共用的扩展点；具体实现（OpenAI / 本地模型等）由外部注入。
//
// 契约：
//   - 实现必须并发安全：同一实例会被多条消息的处理 goroutine 并发调用；
//   - Chat 必须尊重 ctx 的取消与超时，ctx 结束后应尽快返回；
//   - 返回 nil error 时必须返回非 nil 的 resp（Content 允许为空串，如推理模型只产出思考）；
//   - 返回 error 时由调用方降级处理，resp 视为无效。
//
// 注意：工具调用、流式等可选能力不在本接口内，经 EinoCapable / StreamingLLMClient
// 以类型断言探测；基础实现只需满足 Chat。
type LLMClient interface {
	// Chat 执行一次对话补全。
	//
	// 参数：
	//   - ctx：调用上下文，取消/超时后实现应尽快返回
	//   - req：对话请求，Messages 为完整消息序列（system 在前，历史在中间，最后一条为当前用户消息）
	//
	// 返回：补全结果（文本、token 用量与工具调用信息）；调用失败返回错误。
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

// EinoCapable 标记实现方为 EinoClient（或其代理），暴露 eino 专属能力：
// 工具调用、BaseModel（流式直连）、provider/model 标识，调用方用类型断言探测。
type EinoCapable interface {
	LLMClient
	// SupportsToolCalling 检查底层模型是否支持工具调用
	SupportsToolCalling() bool
	// ChatWithTools 返回绑定了工具的模型实例
	ChatWithTools(tools []*schema.ToolInfo) (model.BaseChatModel, error)
	// BaseModel 返回底层 eino BaseChatModel
	BaseModel() model.BaseChatModel
	// ProviderName 返回当前 Provider 名称
	ProviderName() string
	// ModelName 返回当前模型名
	ModelName() string
}

// SamplingDefaultsProvider 可选扩展：给出当前客户端建议的采样默认值。
// ChatService 在对话链路据此填充请求级参数；不实现该接口的客户端不启用惩罚。
type SamplingDefaultsProvider interface {
	SamplingDefaults() SamplingDefaults
}

// StreamingLLMClient 是 LLMClient 的可选扩展接口，表示支持流式响应。
// EinoClient 默认实现（eino BaseChatModel.Stream），调用方用类型断言探测。
type StreamingLLMClient interface {
	LLMClient
	// StreamChat 以流式方式返回聊天补全。
	// 调用方负责通过 StreamReader.Recv 消费 chunk，并在结束时调用 Close。
	StreamChat(ctx context.Context, req *ChatRequest) (*schema.StreamReader[*schema.Message], error)
	// StreamChatWithTools 绑定工具后以流式方式返回聊天补全。
	StreamChatWithTools(ctx context.Context, req *ChatRequest, tools []*schema.ToolInfo) (*schema.StreamReader[*schema.Message], error)
}
