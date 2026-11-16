package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
)

const (
	defaultAgentID = "__maibot_agent__"
	agentName      = "麦麦的 Agent"
)

// Status 对齐 AgentService.status
type Status struct {
	Enabled           bool   `json:"enabled"`
	Name              string `json:"name"`
	AgentID           string `json:"agent_id"`
	Backend           string `json:"backend"`
	ExternalSessionID string `json:"external_session_id,omitempty"`
	Paused            bool   `json:"paused"`
	Error             string `json:"error"`
	AgentState        string `json:"agent_state"`
	ACPConnected      bool   `json:"acp_connected"`
}

// ServiceOptions AcpAgent 依赖；缺省能力必须显式报错而非伪造
type ServiceOptions struct {
	DataDir       string
	RenderPrompt  func(name string, vars map[string]string) (string, error)
	Profile       string
	AgentToolHost AgentToolHost
	FileHost      FileToolHost
	// ReportFailure 对齐 messaging.report_failure；未接线时只持久化错误
	ReportFailure func(target string, taskIDs []string, errMsg string)
}

// Service 对齐 AgentService：任务队列、pause/resume、turn 循环
type Service struct {
	cfg    Config
	name   string
	bridge *Bridge
	opts   ServiceOptions
	root   string
	mu     sync.RWMutex

	agent       *Agent
	started     bool
	paused      bool
	manualPause bool
	errMsg      string
	lastState   string
	currentTurn context.CancelFunc
	wake        chan struct{}
	loopCtx     context.Context
	loopCancel  context.CancelFunc
	loopWG      sync.WaitGroup
}

// NewService 创建 Agent 服务；options 可省略以兼容旧调用
func NewService(cfg Config, options ...ServiceOptions) *Service {
	opts := ServiceOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if strings.TrimSpace(opts.DataDir) == "" {
		opts.DataDir = filepath.Join("data", "agent")
	}
	return &Service{
		cfg:       cfg,
		name:      agentName,
		bridge:    NewBridge(),
		opts:      opts,
		root:      opts.DataDir,
		wake:      make(chan struct{}, 1),
		lastState: "stopped",
	}
}

// Bridge 返回底层 ACP 连接
func (s *Service) Bridge() *Bridge { return s.bridge }

// DataDir 返回 agent 数据根
func (s *Service) DataDir() string { return s.root }

// Agent 返回内部 AcpAgent
func (s *Service) Agent() *Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agent
}

// SetProfile 更新人格资料，供配置热重载后的说明文本使用
func (s *Service) SetProfile(profile string) {
	s.mu.Lock()
	s.opts.Profile = profile
	s.mu.Unlock()
}

// Start 对齐 start/reload；启用后启动后台任务队列，不提前 spawn 外部进程
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if !s.cfg.Enabled {
		s.lastState = "stopped"
		s.mu.Unlock()
		return nil
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if err := s.loadRuntimeState(); err != nil {
		return err
	}
	agent := NewAgent(s.cfg, s.root, s.bridge, s, s.providers())
	if err := agent.Load(); err != nil {
		return err
	}
	loopCtx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.agent = agent
	s.started = true
	s.loopCtx = loopCtx
	s.loopCancel = cancel
	s.lastState = "idle"
	s.mu.Unlock()

	s.loopWG.Add(1)
	go s.runLoop(loopCtx)
	s.Wake()
	_ = ctx
	return nil
}

func (s *Service) providers() []ToolProvider {
	return []ToolProvider{
		NewAgentToolProvider(s.opts.AgentToolHost, s.statusMap),
		NewFileToolProvider(s.opts.FileHost),
	}
}

func (s *Service) runLoop(ctx context.Context) {
	defer s.loopWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			s.mu.RLock()
			paused := s.paused
			agent := s.agent
			s.mu.RUnlock()
			if paused || agent == nil || agent.InboxSize() == 0 {
				break
			}
			turnCtx, cancel := context.WithCancel(ctx)
			s.mu.Lock()
			s.currentTurn = cancel
			s.errMsg = ""
			s.mu.Unlock()
			_, err := agent.RunTurn(turnCtx)
			cancel()
			s.mu.Lock()
			s.currentTurn = nil
			s.lastState = agent.State()
			var turnErr *TurnError
			if err != nil {
				if errors.Is(err, context.Canceled) {
					s.mu.Unlock()
					continue
				}
				s.errMsg = err.Error()
				if !errors.As(err, &turnErr) {
					s.paused = true
					turnErr = nil
				}
			} else {
				s.errMsg = ""
			}
			report := s.opts.ReportFailure
			errMsg := s.errMsg
			s.mu.Unlock()
			_ = s.saveRuntimeState()
			if turnErr != nil && report != nil {
				for target, taskIDs := range turnErr.ReplyTargets {
					func() {
						defer func() { _ = recover() }()
						report(target, taskIDs, errMsg)
					}()
				}
			}
		}
	}
}

