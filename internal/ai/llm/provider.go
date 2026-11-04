package llm

// Provider 运行时的 LLM Provider 配置。
// APIKey 明文仅存在于内存（数据库侧以 AES-256-GCM 加密存储）。
type Provider struct {
	ID uint
	// Name Provider 唯一名称，同时作为 ProviderManager 的查找键。
	Name string
	// BaseURL OpenAI 兼容 API 根地址。
	BaseURL string
	APIKey  string
	// Model 模型名。
	Model string
	// MaxTokens 单次输出 token 上限；<=0 时由 ProviderManager 兜底为 4096。
	MaxTokens   int
	Temperature float64
	// PresencePenalty / FrequencyPenalty 采样重复惩罚（0~2，0 表示不启用）。
	PresencePenalty  float64
	FrequencyPenalty float64
	// 酒馆风格采样参数（0 / 1 表示不启用）。
	TopP              float64
	TopK              int
	RepetitionPenalty float64
	MinP              float64
	TopA              float64
	Seed              int
	InPricePerM       float64 // 每百万输入 token 价格（元）
	OutPricePerM      float64 // 每百万输出 token 价格（元）
	// Enabled 是否启用；禁用的 Provider 不会被选为活跃，也不能被 Switch 切换。
	Enabled bool
	// Priority 优先级，数值越大越优先（启用集合中取最高者作为默认活跃）。
	Priority int
}

// CostCents 按定价表计算一次调用的费用（分）。
// 价格为 0 视为不计费（免费 provider）。
//
// 参数：
//   - inputTokens：输入 token 数
//   - outputTokens：输出 token 数
//
// 返回：四舍五入到分的费用；不计费时返回 0。
func (p *Provider) CostCents(inputTokens, outputTokens int64) int64 {
	if p == nil || (p.InPricePerM <= 0 && p.OutPricePerM <= 0) {
		return 0
	}
	inCost := float64(inputTokens) * p.InPricePerM / 1_000_000
	outCost := float64(outputTokens) * p.OutPricePerM / 1_000_000
	// 四舍五入到分
	return int64((inCost+outCost)*100 + 0.5)
}
