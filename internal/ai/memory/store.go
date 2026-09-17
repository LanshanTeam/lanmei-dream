// Package memory 定义长期记忆的存储/检索抽象（MemoryStore）与多路召回合并（MultiRetriever）。
package memory

import (
	"context"
	"time"
)

// Memory 表示一条长期记忆，用于 RAG 上下文增强。
// GroupID 标识来源群：空 = 个人记忆（私聊/用户画像），非空 = 群级记忆（话题归档，user_id=0）。
type Memory struct {
	ID         string
	UserID     int64
	GroupID    string
	Content    string
	Vector     []float32
	Metadata   map[string]any
	Similarity float64 // 与当前查询的余弦相似度，不是排名分数
	CreatedAt  time.Time
}

// MemoryStore 抽象记忆的存储与检索，PGVectorStore 是其 pgvector 实现。
//
// 群级过滤约定：groupID 为空仅检索用户个人记忆（group_id=”）；
// 非空时仅检索该群记忆（group_id=gid），禁止引用个人记忆。
//
// 契约：实现必须并发安全（对话记忆写入与话题归档、多路召回会从不同 goroutine 并发调用），
// 并尊重 ctx 的取消与超时；检索类方法失败时由调用方降级（返回错误但不中断对话）。
type MemoryStore interface {
	// Store 存储一条记忆（含向量），mem.GroupID 非空时写入群级记忆
	Store(ctx context.Context, mem *Memory) error
	// Retrieve 根据查询向量检索最相关的 N 条记忆（向量召回）
	Retrieve(ctx context.Context, queryVec []float32, userID int64, groupID string, limit int) ([]*Memory, error)
	// RetrieveByKeyword 根据关键词全文搜索检索记忆（关键词召回）
	RetrieveByKeyword(ctx context.Context, query string, userID int64, groupID string, limit int) ([]*Memory, error)
	// RetrieveByTime 根据时间倒序检索最近的 N 条记忆（时间召回）
	RetrieveByTime(ctx context.Context, userID int64, groupID string, limit int) ([]*Memory, error)
	// Delete 删除指定记忆
	Delete(ctx context.Context, id string) error
}
