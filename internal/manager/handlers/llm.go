package handlers

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// LLM Provider 管理（super + step-up 写操作）

// ProviderReq Provider 读写请求体（APIKey 仅写入时使用，查询永远脱敏）。
type ProviderReq struct {
	Name              string  `json:"name"`
	BaseURL           string  `json:"base_url"`
	APIKey            string  `json:"api_key,omitempty"`
	Model             string  `json:"model"`
	MaxTokens         int     `json:"max_tokens"`
	Temperature       float64 `json:"temperature"`
	PresencePenalty   float64 `json:"presence_penalty"`
	FrequencyPenalty  float64 `json:"frequency_penalty"`
	TopP              float64 `json:"top_p"`
	TopK              int     `json:"top_k"`
	RepetitionPenalty float64 `json:"repetition_penalty"`
	MinP              float64 `json:"min_p"`
	TopA              float64 `json:"top_a"`
	Seed              int     `json:"seed"`
	InPricePerM       float64 `json:"in_price_per_m"`
	OutPricePerM      float64 `json:"out_price_per_m"`
	Enabled           *bool   `json:"enabled"`
	Priority          int     `json:"priority"`
}

// ProviderView Provider 查询视图（APIKey 脱敏）。
type ProviderView struct {
	ID                uint    `json:"id"`
	Name              string  `json:"name"`
	BaseURL           string  `json:"base_url"`
	Model             string  `json:"model"`
	MaxTokens         int     `json:"max_tokens"`
	Temperature       float64 `json:"temperature"`
	PresencePenalty   float64 `json:"presence_penalty"`
	FrequencyPenalty  float64 `json:"frequency_penalty"`
	TopP              float64 `json:"top_p"`
	TopK              int     `json:"top_k"`
	RepetitionPenalty float64 `json:"repetition_penalty"`
	MinP              float64 `json:"min_p"`
	TopA              float64 `json:"top_a"`
	Seed              int     `json:"seed"`
	InPricePerM       float64 `json:"in_price_per_m"`
	OutPricePerM      float64 `json:"out_price_per_m"`
	Enabled           bool    `json:"enabled"`
	IsActive          bool    `json:"is_active"`
	Priority          int     `json:"priority"`
	HasAPIKey         bool    `json:"has_api_key"`
}

// ListProviders 列出全部 Provider（APIKey 脱敏）。
// 权限：登录即可，不要求超管。
func (h *Handler) ListProviders(c fiber.Ctx) error {
	list, err := h.store.ListLLMProviders(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	views := make([]ProviderView, 0, len(list))
	for _, p := range list {
		views = append(views, toProviderView(&p))
	}
	active := ""
	if h.llmMgr != nil {
		active = h.llmMgr.ProviderName()
	}
	return c.JSON(fiber.Map{"items": views, "active": active})
}

// CreateProvider 创建 Provider（仅 super + step-up）。
func (h *Handler) CreateProvider(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req ProviderReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Name == "" || req.BaseURL == "" || req.Model == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "名称/base_url/model 不能为空"})
	}
	if req.APIKey == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "API Key 不能为空"})
	}
	enc, err := h.authSvc.Box().Encrypt([]byte(req.APIKey))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "密钥加密失败"})
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p := &model.LLMProvider{
		Name:              req.Name,
		BaseURL:           req.BaseURL,
		APIKeyEnc:         enc,
		Model:             req.Model,
		MaxTokens:         req.MaxTokens,
		Temperature:       req.Temperature,
		PresencePenalty:   clampPenalty(req.PresencePenalty),
		FrequencyPenalty:  clampPenalty(req.FrequencyPenalty),
		TopP:              clampFloat(req.TopP, 0, 1),
		TopK:              max(req.TopK, 0),
		RepetitionPenalty: clampRepetitionPenalty(req.RepetitionPenalty),
		MinP:              clampFloat(req.MinP, 0, 1),
		TopA:              clampFloat(req.TopA, 0, 1),
		Seed:              req.Seed,
		InPricePerM:       req.InPricePerM,
		OutPricePerM:      req.OutPricePerM,
		Enabled:           enabled,
		Priority:          req.Priority,
	}
	if err := h.store.CreateLLMProvider(c.Context(), p); err != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "创建失败（名称可能重复）"})
	}
	if err := h.ReloadProviders(c.Context()); err != nil {
		h.logger.Warn("llm: provider 加载失败", zapErr(err))
	}
	h.auditOK(c, admin, "llm.provider.create", "llm_provider", req.Name, jsonDetail(req))
	return c.Status(fiber.StatusCreated).JSON(toProviderView(p))
}