// Enqueue 对齐 enqueue：写入 inbox 并唤醒任务循环
func (s *Service) Enqueue(text string, images []string, kind string, replyTo string) (HistoryRecord, error) {
	s.mu.RLock()
	enabled := s.cfg.Enabled
	agent := s.agent
	s.mu.RUnlock()
	if !enabled {
		return HistoryRecord{}, ErrDisabled
	}
	if agent == nil {
		return HistoryRecord{}, fmt.Errorf("Agent 未启动，请在设置的“Agent连接”中启用连接。")
	}
	if strings.TrimSpace(kind) == "" {
		kind = "user"
	}
	record, err := agent.Enqueue(text, images, kind, replyTo)
	if err != nil {
		return HistoryRecord{}, err
	}
	if kind == "user" {
		s.resumeAfterError()
	}
	s.Wake()
	return record, nil
}

// Wake 唤醒后台任务循环
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ResumeAfterError 对齐 resume_after_error：仅在 paused+error 且非人工暂停时恢复
func (s *Service) ResumeAfterError() { s.resumeAfterError() }

func (s *Service) resumeAfterError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.paused || s.errMsg == "" || s.manualPause {
		return
	}
	s.paused = false
	s.errMsg = ""
}

// Stop 对齐 stop：保存状态、关闭连接、取消任务
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return s.bridge.Stop(ctx, s.cfg.ACPTimeoutSec)
	}
	cancel := s.loopCancel
	turnCancel := s.currentTurn
	agent := s.agent
	s.started = false
	s.agent = nil
	s.loopCancel = nil
	s.currentTurn = nil
	s.lastState = "stopped"
	s.mu.Unlock()
	if turnCancel != nil {
		turnCancel()
	}
	if cancel != nil {
		cancel()
	}
	s.loopWG.Wait()
	var firstErr error
	if agent != nil {
		if err := agent.Close(ctx); err != nil {
			firstErr = err
		}
	}
	if err := s.saveRuntimeState(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Reload 对齐 reload：配置比较后重建
func (s *Service) Reload(ctx context.Context, cfg Config) error {
	s.mu.RLock()
	previous := s.cfg
	started := s.started
	s.mu.RUnlock()
	if configEqual(previous, cfg) && started == cfg.Enabled {
		return nil
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	if !cfg.Enabled {
		s.paused = false
		s.manualPause = false
		s.errMsg = ""
		s.lastState = "stopped"
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.Start(ctx)
}

// Status 返回 Agent 状态
func (s *Service) Status() Status {
	s.mu.RLock()
	agent := s.agent
	s.mu.RUnlock()
	agentState := s.lastState
	sessionID := ""
	connected := s.bridge.Connected()
	if agent != nil {
		agentState = agent.State()
		agent.mu.Lock()
		sessionID = agent.sessionID
		agent.mu.Unlock()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{
		Enabled:           s.cfg.Enabled,
		Name:              s.name,
		AgentID:           defaultAgentID,
		Backend:           "acp",
		ExternalSessionID: sessionID,
		Paused:            s.paused,
		Error:             s.errMsg,
		AgentState:        agentState,
		ACPConnected:      connected,
	}
}

// Control 对齐 control(pause/resume)；连接可用时可 ping/status 探活
func (s *Service) Control(ctx context.Context, operation string) (Status, error) {
	s.mu.Lock()
	if !s.cfg.Enabled {
		st := s.statusLocked()
		s.mu.Unlock()
		return st, ErrDisabled
	}
	switch operation {
	case "pause":
		s.paused = true
		s.manualPause = true
		s.lastState = "paused"
		cancel := s.currentTurn
		st := s.statusLocked()
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return st, s.saveRuntimeState()
	case "resume":
		s.paused = false
		s.manualPause = false
		s.errMsg = ""
		s.lastState = "idle"
		st := s.statusLocked()
		s.mu.Unlock()
		s.Wake()
		return st, s.saveRuntimeState()
	case "ping", "status":
		bridge := s.bridge
		timeout := s.cfg.ACPTimeoutSec
		if timeout <= 0 {
			timeout = 60
		}
		s.mu.Unlock()
		if !bridge.Connected() {
			return s.Status(), ErrNotConnected
		}
		method := "ping"
		if operation == "status" {
			method = "agent/status"
		}
		pingTimeout := timeout
		if operation == "ping" && pingTimeout > 10 {
			pingTimeout = 10
		}
		if _, err := bridge.Request(ctx, method, map[string]any{}, pingTimeout); err != nil {
			s.mu.Lock()
			s.errMsg = err.Error()
			st := s.statusLocked()
			s.mu.Unlock()
			return st, err
		}
		s.mu.Lock()
		s.errMsg = ""
		if s.lastState == "stopped" {
			s.lastState = "idle"
		}
		st := s.statusLocked()
		s.mu.Unlock()
		return st, nil
	default:
		st := s.statusLocked()
		s.mu.Unlock()
		return st, fmt.Errorf("Agent control 只接受 pause/resume，电脑控制请使用独立电脑接口。")
	}
}

func (s *Service) statusLocked() Status {
	agentState := s.lastState
	sessionID := ""
	if s.agent != nil {
		agentState = s.agent.State()
		s.agent.mu.Lock()
		sessionID = s.agent.sessionID
		s.agent.mu.Unlock()
	}
	return Status{
		Enabled:           s.cfg.Enabled,
		Name:              s.name,
		AgentID:           defaultAgentID,
		Backend:           "acp",
		ExternalSessionID: sessionID,
		Paused:            s.paused,
		Error:             s.errMsg,
		AgentState:        agentState,
		ACPConnected:      s.bridge.Connected(),
	}
}

func (s *Service) statusMap() map[string]any {
	st := s.Status()
	raw, err := sonic.ConfigStd.Marshal(st)
	if err != nil {
		return map[string]any{"enabled": st.Enabled, "agent_state": st.AgentState}
	}
	out := map[string]any{}
	_ = sonic.ConfigStd.Unmarshal(raw, &out)
	return out
}

// RenderPrompt 实现 AgentRuntime
func (s *Service) RenderPrompt(name string, vars map[string]string) (string, error) {
	if s.opts.RenderPrompt == nil {
		return "", fmt.Errorf("acpagent: Prompt 渲染未接线")
	}
	return s.opts.RenderPrompt(name, vars)
}

// Policy 对齐 AgentService.policy
func (s *Service) Policy() string {
	profile := s.opts.Profile
	if strings.TrimSpace(profile) == "" {
		profile = "麦麦"
	}
	policy, err := s.RenderPrompt("agent_acp", map[string]string{"maibot_profile": profile})
	if err != nil {
		return ""
	}
	return policy
}

// Workspace 对齐 AgentService.workspace：优先 acp_cwd
func (s *Service) Workspace() string {
	s.mu.RLock()
	cfg := s.cfg
	root := s.root
	s.mu.RUnlock()
	if strings.TrimSpace(cfg.ACPCwd) != "" {
		if resolved, err := expandUserPath(cfg.ACPCwd); err == nil && resolved != "" {
			return resolved
		}
	}
	return filepath.Join(root, "workspace")
}

// ContextText 对齐 AgentService.context
func (s *Service) ContextText(workspace string) (string, error) {
	maibotWorkspace := s.Workspace()
	if strings.TrimSpace(workspace) == "" {
		workspace = maibotWorkspace
	}
	text, err := s.RenderPrompt("agent_acp_environment", map[string]string{
		"workspace":        workspace,
		"maibot_workspace": "{maibot_workspace}",
	})
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	hasRemote := workspace != maibotWorkspace
	for _, line := range lines {
		if !strings.Contains(line, "{maibot_workspace}") {
			kept = append(kept, line)
			continue
		}
		if hasRemote {
			kept = append(kept, strings.ReplaceAll(line, "{maibot_workspace}", maibotWorkspace))
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n")) + "\n\n", nil
}

type runtimeState struct {
	Paused      bool   `json:"paused"`
	Error       string `json:"error"`
	ManualPause bool   `json:"manual_pause"`
}

func (s *Service) runtimePath() string { return filepath.Join(s.root, "runtime.json") }

func (s *Service) loadRuntimeState() error {
	raw, err := os.ReadFile(s.runtimePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var state runtimeState
	if err := sonic.ConfigStd.Unmarshal(raw, &state); err != nil {
		return err
	}
	s.mu.Lock()
	s.paused = state.Paused
	s.errMsg = state.Error
	s.manualPause = state.ManualPause
	s.mu.Unlock()
	return nil
}

func (s *Service) saveRuntimeState() error {
	s.mu.RLock()
	state := runtimeState{Paused: s.paused, Error: s.errMsg, ManualPause: s.manualPause}
	s.mu.RUnlock()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	raw, err := sonic.ConfigStd.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.runtimePath(), raw, 0o644)
}

func configEqual(a Config, b Config) bool {
	ra, _ := sonic.ConfigStd.Marshal(a)
	rb, _ := sonic.ConfigStd.Marshal(b)
	return string(ra) == string(rb)
}
