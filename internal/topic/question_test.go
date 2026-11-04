package topic

import "testing"

// TestExtractQuestion 问句提取与归一化：只取最后一个问句，去掉口语填充词与语气助词。
func TestExtractQuestion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"无问句", "今天天气不错", ""},
		{"单个问句", "局域网到底通没通？", "局域网通没通"},
		{"多句取最后", "先说网段，然后呢？你那个局域网到底通没通？", "局域网通没通"},
		{"语气助词", "所以局域网通没通呢？", "局域网通没通"},
		{"英文问号", "is it ok?", "isitok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractQuestion(tt.content); got != tt.want {
				t.Fatalf("extractQuestion(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestSameQuestion 同一问题的判定：同义改写/包含关系应命中，不同问题不应命中。
func TestSameQuestion(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"完全相同", "局域网通没通", "局域网通没通", true},
		{"子串包含", "局域网通没通", "局域网通没通啊兄弟", true},
		{"不同问题", "局域网通没通", "学妹加到了吗", false},
		{"空问句", "", "局域网通没通", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameQuestion(tt.a, tt.b); got != tt.want {
				t.Fatalf("sameQuestion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
