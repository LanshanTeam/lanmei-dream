package model

import (
	"encoding/json"
	"strings"
	"time"
)

// FactItem 一条结构化事实，带置信度与证据来源。
//
// Key 是命题主题（细粒度，同一 Key 只应有一种取值），同 Key 不同 Value 视为冲突，
// 降置信而非武断丢弃；Confidence 由 LLM 压缩时自评，跨摘要合并时重复确认提升、矛盾下降；
// Conflict 保留被替换的旧值（双结论供审阅），At 为最近一条证据来源的时间。
//
// 以 jsonb 存于 EpisodeSummary.Facts / TopicCluster.Facts，兼容旧版 []string（按 0.5 置信度转换）。
type FactItem struct {
	Kind       string    `json:"kind,omitempty"` // preference/profile/project/commitment
	Importance float64   `json:"importance,omitempty"`
	Durable    bool      `json:"durable,omitempty"`
	Quote      string    `json:"quote,omitempty"`      // 用户原文中的直接证据
	SubjectID  string    `json:"subject_id,omitempty"` // 群事实主体：平台用户 ID；group 表示群公共事实
	Key        string    `json:"key,omitempty"`
	Value      string    `json:"value"`
	Confidence float64   `json:"confidence"`
	Evidence   []string  `json:"evidence,omitempty"` // 证据来源标识（对话批次，如 "conv:12-20"）
	Conflict   string    `json:"conflict,omitempty"`
	At         time.Time `json:"at,omitempty"`
}

// maxFactConfidence 重复确认的置信度封顶：任何路径都到不了 1.0。
const maxFactConfidence = 0.98

// factBumpStep 每次重复确认的置信度提升步长。
const factBumpStep = 0.05

// factConflictFloor 矛盾降置信的保底值（distill：0.2）。
const factConflictFloor = 0.2

// factConflictPenalty 矛盾时的置信度惩罚（distill：−0.15）。
const factConflictPenalty = 0.15

// maxFactEvidence 单条事实保留的证据条数上限（防止行被撑大）。
const maxFactEvidence = 50

// factDefaultConfidence 无置信度信息（旧数据/LLM 未给出）时的兜底值。
const factDefaultConfidence = 0.5

// 消费端门槛（越靠近 agent 门槛越高）：低于 FactMinConfidence 的事实不进对话上下文；
// FactMinConfidence~FactThinConfidence（0.5~0.65）之间的事实进上下文但标注"证据较少"。
const (
	FactMinConfidence  = 0.5
	FactThinConfidence = 0.65
)

// ParseFacts 解析 Facts jsonb，兼容两种形态：
//   - []FactItem（新）：直接采用；
//   - []string（旧）：转换为置信度 0.5 的事实。
//
// 解析失败/空返回 nil（调用方可安全 for 循环）。
func ParseFacts(raw []byte) []FactItem {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	var items []FactItem
	if err := json.Unmarshal(raw, &items); err == nil {
		out := make([]FactItem, 0, len(items))
		for _, it := range items {
			it.Value = strings.TrimSpace(it.Value)
			if it.Value == "" {
				continue
			}
			it.Confidence = normalizeFactConfidence(it.Confidence)
			out = append(out, it)
		}
		return out
	}

	// 旧格式：[]string
	var legacy []string
	if err := json.Unmarshal(raw, &legacy); err == nil {
		out := make([]FactItem, 0, len(legacy))
		for _, s := range legacy {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, FactItem{Value: s, Confidence: factDefaultConfidence})
		}
		return out
	}
	return nil
}

// MarshalFacts 序列化 []FactItem 为 jsonb（nil 时输出 "[]"）。
func MarshalFacts(facts []FactItem) []byte {
	if facts == nil {
		facts = []FactItem{}
	}
	data, err := json.Marshal(facts)
	if err != nil {
		return []byte("[]")
	}
	return data
}