// UpdateProvider 更新 Provider（仅 super + step-up）。
func (h *Handler) UpdateProvider(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	p, err := h.store.GetLLMProvider(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Provider 不存在"})
	}
	var req ProviderReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Name != "" {
		p.Name = req.Name
	}
	if req.BaseURL != "" {
		p.BaseURL = req.BaseURL
	}
	if req.APIKey != "" {
		enc, err := h.authSvc.Box().Encrypt([]byte(req.APIKey))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "密钥加密失败"})
		}
		p.APIKeyEnc = enc
	}
	if req.Model != "" {
		p.Model = req.Model
	}
	if req.MaxTokens > 0 {
		p.MaxTokens = req.MaxTokens
	}
	if req.Temperature >= 0 {
		p.Temperature = req.Temperature
	}
	if req.PresencePenalty >= 0 {
		p.PresencePenalty = clampPenalty(req.PresencePenalty)
	}
	if req.FrequencyPenalty >= 0 {
		p.FrequencyPenalty = clampPenalty(req.FrequencyPenalty)
	}
	if req.TopP >= 0 {
		p.TopP = clampFloat(req.TopP, 0, 1)
	}
	if req.TopK >= 0 {
		p.TopK = req.TopK
	}
	if req.RepetitionPenalty >= 0 {
		p.RepetitionPenalty = clampRepetitionPenalty(req.RepetitionPenalty)
	}
	if req.MinP >= 0 {
		p.MinP = clampFloat(req.MinP, 0, 1)
	}
	if req.TopA >= 0 {
		p.TopA = clampFloat(req.TopA, 0, 1)
	}
	p.Seed = req.Seed
	if req.InPricePerM >= 0 {
		p.InPricePerM = req.InPricePerM
	}
	if req.OutPricePerM >= 0 {
		p.OutPricePerM = req.OutPricePerM
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if req.Priority != 0 {
		p.Priority = req.Priority
	}
	if err := h.store.UpdateLLMProvider(c.Context(), p); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "保存失败"})
	}
	if err := h.ReloadProviders(c.Context()); err != nil {
		h.logger.Warn("llm: provider 加载失败", zapErr(err))
	}
	h.auditOK(c, admin, "llm.provider.update", "llm_provider", p.Name, jsonDetail(req))
	return c.JSON(toProviderView(p))
}

// DeleteProvider 删除 Provider（仅 super + step-up；活跃项禁止删除）。
func (h *Handler) DeleteProvider(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	p, err := h.store.GetLLMProvider(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Provider 不存在"})
	}
	if p.IsActive {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "活跃 Provider 不可删除，请先切换"})
	}
	if err := h.store.DeleteLLMProvider(c.Context(), p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "删除失败"})
	}
	if err := h.ReloadProviders(c.Context()); err != nil {
		h.logger.Warn("llm: provider 加载失败", zapErr(err))
	}
	h.billing.RemovePrice(p.Name)
	h.auditOK(c, admin, "llm.provider.delete", "llm_provider", p.Name, "")
	return c.JSON(fiber.Map{"ok": true})
}

// ActivateProvider 热切换活跃 Provider（仅 super + step-up）。
// 构建客户端失败时保持原活跃不变并返回错误。
func (h *Handler) ActivateProvider(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	p, err := h.store.GetLLMProvider(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Provider 不存在"})
	}
	if !p.Enabled {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Provider 已禁用，无法激活"})
	}
	// 预检 API Key 可解密（实际密钥已在 reloadProviders 加载进 ProviderManager）
	if _, err := h.authSvc.Box().Decrypt(p.APIKeyEnc); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "API Key 解密失败"})
	}
	if err := h.llmMgr.Switch(p.Name); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "切换失败：" + err.Error()})
	}
	if err := h.store.SetLLMProviderActive(c.Context(), p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "持久化失败"})
	}
	h.billing.UpsertPrice(p.Name, p.InPricePerM, p.OutPricePerM)
	h.auditOK(c, admin, "llm.provider.activate", "llm_provider", p.Name, "")
	return c.JSON(fiber.Map{"ok": true, "active": p.Name, "model": p.Model})
}

// ReloadProviders 将 DB 的 Provider 全量加载进运行时（ProviderManager + 计费价格表），
// 并恢复 DB 标记的活跃项。供 handlers 内部与 manager.LoadProviders 调用。
func (h *Handler) ReloadProviders(ctx context.Context) error {
	if h.llmMgr == nil {
		// 未启用运行时 Provider 管理（未配置 LLM API Key）：不加载运行时，仅同步价格表
		return nil
	}
	list, err := h.store.ListLLMProviders(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		// DB 无 Provider：保留当前运行时 Provider（config.toml 兜底），仅清空价格表
		h.billing.SetPrices(list)
		return nil
	}
	provs := make([]*llm.Provider, 0, len(list))
	var dbActive *model.LLMProvider
	for i := range list {
		key, err := h.authSvc.Box().Decrypt(list[i].APIKeyEnc)
		if err != nil {
			h.logger.Warn("llm: provider API Key 解密失败", zapErr(err))
			continue
		}
		provs = append(provs, toLLMProvider(&list[i], string(key)))
		if list[i].IsActive {
			dbActive = &list[i]
		}
	}
	if _, err := h.llmMgr.SetProviders(provs); err != nil {
		return err
	}
	h.billing.SetPrices(list)
	// 恢复 DB 标记的活跃项（若与优先级选择不一致；Switch 内部自行构建客户端）
	if dbActive != nil && dbActive.Enabled && h.llmMgr.ProviderName() != dbActive.Name {
		_ = h.llmMgr.Switch(dbActive.Name)
	}
	return nil
}

