package database

import (
	"context"
	"fmt"
	"strconv"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"go.uber.org/zap"
)

// currentVectorDim 返回指定表 embedding 列的实际维度；表/列不存在返回 0。
func (db *DB) currentVectorDim(ctx context.Context, table string) int {
	var colType string
	err := db.Orm.WithContext(ctx).Raw(
		`SELECT format_type(a.atttypid, a.atttypmod)
		 FROM pg_catalog.pg_attribute a
		 JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relname = ? AND a.attname = 'embedding'`,
		table,
	).Scan(&colType).Error
	if err != nil || colType == "" {
		return 0
	}
	// format_type 返回 "vector(1024)"，解析括号内数字
	const prefix = "vector("
	if len(colType) > len(prefix) && colType[:len(prefix)] == prefix {
		dim, err := strconv.Atoi(colType[len(prefix) : len(colType)-1])
		if err == nil {
			return dim
		}
	}
	return 0
}

// Migrate 使用 GORM AutoMigrate 自动建表（幂等），并确保 pgvector / pg_trgm 扩展和索引就绪。
//
// vectorDim 为向量列的目标维度（来自 ai.embedding_dim）：>0 且与列实际维度不同时，
// 对 memory_vectors 与 knowledge_chunks 的 embedding 列执行 ALTER 自适应。
func (db *DB) Migrate(ctx context.Context, vectorDim int) error {
	// 迁移顺序：先启用扩展再 AutoMigrate —— memory_vectors/knowledge_chunks 的 vector 列
	// 依赖 vector 类型，缺扩展会导致建表失败。
	if err := db.Orm.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return fmt.Errorf("enable pgvector: %w", err)
	}
	db.logger.Info("pgvector 扩展已启用")

	// pg_trgm 提供本地知识库模糊召回所需的倒排索引。
	if err := db.Orm.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
		return fmt.Errorf("enable pg_trgm: %w", err)
	}
	db.logger.Info("pg_trgm 扩展已启用")

	if err := db.Orm.WithContext(ctx).AutoMigrate(
		&model.User{},
		&model.Conversation{},
		&model.Memory{},
		&model.EpisodeSummary{},
		&model.TopicCluster{},
		&model.MemoryVector{},
		&model.MediaFile{},
		&model.PluginInstallation{},
		&model.PluginKV{},
		&model.KnowledgeChunk{},
		&model.StickerLibrary{},
		&model.RandomBeautyPool{},
		&model.BotAdmin{},
		&model.GroupFact{},
		// 管理面板（Manager）专属表
		&model.ManagerAdmin{},
		&model.AuthCredential{},
		&model.AuthSession{},
		&model.LoginAttempt{},
		&model.AuditLog{},
		&model.ConfigRevision{},
		&model.ConduitTrace{},
		&model.NodeTraffic{},
		&model.LLMProvider{},
		&model.TokenUsage{},
		&model.GroupConfig{},
		&model.ScheduledJob{},
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// 新主体唯一索引已由 AutoMigrate 建立，再移除旧索引；保留旧事实但不猜测主体。
	if err := db.migrateGroupFactIndex(ctx); err != nil {
		return err
	}

	// HNSW 向量索引（IF NOT EXISTS 幂等，已存在则跳过）。
	db.Orm.WithContext(ctx).Exec(
		"CREATE INDEX IF NOT EXISTS idx_memory_vectors_embedding ON memory_vectors USING hnsw (embedding vector_cosine_ops)",
	)
	db.logger.Info("HNSW 向量索引已就绪")

	// GIN 全文搜索索引，供关键词召回使用。
	db.Orm.WithContext(ctx).Exec(
		"CREATE INDEX IF NOT EXISTS idx_memory_vectors_search_vec ON memory_vectors USING gin (search_vec)",
	)
	db.logger.Info("GIN 全文搜索索引已就绪")

	// 创建触发器函数：INSERT/UPDATE 时自动从 content 生成 tsvector
	// 使用 simple 配置（不分词，按空白切割），适合中文等非空格分词语言
	db.Orm.WithContext(ctx).Exec(`
CREATE OR REPLACE FUNCTION memory_vectors_search_vec_trigger() RETURNS trigger AS $$
BEGIN
  NEW.search_vec := to_tsvector('simple', COALESCE(NEW.content, ''));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)

	// 幂等创建触发器（DROP IF EXISTS 再 CREATE，避免重复绑定）
	db.Orm.WithContext(ctx).Exec(`DROP TRIGGER IF EXISTS trg_memory_vectors_search_vec ON memory_vectors`)
	db.Orm.WithContext(ctx).Exec(`
CREATE TRIGGER trg_memory_vectors_search_vec
  BEFORE INSERT OR UPDATE OF content ON memory_vectors
  FOR EACH ROW EXECUTE FUNCTION memory_vectors_search_vec_trigger()`)
	db.logger.Info("全文搜索触发器已就绪")

	// 知识库向量召回索引（HNSW）与模糊召回索引（pg_trgm GIN）。
	db.Orm.WithContext(ctx).Exec(
		"CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_embedding ON knowledge_chunks USING hnsw (embedding vector_cosine_ops)",
	)
	// 模糊召回（pg_trgm GIN 倒排索引，中英文子串/模糊匹配）
	db.Orm.WithContext(ctx).Exec(
		"CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_trgm ON knowledge_chunks USING gin (content gin_trgm_ops)",
	)
	db.logger.Info("知识库索引已就绪")

	// 向量维度自适应：与配置的 ai.embedding_dim 保持一致
	// 注意：vector(N) 的类型修饰符无法参数化，N 为配置的整数维度（非用户输入），直接拼接安全。
	// memory_vectors 与 knowledge_chunks 需同时调整，否则记忆向量写入会因维度不符失败。
	// 以列的实际维度为基准（而非配置是否等于默认值）：从旧维度（如 1536）切回 1024 时
	// 同样需要 ALTER，否则列会永远停留在旧维度，向量写入全部失败且无告警。
	curDim := db.currentVectorDim(ctx, "memory_vectors")
	if vectorDim > 0 && curDim > 0 && curDim != vectorDim {
		// HNSW 索引依赖列维度，ALTER 前先删（幂等），ALTER 后重建
		db.Orm.WithContext(ctx).Exec("DROP INDEX IF EXISTS idx_memory_vectors_embedding")
		db.Orm.WithContext(ctx).Exec("DROP INDEX IF EXISTS idx_knowledge_chunks_embedding")
		if err := db.Orm.WithContext(ctx).Exec(
			fmt.Sprintf("ALTER TABLE memory_vectors ALTER COLUMN embedding TYPE vector(%d)", vectorDim),
		).Error; err != nil {
			return fmt.Errorf("alter memory_vectors embedding dimension: %w", err)
		}
		if err := db.Orm.WithContext(ctx).Exec(
			fmt.Sprintf("ALTER TABLE knowledge_chunks ALTER COLUMN embedding TYPE vector(%d)", vectorDim),
		).Error; err != nil {
			return fmt.Errorf("alter knowledge_chunks embedding dimension: %w", err)
		}
		// ALTER 后重建 HNSW 向量索引
		db.Orm.WithContext(ctx).Exec(
			"CREATE INDEX IF NOT EXISTS idx_memory_vectors_embedding ON memory_vectors USING hnsw (embedding vector_cosine_ops)",
		)
		db.Orm.WithContext(ctx).Exec(
			"CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_embedding ON knowledge_chunks USING hnsw (embedding vector_cosine_ops)",
		)
		db.logger.Info("向量维度已调整并重建索引", zap.Int("dim", vectorDim))
	}

	return nil
}

// migrateGroupFactIndex 允许同一群中不同主体拥有相同命题，重复启动可安全执行。
func (db *DB) migrateGroupFactIndex(ctx context.Context) error {
	if err := db.Orm.WithContext(ctx).Exec("DROP INDEX IF EXISTS uq_group_fact_key").Error; err != nil {
		return fmt.Errorf("migrate group fact index: %w", err)
	}
	return nil
}
