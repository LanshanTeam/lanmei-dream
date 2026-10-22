package kb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Capabilities 声明 Provider 支持的召回能力。
type Capabilities struct {
	// Modes 支持的召回模式列表；应只声明实际可用的模式（如未注入 embedder 时不声明 vector），
	// 引擎按此过滤请求模式，未声明的模式会被跳过并记录告警。
	Modes []RecallMode
}

// Supports 判断是否支持指定模式。
func (c Capabilities) Supports(mode RecallMode) bool {
	for _, m := range c.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Provider 是知识库数据源抽象，各知识库产品（本地数据库、飞书、腾讯 IMA 等）
// 实现此接口以屏蔽底层差异，是知识库系统的外部扩展点；通过 RegisterProvider
// 注册工厂后即可被配置引用。
//
// 实现者契约：
//   - 并发安全：同一实例会被多轮对话并发调用 Search，引擎在替换实例或服务关闭时还可能
//     并发调用 Close，实现需自行加锁保护内部状态；
//   - 模式容错：Search 按 req.Modes 分发到原生能力，不支持的模式跳过（不报错），
//     单个模式失败不中断整体（可只记日志返回其余模式的结果）；
//   - ID 唯一性范围：返回的 Chunk.ID 只需在单个 Provider 实例（单个知识库）内唯一，
//     不同知识库或不同实例之间可以重复，引擎以 Provider+KnowledgeBaseID+ID 作为去重键；
//   - 返回条数与排序：每个模式的列表按相关度降序排列，长度不超过 req.Limit；
//   - 远端数据：远端 API 不提供检索能力时，实现需自行分页拉取全量数据（受配置上限约束）
//     到本地缓存后计算召回，并处理缓存 TTL 与拉取失败降级，保证在召回软超时内返回。
type Provider interface {
	// Name 返回 provider 类型标识（如 local/feishu/sheet），须与配置 bases[].provider
	// 的取值一致，并在同一进程内保持稳定。
	Name() string
	// Capabilities 返回能力声明。
	Capabilities() Capabilities
	// Search 执行召回。
	//
	// 参数：
	//   - ctx：召回上下文，带引擎设置的软超时（当前 15 秒）
	//   - req：召回请求；Query 必填，Modes 为引擎已按能力过滤后的模式列表，
	//     Limit 为单个模式的返回上限（引擎传入值保证 >0），Filter 可为 nil（不筛选）
	//
	// 返回：按模式分组的排序结果（每个模式的列表按相关度降序且长度不超过 req.Limit）；
	// 返回 error 表示本次召回整体失败，引擎记日志后丢弃该 provider 的结果，不影响其他 provider
	Search(ctx context.Context, req *RecallRequest) (*RecallResult, error)
	// Close 释放实例独占资源（HTTP 客户端/连接池/后台任务）。
	// 引擎在替换实例与服务关闭时调用，返回的错误会被调用方忽略；调用后实例不应再被使用。
	Close() error
}

// Syncer 可选接口：实现此接口的 Provider 支持启动时内容同步
// （本地 provider 的 docs_dir 文件摄入、飞书 provider 的文档缓存预热）。
// 实现需保证与 Search 并发调用安全。
type Syncer interface {
	// Sync 执行一次内容同步（幂等）：重复调用不应产生重复数据。
	//
	// 参数：
	//   - ctx：同步上下文；启动阶段由 NewService 设置 30 秒软超时，管理面板触发时由调用方控制
	//
	// 返回：拉取或摄入失败时返回错误；单个知识库同步失败不影响其他知识库，
	// 启动阶段失败仅告警不阻塞启动，管理面板触发时错误会上报给调用方
	Sync(ctx context.Context) error
}

// FileImporter 可选接口：支持管理面板导入知识文件的 Provider（当前为本地知识库）。
// 导入目录必须是该 Provider 自身同步范围内的独立子目录：既与人工维护的知识文件隔离，
// 又要能被内容同步扫描到，否则下次同步会把这些文件视为"已删除"而清掉其分块。
type FileImporter interface {
	// ImportDir 返回文件导入目录（绝对路径或相对进程工作目录）；目录不存在时创建。
	ImportDir() (string, error)
}

// Ingester 可选接口：支持内容写入的 Provider（本地知识库）。
// kb_add 工具通过此接口录入知识，Provider 内部负责向量化。
type Ingester interface {
	// Store 存储/更新一条分块（按 Provider 内唯一标识幂等）。
	//
	// 参数：
	//   - ctx：写入上下文
	//   - chunk：待写入分块；ID、KnowledgeBaseID、Content 需非空
	//
	// 返回：写入失败返回错误；同一 ID 重复写入应更新原记录而非新增
	Store(ctx context.Context, chunk *Chunk) error
}

// Deps 注入给 Provider 工厂的公共依赖（由 main 组装）。
type Deps struct {
	// Orm 本地 provider 的存储（PostgreSQL + pgvector）；纯远程 provider 可忽略。
	Orm *gorm.DB
	// Embedder 向量计算（可为 nil，缺省时 vector 模式降级）。
	Embedder embedding.Embedder
	// Logger 日志器；工厂收到 nil 时应自行兜底为 zap.NewNop()。
	Logger *zap.Logger
}

// Factory 依据配置构造一个 Provider 实例。
//
// 参数：
//   - ctx：构造上下文，可传给需要初始化的 provider
//   - kbb：知识库元信息（ID/Name/RecallLimit 及私有配置 Config）
//   - cfg：provider 私有配置，与 kbb.Config 相同
//   - deps：公共依赖（数据库 / embedder / 日志器）
//
// 返回：构造成功返回可用 Provider；配置缺失或连接失败返回错误，该知识库会被跳过并告警
//
// 注意：每个启用的知识库各自构造一个独立实例，实例由 kb 负责调用 Close；
// 工厂需先经 RegisterProvider 注册才能被配置引用。
type Factory func(ctx context.Context, kbb *KnowledgeBase, cfg map[string]any, deps Deps) (Provider, error)

var (
	factoryMu sync.RWMutex
	factories = map[string]Factory{}
)

// RegisterProvider 注册 provider 工厂（provider 接入点），名称不可重复，
// 且需在 NewService 之前调用。
//
// 参数：
//   - name：provider 类型标识，需与配置 bases[].provider 的取值一致
//   - f：工厂函数
//
// 返回：name 为空、f 为 nil 或 name 已注册时返回错误（不会覆盖已有工厂）
//
// 注意：并发安全；配置中每个知识库复用同一工厂，工厂只注册一次。
func RegisterProvider(name string, f Factory) error {
	if name == "" {
		return fmt.Errorf("kb: provider 名称不能为空")
	}
	if f == nil {
		return fmt.Errorf("kb: provider %q 工厂不能为空", name)
	}
	factoryMu.Lock()
	defer factoryMu.Unlock()
	if _, dup := factories[name]; dup {
		return fmt.Errorf("kb: provider %q 已注册", name)
	}
	factories[name] = f
	return nil
}

// createProvider 按知识库配置构造 provider 实例。
func createProvider(ctx context.Context, kbb *KnowledgeBase, deps Deps) (Provider, error) {
	factoryMu.RLock()
	f, ok := factories[kbb.Provider]
	factoryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("kb: 未知 provider %q（可用：%s）", kbb.Provider, providerNames())
	}
	return f(ctx, kbb, kbb.Config, deps)
}

