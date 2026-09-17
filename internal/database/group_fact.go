package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// groupFactMu 保护 MergeGroupFacts 的"读旧值 → 合并 → 写回"序列，
// 按 groupID 隔离加锁：防止同一群的多个话题并行归档时并发读写，
// 导致同 key 事实的 confirm/矛盾更新互相覆盖（lost update）。
var groupFactMu sync.Map // groupID -> *sync.Mutex

// MergeGroupFacts 将新抽取的群事实并入该群画像（按群、主体、命题 upsert）。
//
// 复用 model.MergeFacts：同值重复确认提升置信度；同 key 异值视为矛盾、降置信并保留旧值到
// Conflict；新 key 追加。按 groupID 加锁串行化"读-改-写"，避免并发归档丢更新（lost update）。
func (db *DB) MergeGroupFacts(ctx context.Context, groupID string, incoming []model.FactItem) error {
	if groupID == "" || len(incoming) == 0 {
		return nil
	}
	muV, _ := groupFactMu.LoadOrStore(groupID, &sync.Mutex{})
	mu := muV.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	var rows []model.GroupFact
	if err := db.Orm.WithContext(ctx).Where("group_id = ?", groupID).Find(&rows).Error; err != nil {
		return fmt.Errorf("merge_group_facts read: %w", err)
	}
	existing := make([]model.FactItem, 0, len(rows))
	for _, r := range rows {
		existing = append(existing, rowToFactItem(r))
	}

	merged := model.MergeFacts(existing, incoming)

	now := time.Now()
	for _, f := range merged {
		if f.SubjectID == "" || f.Key == "" {
			continue // 旧数据保留，不将未知主体继续写回或晋升为公共事实
		}
		ev, _ := json.Marshal(f.Evidence)
		var target model.GroupFact
		err := db.Orm.WithContext(ctx).
			Where("group_id = ? AND subject_id = ? AND key = ?", groupID, f.SubjectID, f.Key).
			First(&target).Error
		if err == nil {
			// 更新既有行（key 是身份，矛盾分支只改 value/confidence）
			target.Value = f.Value
			target.Confidence = f.Confidence
			target.Evidence = ev
			target.Conflict = f.Conflict
			if !f.At.IsZero() {
				target.At = f.At
			}
			if err := db.Orm.WithContext(ctx).Save(&target).Error; err != nil {
				return fmt.Errorf("merge_group_facts update: %w", err)
			}
			continue
		}
		if err := db.Orm.WithContext(ctx).Create(&model.GroupFact{
			GroupID:    groupID,
			SubjectID:  f.SubjectID,
			Key:        f.Key,
			Value:      f.Value,
			Confidence: f.Confidence,
			Evidence:   ev,
			Conflict:   f.Conflict,
			At:         f.At,
			UpdatedAt:  now,
		}).Error; err != nil {
			return fmt.Errorf("merge_group_facts create: %w", err)
		}
	}
	return nil
}

// GetGroupFacts 返回某群主体明确的长期事实画像，按置信度降序取前 limit 条。
// 供群聊对话上下文注入（消费端按 FactMinConfidence / FactThinConfidence 门槛处理）。
func (db *DB) GetGroupFacts(ctx context.Context, groupID string, limit int) ([]model.FactItem, error) {
	if groupID == "" || limit <= 0 {
		return nil, nil
	}
	var rows []model.GroupFact
	err := db.Orm.WithContext(ctx).
		Where("group_id = ? AND subject_id <> ''", groupID).
		Order("confidence DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get_group_facts: %w", err)
	}
	out := make([]model.FactItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToFactItem(r))
	}
	return out, nil
}

// rowToFactItem 将 GroupFact 行转换为 FactItem（用于合并与消费端）。
func rowToFactItem(r model.GroupFact) model.FactItem {
	var ev []string
	if len(r.Evidence) > 0 {
		_ = json.Unmarshal(r.Evidence, &ev)
	}
	return model.FactItem{
		SubjectID:  r.SubjectID,
		Key:        r.Key,
		Value:      r.Value,
		Confidence: r.Confidence,
		Evidence:   ev,
		Conflict:   r.Conflict,
		At:         r.At,
	}
}
