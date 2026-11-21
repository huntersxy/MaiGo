package desktoppet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/common/media"
)

// stopTimeout 关闭 stdin 后等待窗口自行退出的时长，对齐 STOP_TIMEOUT
const stopTimeout = 5 * time.Second

// stderrTailLines stderr 尾部保留行数，对齐 _run 的 deque(maxlen=30)
const stderrTailLines = 30

// Config 对齐 global_config.desktop_pet 子集
type Config struct {
	Enabled                      bool
	Mode                         string
	ChatToolsEnabled             bool
	ShowCurrentTask              bool
	SkinID                       string
	MessageSound                 string
	ProactiveWakeEnabled         bool
	ProactiveWakeIntervalMinutes int
	ScreenReadEnabled            bool
	ScreenReadIntervalSeconds    int
	ScreenSampleIntervalSeconds  float64
	ScreenChangeThreshold        float64
	ExplorationEnabled           bool
}

// Status 对齐 DesktopPetService.status
type Status struct {
	Enabled   bool   `json:"enabled"`
	Supported bool   `json:"supported"`
	Running   bool   `json:"running"`
	Ready     bool   `json:"ready"`
	Visible   bool   `json:"visible"`
	Error     string `json:"error"`
	SkinID    string `json:"skin_id"`
	PID       int    `json:"pid"`
	Thinking  bool   `json:"thinking"`
	Bubble    string `json:"bubble"`
	Mode      string `json:"mode"`
	ClientID  string `json:"client_id"`
}

// Service 桌宠窗口进程与行协议客户端，对齐 DesktopPetService
type Service struct {
	cfg   Config
	spawn SpawnEnv

	mu      sync.RWMutex
	writeMu sync.Mutex

	cmd        *exec.Cmd
	stdin      io.WriteCloser
	ctx        context.Context
	waitCh     chan struct{}
	pending    map[string]chan callResult
	running    bool
	ready      bool
	visible    bool
	closing    bool
	errMsg     string
	skinID     string
	thinking   bool
	bubble     string
	stderrTail []string
	hooks      HookEmitter
	buttons    ButtonProvider

	buttonRevision uint64
	buttonSyncing  bool

	chatRelay       ChatRelay
	historyProvider HistoryProvider

	activeMode        string
	remote            *RemotePeer
	proactiveWake     func(ctx context.Context) error
	proactiveCancel   context.CancelFunc
	proactiveInterval int
	lastOwnerActivity time.Time
	unansweredWakes   int

	screenCancel      context.CancelFunc
	screenGeneration  uint64
	screenBusy        bool
	screenLastCapture string
	screenError       string
	appTodoCancel     context.CancelFunc
	promptRenderer    func(name string) (string, error)
	ownerTaskEnqueuer func(ctx context.Context, sourceKind, text, reason string, images []media.Ref) error
	ownerSessionID    func(ctx context.Context) (string, error)
	captionSummarizer func(ctx context.Context, data map[string]any) (string, error)
	captionRevision   uint64
	captionKey        string
	captionCancel     context.CancelFunc
}

// NewService 创建桌宠服务；未显式指定皮肤时读取 desktop_pet 配置段
func NewService(cfg Config, spawn SpawnEnv) *Service {
	settings := LoadDesktopPetSettings()
	if cfg.SkinID == "" {
		cfg.SkinID = settings.SkinID
	}
	if cfg.MessageSound == "" {
		cfg.MessageSound = settings.MessageSound
	}
	if !cfg.ChatToolsEnabled {
		cfg.ChatToolsEnabled = settings.ChatToolsEnabled
	}
	if !cfg.ShowCurrentTask {
		cfg.ShowCurrentTask = settings.ShowCurrentTask
	}
	if !cfg.ProactiveWakeEnabled {
		cfg.ProactiveWakeEnabled = settings.ProactiveWakeEnabled
	}
	if cfg.ProactiveWakeIntervalMinutes <= 0 {
		cfg.ProactiveWakeIntervalMinutes = settings.ProactiveWakeIntervalMinutes
	}
	skin := cfg.SkinID
	if skin == "" {
		skin = "builtin"
	}
	if cfg.Mode == "" {
		cfg.Mode = settings.Mode
	}
	if !cfg.ScreenReadEnabled {
		cfg.ScreenReadEnabled = settings.ScreenReadEnabled
	}
	if cfg.ScreenReadIntervalSeconds <= 0 {
		cfg.ScreenReadIntervalSeconds = settings.ScreenReadIntervalSeconds
	}
	if cfg.ScreenSampleIntervalSeconds <= 0 {
		cfg.ScreenSampleIntervalSeconds = settings.ScreenSampleIntervalSeconds
	}
	if cfg.ScreenChangeThreshold <= 0 {
		cfg.ScreenChangeThreshold = settings.ScreenChangeThreshold
	}
	if !cfg.ExplorationEnabled {
		cfg.ExplorationEnabled = settings.ExplorationEnabled
	}
	return &Service{
		cfg:               cfg,
		activeMode:        cfg.Mode,
		spawn:             spawn,
		skinID:            skin,
		pending:           make(map[string]chan callResult),
		proactiveInterval: cfg.ProactiveWakeIntervalMinutes,
		lastOwnerActivity: time.Now(),
	}
}

