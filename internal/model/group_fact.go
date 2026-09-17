package model

import "time"

// GroupFact 群聊长期事实画像（群级记忆的事实层）。
//
// 与个人 EpisodeSummary/TopicCluster 把事实内嵌在 jsonb 不同，群画像用独立表按
// (group_id, subject_id, key) 唯一，避免不同成员的同类事实互相覆盖；Evidence 记录来源
// topic_id，供可审计与"证据较早"判断。
type GroupFact struct {
	ID         int64     `json:"id"          gorm:"primaryKey;autoIncrement;comment:群画像事实ID"`
	GroupID    string    `json:"group_id"    gorm:"size:64;not null;uniqueIndex:uq_group_fact_subject_key;comment:群ID"`
	SubjectID  string    `json:"subject_id"  gorm:"size:64;not null;default:'';uniqueIndex:uq_group_fact_subject_key;comment:事实主体"`
	Key        string    `json:"key"         gorm:"size:64;not null;uniqueIndex:uq_group_fact_subject_key;comment:命题主题"`
	Value      string    `json:"value"       gorm:"size:256;not null;comment:事实内容"`
	Confidence float64   `json:"confidence"  gorm:"not null;default:0.5;comment:置信度0~1"`
	Evidence   []byte    `json:"evidence"    gorm:"type:jsonb;comment:证据来源(topic_id列表)"`
	Conflict   string    `json:"conflict"    gorm:"size:256;default:'';comment:矛盾时被替换的旧值"`
	At         time.Time `json:"at"          gorm:"comment:最近证据来源时间"`
	UpdatedAt  time.Time `json:"updated_at"  gorm:"autoUpdateTime;comment:更新时间"`
}
