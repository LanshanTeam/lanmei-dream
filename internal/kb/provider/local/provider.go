// Package local 实现基于 PostgreSQL + pgvector + pg_trgm 的本地知识库 Provider。
//
// 能力：vector（pgvector HNSW 索引，余弦相似度）、fuzzy（pg_trgm GIN 倒排索引，
// 中英文子串/模糊匹配）、time（按 updated_at 倒序），以及内容摄入
// （docs_dir 下 Markdown/CSV 文件同步与 kb_add 工具写入）。
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// providerName 本地 provider 的类型标识。
const providerName = "local"

// importSubdir 面板文件导入子目录：上传文件落在 docs_dir/importSubdir 下，
// 既与人工维护的知识文件隔离，又处在 walkFiles 的递归扫描范围内
// （放到 docs_dir 之外的文件下次同步会被 deleteMissing 清掉分块）。
const importSubdir = "uploads"

// defaultFuzzyThreshold 模糊召回的最低相似度阈值。
const defaultFuzzyThreshold = 0.1

// Provider 基于 PostgreSQL 的本地知识库 Provider。
type Provider struct {
	orm            *gorm.DB
	kb             *kbpkg.KnowledgeBase
	embedder       embedding.Embedder // 可为 nil（向量列与 vector 模式降级）
	fuzzyThreshold float64
	docsDir        string
	skipCSVHeader  bool // 是否跳过 CSV 首行表头（默认 true）
	logger         *zap.Logger
}

// New 构建本地 provider。
func New(_ context.Context, kbb *kbpkg.KnowledgeBase, cfg map[string]any, deps kbpkg.Deps) (kbpkg.Provider, error) {
	if deps.Orm == nil {
		return nil, fmt.Errorf("kb local: 缺少数据库连接")
	}
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	threshold := kbpkg.ConfigFloat(cfg, "fuzzy_threshold", defaultFuzzyThreshold)
	if threshold <= 0 || threshold > 1 {
		threshold = defaultFuzzyThreshold
	}
	return &Provider{
		orm:            deps.Orm,
		kb:             kbb,
		embedder:       deps.Embedder,
		fuzzyThreshold: threshold,
		docsDir:        kbpkg.ConfigString(cfg, "docs_dir", ""),
		skipCSVHeader:  kbpkg.ConfigBool(cfg, "csv_skip_header", true),
		logger:         deps.Logger,
	}, nil
}

// Name 实现 kb.Provider。
func (p *Provider) Name() string { return providerName }

// Capabilities 实现 kb.Provider：支持向量/模糊/时间三种召回模式。
func (p *Provider) Capabilities() kbpkg.Capabilities {
	return kbpkg.Capabilities{Modes: []kbpkg.RecallMode{
		kbpkg.RecallModeVector,
		kbpkg.RecallModeFuzzy,
		kbpkg.RecallModeTime,
	}}
}

// Close 实现 kb.Provider：本地 provider 无独立连接需释放（复用全局连接池）。
func (p *Provider) Close() error { return nil }

// ImportDir 实现 kb.FileImporter：返回（必要时创建）管理面板的文件导入目录。
func (p *Provider) ImportDir() (string, error) {
	if p.docsDir == "" {
		return "", fmt.Errorf("kb local: 未配置 docs_dir，无法导入文件")
	}
	dir := filepath.Join(p.docsDir, importSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("kb local: 创建导入目录 %s: %w", dir, err)
	}
	return dir, nil
}

// toChunk 将数据库行映射为 provider 无关的分块表示。
func (p *Provider) toChunk(row *modelRow) *kbpkg.Chunk {
	meta := map[string]any{}
	if len(row.Meta) > 0 {
		_ = json.Unmarshal(row.Meta, &meta)
	}
	url := ""
	if v, ok := meta["url"].(string); ok {
		url = v
	}
	return &kbpkg.Chunk{
		ID:                strconv.FormatInt(row.ID, 10),
		KnowledgeBaseID:   row.KnowledgeBaseID,
		KnowledgeBaseName: p.kb.Name,
		Provider:          providerName,
		Title:             row.Title,
		Content:           row.Content,
		URL:               url,
		Meta:              meta,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
