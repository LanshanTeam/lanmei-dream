package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// ProviderManager 是 LLMClient 的托管实现：多个业务组件注入同一个实例，
// 切换活跃 Provider 时对调用方零感知（原子替换委托目标）。
// 它同时实现 LLMClient、EinoCapable 与 StreamingLLMClient，可直接作为业务组件的依赖注入。
//
// 并发安全：读路径在 RLock 下取出当前委托客户端引用后立即释放锁，
// 进行中的请求持有旧 client 引用可安全完成，不被切换中断。
type ProviderManager struct {
	mu        sync.RWMutex
	providers map[string]*Provider // key: Provider.Name
	active    string               // 当前活跃 Provider.Name
	client    LLMClient            // 活跃 Provider 对应的 EinoClient（同时实现 EinoCapable）
	usageHook UsageHook
	logger    *zap.Logger
}

// NewProviderManager 创建空的 ProviderManager。
func NewProviderManager(logger *zap.Logger) *ProviderManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ProviderManager{
		providers: make(map[string]*Provider),
		logger:    logger,
	}
}

// SetUsageHook 注入用量上报回调（热切换后自动保持）。
func (m *ProviderManager) SetUsageHook(hook UsageHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageHook = hook
	if ec, ok := m.client.(*EinoClient); ok {
		ec.SetUsageHook(hook)
	}
}

// buildClientLocked 为指定 Provider 构建 EinoClient，仅做配置装配、不发起网络请求
// （调用方需持写锁）。
func (m *ProviderManager) buildClientLocked(p *Provider) (*EinoClient, error) {
	if p == nil || p.Name == "" {
		return nil, fmt.Errorf("llm: provider 未配置")
	}
	if p.BaseURL == "" || p.Model == "" {
		return nil, fmt.Errorf("llm: provider %q 缺少 base_url 或 model", p.Name)
	}
	mt := p.MaxTokens
	if mt <= 0 {
		mt = 4096
	}
	client, err := NewEinoClient(context.Background(), &EinoOptions{
		Provider:          p.Name,
		BaseURL:           p.BaseURL,
		APIKey:            p.APIKey,
		Model:             p.Model,
		MaxTokens:         mt,
		Temperature:       p.Temperature,
		PresencePenalty:   p.PresencePenalty,
		FrequencyPenalty:  p.FrequencyPenalty,
		TopP:              p.TopP,
		TopK:              p.TopK,
		RepetitionPenalty: p.RepetitionPenalty,
		MinP:              p.MinP,
		TopA:              p.TopA,
		Seed:              p.Seed,
	})
	if err != nil {
		return nil, err
	}
	client.SetUsageHook(m.usageHook)
	return client, nil
}

// SetProviders 全量替换 Provider 列表，并保持当前活跃 Provider 不变。
// activeName 为空时选择第一个 enabled 且优先级最高的 Provider 作为活跃。
//
// 参数：
//   - providers：全量 Provider 列表；nil 或空项会被跳过
//
// 返回：当前活跃 Provider 名（可能为空 = 无可用 Provider）；活跃客户端构建失败时返回错误。
func (m *ProviderManager) SetProviders(providers []*Provider) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.providers = make(map[string]*Provider, len(providers))
	for _, p := range providers {
		if p == nil || p.Name == "" {
			continue
		}
		m.providers[p.Name] = p
	}

	// 活跃 Provider 不存在或已禁用 → 回退到最高优先级可用项
	if _, ok := m.providers[m.active]; !ok || !m.providers[m.active].Enabled {
		m.active = ""
		for _, p := range providers {
			if p != nil && p.Enabled && (m.active == "" || p.Priority > m.providers[m.active].Priority) {
				m.active = p.Name
			}
		}
	}

	return m.rebuildLocked()
}

// Switch 原子热切换到指定 Provider。
// 构建失败（配置非法/模型初始化失败）时保持原活跃不变并返回错误。
func (m *ProviderManager) Switch(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.providers[name]
	if !ok {
		return fmt.Errorf("llm: provider %q 不存在", name)
	}
	if !p.Enabled {
		return fmt.Errorf("llm: provider %q 已禁用", name)
	}

	client, err := m.buildClientLocked(p)
	if err != nil {
		return fmt.Errorf("llm: 构建 provider %q 客户端失败: %w", name, err)
	}
	m.client = client
	m.active = name
	m.logger.Info("llm: 活跃 provider 已切换", zap.String("provider", name), zap.String("model", p.Model))
	return nil
}

