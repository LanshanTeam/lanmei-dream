package ai

import (
	"reflect"
	"testing"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
)

// TestRecentAssistantReplies 只取最近若干条 assistant 文本回复，按时间正序返回，
// 跳过空内容与图片消息。
func TestRecentAssistantReplies(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "系统提示"},
		{Role: llm.RoleUser, Content: "问题一"},
		{Role: llm.RoleAssistant, Content: "回答一"},
		{Role: llm.RoleUser, Content: "问题二"},
		{Role: llm.RoleAssistant, Content: ""},
		{Role: llm.RoleAssistant, Content: "回答二"},
		{Role: llm.RoleAssistant, Content: "带图回复", ImageURLs: []string{"https://example.com/a.png"}},
		{Role: llm.RoleAssistant, Content: "回答三"},
		{Role: llm.RoleUser, Content: "问题四"},
	}

	got := recentAssistantReplies(msgs, 2)
	want := []string{"回答二", "回答三"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recentAssistantReplies = %v, want %v", got, want)
	}

	if got := recentAssistantReplies(msgs, 0); got != nil {
		t.Fatalf("limit=0 should return nil, got %v", got)
	}
	if got := recentAssistantReplies(nil, 3); len(got) != 0 {
		t.Fatalf("empty msgs should return empty, got %v", got)
	}
}
