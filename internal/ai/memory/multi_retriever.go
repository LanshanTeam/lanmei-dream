package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// DefaultMinSimilarity 兼顾中文短句的召回覆盖率与无关记忆过滤。
const DefaultMinSimilarity = 0.60

// RecallWeight 定义召回权重；Time 只对相关候选做时间衰减加分，不独立召回。
type RecallWeight struct{ Vector, Keyword, Time float64 }

var DefaultRecallWeight = RecallWeight{Vector: 1.0, Keyword: 0.8, Time: 0.1}

type ScoredMemory struct {
	*Memory
	Score float64
}

type MultiRetriever struct {
	store         MemoryStore
	weights       RecallWeight
	minSimilarity float64
}

func NewMultiRetriever(store MemoryStore, weights RecallWeight) *MultiRetriever {
	return &MultiRetriever{store: store, weights: weights, minSimilarity: DefaultMinSimilarity}
}

// SetMinSimilarity 仅在初始化时配置，避免运行中与 Retrieve 并发修改。
func (r *MultiRetriever) SetMinSimilarity(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return fmt.Errorf("memory_min_similarity must be between 0 and 1")
	}
	r.minSimilarity = v
	return nil
}

// Retrieve 先过滤再融合；有查询向量时，关键词命中也不能绕过相似度门槛。
// 无向量时仅允许真正的关键词命中，不用最近记忆补齐。部分失败返回结果与错误供调用方观测。
func (r *MultiRetriever) Retrieve(ctx context.Context, queryVec []float32, query string, userID int64, groupID string, limit int) ([]*Memory, error) {
	if r.store == nil || limit <= 0 {
		return nil, nil
	}
	candidateLimit := min(limit*4, 100)
	scored := make(map[string]*ScoredMemory)
	var errs []error
	add := func(m *Memory, keyword bool) {
		if m == nil || strings.TrimSpace(m.Content) == "" {
			return
		}
		// 二次作用域校验，存储实现异常时也不得泄露私聊或跨群记忆。
		if m.GroupID != groupID || (groupID == "" && m.UserID != userID) {
			return
		}
		sim := m.Similarity
		if len(queryVec) > 0 {
			if keyword {
				sim = Cosine(queryVec, m.Vector)
			}
			if math.IsNaN(sim) || math.IsInf(sim, 0) || sim < r.minSimilarity {
				return
			}
		}
		copy := *m
		copy.Similarity = sim
		score := r.weights.Vector * sim
		if keyword {
			if len(queryVec) > 0 {
				score = r.weights.Keyword * sim
			} else {
				score = r.weights.Keyword
			}
		}
		key := m.ID
		if key == "" {
			key = normalizedContent(m.Content)
		}
		if old, ok := scored[key]; ok {
			old.Score += score
		} else {
			scored[key] = &ScoredMemory{Memory: &copy, Score: score}
		}
	}
	if len(queryVec) > 0 {
		items, err := r.store.Retrieve(ctx, queryVec, userID, groupID, candidateLimit)
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, m := range items {
				add(m, false)
			}
		}
	}
	if strings.TrimSpace(query) != "" {
		items, err := r.store.RetrieveByKeyword(ctx, query, userID, groupID, candidateLimit)
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, m := range items {
				add(m, true)
			}
		}
	}
	var ranked []*ScoredMemory
	for _, m := range scored {
		if !m.CreatedAt.IsZero() {
			age := max(0, time.Since(m.CreatedAt).Hours()/24)
			m.Score += r.weights.Time * math.Exp(-age/30)
		}
		ranked = append(ranked, m)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].ID < ranked[j].ID
		}
		return ranked[i].Score > ranked[j].Score
	})
	var result []*Memory
	for _, candidate := range ranked {
		duplicate := false
		for _, kept := range result {
			if sameMemory(candidate.Memory, kept) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, candidate.Memory)
		}
		if len(result) == limit {
			break
		}
	}
	return result, errors.Join(errs...)
}

// Cosine 返回真实余弦相似度；无向量、维度不一致或零向量不可比较。
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return -1
	}
	v := dot / math.Sqrt(aa*bb)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return -1
	}
	return max(-1, min(1, v))
}

func normalizedContent(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// sameMemory 仅消除近乎重复的表述；不同用户、数字或否定陈述不做语义合并。
func sameMemory(a, b *Memory) bool {
	if a.UserID != b.UserID || a.GroupID != b.GroupID {
		return false
	}
	x, y := normalizedContent(a.Content), normalizedContent(b.Content)
	if x == y {
		return true
	}
	for _, marker := range []string{"不", "没", "无", "未", "非"} {
		if strings.Contains(x, marker) != strings.Contains(y, marker) {
			return false
		}
	}
	digits := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) {
				return r
			}
			return -1
		}, s)
	}
	if digits(x) != digits(y) || Cosine(a.Vector, b.Vector) < 0.98 {
		return false
	}
	// 高向量相似度仍可能是相反事实，要求文本也高度重合，宁可少合并。
	grams := func(s string) map[string]bool {
		out := map[string]bool{}
		r := []rune(s)
		for i := 1; i < len(r); i++ {
			out[string(r[i-1:i+1])] = true
		}
		return out
	}
	gx, gy := grams(x), grams(y)
	shared := 0
	for g := range gx {
		if gy[g] {
			shared++
		}
	}
	union := len(gx) + len(gy) - shared
	return union > 0 && float64(shared)/float64(union) >= 0.8
}
