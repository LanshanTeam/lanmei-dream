package model

import "time"

// 管理面板（Manager）专属数据模型

// AdminRole 管理员角色
type AdminRole string

const (
	// AdminRoleSuper 超级管理员，拥有全部权限（管理员账号管理等敏感操作仅限此角色）。
	AdminRoleSuper AdminRole = "super_admin" // 超级管理员：全部权限
	// AdminRoleNormal 普通管理员，可执行日常管理操作，部分敏感接口要求超管角色。
	AdminRoleNormal AdminRole = "admin" // 普通管理员：授权范围内的管理操作
)

// AdminStatus 管理员账号状态
type AdminStatus string

const (
	// AdminStatusActive 账号正常，允许登录；会话校验要求账号处于此状态。
	AdminStatusActive AdminStatus = "active" // 正常
	// AdminStatusDisabled 账号已禁用，禁止登录，已登录会话在中间件校验时也会被拒绝。
	AdminStatusDisabled AdminStatus = "disabled" // 禁用
)

// AuthSource 管理员凭据来源
type AuthSource string

const (
	// AuthSourceEnv 凭据来自环境变量引导：每次启动按 LANMEI_MANAGER_ADMIN_PASSWORD 重新派生密码哈希。
	AuthSourceEnv AuthSource = "env" // 环境变量引导（每次启动由 env 重派生哈希）
	// AuthSourceDB 凭据已被面板修改：引导流程不再用环境变量覆盖密码。
	AuthSourceDB AuthSource = "db" // 面板内修改过凭据（env 不再覆盖）
)

