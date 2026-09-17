package infra

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// 确保 PGVectorStore 实现 memory.MemoryStore 接口
var _ memory.MemoryStore = (*PGVectorStore)(nil)

// PGVectorStore 基于 PostgreSQL + pgvector 的 MemoryStore 实现
type PGVectorStore struct {
	orm *gorm.DB
}

// NewPGVectorStore 创建基于 pgvector 的记忆存储。
// db 为共享的 GORM 连接（表由 database.Migrate 建好），本存储不接管其生命周期。
func NewPGVectorStore(db *gorm.DB) *PGVectorStore {
	return &PGVectorStore{orm: db}
}

// Store 存储一条记忆（含向量）。
// mem.GroupID 非空时写入群级记忆（user_id=0 的场景由调用方自行设置）。
// 插入失败时原样返回 GORM 错误。
func (s *PGVectorStore) Store(ctx context.Context, mem *memory.Memory) error {
	row := &model.MemoryVector{
		UserID:    mem.UserID,
		GroupID:   mem.GroupID,
		Content:   mem.Content,
		Embedding: pgvector.NewVector(mem.Vector),
	}
	return s.orm.WithContext(ctx).Create(row).Error
}

// memoryGroupScope 构造记忆检索的群级过滤条件。
//   - groupID 为空（私聊）：仅用户个人记忆（group_id=”）；
//   - groupID 非空（群聊）：仅本群记忆，禁止引用私聊或其他群的记忆。
func memoryGroupScope(groupID string, userID int64) (scope string, args []any) {
	if groupID == "" {
		return "user_id = ? AND group_id = ''", []any{userID}
	}
	return "group_id = ?", []any{groupID}
}

// Retrieve 根据查询向量检索最相关的 N 条记忆（向量召回）。
// 按余弦距离（<=>）升序取 limit 条；检索范围见 memoryGroupScope。
// 查询失败时以 pgvector retrieve 前缀包装返回错误。
func (s *PGVectorStore) Retrieve(ctx context.Context, queryVec []float32, userID int64, groupID string, limit int) ([]*memory.Memory, error) {
	// 显式 ::vector 转换：向量参数化后以 text 到达，<=> 无法从 $n 推断 vector
	// 类型，缺 cast 会报 operator does not exist: vector <=> text。
	vecStr := formatVector(queryVec)
	scope, args := memoryGroupScope(groupID, userID)

	var rows []struct {
		model.MemoryVector
		Similarity float64
	}
	err := s.orm.WithContext(ctx).Raw(
		`SELECT *, 1 - (embedding <=> ?::vector) AS similarity FROM memory_vectors WHERE (`+scope+`) ORDER BY embedding <=> ?::vector LIMIT ?`,
		append(append([]any{vecStr}, args...), vecStr, limit)...,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector retrieve: %w", err)
	}

	result := make([]*memory.Memory, 0, len(rows))
	for _, row := range rows {
		m := rowsToMemories([]model.MemoryVector{row.MemoryVector})[0]
		m.Similarity = row.Similarity
		result = append(result, m)
	}
	return result, nil
}

// Delete 删除指定 ID 的记忆。
// id 为十进制主键字符串，解析失败时返回错误；目标不存在时不报错（影响 0 行）。
func (s *PGVectorStore) Delete(ctx context.Context, id string) error {
	pk, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid id %q: %w", id, err)
	}
	return s.orm.WithContext(ctx).Delete(&model.MemoryVector{}, pk).Error
}

// RetrieveByKeyword 根据关键词全文搜索检索记忆（关键词召回）
// 使用 PostgreSQL tsvector 全文搜索，simple 配置按空白切割适合中文
func (s *PGVectorStore) RetrieveByKeyword(ctx context.Context, query string, userID int64, groupID string, limit int) ([]*memory.Memory, error) {
	tsQuery := toSimpleTSQuery(query)
	if tsQuery == "" {
		return nil, nil
	}
	scope, args := memoryGroupScope(groupID, userID)
	whereSQL := "(" + scope + ") AND search_vec @@ to_tsquery('simple', ?)"
	args = append(args, tsQuery)

	var rows []model.MemoryVector
	err := s.orm.WithContext(ctx).Raw(
		"SELECT * FROM memory_vectors WHERE "+whereSQL+
			" ORDER BY ts_rank(search_vec, to_tsquery('simple', ?)) DESC LIMIT ?",
		append(args, tsQuery, limit)...,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector keyword retrieve: %w", err)
	}

	return rowsToMemories(rows), nil
}

// RetrieveByTime 根据时间倒序检索最近的 N 条记忆（时间召回）
func (s *PGVectorStore) RetrieveByTime(ctx context.Context, userID int64, groupID string, limit int) ([]*memory.Memory, error) {
	scope, args := memoryGroupScope(groupID, userID)

	var rows []model.MemoryVector
	err := s.orm.WithContext(ctx).
		Where(scope, args...).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector time retrieve: %w", err)
	}

	return rowsToMemories(rows), nil
}

// toSimpleTSQuery 将自然语言查询转为 simple 配置的 tsquery：按空白分割后用
// & (AND) 连接各词项。
// 必须先收集全部词项再连接，不能逐项追加 "&" 后缀——否则末词会变成 "词&"，
// PostgreSQL 因缺操作数报 "no operand in tsquery"。
// 词项用单引号包裹并转义内部单引号：用户输入含 & | ! ( ) : ' 等 tsquery 操作符时
// 会导致语法错误（如 "C & C++" → "C & & C++"），包裹后按普通词项解析。
func toSimpleTSQuery(query string) string {
	var parts []string
	for _, w := range splitWhitespace(query) {
		if w != "" {
			parts = append(parts, "'"+strings.ReplaceAll(w, "'", "''")+"'")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " & ")
}

// splitWhitespace 按空白字符（空格/制表/换行）分割字符串。
func splitWhitespace(s string) []string {
	var fields []string
	var buf []rune
	for _, r := range s {
		if isWhitespace(r) {
			if len(buf) > 0 {
				fields = append(fields, string(buf))
				buf = buf[:0]
			}
		} else {
			buf = append(buf, r)
		}
	}
	if len(buf) > 0 {
		fields = append(fields, string(buf))
	}
	return fields
}

func isWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// rowsToMemories 将数据库行转换为 Memory 切片
func rowsToMemories(rows []model.MemoryVector) []*memory.Memory {
	memories := make([]*memory.Memory, len(rows))
	for i, row := range rows {
		memories[i] = &memory.Memory{
			ID:        strconv.FormatInt(row.ID, 10),
			UserID:    row.UserID,
			GroupID:   row.GroupID,
			Content:   row.Content,
			Vector:    row.Embedding.Slice(),
			CreatedAt: row.CreatedAt,
		}
	}
	return memories
}

// formatVector 将 float32 切片格式化为 SQL 向量字面量 '[0.1,0.2,...]'
func formatVector(vec []float32) string {
	s := "["
	for i, v := range vec {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%f", v)
	}
	s += "]"
	return s
}
