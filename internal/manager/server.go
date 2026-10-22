// Package manager 内嵌的管理面板服务：
// Fiber 独立端口提供 HTTP API（认证 / 管理员 / LLM / Conduit 控制 / 审计 / 统计），
// Trace 实时推送走 SSE；与主服务共享 PG/Redis，不堵塞消息主链路。
package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/ai/prompt"
	"github.com/DaWesen/lanmei-dream/internal/ai/skill"
	"github.com/DaWesen/lanmei-dream/internal/bot"
	antifraud "github.com/DaWesen/lanmei-dream/internal/bizplugin/antifraud"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/kb"
	"github.com/DaWesen/lanmei-dream/internal/manager/audit"
	"github.com/DaWesen/lanmei-dream/internal/manager/auth"
	"github.com/DaWesen/lanmei-dream/internal/manager/billing"
	"github.com/DaWesen/lanmei-dream/internal/manager/control"
	"github.com/DaWesen/lanmei-dream/internal/manager/handlers"
	"github.com/DaWesen/lanmei-dream/internal/manager/middleware"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/manager/trace"
	"github.com/DaWesen/lanmei-dream/internal/model"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
)

// Deps Manager 外部依赖（由 main.go 注入）。
type Deps struct {
	DB        *database.DB
	Bot       *bot.Bot
	LLMMgr    *llm.ProviderManager
	Logger    *zap.Logger
	Skills    *skill.Manager
	Prompts   *prompt.Manager
	Knowledge *kb.Service
	Wasm      *pluginpkg.WasmManager
	Commands  *command.System
	// AntiFraud 反诈插件实例（未启用时为 nil，反诈接口返回不可用）
	AntiFraud *antifraud.Plugin
}

// Manager 管理面板服务。
type Manager struct {
	cfg     *config.ManagerConfig
	app     *fiber.App
	logger  *zap.Logger
	store   *store.Store
	authSvc *auth.Service
	ctrl    *control.Controller
	trace   *trace.Collector
	billing *billing.TokenAccounting
	h       *handlers.Handler
}

// New 创建管理面板服务（不启动）。
func New(cfg *config.ManagerConfig, deps Deps) (*Manager, error) {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	m := &Manager{cfg: cfg, logger: deps.Logger}

	m.store = store.New(deps.DB)
	m.trace = trace.NewCollector(m.store, deps.Logger)
	m.billing = billing.New(m.store, deps.Logger)

	authCfg := &auth.Config{
		AccessTokenTTL:      time.Duration(cfg.AccessTokenTTLMinutes) * time.Minute,
		RefreshTokenTTL:     time.Duration(cfg.RefreshTokenTTLHours) * time.Hour,
		MaxSessionsPerUser:  cfg.SessionMaxPerUser,
		MaxLoginFails:       cfg.MaxLoginFails,
		LoginLockWindow:     time.Duration(cfg.LoginLockMinutes) * time.Minute,
		EnableWebAuthn:      cfg.EnableWebAuthn,
		WebAuthnRPID:        cfg.WebAuthnRPID,
		WebAuthnDisplayName: cfg.WebAuthnDisplayName,
		WebAuthnOrigins:     cfg.WebAuthnOrigins,
		// 敏感配置一律来自环境变量（LANMEI_MANAGER_* 前缀，避免与其它项目冲突），绝不写入 toml 配置文件
		SuperAdminUsername: os.Getenv("LANMEI_MANAGER_ADMIN_USERNAME"),
		SuperAdminPassword: os.Getenv("LANMEI_MANAGER_ADMIN_PASSWORD"),
		SecretKey:          os.Getenv("LANMEI_MANAGER_SECRET_KEY"),
	}
	authSvc, err := auth.New(m.store, authCfg, deps.Logger)
	if err != nil {
		return nil, fmt.Errorf("manager: auth 初始化失败: %w", err)
	}
	m.authSvc = authSvc

	// Conduit 控制平面（Bot 实现 control.Descriptor 接口）
	auditLog := audit.New(m.store)
	m.ctrl = control.New(deps.Bot, m.store, deps.Logger)

	h := handlers.New(handlers.Options{
		Store:     m.store,
		Auth:      authSvc,
		Audit:     auditLog,
		Control:   m.ctrl,
		Trace:     m.trace,
		Billing:   m.billing,
		LLMMgr:    deps.LLMMgr,
		Bot:       deps.Bot,
		Logger:    deps.Logger,
		Skills:    deps.Skills,
		Prompts:   deps.Prompts,
		Knowledge: deps.Knowledge,
		Wasm:      deps.Wasm,
		Commands:  deps.Commands,
		AntiFraud: deps.AntiFraud,
	})
	m.h = h

	if deps.Bot != nil {
		deps.Bot.SetTraceSink(m.trace.Sink())
	}

	m.app = fiber.New(fiber.Config{
		AppName: "lanmei-manager",
		// 上限需容纳知识库文件上传（multipart，c.MultipartForm 按此值截断请求体）：
		// 与 manager/nginx.conf 的 client_max_body_size 保持一致，单文件上限另见
		// handlers.maxImportFileSize
		BodyLimit:    16 * 1024 * 1024,
		ErrorHandler: defaultErrorHandler(deps.Logger),
	})
	m.setupRoutes()

	return m, nil
}