func providerNames() string {
	factoryMu.RLock()
	defer factoryMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// ResolveSecret 解析配置中的敏感值。
// 若值为字符串且以 "env:" 开头，则从环境变量读取对应变量名；
// 其它类型返回空字符串。
//
// 参数：
//   - v：配置值（通常取自 provider 私有配置）
//
// 返回：解析后的明文；值为非字符串或环境变量未设置时返回空字符串
func ResolveSecret(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if strings.HasPrefix(s, "env:") {
		return os.Getenv(strings.TrimPrefix(s, "env:"))
	}
	return s
}

// ConfigString 读取字符串配置项，缺失或类型不符返回 def。
//
// 参数：
//   - cfg：provider 私有配置
//   - key：配置键
//   - def：缺省值
//
// 返回：配置值；缺失或非字符串时返回 def
func ConfigString(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// ConfigInt 读取整数配置项，缺失或类型不符返回 def。
//
// 参数：
//   - cfg：provider 私有配置
//   - key：配置键
//   - def：缺省值
//
// 返回：配置值；兼容 int/int64/float64（float64 截断取整），缺失或类型不符时返回 def
func ConfigInt(cfg map[string]any, key string, def int) int {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}

// ConfigFloat 读取浮点配置项，缺失或类型不符返回 def。
//
// 参数：
//   - cfg：provider 私有配置
//   - key：配置键
//   - def：缺省值
//
// 返回：配置值；兼容 float64/int/int64，缺失或类型不符时返回 def
func ConfigFloat(cfg map[string]any, key string, def float64) float64 {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return def
}

// ConfigBool 读取布尔配置项，缺失或类型不符返回 def。
//
// 参数：
//   - cfg：provider 私有配置
//   - key：配置键
//   - def：缺省值
//
// 返回：配置值；仅接受 bool，缺失或类型不符时返回 def
func ConfigBool(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// ConfigStrings 读取字符串数组配置项，缺失或类型不符返回 def。
//
// 参数：
//   - cfg：provider 私有配置
//   - key：配置键
//   - def：缺省值
//
// 返回：配置值；接受 []any（仅收集其中的字符串元素，非字符串元素跳过），
// 缺失或类型不符时返回 def
func ConfigStrings(cfg map[string]any, key string, def []string) []string {
	if v, ok := cfg[key]; ok {
		if items, ok := v.([]any); ok {
			out := make([]string, 0, len(items))
			for _, it := range items {
				if s, ok := it.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return def
}
