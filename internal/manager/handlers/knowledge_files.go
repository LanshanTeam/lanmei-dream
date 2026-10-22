package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// 知识库文件导入的限制。三处上限需保持"外层不小于内层"：
// nginx client_max_body_size（16m，见 manager/nginx.conf）≥ Fiber BodyLimit（16MB，
// 见 manager/server.go 的 fiber.Config）> 单文件上限（这里），
// 单文件取外层的一半是为 multipart 边界、其它表单字段与临时文件留出余量。
const (
	maxImportFileSize  = 8 << 20 // 单个文件上限 8MB
	maxImportFileCount = 10      // 单次最多上传文件数
	importSyncTimeout  = 60 * time.Second
)

// KnowledgeFileView 导入文件的展示信息。
type KnowledgeFileView struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

// importDir 解析知识库的文件导入目录。
// base 为空时取唯一支持文件导入的知识库（见 kb.Service.ImportDir）。
func (h *Handler) importDir(base string) (string, error) {
	if h.knowledge == nil {
		return "", errors.New("知识库未启用")
	}
	return h.knowledge.ImportDir(strings.TrimSpace(base))
}

// importFileName 校验并返回可用的导入文件名。
// 只取路径最后一段（浏览器可能上传完整路径），只放行 .md / .csv（local provider
// 仅解析这两种），拒绝空名、以点开头的隐藏文件与父目录引用，避免穿越到导入目录之外。
func importFileName(raw string) (string, error) {
	name := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "", errors.New("文件名非法")
	}
	if strings.HasPrefix(name, ".") {
		return "", errors.New("文件名不能以点开头")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".csv":
		return name, nil
	default:
		return "", fmt.Errorf("不支持的文件类型：%s（仅支持 .md / .csv）", name)
	}
}

// writeImportFile 将上传文件写入导入目录（同名覆盖）。
// 先写 .part 临时文件再改名：同步可能同时在扫描该目录，直接写目标文件会让它读到半截内容。
func writeImportFile(dir string, fh *multipart.FileHeader, name string) error {
	dst := filepath.Join(dir, name)
	// 名称已过滤，这里再校验拼接结果仍落在导入目录内（防穿越的最后一道）
	rel, err := filepath.Rel(dir, dst)
	if err != nil || rel != name {
		return errors.New("文件名非法")
	}
	src, err := fh.Open()
	if err != nil {
		return fmt.Errorf("读取上传文件: %w", err)
	}
	defer src.Close()

	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建文件: %w", err)
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入失败: %w", err)
	}
	return nil
}

// ListKnowledgeFiles 列出知识库导入目录下的文件。
// 权限：登录即可（与知识分块列表一致）。
func (h *Handler) ListKnowledgeFiles(c fiber.Ctx) error {
	dir, err := h.importDir(c.Query("base"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "导入目录读取失败"})
	}
	items := make([]KnowledgeFileView, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue // 只管理直挂文件，子目录由运维手工维护
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, KnowledgeFileView{
			Name:      e.Name(),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return c.JSON(fiber.Map{"items": items, "total": len(items)})
}

// UploadKnowledgeFiles 上传知识文件到导入目录并立即同步。
// 权限：仅 super + step-up。
// 同名文件直接覆盖；全部文件先校验后落盘，校验失败时一个字节都不写，
// 避免留下"部分写入"的半成品。同步失败时文件保留并如实上报，不静默成功。
func (h *Handler) UploadKnowledgeFiles(c fiber.Ctx) error {
	admin := currentAdmin(c)

	// c.MultipartForm 按 Fiber BodyLimit 限制请求体（超限直接报错，不会读到内存外）
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "表单解析失败（文件过大或格式非法）"})
	}
	base := ""
	if vs := form.Value["base"]; len(vs) > 0 {
		base = vs[0]
	}
	dir, err := h.importDir(base)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	files := form.File["files"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "未选择任何文件"})
	}
	if len(files) > maxImportFileCount {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("单次最多上传 %d 个文件", maxImportFileCount),
		})
	}

	names := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, fh := range files {
		name, err := importFileName(fh.Filename)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if _, dup := seen[name]; dup {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("同一批文件存在重名：%s", name),
			})
		}
		seen[name] = struct{}{}
		if fh.Size > maxImportFileSize {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("文件 %s 超过 %dMB 上限", name, maxImportFileSize>>20),
			})
		}
		names = append(names, name)
	}

	for i, fh := range files {
		if err := writeImportFile(dir, fh, names[i]); err != nil {
			h.auditDeny(c, admin, "knowledge.file.upload", "knowledge", base, err.Error())
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("文件 %s 保存失败", names[i]),
			})
		}
	}

	// 文件已全部落地，用一次同步完成摄入（Sync 为全量幂等，不必按文件逐个触发）
	syncCtx, cancel := context.WithTimeout(c.Context(), importSyncTimeout)
	defer cancel()
	if err := h.knowledge.Sync(syncCtx, strings.TrimSpace(base)); err != nil {
		h.auditDeny(c, admin, "knowledge.file.upload", "knowledge", base, err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "文件已保存，但同步失败：" + err.Error(),
			"files": names,
		})
	}
	h.auditOK(c, admin, "knowledge.file.upload", "knowledge", base, strings.Join(names, ","))
	return c.JSON(fiber.Map{"ok": true, "files": names})
}

// DeleteKnowledgeFile 删除导入目录下的知识文件并立即同步
// （同步会清理该文件对应的分块，无需单独删分块）。
// 权限：仅 super + step-up。
func (h *Handler) DeleteKnowledgeFile(c fiber.Ctx) error {
	admin := currentAdmin(c)
	name, err := importFileName(c.Params("name"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	dir, err := h.importDir(c.Query("base"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	dst := filepath.Join(dir, name)
	if rel, err := filepath.Rel(dir, dst); err != nil || rel != name {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "文件名非法"})
	}
	if err := os.Remove(dst); err != nil {
		if os.IsNotExist(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "文件不存在"})
		}
		h.auditDeny(c, admin, "knowledge.file.delete", "knowledge", name, err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "文件删除失败"})
	}

	syncCtx, cancel := context.WithTimeout(c.Context(), importSyncTimeout)
	defer cancel()
	if err := h.knowledge.Sync(syncCtx, strings.TrimSpace(c.Query("base"))); err != nil {
		h.auditDeny(c, admin, "knowledge.file.delete", "knowledge", name, err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "文件已删除，但同步失败：" + err.Error(),
		})
	}
	h.auditOK(c, admin, "knowledge.file.delete", "knowledge", name, "")
	return c.JSON(fiber.Map{"ok": true})
}