// defaultErrorHandler 统一错误响应（未知路径 404 / 内部错误 500）。
func defaultErrorHandler(logger *zap.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		code := fiber.StatusInternalServerError
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			code = fiberErr.Code
		}
		if code >= 500 {
			logger.Error("manager: http error", zap.Error(err), zap.String("path", c.Path()))
		}
		return c.Status(code).JSON(fiber.Map{"error": fiberErrorText(code)})
	}
}

// fiberErrorText 简化错误文案（避免暴露内部细节）。
func fiberErrorText(code int) string {
	switch code {
	case fiber.StatusNotFound:
		return "接口不存在"
	case fiber.StatusMethodNotAllowed:
		return "方法不允许"
	default:
		return "内部错误"
	}
}

// setupRoutes 注册全部路由与中间件。
func (m *Manager) setupRoutes() {
	app := m.app
	h := m.h
	svc := m.authSvc
	cfg := m.cfg

	app.Use(middleware.Recover(func(format string, args ...any) {
		m.logger.Error(fmt.Sprintf(format, args...))
	}))

	// 前端静态资源（Bun/Vite 构建产物；目录不存在时自动跳过，不影响 API）
	app.Use("/", static.New("./manager/dist", static.Config{
		IndexNames: []string{"index.html"},
		// /api 请求完全跳过 static：避免 fasthttp 文件查找将响应状态置为 404 后
		// 污染后续 API handler（其 c.JSON 不重置状态码，导致 200 body + 404 状态）。
		Next: func(c fiber.Ctx) bool {
			return strings.HasPrefix(c.Path(), "/api")
		},
		NotFoundHandler: func(c fiber.Ctx) error {
			// 非 /api 路径：SPA 回退，返回 index.html（防御历史路由直达刷新）
			data, err := os.ReadFile("./manager/dist/index.html")
			if err != nil {
				return fiber.ErrNotFound
			}
			c.Type("html", "utf-8")
			return c.Send(data)
		},
	}))

	// 公开 API：无需登录，登录接口限流
	api := app.Group("/api")
	api.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "service": "lanmei-manager", "time": time.Now()})
	})

	loginLimit := middleware.RateLimit(cfg.RateLimitPerMinute, time.Minute)
	authPub := api.Group("/auth")
	authPub.Post("/password-login", loginLimit, h.PasswordLogin)
	authPub.Post("/totp-verify", loginLimit, h.VerifyTOTP)
	authPub.Post("/webauthn/begin-login", loginLimit, h.WebAuthnLoginBegin)
	authPub.Post("/webauthn/finish-login", loginLimit, h.WebAuthnLoginFinish)
	authPub.Post("/refresh", loginLimit, h.Refresh)
	authPub.Post("/logout", h.Logout)

	// 受保护 API：统一 CSRF + Bearer 鉴权
	super := middleware.RequireRole(model.AdminRoleSuper)
	stepUp := middleware.RequireStepUp(svc)
	protected := api.Group("", middleware.CSRF(), middleware.Auth(svc))

	// 认证与账号
	protected.Get("/auth/me", h.Me)
	protected.Post("/auth/step-up", h.StepUp)
	protected.Get("/auth/sessions", h.ListSessions)
	protected.Delete("/auth/sessions/:id", h.RevokeSession)
	protected.Delete("/auth/sessions", h.RevokeAllSessions)
	protected.Post("/auth/password", stepUp, h.ChangePassword)
	protected.Post("/auth/totp/setup-begin", h.TOTPSetupBegin)
	protected.Post("/auth/totp/setup-confirm", h.TOTPSetupConfirm)
	protected.Delete("/auth/totp", stepUp, h.TOTPRemove)
	protected.Post("/auth/webauthn/begin-register", h.WebAuthnRegisterBegin)
	protected.Post("/auth/webauthn/finish-register", h.WebAuthnRegisterFinish)
	protected.Get("/auth/passkeys", h.ListPasskeys)
	protected.Delete("/auth/passkeys/:credential_id", stepUp, h.RemovePasskey)

	// 管理员管理（super + step-up）
	protected.Get("/admins", h.ListAdmins)
	protected.Post("/admins", super, stepUp, h.CreateAdmin)
	protected.Put("/admins/:id", super, stepUp, h.UpdateAdmin)
	protected.Delete("/admins/:id", super, stepUp, h.DeleteAdmin)
	protected.Put("/admins/:id/status", super, stepUp, h.SetAdminStatus)
	protected.Put("/admins/:id/password", super, stepUp, h.ResetPassword)

	// LLM Provider 与用量统计
	protected.Get("/llm/providers", h.ListProviders)
	protected.Post("/llm/providers", super, stepUp, h.CreateProvider)
	protected.Put("/llm/providers/:id", super, stepUp, h.UpdateProvider)
	protected.Delete("/llm/providers/:id", super, stepUp, h.DeleteProvider)
	protected.Post("/llm/providers/:id/activate", super, stepUp, h.ActivateProvider)
	protected.Get("/llm/usage/summary", h.UsageSummary)
	protected.Get("/llm/usage/series", h.UsageSeries)

	// Conduit 控制平面
	protected.Get("/conduit/snapshot", h.ConduitSnapshot)
	protected.Put("/conduit/behavior-tree", super, stepUp, h.ApplyBehaviorTree)
	protected.Put("/conduit/subtrees", super, stepUp, h.ApplySubtrees)
	protected.Put("/conduit/pipelines", super, stepUp, h.ApplyPipelines)
	protected.Get("/conduit/revisions", h.ListConduitRevisions)
	protected.Post("/conduit/revisions/:id/rollback", super, stepUp, h.RollbackConduit)
	protected.Get("/conduit/traces", h.ListTraces)
	protected.Get("/conduit/traces/stream", h.TraceStream) // SSE 实时
	protected.Get("/conduit/traffic", h.QueryTraffic)

	// 审计与仪表盘
	protected.Get("/audit-logs", h.ListAuditLogs)
	protected.Get("/dashboard/stats", h.DashboardStats)

	// 内容管理：群组 / 用户 / 知识库 / 记忆 / 插件 / Skills / Prompt / 表情包 / 命令
	protected.Get("/groups", h.ListGroups)
	protected.Get("/groups/:platform/:group_id/config", h.GetGroupConfig)
	protected.Put("/groups/:platform/:group_id/config", super, stepUp, h.SaveGroupConfig)

	protected.Get("/users", h.ListUsers)
	protected.Post("/users/:id/ban", super, stepUp, h.SetUserBan)

	protected.Get("/knowledge/bases", h.ListKnowledgeBases)
	protected.Get("/knowledge/chunks", h.ListKnowledgeChunks)
	protected.Delete("/knowledge/chunks/:id", super, stepUp, h.DeleteKnowledgeChunk)
	protected.Post("/knowledge/sync", super, stepUp, h.SyncKnowledge)
	// 知识文件导入（上传目录 = provider 的 docs_dir/uploads，上传/删除后自动同步）
	protected.Get("/knowledge/files", h.ListKnowledgeFiles)
	protected.Post("/knowledge/files", super, stepUp, h.UploadKnowledgeFiles)
	protected.Delete("/knowledge/files/:name", super, stepUp, h.DeleteKnowledgeFile)

	protected.Get("/memories", h.ListMemories)
	protected.Delete("/memories/:id", super, stepUp, h.DeleteMemory)

	protected.Get("/plugins", h.ListPlugins)
	protected.Post("/plugins/:id/enable", super, stepUp, h.EnablePlugin)
	protected.Post("/plugins/:id/disable", super, stepUp, h.DisablePlugin)
	protected.Delete("/plugins/:id", super, stepUp, h.DeletePlugin)

	protected.Get("/skills", h.ListSkills)
	protected.Post("/skills/:id/enable", super, stepUp, h.EnableSkill)
	protected.Post("/skills/:id/disable", super, stepUp, h.DisableSkill)

	protected.Get("/prompts/fragments", h.ListPromptFragments)
	protected.Get("/prompts/fragments/:id", h.GetPromptFragment)
	protected.Put("/prompts/fragments/:id", super, stepUp, h.UpdatePromptFragment)

	protected.Get("/stickers", h.ListStickers)
	protected.Put("/stickers/:id", super, stepUp, h.UpdateSticker)
	protected.Delete("/stickers/:id", super, stepUp, h.DeleteSticker)

	protected.Get("/commands", h.ListCommands)

	// 反诈：违规记录查询与审批处置（插件未启用时接口统一返回 503）
	protected.Get("/antifraud/stats", h.AntiFraudStats)
	protected.Get("/antifraud/records", h.ListAntiFraudRecords)
	protected.Get("/antifraud/records/:id", h.GetAntiFraudRecord)
	protected.Post("/antifraud/records/:id/approve", h.ApproveAntiFraudRecord)
	protected.Post("/antifraud/records/:id/reject", h.RejectAntiFraudRecord)
	protected.Delete("/antifraud/records/:id", super, stepUp, h.DeleteAntiFraudRecord)
}