// toLLMProvider 将 DB 模型转换为运行时 Provider。
func toLLMProvider(p *model.LLMProvider, apiKey string) *llm.Provider {
	return &llm.Provider{
		ID:                p.ID,
		Name:              p.Name,
		BaseURL:           p.BaseURL,
		APIKey:            apiKey,
		Model:             p.Model,
		MaxTokens:         p.MaxTokens,
		Temperature:       p.Temperature,
		PresencePenalty:   p.PresencePenalty,
		FrequencyPenalty:  p.FrequencyPenalty,
		TopP:              p.TopP,
		TopK:              p.TopK,
		RepetitionPenalty: p.RepetitionPenalty,
		MinP:              p.MinP,
		TopA:              p.TopA,
		Seed:              p.Seed,
		InPricePerM:       p.InPricePerM,
		OutPricePerM:      p.OutPricePerM,
		Enabled:           p.Enabled,
		Priority:          p.Priority,
	}
}

// toProviderView 转换为脱敏视图。
func toProviderView(p *model.LLMProvider) ProviderView {
	return ProviderView{
		ID:                p.ID,
		Name:              p.Name,
		BaseURL:           p.BaseURL,
		Model:             p.Model,
		MaxTokens:         p.MaxTokens,
		Temperature:       p.Temperature,
		PresencePenalty:   p.PresencePenalty,
		FrequencyPenalty:  p.FrequencyPenalty,
		TopP:              p.TopP,
		TopK:              p.TopK,
		RepetitionPenalty: p.RepetitionPenalty,
		MinP:              p.MinP,
		TopA:              p.TopA,
		Seed:              p.Seed,
		InPricePerM:       p.InPricePerM,
		OutPricePerM:      p.OutPricePerM,
		Enabled:           p.Enabled,
		IsActive:          p.IsActive,
		Priority:          p.Priority,
		HasAPIKey:         len(p.APIKeyEnc) > 0,
	}
}

// clampPenalty 将重复惩罚限制在 0~2（OpenAI 兼容接口允许区间），负数按 0 处理。
func clampPenalty(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 2 {
		return 2
	}
	return v
}

// clampFloat 将采样参数限制在 [lo, hi] 区间。
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampRepetitionPenalty 把 repetition_penalty 限制在 [1, 2]：1.0 为中性（不启用）。
func clampRepetitionPenalty(v float64) float64 {
	if v <= 1 {
		return 1
	}
	if v > 2 {
		return 2
	}
	return v
}

// UsageSummary 按维度汇总 token 用量（by=model|provider|scene|user_id|group_id|platform）。
// 权限：登录即可，不要求超管；时间范围缺省为最近 24 小时。
func (h *Handler) UsageSummary(c fiber.Ctx) error {
	by := store.Dimension(c.Query("by", "model"))
	if !by.Valid() {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "非法聚合维度"})
	}
	sinceSec, untilSec, err := timeRange(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	until := time.Now()
	since := until.Add(-24 * time.Hour)
	if sinceSec > 0 {
		since = time.Unix(sinceSec, 0)
	}
	if untilSec > 0 {
		until = time.Unix(untilSec, 0)
	}
	rows, err := h.store.SumTokenUsage(c.Context(), since, until, by)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "统计失败"})
	}
	return c.JSON(fiber.Map{"items": rows})
}

// UsageSeries 用量时间序列（step=minute|hour|day）。
// 权限：登录即可，不要求超管；时间范围缺省为最近 24 小时。
func (h *Handler) UsageSeries(c fiber.Ctx) error {
	step := c.Query("step", "hour")
	switch step {
	case "minute", "hour", "day":
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "非法步长"})
	}
	sinceSec, untilSec, err := timeRange(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	until := time.Now()
	since := until.Add(-24 * time.Hour)
	if sinceSec > 0 {
		since = time.Unix(sinceSec, 0)
	}
	if untilSec > 0 {
		until = time.Unix(untilSec, 0)
	}
	points, err := h.store.UsageSeries(c.Context(), since, until, step)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "统计失败"})
	}
	return c.JSON(fiber.Map{"items": points})
}
