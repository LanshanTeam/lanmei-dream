package kb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// startupSyncTimeout 启动时内容同步的软超时（docs_dir 文件摄入）。
const startupSyncTimeout = 30 * time.Second

// defaultAutoRecallLimit 隐式召回注入条数的默认值。
const defaultAutoRecallLimit = 3

// Service 是知识库系统对外门面：按配置构建可插拔 provider，对外提供召回入口
// （隐式 RAG 与 kb_search 工具共用）与 LLM 工具注册（主动召回）。
//
// 各方法对 nil 接收者安全：未启用知识库（NewService 返回 nil）时召回恒为空、列表为空，
// 调用方无需判空即可直接使用。
type Service struct {
	engine       *Engine
	defaultModes []RecallMode
	autoLimit    int
	logger       *zap.Logger
}

// NewService 依据配置构建知识库服务；provider 工厂需在调用前通过 RegisterProvider 注册。
// 单个知识库配置非法或构造失败时跳过并告警，不影响其它库；无任何可用知识库时仍返回
// Service（召回恒为空），由调用方决定是否启用。
//
// 参数：
//   - ctx：构造上下文，同时用于各 provider 的构造与启动同步
//   - cfg：知识库配置；为 nil 时直接返回 (nil, nil)，表示未配置知识库
//   - orm：本地 provider 的数据库连接（可为 nil，local provider 构造会失败并跳过）
//   - embedder：向量计算（可为 nil，vector 模式降级）
//   - logger：日志器（nil 时兜底为 zap.NewNop）
//
// 返回：cfg 为 nil 时返回 (nil, nil)；否则返回可用的 Service（error 当前恒为 nil，
// 单库失败已内部跳过并告警）
//
// 注意：多路召回权重取配置值，仅当三项权重全为 0 时回退内置默认值；
// 启动同步带 30 秒软超时，失败仅告警不阻塞启动。
func NewService(ctx context.Context, cfg *config.KnowledgeConfig, orm *gorm.DB, embedder embedding.Embedder, logger *zap.Logger) (*Service, error) {
	if cfg == nil {
		return nil, nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	// 多路召回权重直接采用配置值（viper SetDefault 已提供 1.0/0.8/0.5 默认）；
	// 仅当三项全为 0（配置与默认均未提供）时回退内置默认值。
	weights := RecallWeights{
		Vector: cfg.Weights.Vector,
		Fuzzy:  cfg.Weights.Fuzzy,
		Time:   cfg.Weights.Time,
	}
	if weights.Vector == 0 && weights.Fuzzy == 0 && weights.Time == 0 {
		weights = DefaultRecallWeights
	}

	eng := NewEngine(weights, embedder, logger)
	deps := Deps{Orm: orm, Embedder: embedder, Logger: logger}

	for _, bc := range cfg.Bases {
		if !bc.Enabled {
			continue
		}
		if bc.ID == "" {
			logger.Warn("kb: 知识库配置缺少 id，已跳过")
			continue
		}
		if bc.Provider == "" {
			logger.Warn("kb: 知识库配置缺少 provider", zap.String("kb", bc.ID))
			continue
		}
		kbb := &KnowledgeBase{
			ID:          bc.ID,
			Name:        bc.Name,
			Description: bc.Description,
			Provider:    bc.Provider,
			Enabled:     true,
			RecallLimit: bc.RecallLimit,
			Config:      bc.Config,
		}
		p, err := createProvider(ctx, kbb, deps)
		if err != nil {
			logger.Warn("kb: 知识库构造失败", zap.String("kb", bc.ID), zap.Error(err))
			continue
		}
		eng.AddProvider(kbb, p)

		// 启动时同步（本地 provider 的 docs_dir 文件摄入）
		if syncr, ok := p.(Syncer); ok {
			syncCtx, cancel := context.WithTimeout(ctx, startupSyncTimeout)
			if err := syncr.Sync(syncCtx); err != nil {
				logger.Warn("kb: 启动同步失败", zap.String("kb", bc.ID), zap.Error(err))
			}
			cancel()
		}
		logger.Info("kb: 知识库就绪",
			zap.String("kb", bc.ID),
			zap.String("name", bc.Name),
			zap.String("provider", bc.Provider),
		)
	}

	autoLimit := cfg.AutoRecallLimit
	if autoLimit <= 0 {
		autoLimit = defaultAutoRecallLimit
	}

	return &Service{
		engine:       eng,
		defaultModes: parseModes(cfg.DefaultModes),
		autoLimit:    autoLimit,
		logger:       logger,
	}, nil
}

// Recall 执行召回（隐式 RAG 与 kb_search 工具共用入口）。
//
// 参数：
//   - ctx：召回上下文
//   - req：召回请求；Modes 传 s.DefaultModes() 即按配置的默认模式召回
//
// 返回：按合并分数降序的召回结果；未启用知识库（s 为 nil）或 Query 为空时返回 nil, nil；
// 详见 Engine.Recall 的加权/去重/阈值语义
func (s *Service) Recall(ctx context.Context, req *RecallRequest) ([]ScoredChunk, error) {
	if s == nil || s.engine == nil {
		return nil, nil
	}
	return s.engine.Recall(ctx, req)
}

// DefaultModes 返回配置的默认召回模式（空表示使用 provider 全部能力）。
func (s *Service) DefaultModes() []RecallMode {
	if s == nil {
		return nil
	}
	return s.defaultModes
}

// AutoRecallLimit 返回隐式召回注入条数。
func (s *Service) AutoRecallLimit() int {
	if s == nil {
		return 0
	}
	return s.autoLimit
}

// List 返回全部已加载知识库。
func (s *Service) List() []KnowledgeBase {
	if s == nil || s.engine == nil {
		return nil
	}
	return s.engine.List()
}

// ImportDir 返回指定知识库的文件导入目录（管理面板上传入口）。
//
// 参数：
//   - kbID：目标知识库 ID；为空时取唯一支持文件导入的知识库
//
// 返回：目录路径（不存在时由 provider 创建）；知识库未启用、kbID 不存在、
// provider 不支持文件导入，或为空 kbID 但支持导入的库不是恰好一个时返回错误
//
// 注意：支持导入的库多于一个时必须显式指定 kbID，否则文件会落到不可预期的库
func (s *Service) ImportDir(kbID string) (string, error) {
	if s == nil || s.engine == nil {
		return "", errors.New("kb: 知识库未启用")
	}
	if kbID != "" {
		p, ok := s.engine.Provider(kbID)
		if !ok {
			return "", fmt.Errorf("kb: 知识库 %q 不存在", kbID)
		}
		fi, ok := p.(FileImporter)
		if !ok {
			return "", fmt.Errorf("kb: 知识库 %q 不支持文件导入", kbID)
		}
		return fi.ImportDir()
	}

	dir := ""
	found := 0
	for _, kbb := range s.engine.List() {
		p, ok := s.engine.Provider(kbb.ID)
		if !ok {
			continue
		}
		fi, ok := p.(FileImporter)
		if !ok {
			continue
		}
		d, err := fi.ImportDir()
		if err != nil {
			return "", err
		}
		dir = d
		found++
	}
	switch found {
	case 0:
		return "", errors.New("kb: 没有支持文件导入的知识库")
	case 1:
		return dir, nil
	default:
		return "", errors.New("kb: 存在多个支持文件导入的知识库，请指定 base")
	}
}

// Sync 触发内容重同步（管理面板"重新同步"入口）。
// kbID 为空时同步全部实现了 Syncer 的知识库；单个失败不中断其余。
//
// 参数：
//   - ctx：同步上下文，由调用方控制超时
//   - kbID：目标知识库 ID；为空时同步全部支持同步的知识库
//
// 返回：指定 kbID 时，库不存在或 provider 不支持同步、同步失败均返回错误；
// kbID 为空时返回首个失败知识库的错误（其余继续同步），全部成功返回 nil
//
// 注意：未启用知识库（s 为 nil）时直接返回 nil；同步与召回可能并发执行，
// 实现的并发安全由 Provider 保证。
func (s *Service) Sync(ctx context.Context, kbID string) error {
	if s == nil || s.engine == nil {
		return nil
	}
	if kbID != "" {
		p, ok := s.engine.Provider(kbID)
		if !ok {
			return fmt.Errorf("kb: 知识库 %q 不存在", kbID)
		}
		syncr, ok := p.(Syncer)
		if !ok {
			return fmt.Errorf("kb: 知识库 %q 的 provider 不支持内容同步", kbID)
		}
		return syncr.Sync(ctx)
	}
	var firstErr error
	for _, kbb := range s.engine.List() {
		p, ok := s.engine.Provider(kbb.ID)
		if !ok {
			continue
		}
		if syncr, ok := p.(Syncer); ok {
			if err := syncr.Sync(ctx); err != nil && firstErr == nil {
				firstErr = err
				s.logger.Warn("kb: 内容重同步失败", zap.String("kb", kbb.ID), zap.Error(err))
			}
		}
	}
	return firstErr
}

// Close 关闭全部 provider。
func (s *Service) Close() {
	if s != nil && s.engine != nil {
		s.engine.Close()
	}
}

// RegisterTools 将主动召回工具注册进 AI 工具注册表。
// 注册后自动参与 eino 工具调用循环与意图分析工具列表。
//
// 参数：
//   - reg：AI 工具注册表；为 nil 时直接返回 nil
//
// 返回：工具重名等注册失败时返回错误，已注册的工具不回滚
//
// 注意：kb_search 始终注册；kb_add 仅在存在 local provider 知识库时注册（远程 provider 不支持写入）。
func (s *Service) RegisterTools(reg *tool.Registry) error {
	if reg == nil {
		return nil
	}
	if err := reg.Register(kbSearchTool(s)); err != nil {
		return err
	}
	// kb_add 仅在存在本地知识库时提供（远程 provider 不支持写入）
	if s.hasLocalBase() {
		if err := reg.Register(kbAddTool(s)); err != nil {
			return err
		}
	}
	return nil
}

// hasLocalBase 判断是否存在 local provider 知识库。
func (s *Service) hasLocalBase() bool {
	for _, kbb := range s.List() {
		if kbb.Provider == "local" {
			return true
		}
	}
	return false
}

// parseModes 将配置的字符串模式列表解析为 RecallMode 列表（过滤非法值与重复项）。
func parseModes(modes []string) []RecallMode {
	out := make([]RecallMode, 0, len(modes))
	seen := make(map[RecallMode]struct{}, len(modes))
	for _, m := range modes {
		mode := RecallMode(m)
		if !mode.Valid() {
			continue
		}
		if _, dup := seen[mode]; dup {
			continue
		}
		seen[mode] = struct{}{}
		out = append(out, mode)
	}
	return out
}
