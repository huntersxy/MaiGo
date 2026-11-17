package host

import (
	"MaiGo/internal/plugin_runtime/capabilities"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"MaiGo/internal/core/eventbus"
	"MaiGo/internal/core/tooling"
	"MaiGo/internal/plugin_runtime/hooks"
	"MaiGo/internal/plugin_runtime/protocol"
)

// CapabilityHandler Host 侧能力实现，对齐 capability_service.CapabilityImpl 签名
//
// 定义在 capabilities 子包（能力实现与依赖契约同处一层），这里用别名保持
// 包内调用点与外部 API 不变。
type CapabilityHandler = capabilities.CapabilityHandler

// PluginRecord Host 已注册插件快照
type PluginRecord struct {
	PluginID                  string
	DisplayName               string
	PluginVersion             string
	PluginType                string
	RunnerGroup               RunnerGroup
	ConfigError               string
	DefaultConfig             map[string]any
	ConfigSchema              map[string]any
	Capabilities              []string
	ConfigReloadSubscriptions []string
	LLMProviders              []protocol.LLMProviderDeclaration
	DesktopPetButtons         []protocol.DesktopPetButton
	Revision                  uint64
}

// Host 插件运行时 Host 骨架，对齐 PluginRunnerSupervisor + CapabilityService 的子集
type Host struct {
	mu             sync.RWMutex
	capabilities   map[string]CapabilityHandler
	plugins        map[string]PluginRecord
	pluginRevision map[string]uint64
	runners        map[RunnerGroup]*RunnerState
	hooks          *hooks.Registry
	hookSpecs      *hooks.SpecRegistry
	components     *ComponentRegistry
	authorization  *CapabilityAuthorization
	apis           *APIRegistry
	maisakaPort    PluginMaisakaPort
	restartMu      sync.Mutex
	// lastStartOptions 最近一次启动选项，供源码变更与配置启用时重启复用
	lastStartOptions StartRunnerOptions
	hasStartOptions  bool
	// appliedPluginConfigs 已应用的插件配置快照，避免重复通知
	appliedPluginConfigs map[string]map[string]any
	// pluginTransitions 插件加载/停止中的过渡状态
	pluginTransitions map[string]string
	// pluginFileUpdateMu 串行化配置应用与源码重载
	pluginFileUpdateMu sync.Mutex
	llmProviders       *LLMProviderRegistry
	codec              protocol.Codec
	requestIDs         *protocol.RequestIDGenerator
	fileScopes         FileScopeIssuer
	uploadStore        *UploadStore
	pluginDataDir      func(pluginID string) (string, error)
	eventBus           *eventbus.EventBus
	toolRegistry       *tooling.ToolRegistry
	pluginManagement   *pluginManagementState

	statusSubscribers   map[uint64]func()
	statusSubscriberSeq uint64

	eventMu             sync.RWMutex
	eventHandlers       map[string][]EventHandlerComponent
	eventPluginOrder    []string
	eventHistory        map[string][]EventResult
	eventHistoryEnabled map[string]bool
	eventDisabledLocal  map[string]map[string]struct{}
	eventBusBridged     map[string]struct{}
	eventsCtx           context.Context
	eventsCancel        context.CancelFunc
	eventWG             sync.WaitGroup

	webuiExtensions map[string]map[string]any
	runnerReady     protocol.RunnerReadyPayload
	blockedReasons  map[string]string
	// adapterMigrationErrors 适配器迁移失败原因，随每次依赖同步并入阻止列表
	adapterMigrationErrors map[string]string

	externalAvailablePlugins map[string]string
	forcePluginCompatibility bool
	usage                    *PluginUsageTracker
	usageTracking            bool
	usageError               error
	usageCancel              context.CancelFunc
	usageWG                  sync.WaitGroup
	logSink                  func(pluginID string, entry protocol.RunnerLogEntry)
	gateway                  PluginMessageGatewayPort
	botAccounts              PluginBotAccountPort
	gatewayOpsMu             sync.Mutex
	gatewayStateMu           sync.RWMutex
	gatewayStates            map[string]map[string]MessageGatewayRuntimeState
	loading                  bool

	// instanceRoot Bot 实例根目录，供实例级命令配置定位
	instanceRoot string

	runnerLifecycles   map[RunnerGroup]*runnerLifecycle
	runnerReadyByGroup map[RunnerGroup]protocol.RunnerReadyPayload
	// runnerReadySet 本轮 runner.ready 是否已到达，对齐 _runner_ready_events.is_set()
	runnerReadySet bool
	// startupLoadResult 最近一次完成的加载结果（进度或 ready）
	startupLoadResult map[RunnerGroup]protocol.RunnerReadyPayload
	// runtimeLoadStatuses 热加载/卸载后的插件状态，覆盖启动快照
	runtimeLoadStatuses map[string]string
	// startupPending 尚未完成初始化的插件，对齐 _startup_pending_plugins
	startupPending map[RunnerGroup]map[string]struct{}
	// startupStartedAt 本轮开始等待的时刻，对齐 _startup_started_at
	startupStartedAt map[RunnerGroup]time.Time
	// startupFailure 本组启动失败原因，对齐 _startup_failure_reason
	startupFailure map[RunnerGroup]string
	// statusBadges 插件自报的运行标记，随运行实例清理
	statusBadges       *RuntimeStatusBadges
	maxRestartAttempts int

	runnerHealthInterval time.Duration
	runnerSpawnTimeout   time.Duration
	hookBlockingTimeout  time.Duration
	ipcSocketPath        string

	recoveryMu       sync.Mutex
	recoveryTargets  map[string]struct{}
	recoveryAttempts map[string]int

	// dependencyRunner 插件依赖分析流水线，由集成层注入
	dependencyRunner DependencyRunner
	// manifestConflicts 重复插件 ID / Provider 冲突扫描器，由集成层注入
	manifestConflicts ManifestConflictScanner

	shutdownRequested atomic.Bool
}

