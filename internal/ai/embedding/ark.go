package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VolcEmbedder 火山方舟多模态 Embedding 客户端，适配 doubao-embedding-vision 系列。
// 请求 POST {base}/embeddings/multimodal，入参 input=[{"type":"text","text":"..."}]。
type VolcEmbedder struct {
	baseURL   string
	apiKey    string
	model     string
	dimension int
	client    *http.Client
}

// NewVolcEmbedder 创建火山方舟多模态 Embedding 客户端。
// opts.BaseURL 为方舟 API 根地址（如 https://ark.cn-beijing.volces.com/api/v3），
// 客户端自动追加 /embeddings/multimodal 路径。
func NewVolcEmbedder(opts *EinoOptions) (*VolcEmbedder, error) {
	if opts.APIKey == "" {
		return nil, fmt.Errorf("embedding: 火山方舟需要 api_key")
	}
	if opts.Model == "" {
		return nil, fmt.Errorf("embedding: 火山方舟需要 model")
	}
	return &VolcEmbedder{
		baseURL:   strings.TrimRight(opts.BaseURL, "/"),
		apiKey:    opts.APIKey,
		model:     opts.Model,
		dimension: opts.Dimension,
		client:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Dimension 实现 Embedder 接口。
func (e *VolcEmbedder) Dimension() int { return e.dimension }

// Embed 实现 Embedder 接口。
func (e *VolcEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedding: 火山方舟空响应")
	}
	return vecs[0], nil
}

// EmbedBatch 实现 Embedder 接口。
// 实测火山方舟多模态接口每条请求仅返回一个向量（多 input 也只回第一条），
// 且响应中 data 为对象 {embedding:[...]}（非 OpenAI 标准数组），故逐条调用并解析对象。
func (e *VolcEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	vecs := make([][]float32, 0, len(texts))
	for _, t := range texts {
		v, err := e.embedOne(ctx, t)
		if err != nil {
			return nil, err
		}
		vecs = append(vecs, v)
	}
	return vecs, nil
}

// embedOne 请求单条文本的向量。
func (e *VolcEmbedder) embedOne(ctx context.Context, text string) ([]float32, error) {
	// 多模态格式：input 为 {type,text} 对象数组
	payload := map[string]any{
		"model":      e.model,
		"input":      []map[string]string{{"type": "text", "text": text}},
		"dimensions": e.dimension,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("embedding: 序列化请求: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/embeddings/multimodal", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: 构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: 火山方舟请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("embedding: 读取响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding: 火山方舟错误 status=%d body=%s",
			resp.StatusCode, truncateString(string(data), 300))
	}

	// 响应结构：{"data":{"embedding":[...],"index":0},...}
	var out struct {
		Data struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("embedding: 解析响应: %w", err)
	}
	if len(out.Data.Embedding) == 0 {
		return nil, fmt.Errorf("embedding: 响应缺少 embedding 向量")
	}
	if len(out.Data.Embedding) != e.dimension {
		return nil, fmt.Errorf("embedding: dimension mismatch: configured %d, received %d; check LANMEI_AI_EMBEDDING_DIM", e.dimension, len(out.Data.Embedding))
	}

	v := make([]float32, len(out.Data.Embedding))
	for j, f := range out.Data.Embedding {
		v[j] = float32(f)
	}
	return v, nil
}

// truncateString 截断错误响应体，避免日志刷屏。
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
