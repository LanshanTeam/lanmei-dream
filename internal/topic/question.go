package topic

import (
	"strings"
	"unicode"
)

// maxConsecutiveBotQuestions 非显式续聊链内允许"重复同一问题"的次数。
// 取 0：问题已经问过一次、对方没回答时，不允许再问第二次（自问自答式追问）。
const maxConsecutiveBotQuestions = 0

// questionMarkRunes 中文/英文问号。
const questionMarkRunes = "?？"

// questionFillerWords 问句归一化时剔除的口语填充词（不影响语义主体）。
var questionFillerWords = []string{
	"到底", "究竟", "所以", "然后", "那么", "其实", "反正", "那个", "这个",
	"你", "我", "他", "她", "它", "咱", "的", "了", "吗", "呢", "吧", "啊",
	"呀", "嘛", "哦", "嗯", "那", "这", "个", "就", "还", "也", "都", "是",
}

// extractQuestion 提取回复中最后一个问句并归一化，供"是否在重复追问"判定。
//
// 只关心"有没有在提问"与"问的是不是同一个问题"，因此归一化会去掉空白、标点、
// 口语填充词与语气助词，使「你那个局域网到底通没通？」与「所以局域网通没通呢」
// 被判定为同一问题。
//
// 返回空串表示回复中没有问句（陈述/表情/工具结果等）。
func extractQuestion(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	// 从后往前找最后一个问号，取其所在分句（逗号/句号/分号/换行都算分句边界）
	idx := strings.LastIndexAny(content, questionMarkRunes)
	if idx < 0 {
		return ""
	}
	start := 0
	if sep := strings.LastIndexAny(content[:idx], "。！!；;，,、\n"); sep >= 0 {
		start = sep + 1
	}
	return normalizeQuestion(content[start : idx+1])
}

// normalizeQuestion 归一化问句：只保留字母、数字与汉字，剔除口语填充词。
func normalizeQuestion(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	out := b.String()
	for _, w := range questionFillerWords {
		out = strings.ReplaceAll(out, w, "")
	}
	return out
}

// sameQuestion 判断两个归一化问句是否为同一个问题：
// 相同，或一方包含另一方（短句不短于 4 个字符），或字符二元组 Jaccard 相似度达标。
func sameQuestion(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len([]rune(a)) < 4 || len([]rune(b)) < 4 {
		return false
	}
	short, long := a, b
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	if strings.Contains(long, short) {
		return true
	}
	return bigramSimilarity(a, b) >= 0.6
}

// bigramSimilarity 计算两个字符串的字符二元组 Jaccard 相似度（0~1）。
func bigramSimilarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 2 || len(rb) < 2 {
		return 0
	}
	set := make(map[string]struct{}, len(ra)-1)
	for i := 0; i+1 < len(ra); i++ {
		set[string(ra[i:i+2])] = struct{}{}
	}
	inter := 0
	union := len(set)
	seen := make(map[string]struct{}, len(rb)-1)
	for i := 0; i+1 < len(rb); i++ {
		g := string(rb[i : i+2])
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		if _, ok := set[g]; ok {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