// SetDependencyRunner 注入依赖分析流水线
//
// 依赖流水线属于集成层（依赖解析 + pip 补装），Host 只在启用插件与
// 应用配置时触发它，因此保持端口注入而非直接依赖。
func (h *Host) SetDependencyRunner(runner DependencyRunner) {
	if h == nil {
		return
	}
	h.dependencyRunner = runner
}

// SetManifestConflictScanner 注入 manifest 冲突扫描器
func (h *Host) SetManifestConflictScanner(scanner ManifestConflictScanner) {
	if h == nil {
		return
	}
	h.manifestConflicts = scanner
}

// NewHost 构造 Host 并注册 MaiBot 内置 Hook 规格占位
func NewHost() *Host {
	specs := hooks.NewSpecRegistry()
	hooks.RegisterBuiltinHookSpecs(specs)
	eventsCtx, eventsCancel := context.WithCancel(context.Background())
	host := &Host{
		capabilities:             make(map[string]CapabilityHandler),
		plugins:                  make(map[string]PluginRecord),
		pluginRevision:           make(map[string]uint64),
		runners:                  make(map[RunnerGroup]*RunnerState),
		hookSpecs:                specs,
		hooks:                    hooks.NewRegistry(specs),
		components:               NewComponentRegistry(),
		authorization:            NewCapabilityAuthorization(),
		apis:                     NewAPIRegistry(),
		codec:                    protocol.MsgPackCodec{},
		requestIDs:               protocol.NewRequestIDGenerator(1),
		statusSubscribers:        make(map[uint64]func()),
		eventHandlers:            make(map[string][]EventHandlerComponent),
		eventHistory:             make(map[string][]EventResult),
		eventHistoryEnabled:      make(map[string]bool),
		eventDisabledLocal:       make(map[string]map[string]struct{}),
		eventBusBridged:          make(map[string]struct{}),
		eventsCtx:                eventsCtx,
		eventsCancel:             eventsCancel,
		webuiExtensions:          make(map[string]map[string]any),
		blockedReasons:           make(map[string]string),
		externalAvailablePlugins: make(map[string]string),
		forcePluginCompatibility: parseForcePluginCompatibilityEnv(os.Getenv(EnvForcePluginCompatibility)),
		gatewayStates:            make(map[string]map[string]MessageGatewayRuntimeState),
		runnerLifecycles:         make(map[RunnerGroup]*runnerLifecycle),
		runnerReadyByGroup:       make(map[RunnerGroup]protocol.RunnerReadyPayload),
		startupLoadResult:        make(map[RunnerGroup]protocol.RunnerReadyPayload),
		runtimeLoadStatuses:      make(map[string]string),
		startupPending:           make(map[RunnerGroup]map[string]struct{}),
		startupStartedAt:         make(map[RunnerGroup]time.Time),
		startupFailure:           make(map[RunnerGroup]string),
		maxRestartAttempts:       DefaultMaxRestartAttempt,
		runnerHealthInterval:     DefaultRunnerHealthInterval,
		runnerSpawnTimeout:       DefaultRunnerSpawnWait,
		hookBlockingTimeout:      DefaultHookBlockingTimeout,
		recoveryTargets:          make(map[string]struct{}),
		recoveryAttempts:         make(map[string]int),
	}
	host.llmProviders = NewLLMProviderRegistry()
	host.llmProviders.hostInvoker = host
	host.statusBadges = NewRuntimeStatusBadges(host.notifyStatusChanges)
	host.eventBus = eventbus.NewEventBus()
	host.installEventBusBridge()
	host.toolRegistry = tooling.NewToolRegistry()
	host.toolRegistry.RegisterProvider(newPluginToolProvider(host))
	host.installPluginManagementCommand()
	setDefaultHost(host)
	return host
}

