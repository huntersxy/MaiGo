package host

// Bot 生命周期：注册表记录期望状态，宿主让实际进程向它收敛，对齐 src/host/supervisor.py。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// StopTimeout 宿主的硬截止比 Worker 自身关闭预算多 15 秒
	StopTimeout = WorkerShutdownTimeout + 15*time.Second
	// restartDelayMin 崩溃重启初始延迟
	restartDelayMin = 1 * time.Second
	// restartDelayMax 崩溃重启最大延迟
	restartDelayMax = 300 * time.Second
	// crashWindow 崩溃计数窗口
	crashWindow = 600 * time.Second
	// crashLimit 窗口内允许的最大崩溃次数
	crashLimit = 5
	// pollInterval 进程退出轮询间隔
	pollInterval = 250 * time.Millisecond
)

// BotShutdownError 有 Bot 未能正常停止
type BotShutdownError struct {
	Message string
}

func (e *BotShutdownError) Error() string { return e.Message }

// BotStatus 单个 Bot 的运行时状态
type BotStatus struct {
	BotID               string   `json:"bot_id"`
	Name                string   `json:"name"`
	Directory           string   `json:"directory"`
	Owned               bool     `json:"owned"`
	CreatedAt           string   `json:"created_at"`
	Initialization      string   `json:"initialization"`
	InitializationError string   `json:"initialization_error"`
	DesiredState        string   `json:"desired_state"`
	RestartPolicy       string   `json:"restart_policy"`
	Status              string   `json:"status"`
	Detail              string   `json:"detail"`
	PID                 *int     `json:"pid"`
	StartedAt           string   `json:"started_at"`
	ExitedAt            string   `json:"exited_at"`
	ExitCode            *int     `json:"exit_code"`
	Error               string   `json:"error"`
	RetryAt             *float64 `json:"retry_at"`
	LogPath             string   `json:"log_path"`
}

// SupervisedBot 单个 Bot 的运行时
type SupervisedBot struct {
	bot        BotRecord
	mu         sync.Mutex
	process    *exec.Cmd
	waitDone   chan struct{}
	status     string
	detail     string
	ready      bool
	startedAt  string
	exitedAt   string
	exitCode   *int
	errMsg     string
	retryAt    *float64
	crashes    []time.Time
	endpoint   string
	retryTimer *time.Timer
}

func (b *SupervisedBot) LogPath() string {
	return filepath.Join(b.bot.Directory, "temp", "worker-console.log")
}

func (b *SupervisedBot) StatusPath() string {
	return filepath.Join(b.bot.Directory, "temp", "host-status.json")
}

// Snapshot 运行时状态快照
func (b *SupervisedBot) Snapshot() BotStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := BotStatus{
		BotID: b.bot.BotID, Name: b.bot.Name, Directory: b.bot.Directory, Owned: b.bot.Owned,
		CreatedAt: b.bot.CreatedAt, Initialization: b.bot.Initialization,
		InitializationError: b.bot.Error, DesiredState: b.bot.DesiredState,
		RestartPolicy: b.bot.RestartPolicy, Status: b.status, Detail: b.detail,
		StartedAt: b.startedAt, ExitedAt: b.exitedAt, ExitCode: b.exitCode, Error: b.errMsg,
		RetryAt: b.retryAt, LogPath: b.LogPath(),
	}
	if b.process != nil && b.process.Process != nil {
		pid := b.process.Process.Pid
		status.PID = &pid
	}
	return status
}

// Supervisor 监督工作区内全部 Bot
type Supervisor struct {
	workspace        Workspace
	publicBaseURL    string
	upgradeConfirmed map[string]struct{}
	mu               sync.Mutex
	bots             map[string]*SupervisedBot
	closing          bool
	maintenance      bool
	existingPaths    func() []string
}

// NewSupervisor 创建监督器
func NewSupervisor(workspace Workspace) *Supervisor {
	return &Supervisor{
		workspace:        workspace,
		publicBaseURL:    workspace.Config.AdvertisedBaseURL(),
		upgradeConfirmed: map[string]struct{}{},
		bots:             map[string]*SupervisedBot{},
	}
}