// ManagerAdmin 管理面板账户。
// 与 IM 内管理员（bot_admin，platform:userID）是两套体系。
type ManagerAdmin struct {
	ID           uint        `gorm:"primaryKey;autoIncrement" json:"id"`
	Username     string      `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string      `gorm:"size:255;not null" json:"-"` // argon2id 编码串（$argon2id$v=19$m=...）
	Role         AdminRole   `gorm:"size:16;not null;default:admin" json:"role"`
	Status       AdminStatus `gorm:"size:16;not null;default:active" json:"status"`
	AuthSource   AuthSource  `gorm:"size:16;not null;default:db" json:"auth_source"`
	DisplayName  string      `gorm:"size:64" json:"display_name"`
	Avatar       string      `gorm:"size:255" json:"avatar"`
	LastLoginAt  *time.Time  `json:"last_login_at"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// TableName 指定 GORM 表名为 manager_admin。
func (ManagerAdmin) TableName() string { return "manager_admin" }

// CredentialKind 认证凭据类型
type CredentialKind string

const (
	// CredentialPassword 密码凭据：manager_admin.PasswordHash（argon2id）的镜像记录，用于凭据枚举与审计追踪。
	CredentialPassword CredentialKind = "password" // argon2id 密码哈希（manager_admin.PasswordHash 的镜像，用于审计追踪）
	// CredentialTOTP TOTP 二次验证凭据，Data 为加密后的 TOTP 密钥。
	CredentialTOTP CredentialKind = "totp" // TOTP 二次验证
	// CredentialWebAuthn WebAuthn passkey 凭据，Data 为 webauthn.Credential 的 JSON 序列化。
	CredentialWebAuthn CredentialKind = "webauthn" // WebAuthn passkey
)

// AuthCredential 多因子认证凭据。
// Data 列按 Kind 区分编码：
//   - totp：AES-256-GCM 加密后的 TOTP 密钥（前缀 v1:）
//   - webauthn：webauthn.Credential 的 JSON 序列化
type AuthCredential struct {
	ID           uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	AdminID      uint           `gorm:"index;not null" json:"admin_id"`
	Kind         CredentialKind `gorm:"size:16;not null" json:"kind"`
	CredentialID string         `gorm:"size:512;index" json:"credential_id"` // webauthn credential id（base64url）；totp 时为唯一标识
	Data         []byte         `gorm:"type:bytea" json:"-"`                 // 见上说明
	Enabled      bool           `gorm:"not null;default:true" json:"enabled"`
	LastUsedAt   *time.Time     `json:"last_used_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// TableName 指定 GORM 表名为 auth_credential。
func (AuthCredential) TableName() string { return "auth_credential" }

// AuthSession 管理员登录会话（长期 Refresh Token 的载体）。
// RefreshHash 为 refresh token 的 SHA-256 摘要，明文 token 不落库。
// PrevRefreshHash 记录轮换前的旧摘要：旧 token 再次出现（复用攻击）时据此识别并吊销全部会话。
type AuthSession struct {
	ID              uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	AdminID         uint       `gorm:"index;not null" json:"admin_id"`
	RefreshHash     string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	PrevRefreshHash string     `gorm:"size:64" json:"-"`
	Device          string     `gorm:"size:128" json:"device"`
	IP              string     `gorm:"size:64" json:"ip"`
	UserAgent       string     `gorm:"size:255" json:"user_agent"`
	IssuedAt        time.Time  `json:"issued_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// TableName 指定 GORM 表名为 auth_session。
func (AuthSession) TableName() string { return "auth_session" }

// LoginMethod 登录方式
type LoginMethod string

const (
	// LoginMethodPassword 密码登录；账号绑定 TOTP 时验证密码后还需二次校验 TOTP。
	LoginMethodPassword LoginMethod = "password" // 密码 + TOTP
	// LoginMethodWebAuthn WebAuthn passkey 登录，无需密码。
	LoginMethodWebAuthn LoginMethod = "webauthn" // passkey
)

// LoginAttempt 登录尝试审计（用于失败锁定与安全告警）。
type LoginAttempt struct {
	ID        uint        `gorm:"primaryKey;autoIncrement" json:"id"`
	AdminID   *uint       `gorm:"index" json:"admin_id"`
	Username  string      `gorm:"size:64;index" json:"username"`
	IP        string      `gorm:"size:64" json:"ip"`
	UserAgent string      `gorm:"size:255" json:"user_agent"`
	Method    LoginMethod `gorm:"size:16" json:"method"`
	Success   bool        `gorm:"index" json:"success"`
	Reason    string      `gorm:"size:128" json:"reason"`
	CreatedAt time.Time   `gorm:"index" json:"created_at"`
}

// TableName 指定 GORM 表名为 login_attempt。
func (LoginAttempt) TableName() string { return "login_attempt" }

// AuditLog 操作审计日志（零信任：敏感操作全量留痕）。
type AuditLog struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	AdminID    *uint     `gorm:"index" json:"admin_id"`
	Username   string    `gorm:"size:64" json:"username"`
	Action     string    `gorm:"size:64;index" json:"action"` // 如 admin.create / llm.switch / conduit.bt.update
	TargetType string    `gorm:"size:32" json:"target_type"`
	TargetID   string    `gorm:"size:128" json:"target_id"`
	Detail     string    `gorm:"type:text" json:"detail"` // 变更前后值 JSON diff
	IP         string    `gorm:"size:64" json:"ip"`
	Result     string    `gorm:"size:16" json:"result"` // ok / deny / error
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// TableName 指定 GORM 表名为 audit_log。
func (AuditLog) TableName() string { return "audit_log" }

// ConfigScope 配置版本作用域
type ConfigScope string

const (
	// ConfigScopeConduit 管道（行为树/管线）配置，控制台保存管道时写入此作用域的修订。
	ConfigScopeConduit ConfigScope = "conduit"
	// ConfigScopePrompts 提示词配置的版本作用域。
	ConfigScopePrompts ConfigScope = "prompts"
	// ConfigScopeSkills 技能配置的版本作用域。
	ConfigScopeSkills ConfigScope = "skills"
	// ConfigScopeKnowledge 知识库配置的版本作用域。
	ConfigScopeKnowledge ConfigScope = "knowledge"
	// ConfigScopeBot Bot 全局配置的版本作用域。
	ConfigScopeBot ConfigScope = "bot"
)

// ConfigRevision 配置变更版本（支持回滚）。
type ConfigRevision struct {
	ID         uint        `gorm:"primaryKey;autoIncrement" json:"id"`
	Scope      ConfigScope `gorm:"size:32;index" json:"scope"`
	Name       string      `gorm:"size:128" json:"name"`
	Content    []byte      `gorm:"type:bytea" json:"content"` // JSON 快照
	Comment    string      `gorm:"size:255" json:"comment"`
	AuthorID   *uint       `json:"author_id"`
	AuthorName string      `gorm:"size:64" json:"author_name"`
	CreatedAt  time.Time   `json:"created_at"`
}

// TableName 指定 GORM 表名为 config_revision。
func (ConfigRevision) TableName() string { return "config_revision" }

// ConduitTrace 单条消息的执行 Trace（行为树 + 管线各节点状态与耗时）。
type ConduitTrace struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TraceID    string    `gorm:"size:64;index" json:"trace_id"`
	MessageID  string    `gorm:"size:128;index" json:"message_id"`
	UserID     string    `gorm:"size:64;index" json:"user_id"`
	GroupID    string    `gorm:"size:64;index" json:"group_id"`
	Platform   string    `gorm:"size:16;index" json:"platform"`
	Pipeline   string    `gorm:"size:64" json:"pipeline"`
	Status     string    `gorm:"size:16" json:"status"` // ok / error
	ErrMsg     string    `gorm:"size:512" json:"err_msg"`
	DurationMS int64     `json:"duration_ms"`
	Trace      []byte    `gorm:"type:bytea" json:"trace"` // conduit.TraceSpan JSON
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// TableName 指定 GORM 表名为 conduit_trace。
func (ConduitTrace) TableName() string { return "conduit_trace" }

// NodeTraffic 节点级流量聚合（按分钟分桶）。
// 供面板查看"经过某节点的流量"。
type NodeTraffic struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Bucket          time.Time `gorm:"index" json:"bucket"` // 分钟级桶
	PipelineID      string    `gorm:"size:64;index" json:"pipeline_id"`
	NodeName        string    `gorm:"size:128;index" json:"node_name"`
	Count           int64     `json:"count"`
	ErrorCount      int64     `json:"error_count"`
	TotalDurationMS int64     `json:"total_duration_ms"`
}

// TableName 指定 GORM 表名为 node_traffic。
func (NodeTraffic) TableName() string { return "node_traffic" }

// LLMProvider LLM Provider 配置（支持热切换 + 计费）。
// APIKey 以 AES-256-GCM 加密存储（主密钥 LANMEI_MANAGER_SECRET_KEY）。
type LLMProvider struct {
	ID          uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string  `gorm:"size:64;uniqueIndex;not null" json:"name"`
	BaseURL     string  `gorm:"size:255;not null" json:"base_url"`
	APIKeyEnc   []byte  `gorm:"type:bytea" json:"-"` // AES-256-GCM 加密后的 API Key
	Model       string  `gorm:"size:128;not null" json:"model"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// PresencePenalty / FrequencyPenalty 采样重复惩罚（0~2）。
	// 数据库默认值给到温和档位，避免存量 Provider 迁移后仍为 0（不生效）。
	PresencePenalty  float64 `gorm:"not null;default:0.3" json:"presence_penalty"`
	FrequencyPenalty float64 `gorm:"not null;default:0.2" json:"frequency_penalty"`
	// 酒馆风格采样参数（0 / 1 表示不启用）。
	TopP              float64   `gorm:"not null;default:0.95" json:"top_p"`
	TopK              int       `gorm:"not null;default:0" json:"top_k"`
	RepetitionPenalty float64   `gorm:"not null;default:1" json:"repetition_penalty"`
	MinP              float64   `gorm:"not null;default:0" json:"min_p"`
	TopA              float64   `gorm:"not null;default:0" json:"top_a"`
	Seed              int       `gorm:"not null;default:0" json:"seed"`
	InPricePerM       float64   `json:"in_price_per_m"`  // 每百万输入 token 价格（元）
	OutPricePerM      float64   `json:"out_price_per_m"` // 每百万输出 token 价格（元）
	Enabled           bool      `gorm:"not null;default:true" json:"enabled"`
	IsActive          bool      `gorm:"not null;default:false" json:"is_active"`
	Priority          int       `gorm:"not null;default:0" json:"priority"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// TableName 指定 GORM 表名为 llm_provider。
func (LLMProvider) TableName() string { return "llm_provider" }

// UsageScene token 用量场景
type UsageScene string

const (
	// UsageSceneChat 普通对话（主聊天链路）的 token 用量。
	UsageSceneChat UsageScene = "chat"
	// UsageSceneIntent 意图识别调用的 token 用量。
	UsageSceneIntent UsageScene = "intent"
	// UsageSceneCompress 记忆压缩（摘要生成）调用的 token 用量。
	UsageSceneCompress UsageScene = "compress"
	// UsageSceneTopic 话题标签/归档生成调用的 token 用量。
	UsageSceneTopic UsageScene = "topic"
	// UsageSceneVision 图片视觉理解与安全审核调用的 token 用量。
	UsageSceneVision UsageScene = "vision"
)

// TokenUsage Token 用量明细（计费基础数据）。
type TokenUsage struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Ts           time.Time  `gorm:"index" json:"ts"`
	Platform     string     `gorm:"size:16;index" json:"platform"`
	UserID       string     `gorm:"size:64;index" json:"user_id"`
	GroupID      string     `gorm:"size:64;index" json:"group_id"`
	Provider     string     `gorm:"size:64" json:"provider"`
	Model        string     `gorm:"size:128;index" json:"model"`
	Scene        UsageScene `gorm:"size:16;index" json:"scene"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	TotalTokens  int64      `json:"total_tokens"`
	CostCents    int64      `json:"cost_cents"` // 费用（分）
	MessageID    string     `gorm:"size:128" json:"message_id"`
	TraceID      string     `gorm:"size:64" json:"trace_id"`
	CreatedAt    time.Time  `json:"created_at"`
}

// TableName 指定 GORM 表名为 token_usage。
func (TokenUsage) TableName() string { return "token_usage" }

// GroupConfig 群级配置（群管理模块）。
// 白名单/黑名单为 JSON 数组（platform userID 列表）。
type GroupConfig struct {
	ID            uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Platform      string    `gorm:"size:16;not null" json:"platform"`
	GroupID       string    `gorm:"size:64;not null" json:"group_id"`
	BotEnabled    *bool     `json:"bot_enabled"`   // nil = 使用全局默认
	TopicEnabled  *bool     `json:"topic_enabled"` // nil = 使用全局默认
	CreditEnabled *bool     `json:"credit_enabled"`
	Whitelist     string    `gorm:"type:text" json:"whitelist"` // JSON: []string
	Blacklist     string    `gorm:"type:text" json:"blacklist"` // JSON: []string
	WelcomeMsg    string    `gorm:"type:text" json:"welcome_msg"`
	Remark        string    `gorm:"size:255" json:"remark"` // 管理标记/备注（仅面板可见）
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 指定 GORM 表名为 group_config。
func (GroupConfig) TableName() string { return "group_config" }

// ScheduledJob 定时任务。
// Action 为 JSON 描述的任务动作（如 {"type":"broadcast","group_id":"...","content":"..."}）。
type ScheduledJob struct {
	ID        uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string     `gorm:"size:64" json:"name"`
	Cron      string     `gorm:"size:64;not null" json:"cron"`
	Action    string     `gorm:"type:text" json:"action"`
	Enabled   bool       `gorm:"not null;default:true" json:"enabled"`
	LastRunAt *time.Time `json:"last_run_at"`
	NextRunAt *time.Time `json:"next_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TableName 指定 GORM 表名为 scheduled_job。
func (ScheduledJob) TableName() string { return "scheduled_job" }