// RegisterCapability 注册 Host 能力实现，能力名为点分字符串（如 send.text）
func (h *Host) RegisterCapability(name string, handler CapabilityHandler) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("能力名称不能为空")
	}
	if handler == nil {
		return fmt.Errorf("能力 %q 的实现不能为空", name)
	}
	h.mu.Lock()
	h.capabilities[name] = handler
	h.mu.Unlock()
	return nil
}

// InvokeCapability 本地调用已注册能力（供后续 cap.* RPC handler 使用）
func (h *Host) InvokeCapability(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
	h.mu.RLock()
	handler, ok := h.capabilities[capability]
	h.mu.RUnlock()
	if !ok {
		return nil, &CapabilityError{Code: string(protocol.ErrorCodeMethodNotAllowed), Message: "未注册的能力: " + capability}
	}
	return handler(ctx, pluginID, capability, args)
}

// RegisterPlugin 记录 Runner 通过 plugin.register_components 上报的插件（占位）
func (h *Host) RegisterPlugin(rec PluginRecord) {
	if rec.PluginID == "" {
		return
	}
	h.mu.Lock()
	h.pluginRevision[rec.PluginID]++
	rec.Revision = h.pluginRevision[rec.PluginID]
	h.plugins[rec.PluginID] = rec
	if h.runtimeLoadStatuses == nil {
		h.runtimeLoadStatuses = make(map[string]string)
	}
	h.runtimeLoadStatuses[rec.PluginID] = "success"
	h.mu.Unlock()
	if h.authorization != nil {
		h.authorization.RegisterPlugin(rec.PluginID, rec.Capabilities)
	}
	h.notifyStatusChanges()
}