// rebuildLocked 根据当前 providers 重建活跃客户端（调用方需持写锁）。
func (m *ProviderManager) rebuildLocked() (string, error) {
	p, ok := m.providers[m.active]
	if !ok || !p.Enabled {
		m.client = nil
		return m.active, nil
	}
	client, err := m.buildClientLocked(p)
	if err != nil {
		m.client = nil
		m.logger.Warn("llm: 活跃 provider 客户端构建失败", zap.String("provider", p.Name), zap.Error(err))
		return m.active, err
	}
	m.client = client
	return m.active, nil
}

// ActiveProvider 返回当前活跃 Provider（nil = 无可用）。
func (m *ProviderManager) ActiveProvider() *Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[m.active]
	if !ok {
		return nil
	}
	return p
}

// Providers 返回全部 Provider（按名称排序的副本）。
func (m *ProviderManager) Providers() []*Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Provider, 0, len(m.providers))
	for _, p := range m.providers {
		cp := *p // 浅拷贝，防止调用方修改内部状态
		out = append(out, &cp)
	}
	return out
}

// current 取出当前委托客户端（读锁下获取引用，调用方在锁外使用）。
func (m *ProviderManager) current() LLMClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client
}

// Chat 委托给当前活跃客户端。
//
// 返回：无可用 Provider 时返回错误；其余情况原样返回活跃客户端的结果与错误。
func (m *ProviderManager) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	c := m.current()
	if c == nil {
		return nil, fmt.Errorf("llm: 无可用 provider（请先配置并启用）")
	}
	return c.Chat(ctx, req)
}

// SupportsToolCalling 委托当前活跃客户端。
func (m *ProviderManager) SupportsToolCalling() bool {
	c, _ := m.current().(EinoCapable)
	return c != nil && c.SupportsToolCalling()
}

// ChatWithTools 委托当前活跃客户端。
func (m *ProviderManager) ChatWithTools(tools []*schema.ToolInfo) (model.BaseChatModel, error) {
	c, ok := m.current().(EinoCapable)
	if !ok {
		return nil, fmt.Errorf("llm: 当前客户端不支持工具调用")
	}
	return c.ChatWithTools(tools)
}

// BaseModel 委托当前活跃客户端。
func (m *ProviderManager) BaseModel() model.BaseChatModel {
	c, ok := m.current().(EinoCapable)
	if !ok {
		return nil
	}
	return c.BaseModel()
}

// ProviderName 返回当前活跃 Provider 名。
func (m *ProviderManager) ProviderName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// ModelName 返回当前活跃模型名。
func (m *ProviderManager) ModelName() string {
	c, ok := m.current().(EinoCapable)
	if !ok {
		return ""
	}
	return c.ModelName()
}

// SamplingDefaults 委托当前活跃客户端的采样默认值。
func (m *ProviderManager) SamplingDefaults() SamplingDefaults {
	if c, ok := m.current().(SamplingDefaultsProvider); ok {
		return c.SamplingDefaults()
	}
	return SamplingDefaults{}
}

// StreamChat 委托当前活跃客户端。
func (m *ProviderManager) StreamChat(ctx context.Context, req *ChatRequest) (*schema.StreamReader[*schema.Message], error) {
	c, ok := m.current().(StreamingLLMClient)
	if !ok {
		return nil, fmt.Errorf("llm: 当前客户端不支持流式")
	}
	return c.StreamChat(ctx, req)
}

// StreamChatWithTools 委托当前活跃客户端。
func (m *ProviderManager) StreamChatWithTools(ctx context.Context, req *ChatRequest, tools []*schema.ToolInfo) (*schema.StreamReader[*schema.Message], error) {
	c, ok := m.current().(StreamingLLMClient)
	if !ok {
		return nil, fmt.Errorf("llm: 当前客户端不支持流式")
	}
	return c.StreamChatWithTools(ctx, req, tools)
}
