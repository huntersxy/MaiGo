package memorix

// A_memorix 宿主服务：对齐 src/A_memorix/host_service.py 的生命周期、状态机与 Host 门面
// 差异：Python 用 asyncio.Task/Lock；Go 用 goroutine + context.CancelFunc + sync.Mutex

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

const (
	hostStateStopped   = "stopped"
	hostStateStarting  = "starting"
	hostStateMigrating = "migrating"
	hostStateReady     = "ready"
	hostStateFailed    = "failed"
)

// AMemorixHostService 是 A_memorix 长期记忆宿主服务
// 对应 Python 模块级单例 a_memorix_host_service，依赖全部由 HostServiceOptions 注入
type AMemorixHostService struct {
	opts    HostServiceOptions
	fileOps HostFileOps

	// lifecycleMu 串行化 Start/Stop/Reload/OnConfigReload，对齐 Python _lock 的生命周期用途
	lifecycleMu sync.Mutex

	mu               sync.Mutex
	kernel           HostKernel
	state            string
	startupCancel    context.CancelFunc
	startupDone      chan struct{}
	startupRunning   bool
	startupError     string
	startupStage     string
	startupStartedAt *float64
	startupFinishAt  *float64

	queueMu          sync.Mutex
	queuePending     int
	queueCacheLoaded bool

	configMu    sync.RWMutex
	configCache map[string]any

	configUpdateMu           sync.Mutex
	reloadCallbackMu         sync.Mutex
	reloadCallbackRegistered bool
	suppressReloadCallback   atomic.Bool
}

// NewAMemorixHostService 构造宿主服务；nil 依赖会退化为显式失败或本包默认实现
func NewAMemorixHostService(opts HostServiceOptions) *AMemorixHostService {
	if opts.FileOps == nil {
		opts.FileOps = osHostFileOps{}
	}
	return &AMemorixHostService{
		opts:    opts,
		fileOps: opts.FileOps,
		state:   hostStateStopped,
	}
}

// Start 对齐 host_service.start：未启用时只记日志，启用时后台拉起内核
func (s *AMemorixHostService) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.IsEnabled() {
		zap.L().Info("A_Memorix 未启用，跳过长期记忆运行时初始化")
		return nil
	}
	return s.ensureStartupTaskLocked(ctx)
}

// Stop 对齐 host_service.stop
func (s *AMemorixHostService) Stop(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.shutdownLocked(ctx)
}

// Reload 对齐 host_service.reload：重启内核并清空配置缓存
func (s *AMemorixHostService) Reload(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	if err := s.shutdownLocked(ctx); err != nil {
		return err
	}
	s.clearConfigCache()
	config := s.readConfig()
	if isEnabledConfig(config) {
		return s.ensureStartupTaskLocked(ctx)
	}
	zap.L().Info("A_Memorix 配置为未启用，运行时保持关闭")
	return nil
}