// SubscribeStatusChanges 注册插件装载状态变化回调，对齐 subscribe_status_changes
func (h *Host) SubscribeStatusChanges(callback func()) func() {
	if callback == nil {
		return func() {}
	}
	h.mu.Lock()
	if h.statusSubscribers == nil {
		h.statusSubscribers = make(map[uint64]func())
	}
	h.statusSubscriberSeq++
	id := h.statusSubscriberSeq
	h.statusSubscribers[id] = callback
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.statusSubscribers, id)
		h.mu.Unlock()
	}
}

// notifyStatusChanges 在锁外触发状态变化回调，避免回调内再次访问 Host 造成死锁
func (h *Host) notifyStatusChanges() {
	h.ReconcileUsage()
	h.mu.RLock()
	callbacks := make([]func(), 0, len(h.statusSubscribers))
	for _, callback := range h.statusSubscribers {
		callbacks = append(callbacks, callback)
	}
	h.mu.RUnlock()
	for _, callback := range callbacks {
		callback()
	}
}

// ListPlugins 返回当前 Host 已知的插件列表
func (h *Host) ListPlugins() []PluginRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]PluginRecord, 0, len(h.plugins))
	for _, rec := range h.plugins {
		out = append(out, rec)
	}
	return out
}

// RequestShutdown 标记当前 Host 正在关停，后续 Runner 停止使用快速时序
func (h *Host) RequestShutdown() {
	if h != nil {
		h.shutdownRequested.Store(true)
	}
}

// shutdownRequestedNow 返回 Host 或进程级关停状态
func (h *Host) shutdownRequestedNow() bool {
	return (h != nil && h.shutdownRequested.Load()) || IsShutdownRequested()
}

// Stop 停止 Host（注销插件网关驱动、终止 Runner 子进程并关闭 RPC 监听）
func (h *Host) Stop(ctx context.Context) error {
	h.RequestShutdown()
	h.stopEventDispatch()
	usageErr := h.stopUsageTracking()
	gatewayErr := h.UnregisterAllPluginGatewayDrivers(ctx)
	h.StopAllRunners()
	if usageErr != nil {
		return errors.Join(gatewayErr, usageErr)
	}
	return gatewayErr
}

// stopEventDispatch 取消未完成的异步事件分发，对齐 EventDispatcher.stop
func (h *Host) stopEventDispatch() {
	if h.eventsCancel != nil {
		h.eventsCancel()
	}
}

// Hooks 返回命名 Hook 注册表
func (h *Host) Hooks() *hooks.Registry {
	return h.hooks
}

// HookSpecs 返回 Hook 规格注册中心
func (h *Host) HookSpecs() *hooks.SpecRegistry {
	return h.hookSpecs
}

// Components 返回统一组件注册表（含 EVENT_HANDLER / HOOK_HANDLER 声明）
func (h *Host) Components() *ComponentRegistry {
	if h == nil {
		return nil
	}
	return h.components
}

// Authorization 返回能力授权表
func (h *Host) Authorization() *CapabilityAuthorization {
	if h == nil {
		return nil
	}
	return h.authorization
}

// APIRegistry 返回插件 API 动态注册表
func (h *Host) APIRegistry() *APIRegistry {
	if h == nil {
		return nil
	}
	return h.apis
}

// LLMProviderRegistry 返回插件 LLM Provider 注册表
func (h *Host) SetMaisakaPort(port PluginMaisakaPort) {
	if h == nil {
		return
	}
	h.maisakaPort = port
}

// MaisakaPort 返回当前注入的 Maisaka 端口
func (h *Host) MaisakaPort() PluginMaisakaPort {
	if h == nil {
		return nil
	}
	return h.maisakaPort
}

func (h *Host) LLMProviderRegistry() *LLMProviderRegistry {
	if h == nil {
		return nil
	}
	return h.llmProviders
}

// Codec 返回 RPC 编解码器
func (h *Host) Codec() protocol.Codec {
	return h.codec
}

// NextRequestID 生成下一个 RPC 请求 ID
func (h *Host) NextRequestID() int64 {
	return h.requestIDs.Next()
}
