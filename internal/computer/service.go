package computer

// 共享沙盒电脑：独立于 Agent 的生命周期、输入互斥及人工接管，对齐 service.py

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Status 对齐 ComputerService.status 与 computer.status WebUI 字段
type Status struct {
	Enabled        bool           `json:"enabled"`
	Paused         bool           `json:"paused"`
	Takeover       bool           `json:"takeover"`
	Error          string         `json:"error"`
	Activity       map[string]any `json:"activity"`
	PendingRequest *string        `json:"pending_request"`
	Connected      bool           `json:"connected"`
	SandboxID      *string        `json:"sandbox_id"`
	SandboxRunning bool           `json:"sandbox_running"`
}

// Service 对齐 service.ComputerService
type Service struct {
	mu       sync.RWMutex
	cfg      Config
	root     string
	bridge   *SandboxBridge
	paused   bool
	takeover bool
	closing  bool
	errMsg   string
	activity map[string]any

	inputRevision uint64
	started       bool

	controlMu   sync.Mutex
	stateMu     sync.Mutex
	stateLoaded bool

	instanceRoot string
	autoConfig   bool
}

// NewService 创建电脑服务；实例目录取 MAIBOT_INSTANCE_DIR，数据根为 data/computer
func NewService(cfg Config) *Service {
	instance := InstanceRoot()
	return NewServiceWithRoot(cfg, filepath.Join(instance, "data", "computer"))
}

// NewServiceWithRoot 用显式数据根创建服务，便于测试与实例隔离
func NewServiceWithRoot(cfg Config, root string) *Service {
	instance := filepath.Dir(filepath.Dir(root))
	if instance == "." || instance == "" {
		instance = InstanceRoot()
	}
	service := &Service{
		cfg:      cfg,
		root:     filepath.Clean(root),
		activity: map[string]any{},
		closing:  true,
	}
	service.bridge = NewSandboxBridge(filepath.Join(instance, "temp", "computer"), filepath.Join(service.root, "workspace"), cfg)
	return service
}

// Config 返回当前配置快照
func (s *Service) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Enabled 返回 computer.enabled
func (s *Service) Enabled() bool {
	return s.Config().Enabled
}

// ToolsEnabled 重新读取实例配置后返回是否注册电脑工具
func (s *Service) ToolsEnabled() bool {
	_ = s.SyncConfig()
	return s.Enabled()
}

// Root 返回 data/computer 数据根
func (s *Service) Root() string { return s.root }

// Bridge 返回底层桥，供测试与状态查看
func (s *Service) Bridge() *SandboxBridge { return s.bridge }

// LastFrameSnapshot 返回桌面最近一帧的只读快照
func (s *Service) LastFrameSnapshot() map[string]any {
	if s == nil || s.bridge == nil {
		return nil
	}
	return s.bridge.lastFrameSnapshot()
}

func (s *Service) setConfig(cfg Config) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.bridge.setConfig(cfg)
}

// SyncConfig 从实例 TOML 重新读取 [computer]；autoConfig 服务才生效
func (s *Service) SyncConfig() error {
	if !s.autoConfig {
		return nil
	}
	cfg, err := LoadConfig(s.instanceRoot)
	if err != nil {
		s.mu.Lock()
		s.errMsg = err.Error()
		s.mu.Unlock()
		return err
	}
	s.mu.RLock()
	previous := s.cfg
	s.mu.RUnlock()
	if cfg == previous {
		return nil
	}
	s.setConfig(cfg)
	if previous.Enabled && !cfg.Enabled {
		return s.Stop(context.Background())
	}
	return nil
}

// Start 对齐 start/reload：注册配置回调并恢复已有实例，不主动创建沙盒
func (s *Service) Start(ctx context.Context) error {
	return s.Reload(ctx)
}

// Reload 对齐 reload：读取状态，更新桥配置；未启用时停止，启用时仅恢复已有实例
func (s *Service) Reload(ctx context.Context) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.reloadLocked(ctx)
}

func (s *Service) reloadLocked(ctx context.Context) error {
	if err := s.loadState(); err != nil {
		return err
	}
	s.mu.Lock()
	s.bridge.setConfig(s.cfg)
	enabled := s.cfg.Enabled
	s.mu.Unlock()
	if !enabled {
		s.mu.Lock()
		s.started = true
		s.mu.Unlock()
		return s.stopLocked(ctx)
	}
	s.mu.Lock()
	s.closing = false
	s.started = true
	s.mu.Unlock()
	if s.bridge.snapshot().Connected {
		return nil
	}
	if _, err := s.bridge.restore(); err != nil {
		var connection *ConnectionError
		if errors.As(err, &connection) {
			s.mu.Lock()
			s.errMsg = err.Error()
			s.paused = true
			s.mu.Unlock()
			return nil
		}
		return err
	}
	return nil
}