// WorkerCommand 拉起 Worker 的命令；默认使用当前可执行文件
func WorkerCommand(bot BotRecord) []string {
	executable, err := os.Executable()
	if err != nil {
		executable = "maigo"
	}
	return []string{executable}
}

func (s *Supervisor) startBlocker() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("宿主正在关闭，不能启动 Bot")
	}
	if s.maintenance {
		return errors.New("宿主正在维护，完成后会按期望状态恢复 Bot")
	}
	return nil
}

func (s *Supervisor) runtime(bot BotRecord) *SupervisedBot {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance := s.bots[bot.BotID]
	if instance == nil {
		instance = &SupervisedBot{bot: bot, status: "stopped"}
		s.bots[bot.BotID] = instance
	}
	instance.mu.Lock()
	instance.bot = bot
	instance.mu.Unlock()
	return instance
}

// Statuses 全部 Bot 的状态快照
func (s *Supervisor) Statuses() ([]BotStatus, error) {
	bots, err := s.workspace.Registry.ListBots()
	if err != nil {
		return nil, err
	}
	result := make([]BotStatus, 0, len(bots))
	for _, bot := range bots {
		result = append(result, s.runtime(bot).Snapshot())
	}
	return result, nil
}

// Status 单个 Bot 的状态
func (s *Supervisor) Status(botID string) (BotStatus, error) {
	bot, err := s.workspace.Registry.GetBot(botID)
	if err != nil {
		return BotStatus{}, err
	}
	return s.runtime(bot).Snapshot(), nil
}

// ProxyEndpoint 只读内存状态，返回已就绪 Bot 的进程间通道地址
func (s *Supervisor) ProxyEndpoint(botID string) (string, error) {
	s.mu.Lock()
	instance := s.bots[botID]
	s.mu.Unlock()
	if instance == nil {
		return "", errors.New("此 Bot 未运行或尚未就绪")
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.status != "running" || instance.endpoint == "" {
		return "", errors.New("此 Bot 未运行或尚未就绪")
	}
	return instance.endpoint, nil
}

// Start 启动指定 Bot 并记为期望运行
func (s *Supervisor) Start(botID string) (BotStatus, error) {
	if err := s.startBlocker(); err != nil {
		return BotStatus{}, err
	}
	bot, err := s.workspace.Registry.GetBot(botID)
	if err != nil {
		return BotStatus{}, err
	}
	if _, err := s.workspace.Registry.SetDesiredState(botID, "running"); err != nil {
		return BotStatus{}, err
	}
	bot.DesiredState = "running"
	instance := s.runtime(bot)
	if err := s.spawn(context.Background(), instance); err != nil {
		return instance.Snapshot(), err
	}
	return instance.Snapshot(), nil
}

// Stop 停止指定 Bot 并记为期望停止
func (s *Supervisor) Stop(botID string) (BotStatus, error) {
	bot, err := s.workspace.Registry.GetBot(botID)
	if err != nil {
		return BotStatus{}, err
	}
	if _, err := s.workspace.Registry.SetDesiredState(botID, "stopped"); err != nil {
		return BotStatus{}, err
	}
	bot.DesiredState = "stopped"
	instance := s.runtime(bot)
	instance.mu.Lock()
	if instance.retryTimer != nil {
		instance.retryTimer.Stop()
		instance.retryTimer = nil
	}
	instance.retryAt = nil
	instance.mu.Unlock()
	if err := s.stop(instance, StopTimeout); err != nil {
		return instance.Snapshot(), err
	}
	return instance.Snapshot(), nil
}

// Restart 重启指定 Bot
func (s *Supervisor) Restart(botID string) (BotStatus, error) {
	if _, err := s.Stop(botID); err != nil {
		return BotStatus{}, err
	}
	return s.Start(botID)
}

// EnterMaintenance 维护期间阻止自动重启与手动启动
func (s *Supervisor) EnterMaintenance() error {
	s.mu.Lock()
	s.maintenance = true
	s.mu.Unlock()
	return nil
}

// LeaveMaintenance 结束维护
func (s *Supervisor) LeaveMaintenance() {
	s.mu.Lock()
	s.maintenance = false
	s.mu.Unlock()
}

// StartDesired 按期望状态拉起全部 Bot，返回启动失败列表
func (s *Supervisor) StartDesired() []string {
	bots, err := s.workspace.Registry.ListBots()
	if err != nil {
		return []string{err.Error()}
	}
	failures := make([]string, 0)
	for _, bot := range bots {
		if bot.DesiredState != "running" || bot.Initialization != "initialized" {
			continue
		}
		instance := s.runtime(bot)
		if err := s.spawn(context.Background(), instance); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", bot.Name, err))
		}
	}
	return failures
}

