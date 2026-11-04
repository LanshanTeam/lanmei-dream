# CLAUDE.md — 项目规则（AI 助手必读）

## 项目概况

蓝妹（lanmei-dream）是一个跨平台 IM 聊天机器人：通过反向 WebSocket 网关接入 OneBot 12/11 协议的 IM 平台（Onebots 网关连接微信、钉钉、Telegram 等，NapCat 直连 NTQQ）。消息路由由 Conduit 引擎驱动（行为树 + 管线）；对话能力来自 LLM 角色扮演、LOD 分级记忆与 RAG 检索；功能以插件扩展；另带内嵌管理面板与 Vue 前端。

## 技术栈

| 层 | 技术 |
|---|---|
| 语言 | Go 1.26（go.mod 声明 1.26.5，Dockerfile 用 golang:1.26）；管理面板前端 Vue 3 + TypeScript + Vite + TDesign |
| 消息引擎 | github.com/zrurf/conduit v0.2（行为树 + 管线，核心管线以 PassID 动态注册） |
| 网关 | lxzan/gws 反向 WebSocket 服务端，OneBot 12/11 协议自适配 |
| AI 组件 | CloudWeGo Eino；LLM 走 OpenAI 兼容接口（OpenAI / DeepSeek / Qwen / Moonshot / 火山方舟 / Ollama），Embedding 走 OpenAI 兼容接口，火山方舟 doubao-embedding-vision 走专用多模态实现 |
| 数据库 | PostgreSQL 18 + pgvector（HNSW 索引）+ pg_trgm；GORM + pgx，参数化查询 |
| 缓存 | Redis 7（Conduit StateStore、消息去重、话题状态；容器 allkeys-lru） |
| 对象存储 | RustFS（S3 兼容，aws-sdk-go-v2 s3），多媒体缓存与内网图片转 base64 |
| 插件 | 内置业务插件（Go，配置驱动）+ Wasm 插件（Extism + wazero，WIT ABI） |
| 权限 | Casbin RBAC（策略持久化 plugin_casbin_rule）+ Capability 配额与审计日志 |
| 管理 API | gofiber/fiber v3 内嵌服务（默认 :8090），前端在 manager/ |
| 日志 | zap + lumberjack（可选文件轮转） |

LLM / Embedding 通过 LANMEI_AI_LLM_* / LANMEI_AI_EMBEDDING_* 配置为 OpenAI 兼容端点，Key 未配置时角色扮演与 RAG 自动关闭；管理面板启用时 LLM 由 llm.ProviderManager 托管（Provider 存库、密钥 AES-256-GCM 加密、面板热切换并计费）。

## 目录结构

```
cmd/lanmei/          → 入口 main.go，组装全部依赖
internal/
  ai/                → ChatService 对话编排、LOD 压缩、记忆维护、流式分段、视觉理解、防注入
    intent/          → 意图分析（chat/command/tool/ignore + 群聊提及判断）
    llm/             → LLMClient 接口 + Eino 实现 + ProviderManager 多 Provider 热切换
    embedding/       → Embedder 接口 + OpenAI 兼容实现 + 火山方舟多模态实现
    memory/          → MemoryStore 接口 + 多路召回合并（向量/关键词/时间）
    prompt/          → Prompt 片段化组装（prompts.toml + assembly_template）
    skill/           → 技能系统（skills/ 目录 + skills.toml 启停）
    tool/            → AI 工具注册表（LLM ToolCalling）
  bot/               → Conduit 引擎装配、行为树、Pass 实现、消息回调与流式段落投递、去重
  command/           → 斜杠命令系统（注册/解析/分发）
  config/            → 配置加载（viper：config.toml + LANMEI_ 环境变量 + 命令行参数）
  database/          → GORM 连接、迁移、各实体 CRUD、LOD 查询、插件 KV
  gateway/           → 反向 WS 服务端 + OneBot 12/11 适配 + 连接管理 + 消息标准化
  infra/             → 基础设施生命周期（PG/pgvector + Redis + RustFS + zap）
  kb/                → 知识库：provider 抽象（local/feishu/sheet）、多路召回、隐式召回与工具
  websearch/         → Agent 原生 OpenSERP 摘要检索、参数校验、限流与会话隔离缓存
  manager/           → 管理面板后端（Fiber API、认证、Conduit 控制平面、计费、审计、Trace）
  media/             → 多媒体 MIME 与 RustFS 对象存储封装
  model/             → 数据模型与事实/置信度合并逻辑
  plugin/            → 插件系统（注册表、Wasm 运行时与 ABI、Casbin 授权、KV、配额）
  topic/             → 群聊话题系统（提及决策、状态机、归档）
  bizplugin/         → 内置业务插件（签到/欢迎/戳一戳/答题/海龟汤/表情/美图等）
manager/             → 管理面板前端（Vue 3 + Vite + TDesign）
config/              → 运行时配置（config.toml、skills.toml、onebots/napcat 配置）
prompts/ skills/ quizdata/ → Prompt 片段、技能、编程答题题库
schema/plugin/       → Wasm 插件 WIT 接口定义
docs/                → 架构与数据流文档
```