// Start 对齐 start/reload；Windows 上 spawn pet_window.py 并开始读取事件
func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.cfg.Enabled {
		running := s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil
		s.mu.Unlock()
		if running {
			// 关闭配置时先停掉旧窗口，避免留下孤儿进程
			if err := s.Stop(ctx); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.clearProcessLocked()
		s.errMsg = ""
		s.mu.Unlock()
		return nil
	}
	if s.cfg.Mode == "remote" {
		s.activeMode = "remote"
		s.closing = false
		s.running = false
		s.mu.Unlock()
		s.restartProactiveWake()
		s.startAppTodoLoop()
		return nil
	}
	if runtime.GOOS != "windows" {
		s.clearProcessLocked()
		s.errMsg = ErrUnsupportedPlatform.Error()
		s.mu.Unlock()
		return ErrUnsupportedPlatform
	}
	if s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil {
		ready := s.ready
		previousSkin := s.skinID
		skinID := s.cfg.SkinID
		if skinID == "" {
			skinID = "builtin"
		}
		if !ready {
			s.skinID = skinID
		}
		s.mu.Unlock()
		if ready && skinID != previousSkin {
			return s.ApplySkin(ctx, skinID)
		}
		return nil
	}

	spawn := resolveSpawnEnv(s.spawn)
	s.clearProcessLocked()
	s.errMsg = ""
	if s.cfg.SkinID != "" {
		s.skinID = s.cfg.SkinID
	}

	cmd, stdin, stdout, stderr, err := buildPetCommand(spawn)
	if err != nil {
		s.errMsg = err.Error()
		s.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		message := fmt.Sprintf("拉起桌宠子进程失败: %v", err)
		s.errMsg = message
		s.mu.Unlock()
		return errors.New(message)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.ctx = ctx
	s.pending = make(map[string]chan callResult)
	s.running = true
	s.ready = false
	s.visible = false
	s.closing = false
	s.stderrTail = nil
	waitCh := make(chan struct{})
	s.waitCh = waitCh

	go s.watchProcess(cmd, waitCh)
	go s.readEvents(ctx, stdout)
	go s.drainPetStderr(stderr)
	go s.watchReady(waitCh)
	s.mu.Unlock()

	zap.L().Info("桌宠 Python 子进程已拉起",
		zap.Int("pid", cmd.Process.Pid),
		zap.String("python", cmd.Path),
		zap.Strings("args", cmd.Args[1:]),
		zap.String("workdir", cmd.Dir))
	return nil
}

// ApplyConfig 对齐 DesktopPetService._reload：按新配置停止、启动或热切皮肤
func (s *Service) ApplyConfig(ctx context.Context, cfg Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg.SkinID = strings.TrimSpace(cfg.SkinID)
	if cfg.SkinID == "" {
		cfg.SkinID = "builtin"
	}
	cfg.MessageSound = strings.TrimSpace(cfg.MessageSound)
	if cfg.MessageSound == "soft" || cfg.MessageSound == "" {
		cfg.MessageSound = "wood"
	}
	switch cfg.MessageSound {
	case "wood", "bubbles", "bell", "water", "off":
	default:
		cfg.MessageSound = "wood"
	}

	s.mu.Lock()
	running := s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil
	ready := s.ready
	currentSkin := s.skinID
	s.cfg = cfg
	s.activeMode = cfg.Mode
	remote := s.remote
	if !cfg.Enabled {
		s.mu.Unlock()
		if remote != nil {
			s.DetachRemote(remote)
		}
		if running {
			if err := s.Stop(ctx); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.errMsg = ""
		s.mu.Unlock()
		return nil
	}
	if cfg.Mode == "remote" {
		s.mu.Unlock()
		if running {
			if err := s.Stop(ctx); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.closing = false
		s.errMsg = ""
		s.mu.Unlock()
		s.startAppTodoLoop()
		s.restartProactiveWake()
		return nil
	}
	if remote != nil {
		s.mu.Unlock()
		s.DetachRemote(remote)
		s.mu.Lock()
	}
	if running && !ready {
		s.skinID = cfg.SkinID
	}
	s.mu.Unlock()

	if runtime.GOOS != "windows" {
		err := ErrUnsupportedPlatform
		s.setError(err)
		return err
	}
	if running && ready {
		if cfg.SkinID != currentSkin {
			if err := s.ApplySkin(ctx, cfg.SkinID); err != nil {
				return err
			}
		}
		s.restartProactiveWake()
		return nil
	}
	if running {
		// 就绪前收到配置变更时按 Python _reload 重建窗口，让新配置从首帧生效
		if err := s.Stop(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		s.errMsg = ""
		s.mu.Unlock()
	}
	return s.Start(ctx)
}

// Reload 读取 bot_config.toml 的 desktop_pet 段并应用
func (s *Service) Reload(ctx context.Context) error {
	settings := LoadDesktopPetSettings()
	return s.ApplyConfig(ctx, Config{
		Enabled:                      settings.Enabled,
		Mode:                         settings.Mode,
		ChatToolsEnabled:             settings.ChatToolsEnabled,
		ShowCurrentTask:              settings.ShowCurrentTask,
		SkinID:                       settings.SkinID,
		MessageSound:                 settings.MessageSound,
		ProactiveWakeEnabled:         settings.ProactiveWakeEnabled,
		ProactiveWakeIntervalMinutes: settings.ProactiveWakeIntervalMinutes,
		ScreenReadEnabled:            settings.ScreenReadEnabled,
		ScreenReadIntervalSeconds:    settings.ScreenReadIntervalSeconds,
		ScreenSampleIntervalSeconds:  settings.ScreenSampleIntervalSeconds,
		ScreenChangeThreshold:        settings.ScreenChangeThreshold,
		ExplorationEnabled:           settings.ExplorationEnabled,
	})
}

func (s *Service) Restart(ctx context.Context) error {
	s.mu.RLock()
	enabled, mode := s.cfg.Enabled, s.cfg.Mode
	windowCtx := s.ctx
	s.mu.RUnlock()
	if !enabled || mode != "local" {
		return errors.New("请先启用本机桌宠，再重载桌宠")
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(windowCtx)
}

// Config 返回当前桌宠配置快照
func (s *Service) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// resolveSpawnEnv 用环境变量补全缺失的 spawn 配置
func resolveSpawnEnv(spawn SpawnEnv) SpawnEnv {
	if strings.TrimSpace(spawn.ProjectRoot) != "" && strings.TrimSpace(spawn.PythonBin) != "" {
		return spawn
	}
	fromEnv := LoadConfigFromEnv().SpawnEnv
	if strings.TrimSpace(spawn.ProjectRoot) == "" {
		spawn.ProjectRoot = fromEnv.ProjectRoot
	}
	if strings.TrimSpace(spawn.InstanceRoot) == "" {
		spawn.InstanceRoot = fromEnv.InstanceRoot
	}
	if strings.TrimSpace(spawn.PythonBin) == "" {
		spawn.PythonBin = fromEnv.PythonBin
	}
	return spawn
}

// watchReady 未在 readyTimeout 内收到 ready 即终止窗口，对齐 _run 的 30 秒超时
func (s *Service) watchReady(done <-chan struct{}) {
	timer := time.NewTimer(readyTimeout)
	defer timer.Stop()
	select {
	case <-timer.C:
		s.mu.RLock()
		ready := s.ready
		s.mu.RUnlock()
		if !ready {
			s.fail(errors.New("桌宠窗口在 30 秒内未就绪"))
		}
	case <-done:
	}
}

func (s *Service) watchProcess(cmd *exec.Cmd, done chan struct{}) {
	waitErr := cmd.Wait()
	close(done)

	s.mu.Lock()
	if s.cmd != cmd {
		s.mu.Unlock()
		return
	}
	wasReady := s.ready
	closing := s.closing
	tail := append([]string(nil), s.stderrTail...)

	s.cmd = nil
	s.stdin = nil
	s.waitCh = nil
	s.running = false
	s.ready = false
	s.visible = false
	pending := s.pending
	s.pending = make(map[string]chan callResult)
	if !closing && s.errMsg == "" {
		// Python 侧即使退出码为 0 也视为异常退出，stderr 尾部一并带回
		message := "桌宠窗口退出"
		if state := cmd.ProcessState; state != nil {
			message += fmt.Sprintf(": code=%d", state.ExitCode())
		}
		if waitErr != nil {
			message += ": " + waitErr.Error()
		}
		if len(tail) > 0 {
			message += "\n" + strings.Join(tail, "\n")
		}
		s.errMsg = message
	}
	s.mu.Unlock()

	for _, waiter := range pending {
		select {
		case waiter <- callResult{err: errIPCClosed}:
		default:
		}
	}

	if waitErr != nil && !closing {
		zap.L().Warn("桌宠子进程异常退出", zap.Error(waitErr))
	}
	if wasReady {
		reason := "unexpected"
		if closing {
			reason = "stopped"
		}
		s.emitHook(s.serviceContext(), "closed", map[string]any{"reason": reason})
	}
}

func (s *Service) serviceContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Stop 对齐 stop/_stop：关闭 stdin 让窗口自行退出，超时后强制结束
func (s *Service) Stop(ctx context.Context) error {
	s.stopScreenReader()
	s.stopAppTodoLoop()
	s.mu.Lock()
	if s.remote != nil {
		peer := s.remote
		s.closing = true
		s.mu.Unlock()
		peer.Close("桌宠已停止")
		s.DetachRemote(peer)
		return nil
	}
	cmd := s.cmd
	stdin := s.stdin
	waitCh := s.waitCh
	if cmd == nil || cmd.Process == nil {
		s.clearProcessLocked()
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.thinking = false
	s.mu.Unlock()

	pid := cmd.Process.Pid
	if stdin != nil {
		// 关闭指令管道后窗口进程会自行收起退出
		_ = stdin.Close()
	}

	select {
	case <-waitCh:
	case <-time.After(stopTimeout):
		zap.L().Warn("桌宠窗口未及时退出，强制结束")
		_ = cmd.Process.Kill()
		select {
		case <-waitCh:
		case <-time.After(2 * time.Second):
		}
	}

	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	s.stopProactiveWake()
	zap.L().Info("桌宠 Python 子进程已停止", zap.Int("pid", pid))
	return nil
}

func (s *Service) clearProcessLocked() {
	s.cmd = nil
	s.stdin = nil
	s.waitCh = nil
	s.running = false
	s.ready = false
	s.visible = false
}

// Status 返回桌宠状态
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pid := 0
	running := s.running
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
		running = s.running && s.cmd.ProcessState == nil
	} else {
		running = false
	}
	clientID := ""
	if s.remote != nil {
		running = true
		clientID = s.remote.clientID
	}
	return Status{
		Enabled:   s.cfg.Enabled,
		Supported: s.cfg.Mode == "remote" || runtime.GOOS == "windows",
		Running:   running,
		Ready:     s.ready && !s.closing,
		Visible:   s.visible,
		Error:     s.errMsg,
		SkinID:    s.skinID,
		PID:       pid,
		Thinking:  s.thinking && s.ready && !s.closing,
		Bubble:    s.bubble,
		Mode:      s.cfg.Mode,
		ClientID:  clientID,
	}
}
