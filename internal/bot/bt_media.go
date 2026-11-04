package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/gateway"
	"github.com/DaWesen/lanmei-dream/internal/media"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// httpClient 媒体下载客户端：10s 超时。
var httpClient = &http.Client{Timeout: 10 * time.Second}

func isImageSeg(seg gateway.NormalizedSegment) bool {
	return seg.Type == "image"
}

// MediaPass 处理消息中的多媒体段：图片走"下载 → RustFS 内容寻址缓存（幂等去重）→
// 视觉理解 → 文字描述"，其它媒体段（audio/video/file）本轮仅记录、不解析内容。
// 结果写入黑板（KeyImageDesc / KeyMediaHandled）；纯图片消息（无文字）时将描述并入
// ctx.RawMsg，供意图分析/对话消费。
//
// 降级链：RustFS 不可用 → 跳过缓存仍做理解；视觉模型未配置 → 仅缓存不描述；
// 下载失败 → 跳过该段，不阻塞整条消息处理。
type MediaPass struct {
	Store  *media.ObjectStore
	Vision *ai.VisionService
	DB     *database.DB
	Cfg    *config.MediaConfig
	Logger *zap.Logger
}

// Execute 处理消息中的图片段（下载 → RustFS 缓存 → 视觉理解），把结果写入黑板；
// 非图片媒体段仅记录日志，单张图片失败跳过该段、不阻塞整条消息。
//
// 位置：pipeline.media 的首个 Pass（行为树 IsMedia 条件命中后进入），后接 MediaRouterPass 路由。
//
// 依赖上下文键：读取 KeySegments（Extra）；写入 KeyImageDesc/KeyMediaHandled（ctx.data）。
// 纯图片消息（RawMsg 为空）时把描述并入 ctx.RawMsg，使其进入正常对话流。
//
// 返回：始终返回 nil；缓存失败/视觉未配置/下载失败等降级链均在内部消化并记日志。
func (p *MediaPass) Execute(ctx *conduit.MessageContext) error {
	segs := SegmentsFromCtx(ctx)
	if len(segs) == 0 {
		return nil
	}

	var descs []string
	for _, seg := range segs {
		switch {
		case isImageSeg(seg):
			desc, err := p.processImage(ctx, seg)
			if err != nil {
				p.Logger.Warn("media: 图片处理失败",
					zap.String("group", ctx.GroupID), zap.String("user", ctx.UserID), zap.Error(err))
				continue // 单张图片失败不阻塞整条消息
			}
			if desc != "" {
				descs = append(descs, desc)
			}
		case seg.MimeType != "" || seg.Data["url"] != "":
			// 非图片媒体段：仅记录来源信息（本轮不解析音频/视频内容）
			p.Logger.Debug("media: 非图片媒体段已记录",
				zap.String("type", seg.Type), zap.String("mime", seg.MimeType))
		}
	}

	if len(descs) > 0 {
		desc := "[图片：" + strings.Join(descs, "；") + "]"
		conduit.Set(ctx, KeyImageDesc, desc)
		conduit.Set(ctx, KeyMediaHandled, true)
		if strings.TrimSpace(ctx.RawMsg) == "" {
			// 纯图片消息：描述即消息文本，进入正常对话流
			ctx.RawMsg = desc
			// 标记"纯图片"：话题系统据此只入窗不自动回复（除非被 at/强提及点名），
			// 避免同一张截图触发一次额外回复。
			conduit.Set(ctx, KeyImageOnly, true)
		}
	}
	return nil
}

// processImage 处理单张图片：下载 → 缓存 → 视觉理解 → 返回描述（可能为空）。
func (p *MediaPass) processImage(ctx *conduit.MessageContext, seg gateway.NormalizedSegment) (string, error) {
	url := seg.Data["url"]
	if url == "" {
		// 无 url（如 file_id 场景）无法下载，本轮降级跳过
		p.Logger.Debug("media: 图片无下载 url，跳过", zap.String("group", ctx.GroupID))
		return "", nil
	}

	data, mime, err := p.download(ctx.Ctx, url)
	if err != nil {
		return "", err
	}

	// 缓存到 RustFS（内容寻址，重复图片零成本）
	if p.Store != nil {
		if _, putErr := p.cacheToStore(ctx, data, mime); putErr != nil {
			p.Logger.Warn("media: 媒体缓存失败（不影响视觉理解）", zap.Error(putErr))
		}
	}

	// 视觉理解：未开启或模型未配置时返回空描述（仅缓存）
	if p.Vision == nil || p.Cfg == nil || !p.Cfg.VisionEnabled {
		return "", nil
	}
	desc, err := p.Vision.Describe(ctx.Ctx, data, mime)
	if err != nil {
		return "", err
	}
	return desc, nil
}

// download 下载媒体内容，限制大小（MaxDownloadBytes，默认 10MB）。
func (p *MediaPass) download(ctx context.Context, url string) ([]byte, string, error) {
	maxBytes := int64(10 << 20)
	if p.Cfg != nil && p.Cfg.MaxDownloadBytes > 0 {
		maxBytes = p.Cfg.MaxDownloadBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("media: 构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "lanmei-dream/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("media: 下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media: 下载 HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("media: 读取响应失败: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("media: 文件超过大小上限 %d 字节", maxBytes)
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		mime = media.SniffMime(data) // 本地嗅探兜底
	}
	return data, mime, nil
}

// cacheToStore 将媒体字节上传 RustFS 并记录 media_files 表（hash 幂等）。
func (p *MediaPass) cacheToStore(ctx *conduit.MessageContext, data []byte, mime string) (string, error) {
	if p.Store == nil {
		return "", errors.New("media: 对象存储未配置")
	}
	key, err := p.Store.Put(ctx.Ctx, data, mime)
	if err != nil {
		return "", err
	}
	if p.DB != nil {
		h := sha256.Sum256(data)
		_ = p.DB.UpsertMediaFile(ctx.Ctx, &model.MediaFile{
			Hash:      hex.EncodeToString(h[:]),
			ObjectKey: key,
			MimeType:  mime,
			SizeBytes: int64(len(data)),
			GroupID:   ctx.GroupID,
			UserID:    ctx.UserID,
		})
	}
	return key, nil
}

// MediaRouterPass 在 MediaPass 执行后决定媒体消息的去向：含文字或图片已理解 →
// pipeline.intent_analysis（进入正常对话流）；图片理解失败且无文字 → pipeline.intent_ignore（静默保存）。
type MediaRouterPass struct{}

// Execute 无业务逻辑，路由依据在 Route 中读取。
func (p *MediaRouterPass) Execute(ctx *conduit.MessageContext) error {
	return nil
}

// Route 根据媒体处理结果路由到下游管线。
// 群聊消息统一先经 pipeline.topic_gate 做话题决策（含无文字图片）；私聊直接进意图分析/忽略。
func (p *MediaRouterPass) Route(ctx *conduit.MessageContext) (string, error) {
	if ctx.IsGroup {
		return "pipeline.topic_gate", nil
	}
	handled := ImageDescFromCtx(ctx) != ""
	if strings.TrimSpace(ctx.RawMsg) != "" || handled {
		return "pipeline.intent_analysis", nil
	}
	return "pipeline.intent_ignore", nil
}