## 约定

- **注释**：准确简洁，中文为主，与所在模块风格保持一致；不复述代码，说明"为什么"。
- **文案**：聊天文案面向最终用户，允许活泼；日志/错误面向运维者，保持平实。
- **命名**：Go 导出符号大驼峰、包名小写短词；数据库表/字段用英文蛇形。
- **错误处理**：fmt.Errorf 带 %w 包装；日志统一用 zap 结构化字段，不吞错；管线内可降级错误用 conduit.NewSoftError（引擎记录日志但不中断管线）。
- **SQL 安全**：一律 GORM 参数化，绝不拼接用户输入；原生 SQL 仅限 DDL/索引/扩展；向量参数必须显式 ::vector 转换（否则报 operator does not exist: vector <=> text）。
- **依赖注入**：构造函数注入（NewXxx）；可扩展点用包级注册表（如 kb.RegisterProvider）；不用全局变量传递运行时依赖。
- **消息路由**：行为树决定走哪条管线，管线内 Pass 链式执行，不写 if-else 面条。
- **新增功能**：优先写内置业务插件（internal/bizplugin/，纳入 [plugin.builtins] 配置驱动注册，不在 main.go 硬编码）；需要进核心链路时新增 Pass 实现 conduit.Pass 并注册到管线；对外分发走 Wasm 插件（PLUGIN_DEVELOPMENT.md、schema/plugin/lanmei-plugin.wit）。
- **基础设施**：新增外部连接统一放 internal/infra/，经 infra.Setup() 初始化、Close() 释放，不在业务包直连。
- **幂等**：入口按 message_id 去重（Redis SETNX，存储故障时放行）。
- **平台标识**：用户以 (platform, platform_user_id) 唯一标识，platform ∈ qq/napcat/wechat/telegram（未识别回退 unknown）。
- **权限主体（Casbin）**：用户 user::<platform>::<platformUserID>；插件安装实例 plugin::<pluginID>::<installationID>；宿主 system::<name>。机器人超管另有静态配置 + bot_admin 表动态合并。
- **向量存储**：使用 pgvector 扩展（HNSW），不引入独立向量数据库；关键词/模糊检索用 tsvector 与 pg_trgm。

## 关键设计决策

**行为树结构（优先级从高到低）**

插件子树 → 流式段落重入 → 互动事件 → 管理员命令（/admin）→ 斜杠命令（/）→ 多媒体消息 → 话题门控（内部转意图分析）。

**核心管线（均以 PassID 动态注册，面板可编辑 Pass 顺序）**

| 管线 ID | 职责 |
|---|---|
| pipeline.admin | 超管校验 + 管理员命令执行 |
| pipeline.command | 斜杠命令解析 + 执行 |
| pipeline.intent_analysis | LLM 意图分析（RouterPass 动态路由） |
| pipeline.intent_command_exec | 自然语言命中命令时执行（参数由 LLM 提取） |
| pipeline.intent_ignore | 无需回复的消息仅存对话历史 |
| pipeline.roleplay | 流式角色扮演（yield 挂起 + 工具调用循环） |
| pipeline.roleplay_segment | 流式段落交付（子消息重入） |
| pipeline.media | 媒体下载/缓存/视觉理解后二次路由 |
| pipeline.notice | 互动事件兜底（插件子树优先消费） |
| pipeline.topic_gate | 群聊话题决策；私聊直接放行到意图分析 |
| pipeline.topic_ignore | 话题外消息静默保存 |
| pipeline.fallback | 引擎超时兜底话术 |

引擎参数：4 worker、消息超时 20s、超时走 pipeline.fallback、开启 Tracing（管理面板采集执行链路）。

**意图分析**：一次 LLM 调用把消息分为 chat / command / tool / ignore；群聊在同一次调用中附带"是否在跟机器人说话"的提及判断。LLM 不可用或结果非法时降级为 chat（LLM 完全未配置时不注册角色扮演管线，直接走 fallback）；意图分析有独立超时（默认 8s），避免吃满 20s 消息预算。

**角色扮演链路**：私聊直接进意图分析，群聊先过 topic_gate → RoleplayStreamPass 流式生成（独立 context，上限 60s）→ 段落经子消息重入引擎走交付管线，按"字数 × 打字速度"加抖动与上下限逐段发送（模拟真人节奏）→ LLM 返回 ToolCalls 时循环执行工具（最多 5 轮）→ 结束后存 L0 对话并异步触发记忆与压缩。

