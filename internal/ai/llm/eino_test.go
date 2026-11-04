package llm

import (
	"math"
	"testing"
)

func TestClampRange(t *testing.T) {
	cases := []struct {
		name     string
		in       float64
		lo, hi   float64
		expected float64
	}{
		{name: "低于下限", in: -1, lo: 0, hi: 1, expected: 0},
		{name: "区间内", in: 0.3, lo: 0, hi: 1, expected: 0.3},
		{name: "高于上限", in: 3, lo: 0, hi: 2, expected: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampRange(tc.in, tc.lo, tc.hi)
			if math.Abs(got-tc.expected) > 1e-6 {
				t.Fatalf("clampRange(%v, %v, %v) = %v, want %v", tc.in, tc.lo, tc.hi, got, tc.expected)
			}
		})
	}
}

func TestClampRepetitionPenalty(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{in: -1, want: 1},
		{in: 1, want: 1},
		{in: 1.1, want: 1.1},
		{in: 3, want: 2},
	}
	for _, tc := range cases {
		if got := clampRepetitionPenalty(tc.in); math.Abs(got-tc.want) > 1e-6 {
			t.Fatalf("clampRepetitionPenalty(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSamplingOptions(t *testing.T) {
	if got := SamplingOptions(SamplingParams{}, false); len(got) != 0 {
		t.Fatalf("未设置参数时应返回空 options，got %d", len(got))
	}

	zero := 0.0
	zeroInt := 0
	if got := SamplingOptions(SamplingParams{PresencePenalty: &zero, FrequencyPenalty: &zero, TopK: &zeroInt}, false); len(got) != 0 {
		t.Fatalf("显式 0 不应下发参数，got %d options", len(got))
	}

	topP := 0.95
	if got := SamplingOptions(SamplingParams{TopP: &topP}, false); len(got) != 1 {
		t.Fatalf("top_p 应下发 1 个 option，got %d", len(got))
	}

	presence := 0.3
	frequency := 0.2
	topK := 40
	repetition := 1.05
	minP := 0.05
	topA := 0.1
	seed := 42
	params := SamplingParams{
		TopP:              &topP,
		TopK:              &topK,
		PresencePenalty:   &presence,
		FrequencyPenalty:  &frequency,
		RepetitionPenalty: &repetition,
		MinP:              &minP,
		TopA:              &topA,
		Seed:              &seed,
	}
	// top_p 走通用 option，其余合并为 1 个 ExtraFields。
	if got := SamplingOptions(params, false); len(got) != 2 {
		t.Fatalf("完整采样参数应返回 2 个 option，got %d", len(got))
	}

	// 关闭思考必须与采样参数合并进同一个 ExtraFields，避免覆盖。
	if got := SamplingOptions(params, true); len(got) != 2 {
		t.Fatalf("采样参数 + 关闭思考应返回 2 个 option，got %d", len(got))
	}
	if got := SamplingOptions(SamplingParams{}, true); len(got) != 1 {
		t.Fatalf("仅关闭思考应返回 1 个 option，got %d", len(got))
	}
}

func TestSamplingDefaults(t *testing.T) {
	c := &EinoClient{
		sampling: SamplingDefaults{TopP: 0.95, PresencePenalty: 0.3},
	}
	got := c.SamplingDefaults()
	if got.TopP != 0.95 || got.PresencePenalty != 0.3 {
		t.Fatalf("SamplingDefaults() = %+v", got)
	}
}