// Start 启动服务：超管引导 → 后台协程 → 监听。
func (m *Manager) Start(ctx context.Context) error {
	// env 超级管理员引导（幂等）
	if err := m.authSvc.Bootstrap(context.Background()); err != nil {
		return fmt.Errorf("manager: 超级管理员引导失败: %w", err)
	}

	m.trace.Start()
	m.billing.Start()

	go func() {
		m.logger.Info("manager: 管理面板已启动", zap.String("listen", m.cfg.ListenAddr))
		if err := m.app.Listen(m.cfg.ListenAddr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.logger.Error("manager: 监听失败", zap.Error(err))
		}
	}()
	return nil
}

// Stop 优雅停止：先停 HTTP，再排空后台协程。
func (m *Manager) Stop() error {
	var firstErr error
	if err := m.app.Shutdown(); err != nil {
		firstErr = err
	}
	m.trace.Stop()
	m.billing.Stop()
	return firstErr
}

// TraceCollector 暴露 Trace 采集器（供测试/扩展）。
func (m *Manager) TraceCollector() *trace.Collector { return m.trace }

// LoadProviders 从数据库加载全部 LLM Provider 到运行时（ProviderManager + 计费价格表）。
// 供 main.go 在启动流程调用；面板内 CRUD 后由 handlers 自动 reload。
func (m *Manager) LoadProviders(ctx context.Context) error {
	if m.h == nil {
		return nil
	}
	return m.h.ReloadProviders(ctx)
}

// BillingHook 返回计费用量上报回调（注入 ChatService / ProviderManager）。
func (m *Manager) BillingHook() llm.UsageHook { return m.billing.Hook() }