// EnsureStarted 在首次工具调用/控制前完成状态恢复
func (s *Service) EnsureStarted(ctx context.Context) error {
	s.mu.RLock()
	started := s.started
	enabled := s.cfg.Enabled
	closing := s.closing
	s.mu.RUnlock()
	if !started || (enabled && closing) {
		return s.Start(ctx)
	}
	return nil
}

// Stop 对齐 stop：先阻止新输入，只保存并断开连接，保留来宾操作
func (s *Service) Stop(ctx context.Context) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.stopLocked(ctx)
}

func (s *Service) stopLocked(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	s.closing = true
	s.inputRevision++
	s.mu.Unlock()
	var firstErr error
	if err := s.loadState(); err != nil {
		firstErr = err
	}
	if err := s.persist(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.bridge.detach(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// syncAndLoad 刷新实例配置并恢复持久状态；首次调用不会启动沙盒
func (s *Service) syncAndLoad() error {
	if err := s.SyncConfig(); err != nil {
		return err
	}
	return s.loadState()
}

func (s *Service) requireEnabledLocked() error {
	if !s.cfg.Enabled || s.closing {
		return fmt.Errorf("电脑服务未启用或正在关闭，请检查 computer.enabled。")
	}
	return nil
}

// RequireEnabled 对齐 Python computer.require_enabled，供 Agent 附图路径映射使用
func (s *Service) RequireEnabled() error {
	if s == nil {
		return fmt.Errorf("电脑服务未启用或正在关闭，请检查 computer.enabled。")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.requireEnabledLocked()
}

// Workspace 返回 data/computer/workspace（来宾 C:\MaiWorkspace 的宿主映射）
func (s *Service) Workspace() string {
	if s == nil || s.bridge == nil {
		return ""
	}
	return s.bridge.Workspace()
}

func (s *Service) requireInputLocked(manual bool) error {
	if err := s.requireEnabledLocked(); err != nil {
		return err
	}
	if manual {
		if !s.takeover {
			return fmt.Errorf("先接管电脑，再发送人工输入。")
		}
		return nil
	}
	if s.paused || s.takeover {
		return fmt.Errorf("电脑已暂停或正在人工接管，请在 WebUI 恢复。")
	}
	return nil
}

// Desktop 执行一批桌面动作；manual 为 WebUI 人工输入
func (s *Service) Desktop(ctx context.Context, actions []map[string]any, timeout *float64, manual bool, reason, caller string) (map[string]any, error) {
	if err := s.syncAndLoad(); err != nil {
		return nil, err
	}
	// 前端可能在首次工具调用前直接接管/输入；先完成懒启动，再判断接管状态
	if err := s.EnsureStarted(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	err := s.requireInputLocked(manual)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := s.requireInputLocked(manual); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	revision := s.inputRevision
	s.activity = map[string]any{"reason": reason, "caller": caller, "actions": actions}
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		return nil, err
	}
	guard := func() error {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if err := s.requireInputLocked(manual); err != nil {
			return err
		}
		if revision != s.inputRevision {
			return fmt.Errorf("电脑控制状态已改变，原排队输入已取消，请重新观察桌面。")
		}
		return nil
	}
	result, err := s.bridge.perform(ctx, actions, timeout, guard)
	if err != nil {
		var connection *ConnectionError
		if errors.As(err, &connection) {
			s.mu.Lock()
			s.errMsg = err.Error()
			s.paused = true
			s.mu.Unlock()
			_ = s.persist()
		}
		return nil, err
	}
	s.mu.Lock()
	s.errMsg = ""
	s.mu.Unlock()
	return result, nil
}

// Observe 独立获取当前桌面，不受输入锁影响
func (s *Service) Observe(ctx context.Context, timeout *float64) (map[string]any, error) {
	if err := s.syncAndLoad(); err != nil {
		return nil, err
	}
	if err := s.EnsureStarted(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	enabled := s.cfg.Enabled
	closing := s.closing
	paused := s.paused
	s.mu.RUnlock()
	if !enabled || closing {
		return nil, fmt.Errorf("电脑服务未启用或正在关闭，请检查 computer.enabled。")
	}
	if paused && !s.bridge.running() {
		return nil, fmt.Errorf("沙盒已停止，请先恢复电脑。")
	}
	return s.bridge.observe(ctx, timeout)
}

// Poll 查询原操作，不重复执行
func (s *Service) Poll(ctx context.Context, operationID string, timeout *float64) (map[string]any, error) {
	if err := s.syncAndLoad(); err != nil {
		return nil, err
	}
	if err := s.EnsureStarted(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	enabled := s.cfg.Enabled
	closing := s.closing
	s.mu.RUnlock()
	if !enabled || closing {
		return nil, fmt.Errorf("电脑服务未启用或正在关闭，请检查 computer.enabled。")
	}
	if !s.bridge.running() {
		return nil, fmt.Errorf("没有运行中的沙盒，不能查询旧操作；请先恢复电脑。")
	}
	return s.bridge.poll(ctx, operationID, timeout)
}

// Cancel 取消指定操作并释放按键
func (s *Service) Cancel(ctx context.Context, operationID string) (map[string]any, error) {
	if err := s.syncAndLoad(); err != nil {
		return nil, err
	}
	if err := s.EnsureStarted(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	err := s.requireInputLocked(false)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if !s.bridge.running() {
		return nil, fmt.Errorf("没有运行中的沙盒，不能取消旧操作。")
	}
	return s.bridge.cancel(ctx, operationID)
}

// Control 处理 pause/resume/takeover/stop/restart
func (s *Service) Control(ctx context.Context, operation string) (Status, error) {
	if err := s.syncAndLoad(); err != nil {
		return s.Status(), err
	}
	switch operation {
	case "pause", "resume", "takeover", "stop", "restart":
	default:
		return s.Status(), fmt.Errorf("operation 只接受 pause/resume/takeover/stop/restart。")
	}
	if err := s.EnsureStarted(ctx); err != nil {
		return s.Status(), err
	}
	s.mu.RLock()
	requireErr := s.requireEnabledLocked()
	s.mu.RUnlock()
	if requireErr != nil {
		return s.Status(), requireErr
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	s.mu.Lock()
	if err := s.requireEnabledLocked(); err != nil {
		s.mu.Unlock()
		return s.Status(), err
	}
	s.inputRevision++
	if operation != "resume" {
		s.paused = true
	}
	s.mu.Unlock()
	defer s.persist()
	var err error
	switch operation {
	case "takeover":
		err = s.takeoverLocked(ctx)
	case "resume":
		err = s.resumeLocked(ctx)
	case "stop", "restart":
		err = s.stopOrRestartLocked(ctx, operation == "restart")
	}
	if err != nil {
		s.mu.Lock()
		s.errMsg = err.Error()
		s.paused = true
		s.mu.Unlock()
		return s.Status(), err
	}
	return s.Status(), nil
}

func (s *Service) takeoverLocked(ctx context.Context) error {
	s.mu.Lock()
	s.takeover = false
	s.mu.Unlock()
	pending := s.bridge.snapshot().Pending
	if pending != "" {
		cancelled, err := s.bridge.cancel(ctx, pending)
		if err != nil {
			return err
		}
		status, _ := cancelled["status"].(string)
		if status == "running" {
			operationID, _ := cancelled["operation_id"].(string)
			if operationID == "" {
				operationID = pending
			}
			zero := 0.0
			if _, err := s.bridge.poll(ctx, operationID, &zero); err != nil {
				return err
			}
		}
	}
	released, err := s.bridge.perform(ctx, []map[string]any{{"action": string(ActionRelease)}}, nil, nil)
	if err != nil {
		return err
	}
	if status, _ := released["status"].(string); status == "running" {
		operationID, _ := released["operation_id"].(string)
		if operationID == "" {
			return connectionErrorf("释放桌面输入响应缺少 operation_id")
		}
		zero := 0.0
		polled, err := s.bridge.poll(ctx, operationID, &zero)
		if err != nil {
			return err
		}
		released = polled
	}
	if success, _ := released["success"].(bool); !success {
		return fmt.Errorf("释放桌面输入失败：%v", released)
	}
	s.mu.Lock()
	s.takeover = true
	s.mu.Unlock()
	return nil
}

func (s *Service) resumeLocked(ctx context.Context) error {
	s.mu.Lock()
	s.takeover = false
	s.mu.Unlock()
	zero := 0.0
	if _, err := s.bridge.observe(ctx, &zero); err != nil {
		return err
	}
	s.mu.Lock()
	s.paused = false
	s.errMsg = ""
	s.mu.Unlock()
	return nil
}

func (s *Service) stopOrRestartLocked(ctx context.Context, restart bool) error {
	s.mu.Lock()
	s.takeover = false
	s.mu.Unlock()
	if err := s.bridge.close(); err != nil {
		return err
	}
	if restart {
		zero := 0.0
		if _, err := s.bridge.observe(ctx, &zero); err != nil {
			return err
		}
		s.mu.Lock()
		s.paused = false
		s.errMsg = ""
		s.mu.Unlock()
	}
	return nil
}

// Status 返回当前状态快照
func (s *Service) Status() Status {
	_ = s.loadState()
	s.mu.RLock()
	cfg := s.cfg
	paused := s.paused
	takeover := s.takeover
	errMsg := s.errMsg
	activity := make(map[string]any, len(s.activity))
	for key, value := range s.activity {
		activity[key] = value
	}
	s.mu.RUnlock()
	snapshot := s.bridge.snapshot()
	var pending *string
	if snapshot.Pending != "" {
		value := snapshot.Pending
		pending = &value
	}
	var sandboxID *string
	if snapshot.SandboxID != "" {
		value := snapshot.SandboxID
		sandboxID = &value
	}
	return Status{
		Enabled:        cfg.Enabled,
		Paused:         paused,
		Takeover:       takeover,
		Error:          errMsg,
		Activity:       activity,
		PendingRequest: pending,
		Connected:      snapshot.Connected,
		SandboxID:      sandboxID,
		SandboxRunning: snapshot.SandboxRunning,
	}
}

// persist 原子写入 data/computer/runtime.json
func (s *Service) persist() error {
	s.mu.RLock()
	state := map[string]any{
		"paused":   s.paused,
		"takeover": s.takeover,
		"error":    s.errMsg,
		"activity": cloneAnyMap(s.activity),
	}
	s.mu.RUnlock()
	// 先释放服务锁再取 stateMu，避免与 loadState 的 stateMu -> s.mu 顺序成环
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(s.root, "runtime.json"), state); err != nil {
		return err
	}
	s.mu.Lock()
	s.stateLoaded = true
	s.mu.Unlock()
	return nil
}

func (s *Service) loadState() error {
	s.stateMu.Lock()
	s.mu.RLock()
	loaded := s.stateLoaded
	s.mu.RUnlock()
	if loaded {
		s.stateMu.Unlock()
		return nil
	}
	path := filepath.Join(s.root, "runtime.json")
	legacyPath := filepath.Join(filepath.Dir(s.root), "agent", "runtime.json")
	state := map[string]any{}
	if fileExists(path) {
		decoded, err := readJSONMap(path)
		if err != nil {
			s.stateMu.Unlock()
			return err
		}
		state = decoded
	} else if fileExists(legacyPath) {
		decoded, err := readJSONMap(legacyPath)
		if err != nil {
			s.stateMu.Unlock()
			return err
		}
		if _, ok := decoded["takeover"]; !ok {
			s.mu.Lock()
			s.stateLoaded = true
			s.mu.Unlock()
			s.stateMu.Unlock()
			return nil
		}
		state = decoded
	} else {
		s.mu.Lock()
		s.stateLoaded = true
		s.mu.Unlock()
		s.stateMu.Unlock()
		return nil
	}
	s.mu.Lock()
	s.paused, _ = state["paused"].(bool)
	s.takeover, _ = state["takeover"].(bool)
	s.errMsg, _ = state["error"].(string)
	if activity, ok := state["activity"].(map[string]any); ok && activity != nil {
		s.activity = cloneAnyMap(activity)
	} else {
		s.activity = map[string]any{}
	}
	s.stateLoaded = true
	s.mu.Unlock()
	s.stateMu.Unlock()
	return s.persist()
}

// ListTools 按 enabled 返回工具清单
func (s *Service) ListTools() []ToolSpec {
	return ListTools(s.Enabled())
}

var (
	defaultMu      sync.Mutex
	defaultService *Service
	defaultRoot    string
	defaultPinned  bool
)

// ServiceForRoot 返回实例根目录对应的进程内默认服务
func ServiceForRoot(root string) *Service {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		root = InstanceRoot()
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultService != nil && (defaultPinned || defaultRoot == root) {
		return defaultService
	}
	cfg, _ := LoadConfig(root)
	service := NewServiceWithRoot(cfg, filepath.Join(root, "data", "computer"))
	service.instanceRoot = root
	service.autoConfig = true
	defaultService = service
	defaultRoot = root
	return service
}

// DefaultService 使用 MAIBOT_INSTANCE_DIR/当前目录的默认服务
func DefaultService() *Service {
	return ServiceForRoot(InstanceRoot())
}

// ToolsEnabled 返回当前实例是否启用电脑；本机调试模式保留旧注册行为
func ToolsEnabled() bool {
	return LocalModeEnabled() || DefaultService().ToolsEnabled()
}

// DefaultServiceForRoot 供 WebUI 等已知实例目录的调用点使用
func DefaultServiceForRoot(root string) *Service {
	return ServiceForRoot(root)
}

// SetDefaultService 仅供测试替换进程内默认服务
func SetDefaultService(service *Service) {
	defaultMu.Lock()
	defaultService = service
	defaultRoot = ""
	defaultPinned = true
	defaultMu.Unlock()
}