// Shutdown 停止全部 Bot；任一未能正常停止时返回错误
func (s *Supervisor) Shutdown() error {
	s.mu.Lock()
	s.closing = true
	instances := make([]*SupervisedBot, 0, len(s.bots))
	for _, instance := range s.bots {
		instances = append(instances, instance)
	}
	s.mu.Unlock()
	var failures []string
	for _, instance := range instances {
		instance.mu.Lock()
		if instance.retryTimer != nil {
			instance.retryTimer.Stop()
			instance.retryTimer = nil
		}
		instance.mu.Unlock()
		if err := s.stop(instance, StopTimeout); err != nil {
			instance.mu.Lock()
			name := instance.bot.Name
			instance.mu.Unlock()
			failures = append(failures, name+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return &BotShutdownError{Message: strings.Join(failures, "；")}
	}
	return nil
}

func (s *Supervisor) environment(instance *SupervisedBot, endpoint string) []string {
	instance.mu.Lock()
	bot := instance.bot
	instance.mu.Unlock()
	env := BotEnvironment(bot.Directory)
	context := RuntimeContext{
		BotID:         bot.BotID,
		HTTPEndpoint:  endpoint,
		PublicBaseURL: s.publicBaseURL,
		StatusPath:    instance.StatusPath(),
	}
	env = append(env, RuntimeContextEnv+"="+context.ToEnv())
	if _, confirmed := s.upgradeConfirmed[bot.BotID]; confirmed {
		env = append(env, "MAIBOT_LEGACY_0X_UPGRADE_CONFIRMED=1")
	}
	return env
}

func (s *Supervisor) spawn(ctx context.Context, instance *SupervisedBot) error {
	instance.mu.Lock()
	bot := instance.bot
	instance.mu.Unlock()
	instance.mu.Lock()
	instance.status, instance.detail, instance.ready = "starting", "", false
	instance.errMsg, instance.exitCode, instance.exitedAt, instance.retryAt = "", nil, "", nil
	instance.mu.Unlock()
	blocker := func(message string) error {
		instance.mu.Lock()
		instance.status = "failed"
		instance.errMsg = message
		instance.exitedAt = time.Now().UTC().Format(time.RFC3339)
		instance.mu.Unlock()
		return errors.New(message)
	}
	if bot.Initialization != "initialized" {
		return blocker(fmt.Sprintf("Bot 尚未完成配置初始化: %s", bot.Name))
	}
	if err := ValidateWorkspaceBot(bot.Directory); err != nil {
		return blocker(err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(instance.LogPath()), 0o777); err != nil {
		return blocker(err.Error())
	}
	_ = os.Remove(instance.StatusPath())
	endpoint, listener, err := allocateEndpoint("bot")
	if err != nil {
		return blocker(err.Error())
	}
	arguments := WorkerCommand(bot)
	command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
	command.Dir = bot.Directory
	command.Env = s.environment(instance, endpoint)
	logFile, err := os.OpenFile(instance.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		releaseEndpoint(listener)
		return blocker(err.Error())
	}
	command.Stdout = logFile
	command.Stderr = logFile
	setChildProcessAttrs(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		releaseEndpoint(listener)
		return blocker(err.Error())
	}
	_ = logFile.Close()
	waitDone := make(chan struct{})
	instance.mu.Lock()
	instance.process = command
	instance.waitDone = waitDone
	instance.endpoint = endpoint
	instance.startedAt = time.Now().UTC().Format(time.RFC3339)
	instance.status = "starting"
	instance.mu.Unlock()
	go s.watch(instance, command, listener, waitDone)
	go s.watchReadiness(instance)
	return nil
}

// watch 等待进程退出，按重启策略决定是否重拉
func (s *Supervisor) watch(instance *SupervisedBot, command *exec.Cmd, listener *endpointListener, waitDone chan struct{}) {
	err := command.Wait()
	releaseEndpoint(listener)
	close(waitDone)
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	} else if err != nil {
		exitCode = -1
	}
	now := time.Now()
	instance.mu.Lock()
	desired := instance.bot.DesiredState
	policy := instance.bot.RestartPolicy
	instance.exitCode = &exitCode
	instance.exitedAt = now.UTC().Format(time.RFC3339)
	instance.endpoint = ""
	instance.ready = false
	instance.process = nil
	shouldRestart := desired == "running" && policy == "on-failure"
	if shouldRestart {
		instance.crashes = append(instance.crashes, now)
		cutoff := now.Add(-crashWindow)
		kept := instance.crashes[:0]
		for _, crash := range instance.crashes {
			if crash.After(cutoff) {
				kept = append(kept, crash)
			}
		}
		instance.crashes = kept
		if len(instance.crashes) >= crashLimit {
			instance.status = "crash_loop"
			instance.errMsg = fmt.Sprintf("%d 秒内崩溃 %d 次，已停止自动重启", int(crashWindow.Seconds()), len(instance.crashes))
			shouldRestart = false
		}
	}
	if exitCode == RestartExitCode && desired == "running" {
		shouldRestart = true
		instance.crashes = nil
	}
	if desired != "running" {
		instance.status = "stopped"
	} else if !shouldRestart && instance.status != "crash_loop" {
		instance.status = "exited"
	}
	var delay time.Duration
	if shouldRestart {
		delay = restartDelayMin
		if count := len(instance.crashes); count > 1 {
			delay = restartDelayMin << (count - 1)
			if delay > restartDelayMax {
				delay = restartDelayMax
			}
		}
		retryAt := float64(now.Add(delay).UnixNano()) / 1e9
		instance.retryAt = &retryAt
		instance.status = "exited"
	}
	instance.mu.Unlock()
	if shouldRestart {
		s.scheduleRestart(instance, delay)
	}
}

func (s *Supervisor) scheduleRestart(instance *SupervisedBot, delay time.Duration) {
	timer := time.AfterFunc(delay, func() {
		if err := s.startBlocker(); err != nil {
			return
		}
		instance.mu.Lock()
		instance.retryTimer = nil
		desired := instance.bot.DesiredState
		instance.mu.Unlock()
		if desired != "running" {
			return
		}
		_ = s.spawn(context.Background(), instance)
	})
	instance.mu.Lock()
	instance.retryTimer = timer
	instance.mu.Unlock()
}

// watchReadiness 轮询 Worker 上报的阶段，就绪后把状态推进到 running
func (s *Supervisor) watchReadiness(instance *SupervisedBot) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		instance.mu.Lock()
		if instance.process == nil {
			instance.mu.Unlock()
			return
		}
		path := instance.StatusPath()
		instance.mu.Unlock()
		status, ok := ReadWorkerStatus(path)
		if !ok {
			continue
		}
		instance.mu.Lock()
		instance.detail = status.Detail
		switch status.Phase {
		case PhaseStarting:
			instance.status = "starting"
		case PhaseMigrating:
			instance.status = "migrating"
		case PhaseReady:
			instance.status = "running"
			instance.ready = true
		case PhaseStopping:
			instance.status = "stopping"
		}
		running := instance.status == "running"
		instance.mu.Unlock()
		if running {
			return
		}
	}
}

// stop 先请求 Worker 优雅退出，超时后强制结束进程树
func (s *Supervisor) stop(instance *SupervisedBot, timeout time.Duration) error {
	instance.mu.Lock()
	command := instance.process
	waitDone := instance.waitDone
	instance.mu.Unlock()
	if command == nil || waitDone == nil {
		instance.mu.Lock()
		if instance.status == "running" || instance.status == "starting" {
			instance.status = "stopped"
		}
		instance.mu.Unlock()
		return nil
	}
	instance.mu.Lock()
	instance.status = "stopping"
	instance.mu.Unlock()
	_ = signalGracefulStop(command)
	select {
	case <-waitDone:
		return nil
	case <-time.After(timeout):
		killProcessTree(command)
		select {
		case <-waitDone:
			return nil
		case <-time.After(6 * time.Second):
			return errors.New("Bot 进程树强制回收超时")
		}
	}
}