// MergeFacts 跨摘要合并事实集合，在同一 SubjectID 内按 value 精确匹配区分三态：新 value 追加；同 value 重复
// 确认则提升置信度并合并证据；同 Key 但 value 不同视为矛盾，降置信、保留新 value 并把旧值
// 记入 Conflict。无 Key 或 Key 不同的取值各自保留——矛盾暴露不确定性，降置信而非删除。
func MergeFacts(existing, incoming []FactItem) []FactItem {
	out := make([]FactItem, 0, len(existing)+len(incoming))
	type identity struct{ subject, text string }
	valueIdx := make(map[identity]int, len(existing)+len(incoming)) // (主体, value) -> out 索引
	keyIdx := make(map[identity]int, len(existing)+len(incoming))   // (主体, key) -> out 索引

	for _, f := range existing {
		f.Value = strings.TrimSpace(f.Value)
		if f.Value == "" {
			continue
		}
		if _, dup := valueIdx[identity{f.SubjectID, f.Value}]; dup {
			continue
		}
		f.Confidence = normalizeFactConfidence(f.Confidence)
		valueIdx[identity{f.SubjectID, f.Value}] = len(out)
		if f.Key != "" {
			keyIdx[identity{f.SubjectID, f.Key}] = len(out)
		}
		out = append(out, f)
	}

	for _, f := range incoming {
		f.Value = strings.TrimSpace(f.Value)
		if f.Value == "" {
			continue
		}
		f.Confidence = normalizeFactConfidence(f.Confidence)

		// 确认：同一 value 重复出现
		if i, ok := valueIdx[identity{f.SubjectID, f.Value}]; ok {
			if f.Durable && f.Quote != "" {
				out[i].Kind = f.Kind
				out[i].Importance = f.Importance
				out[i].Durable = f.Durable
				out[i].Quote = f.Quote
			}
			out[i].Confidence = min(max(out[i].Confidence, f.Confidence)+factBumpStep, maxFactConfidence)
			out[i].Evidence = mergeFactEvidence(out[i].Evidence, f.Evidence)
			if f.At.After(out[i].At) {
				out[i].At = f.At
			}
			if f.Key != "" {
				keyIdx[identity{f.SubjectID, f.Key}] = i
			}
			continue
		}

		// 矛盾：同 Key 但 value 不同（同一命题出现相反/不同取值）
		if f.Key != "" {
			if j, ok := keyIdx[identity{f.SubjectID, f.Key}]; ok && out[j].Value != f.Value {
				old := out[j]
				nc := min(old.Confidence, f.Confidence) - factConflictPenalty
				if nc < factConflictFloor {
					nc = factConflictFloor
				}
				out[j].Value = f.Value
				out[j].Kind = f.Kind
				out[j].Importance = f.Importance
				out[j].Durable = f.Durable
				out[j].Quote = f.Quote
				out[j].Confidence = nc
				out[j].Conflict = old.Value
				out[j].Evidence = mergeFactEvidence(old.Evidence, f.Evidence)
				if f.At.After(old.At) {
					out[j].At = f.At
				}
				delete(valueIdx, identity{old.SubjectID, old.Value})
				valueIdx[identity{f.SubjectID, f.Value}] = j
				keyIdx[identity{f.SubjectID, f.Key}] = j
				continue
			}
		}

		// 插入：新事实
		valueIdx[identity{f.SubjectID, f.Value}] = len(out)
		if f.Key != "" {
			keyIdx[identity{f.SubjectID, f.Key}] = len(out)
		}
		out = append(out, f)
	}
	return out
}

// mergeFactEvidence 合并证据列表（去重、新证据在前、上限 maxFactEvidence）。
func mergeFactEvidence(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, e := range incoming {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		merged = append(merged, e)
	}
	for _, e := range existing {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		merged = append(merged, e)
	}
	if len(merged) > maxFactEvidence {
		merged = merged[:maxFactEvidence]
	}
	return merged
}

// normalizeFactConfidence 归一化置信度：越界收敛 0~1；垃圾值（NaN）按 0.5 兜底。
func normalizeFactConfidence(v float64) float64 {
	if v != v { // NaN
		return factDefaultConfidence
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