**LOD 记忆压缩**

- L0 原始对话 → L1 EpisodeSummary（brief + detailed + facts）：私聊对话 ≥40 条触发，每批压缩 20 条
- L1 → L2 TopicCluster（brief + detailed + facts + pgvector 向量）：摘要 ≥10 条触发，每批聚合 5 条
- 先存摘要再删被覆盖的原文；压缩只针对私聊维度，群聊记忆由话题归档沉淀
- 长期事实带置信度（跨摘要合并时重复确认提升、矛盾下降），注入上下文有门槛，低置信显式标注
- 后台 MemoryMaintainer 每 6h 维护：群聊每 (user, group) 保留最近 200 条原文、每用户保留最近 50 个主题、淘汰 180 天未更新的向量记忆

**上下文组装**（ChatService.assembleContext，按顺序）：Prompt 片段渲染的 System Prompt（静态在前，命中前缀缓存）→ 表情节奏提示 → 防注入安全规则 → LOD 历史（token 预算 3000，按 L2 摘要 → L1 摘要 → L0 原文优先级填充；群聊话题命中时以话题近期消息替代 L0）→ RAG 长期记忆（向量 + 关键词 + 时间多路召回，默认 5 条）→ 长期事实画像（私聊=用户画像，群聊=本群画像）→ 知识库隐式召回（默认 3 条）→ 当前用户消息。插件/工具的随机输出不直接进上下文，替换为"用户使用了 XX 功能"占位，避免事实污染。

**群聊话题系统**：群聊不再全量回复。at 为精确强信号；其余提及由意图分析的提及判断按强/弱阈值分档；维护每群话题状态机（Active → Cooling → 归档，状态存 Redis 可重启恢复）、成员与消息窗口、回复配额（防刷屏）；话题外消息静默保存，冷却后归档为群记忆。

**知识库**：provider 抽象（local 的 Markdown/CSV、飞书 Wiki、飞书表格），多路召回按权重合并（vector/fuzzy/time）；既做每轮对话的隐式召回注入，也提供 kb_search / kb_add 工具供 LLM 主动检索；新增 provider 在 kb.RegisterProvider 注册。

**联网搜索**：`ai.web_search` 默认关闭，由 main 在 AI 初始化阶段直接注册 `web_search`，不经业务插件。`internal/websearch` 管理 OpenSERP HTTP 连接与关闭；`tool.TurnState` 管理单条消息预算/来源与重试证据。启用后流式决策轮缓冲正文，流式与非流式共用安全循环；最多两次搜索、五轮工具后一次空工具收尾，禁止原始 JSON 兜底。不抓网页正文、不自动永久存储搜索资料。配置、部署、验收及回滚见 `docs/web-search.md`。

**插件与权限**：内置插件与 Wasm 插件共用同一注册表（同名 Wasm 优先）；插件经 PluginContext 注册 Pass/Pipeline/子树/命令/工具，行为树以 SubtreeRef 挂载（优先级最高）；Wasm 基于 Extism + wazero，走 WIT ABI，支持 /插件 安装 <url> 从 HTTPS 直链安装；授权用 Casbin RBAC，主体精确到插件安装实例，动作/角色为白名单，另有 Capability 资源配额与审计日志；插件持久化 KV 在 PostgreSQL。

**网关**：单个 HTTP 服务挂 /onebot（自动识别）、/onebot/v12、/onebot/v11；平台由握手子协议（如 12.go_onebot_wechat）或 ?platform= 查询参数识别，缺省 QQ；消息统一标准化为 NormalizedMessage 交给 Bot，出站按连接协议转换 at/reply/图片段（OneBot 11 的 reply 用 id 字段，12 用 message_id）。

**管理面板**：Fiber API 内嵌于主进程（默认关闭；开启时超管凭据与加密主密钥仅从环境变量读取，缺失或过弱则启动失败）。认证为账号密码 + TOTP + WebAuthn passkey，Access/Refresh 双 Token 与会话管理；控制平面可查看/热更新/回滚行为树与管线（行为树条件只能引用已注册的命名条件）；Trace 落库并聚合节点流量；LLM Provider 管理（加密存储、热切换、价格表与用量计费）；内容管理覆盖群组、拉黑、知识库、记忆、插件、技能、Prompt 片段、表情库等。

**基础设施与降级**：internal/infra/ 统一 Setup/Close；基础设施或可选依赖缺失时相关能力降级而非崩溃（如无 Embedder 跳过语义检索、无 RustFS 跳过媒体缓存）。

## 环境变量

敏感信息一律走环境变量（前缀 LANMEI_），非敏感配置放 config/config.toml；完整模板见 .env.example，加载逻辑见 internal/config/init.go。

