package hooks

import "context"

// HookSpec 命名 Hook 静态规格，对齐 host/hook_spec_registry.py HookSpec
// MaiBot dataclass 默认 allow_blocking/allow_observe/allow_abort/allow_kwargs_mutation 全为 True，
// Go 结构体零值无法表达"默认允许"，因此改用反向 Deny 开关，零值即等价于 MaiBot 默认
type HookSpec struct {
	Name               string
	Description        string
	ParametersSchema   map[string]any
	DefaultTimeoutMS   int
	DenyBlocking       bool
	DenyObserve        bool
	DenyAbort          bool
	DenyKwargsMutation bool
}

// AllowBlocking 是否允许注册 blocking 处理器
func (s HookSpec) AllowBlocking() bool { return !s.DenyBlocking }

// AllowObserve 是否允许注册 observe 处理器
func (s HookSpec) AllowObserve() bool { return !s.DenyObserve }

// AllowAbort 是否允许处理器中止当前 Hook 调用
func (s HookSpec) AllowAbort() bool { return !s.DenyAbort }

// AllowKwargsMutation 是否允许 blocking 处理器修改 kwargs
func (s HookSpec) AllowKwargsMutation() bool { return !s.DenyKwargsMutation }

// HandlerMode Hook 处理器模式，对齐 component_registry HookHandlerEntry
type HandlerMode string

const (
	HandlerModeBlocking HandlerMode = "blocking"
	HandlerModeObserve  HandlerMode = "observe"
)

// HandlerPriority Hook 处理器优先级
type HandlerPriority string

const (
	PriorityEarly  HandlerPriority = "early"
	PriorityNormal HandlerPriority = "normal"
	PriorityLate   HandlerPriority = "late"
)

// Hook 处理器异常处理策略，对齐 component_registry HookHandlerEntry.error_policy
const (
	ErrorPolicySkip  = "skip"
	ErrorPolicyLog   = "log"
	ErrorPolicyAbort = "abort"
)

// HandlerResult 单个 Hook 处理器执行结果，对齐 hook_dispatcher HookHandlerExecutionResult
type HandlerResult struct {
	Success        bool
	Action         string
	ModifiedKwargs map[string]any
	CustomResult   any
	ErrorMessage   string
}

// HandlerFunc 进程内 Hook 回调
type HandlerFunc func(ctx context.Context, kwargs map[string]any) (HandlerResult, error)

// Subscription Hook 订阅：本地 Handler 和/或 Runner 侧 HOOK_HANDLER（PluginID+HandlerName）
type Subscription struct {
	HookName    string
	PluginID    string
	HandlerName string
	Mode        HandlerMode
	Priority    HandlerPriority
	SourceRank  int // 0 内置 Runner 组，1 第三方
	ErrorPolicy string
	TimeoutMS   int64 // 处理器级超时，0 表示回退 Hook 默认
	Handler     HandlerFunc
}