// WaitStartup 等待后台初始化结束，供启动编排与测试使用
func (s *AMemorixHostService) WaitStartup(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		state := s.state
		errMessage := s.startupError
		done := s.startupDone
		running := s.startupRunning
		s.mu.Unlock()

		// 状态先到 ready 但回放队列尚未结束时，继续等 startupDone 关闭
		if running && done != nil {
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		switch state {
		case hostStateReady:
			return nil
		case hostStateFailed:
			return errors.New(errMessage)
		case hostStateStopped:
			return nil
		}
		if done == nil {
			return nil
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// State 返回当前启动状态，取值 stopped/starting/migrating/ready/failed
func (s *AMemorixHostService) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *AMemorixHostService) ensureStartupTask(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.ensureStartupTaskLocked(ctx)
}

func (s *AMemorixHostService) ensureStartupTaskLocked(_ context.Context) error {
	if err := s.ensureStartupQueueCache(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.kernel != nil && s.state == hostStateReady {
		s.mu.Unlock()
		return nil
	}
	if s.startupRunning {
		s.mu.Unlock()
		return nil
	}
	s.state = hostStateStarting
	s.startupError = ""
	s.startupStage = ""
	started := nowUnixFloat()
	s.startupStartedAt = &started
	s.startupFinishAt = nil
	runCtx, cancel := context.WithCancel(context.Background())
	s.startupCancel = cancel
	done := make(chan struct{})
	s.startupDone = done
	s.startupRunning = true
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.startupKernelTask(runCtx)
	}()
	return nil
}

func (s *AMemorixHostService) startupKernelTask(ctx context.Context) {
	config := s.readConfig()
	if !isEnabledConfig(config) {
		s.mu.Lock()
		s.state = hostStateStopped
		finished := nowUnixFloat()
		s.startupFinishAt = &finished
		s.startupRunning = false
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	s.state = hostStateMigrating
	s.startupStage = "startup_migration"
	s.mu.Unlock()

	if s.opts.KernelFactory == nil {
		s.failStartup(nil, "startup", errors.New("A_Memorix 内核工厂未配置"))
		return
	}
	kernel, err := s.opts.KernelFactory(ctx, s.pluginRoot(), config)
	if err == nil && kernel == nil {
		err = errors.New("A_Memorix 内核工厂返回空内核")
	}
	if err != nil {
		if ctx.Err() != nil {
			s.cancelStartup(nil)
			return
		}
		s.failStartup(nil, "", err)
		return
	}
	if err := kernel.Initialize(ctx); err != nil {
		if ctx.Err() != nil {
			s.cancelStartup(kernel)
			return
		}
		s.failStartup(kernel, "startup", err)
		return
	}
	if ctx.Err() != nil {
		s.cancelStartup(kernel)
		return
	}

	s.mu.Lock()
	s.kernel = kernel
	s.state = hostStateReady
	s.startupError = ""
	s.startupStage = ""
	finished := nowUnixFloat()
	s.startupFinishAt = &finished
	s.mu.Unlock()
	s.setRuntimeKernel(kernel)

	if err := s.replayStartupWriteQueue(ctx, kernel); err != nil {
		s.failStartup(kernel, "startup", err)
		return
	}

	s.mu.Lock()
	if s.kernel != nil && s.state == hostStateReady {
		finished = nowUnixFloat()
		s.startupFinishAt = &finished
	}
	s.startupRunning = false
	s.mu.Unlock()
}

func (s *AMemorixHostService) failStartup(kernel HostKernel, stage string, cause error) {
	s.shutdownKernel(kernel)
	s.setRuntimeKernel(nil)

	s.mu.Lock()
	s.kernel = nil
	s.state = hostStateFailed
	s.startupError = cause.Error()
	if s.startupStage == "" {
		s.startupStage = stage
	}
	finished := nowUnixFloat()
	s.startupFinishAt = &finished
	s.startupRunning = false
	s.mu.Unlock()
	zap.L().Error("A_Memorix 后台初始化失败", zap.Error(cause))
}

func (s *AMemorixHostService) cancelStartup(kernel HostKernel) {
	s.shutdownKernel(kernel)
	s.setRuntimeKernel(nil)

	s.mu.Lock()
	s.kernel = nil
	s.state = hostStateStopped
	finished := nowUnixFloat()
	s.startupFinishAt = &finished
	s.startupRunning = false
	s.mu.Unlock()
}

func (s *AMemorixHostService) shutdownKernel(kernel HostKernel) {
	if kernel == nil {
		return
	}
	if err := kernel.Shutdown(context.Background()); err != nil {
		zap.L().Warn("A_Memorix 内核停机失败，尝试 Close", zap.Error(err))
		kernel.Close()
	}
}

func (s *AMemorixHostService) shutdownLocked(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	cancel := s.startupCancel
	done := s.startupDone
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	s.mu.Lock()
	kernel := s.kernel
	s.startupCancel = nil
	s.startupDone = nil
	s.startupRunning = false
	s.mu.Unlock()

	if kernel != nil {
		if err := kernel.Shutdown(ctx); err != nil {
			kernel.Close()
			s.clearKernel(kernel)
			return err
		}
	}

	s.clearKernel(kernel)
	s.setRuntimeKernel(nil)
	return nil
}

func (s *AMemorixHostService) clearKernel(HostKernel) {
	s.mu.Lock()
	s.kernel = nil
	s.state = hostStateStopped
	finished := nowUnixFloat()
	s.startupFinishAt = &finished
	s.mu.Unlock()
}

func (s *AMemorixHostService) setRuntimeKernel(kernel HostKernel) {
	if s.opts.RegisterRuntimeKernel != nil {
		s.opts.RegisterRuntimeKernel(kernel)
	}
}

// ensureKernel 对齐 _ensure_kernel：内核缺失时抛出启动错误或初始化提示
func (s *AMemorixHostService) ensureKernel() (HostKernel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kernel == nil {
		message := s.startupError
		if message == "" {
			message = "A_Memorix 正在初始化"
		}
		return nil, errors.New(message)
	}
	return s.kernel, nil
}

func (s *AMemorixHostService) kernelSnapshot() (HostKernel, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kernel, s.state
}

func (s *AMemorixHostService) startupStatusPayload() map[string]any {
	memoryEnabled := s.IsEnabled()
	kernel, state := s.kernelSnapshot()

	runtimeStatus := map[string]any{}
	statusReport := map[string]any{}
	if kernel != nil && state == hostStateReady {
		runtimeStatus = kernel.RuntimeCapabilityStatus()
		if reporter, ok := kernel.(HostKernelStatusReporter); ok {
			statusReport = reporter.HostStatusReport()
		}
	}
	degradedReasons := anyToStringSliceOrEmpty(statusReport["degraded_reasons"])
	missingServices := anyToStringSliceOrEmpty(statusReport["missing_services"])
	missingRequiredServices := anyToStringSliceOrEmpty(statusReport["missing_required_services"])

	runtimeReady := state == hostStateReady
	if value, ok := runtimeStatus["runtime_ready"].(bool); ok {
		runtimeReady = value
	}
	retrievalReady, _ := runtimeStatus["retrieval_ready"].(bool)
	degraded, _ := runtimeStatus["degraded"].(bool)
	if len(degradedReasons) > 0 {
		degraded = true
	}
	retrievalMode := anyToString(runtimeStatus["retrieval_mode"])
	if retrievalMode == "" {
		retrievalMode = "unavailable"
	}
	vectorHealth := anyToMap(runtimeStatus["vector_health"])
	if vectorHealth == nil {
		vectorHealth = map[string]any{}
	}

	payload := map[string]any{
		"enabled":                   memoryEnabled,
		"memory_enabled":            memoryEnabled,
		"runtime_ready":             runtimeReady,
		"retrieval_ready":           retrievalReady,
		"degraded":                  degraded,
		"degraded_reasons":          degradedReasons,
		"missing_services":          missingServices,
		"missing_required_services": missingRequiredServices,
		"retrieval_mode":            retrievalMode,
		"available_channels":        anyToStringSliceOrEmpty(runtimeStatus["available_channels"]),
		"unavailable_channels":      anyToStringSliceOrEmpty(runtimeStatus["unavailable_channels"]),
		"vector_health":             vectorHealth,
		"startup_state":             state,
		"initializing":              state == hostStateStarting || state == hostStateMigrating,
		"initialization_failed":     state == hostStateFailed,
		"error_stage":               s.startupStageValue(),
		"error":                     s.startupErrorValue(),
		"startup_started_at":        s.startupTimeValue(true),
		"startup_finished_at":       s.startupTimeValue(false),
		"startup_queue_pending":     s.startupQueuePending(),
		"data_dir":                  s.GetRuntimeDataDir(),
	}
	return payload
}

// StartupStatusPayload 导出启动状态快照，对齐 _startup_status_payload
func (s *AMemorixHostService) StartupStatusPayload() map[string]any {
	return s.startupStatusPayload()
}

func (s *AMemorixHostService) startupStageValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startupStage
}

func (s *AMemorixHostService) startupErrorValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startupError
}

func (s *AMemorixHostService) startupTimeValue(started bool) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if started {
		if s.startupStartedAt == nil {
			return nil
		}
		return *s.startupStartedAt
	}
	if s.startupFinishAt == nil {
		return nil
	}
	return *s.startupFinishAt
}

func (s *AMemorixHostService) pluginRoot() string {
	if s.opts.PluginRoot != nil {
		return s.opts.PluginRoot()
	}
	return RepoRoot()
}

func (s *AMemorixHostService) schemaPath() string {
	if s.opts.SchemaPath != nil {
		return s.opts.SchemaPath()
	}
	return SchemaPath()
}

func anyToStringSliceOrEmpty(value any) []string {
	values := anyToStringSlice(value)
	if values == nil {
		return []string{}
	}
	return values
}