| 变量 | 说明 |
|---|---|
| LANMEI_DATABASE_URL | PostgreSQL 连接串（使用含 pgvector 的实例；启动时自动创建 pgvector / pg_trgm 扩展） |
| LANMEI_REDIS_ADDR | Redis 地址（StateStore / 去重 / 话题状态） |
| LANMEI_BOT_GATEWAY_LISTEN_ADDR / LANMEI_BOT_GATEWAY_ACCESS_TOKEN | 反向 WS 监听地址（默认 0.0.0.0:8080） / 接入 token（敏感，空则不鉴权） |
| LANMEI_BOT_NICKNAME | 机器人昵称，默认"蓝妹" |
| LANMEI_BOT_SUPER_USERS | 超管列表，格式 platform:userID，逗号分隔 |
| LANMEI_BOT_INTENT_TIMEOUT_SECONDS | 意图分析独立超时，默认 8 秒 |
| LANMEI_BOT_TURTLE_SOUP_TIMEOUT_SECONDS | 海龟汤异步出题/判定上限，默认 120 秒 |
| LANMEI_AI_LLM_API_KEY | LLM API Key（敏感；未配置则角色扮演不可用、意图分析降级为 chat） |
| LANMEI_AI_LLM_BASE_URL / _MODEL / _MAX_TOKENS / _TEMPERATURE / _PRESENCE_PENALTY / _FREQUENCY_PENALTY / _TOP_P / _TOP_K / _REPETITION_PENALTY / _MIN_P / _TOP_A / _SEED | LLM 端点、模型与酒馆风格采样参数（仅角色扮演生效；top_k/repetition/min_p/top_a 仅本地推理后端支持） |
| LANMEI_AI_EMBEDDING_API_KEY | Embedding API Key（敏感；未配置则向量召回与语义判定不可用，关键词/时间召回仍可用） |
| LANMEI_AI_EMBEDDING_BASE_URL / _MODEL / _DIM | Embedding 端点、模型与维度（维度须与模型一致，影响建表维度） |
| LANMEI_PLUGIN_ROOT_DIR | Wasm 插件托管目录，默认 ./data/plugins |
| LANMEI_PLUGIN_NCM_URL | 网易云点歌 API 地址（空则点歌不可用） |
| LANMEI_PLUGIN_MUSIC_SEND_MODE | 点歌发送方式：auto / card / link |
| LANMEI_MEDIA_ENDPOINT / _ACCESS_KEY / _SECRET_KEY / _BUCKET / _REGION | RustFS 对象存储连接（凭据敏感） |
| LANMEI_TOPIC_ENABLED | 群聊话题系统开关 |
| LANMEI_KNOWLEDGE_ENABLED / LANMEI_KNOWLEDGE_AUTO_RECALL_LIMIT | 知识库总开关 / 每轮隐式召回条数 |
| LANMEI_MANAGER_ENABLED / LANMEI_MANAGER_LISTEN_ADDR | 管理面板开关 / 监听地址 |
| LANMEI_MANAGER_ADMIN_USERNAME / _ADMIN_PASSWORD / _SECRET_KEY | 面板超管账号、密码、加密与 JWT 派生主密钥（敏感，仅环境变量，缺失或过弱时启动失败） |
| LANMEI_MANAGER_WEBAUTHN_RPID | 可选，passkey 所需域名（IP 部署留空则回退密码登录） |
| FEISHU_APP_ID / FEISHU_APP_SECRET | 飞书知识库凭据（无 LANMEI_ 前缀，在知识库 provider 配置中以 "env:VAR" 引用） |

其余可调参数（流式节奏、话题阈值、随机美图、内置插件开关、Prompt/Skill/题库路径等）在 config/config.toml。

## 近期主要变更

- 2026-09：随机美图改为预审核图池（审核前置、后台补图）；记忆置信度体系、群画像与防注入增强；表情库视觉自动打标与情绪滑动窗口；新增招新群导航插件；OpenCode 网关请求头适配。
- 2026-08：Prompt 模块化与技能系统；知识库与多路召回；群聊话题、多媒体与互动事件；管理面板前后端；海龟汤 / 编程答题 / 每日一句等内置插件；意图分析独立超时。
- 2026-07：插件系统与 Wasm 运行时；Eino 替换手写 OpenAI 客户端（Provider 无关）；跨平台 OneBot 12/11 网关重构（移除 ZeroBot/QQ 强依赖）；Conduit 行为树 + 管线与 LOD 记忆压缩；向量存储 Milvus → pgvector 与 GORM 迁移。

> **本文档状态：当前有效** — 架构变更时须同步更新此文件。详细变更见 git 历史与 README「变更主线」。
