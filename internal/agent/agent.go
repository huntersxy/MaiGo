package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
)

// TurnError 对齐 Python AgentTurnError：携带失败通知所需的 reply_targets
type TurnError struct {
	Message      string
	ReplyTargets map[string][]string
	Err          error
}

func (e *TurnError) Error() string { return e.Message }
func (e *TurnError) Unwrap() error { return e.Err }

// AgentRuntime 提供 prompt 与麦麦上下文，由 Service 实现
type AgentRuntime interface {
	RenderPrompt(name string, vars map[string]string) (string, error)
	Policy() string
	ContextText(workspace string) (string, error)
}

// Agent 对齐 acp_agent.AcpAgent：inbox、pending、backend state 与单轮执行
type Agent struct {
	mu sync.Mutex

	cfg        Config
	history    *AgentHistory
	statePath  string
	state      agentStateFile
	backendKey string

	bridge  *Bridge
	runtime AgentRuntime
	tools   []ToolProvider

	sessionID    string
	connectedID  string
	cwd          string
	capabilities map[string]any
	agentState   string
	toolServer   *ToolMCPServer

	onState func(state string)
}

// NewAgent 创建 AcpAgent；bridge 由 Service 共享
func NewAgent(cfg Config, root string, bridge *Bridge, runtime AgentRuntime, tools []ToolProvider) *Agent {
	return &Agent{
		cfg:        cfg,
		history:    NewAgentHistory(root),
		statePath:  filepath.Join(root, "state.json"),
		state:      agentStateFile{Backends: map[string]*BackendState{}},
		backendKey: backendStateKey(cfg),
		bridge:     bridge,
		runtime:    runtime,
		tools:      tools,
		agentState: "idle",
	}
}

// History 返回历史存储
func (a *Agent) History() *AgentHistory { return a.history }

// State 返回当前 agent_state
func (a *Agent) State() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.agentState
}

// Load 读取历史与状态
func (a *Agent) Load() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loadLocked()
}

func (a *Agent) loadLocked() error {
	if err := os.MkdirAll(a.history.Root(), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(a.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else if len(strings.TrimSpace(string(raw))) > 0 {
		if err := sonic.ConfigStd.Unmarshal(raw, &a.state); err != nil {
			return fmt.Errorf("解析 Agent 状态失败: %w", err)
		}
	}
	if a.state.Backends == nil {
		a.state.Backends = map[string]*BackendState{}
	}
	if a.state.Inbox == nil {
		a.state.Inbox = []HistoryRecord{}
	}
	if _, ok := a.state.Backends[a.backendKey]; !ok {
		a.state.Backends[a.backendKey] = &BackendState{}
	}
	backend := a.state.Backends[a.backendKey]
	a.sessionID = backend.SessionID
	a.cwd = backend.CWD
	return nil
}

func (a *Agent) saveLocked() error {
	if err := os.MkdirAll(a.history.Root(), 0o755); err != nil {
		return err
	}
	if a.state.Backends == nil {
		a.state.Backends = map[string]*BackendState{}
	}
	backend := a.state.Backends[a.backendKey]
	if backend == nil {
		backend = &BackendState{}
		a.state.Backends[a.backendKey] = backend
	}
	backend.SessionID = a.sessionID
	backend.CWD = a.cwd
	raw, err := sonic.ConfigStd.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath)
}

// Enqueue 对齐 enqueue：保存快照记录到 inbox
func (a *Agent) Enqueue(text string, images []string, kind string, replyTo string) (HistoryRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.loadLocked(); err != nil {
		return HistoryRecord{}, err
	}
	items := make([]ContextItem, 0, 1+len(images))
	if strings.TrimSpace(text) != "" {
		items = append(items, ContextItem{Type: "text", Text: text})
	}
	for _, image := range images {
		items = append(items, imageContextItem(image))
	}
	if len(items) == 0 {
		return HistoryRecord{}, fmt.Errorf("A_Memorix Agent 任务内容为空")
	}
	record, err := NewRecord(kind, replyTo, items, a.history)
	if err != nil {
		return HistoryRecord{}, err
	}
	a.state.Inbox = append(a.state.Inbox, record)
	if err := a.saveLocked(); err != nil {
		return HistoryRecord{}, err
	}
	return record, nil
}

// InboxSize 返回待执行任务数
func (a *Agent) InboxSize() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.state.Inbox)
}

// InboxSnapshot 返回 inbox 副本，供 WebUI /events 的 queued 字段
func (a *Agent) InboxSnapshot() []HistoryRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]HistoryRecord, len(a.state.Inbox))
	copy(out, a.state.Inbox)
	return out
}

// HasPending 对齐 Python agent.pending is not None
func (a *Agent) HasPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	backend := a.state.Backends[a.backendKey]
	return backend != nil && backend.Pending != nil
}

// Connect 对齐 connect；返回真实错误，不伪造连接成功
func (a *Agent) Connect(ctx context.Context) error {
	a.mu.Lock()
	if a.bridge.Connected() && a.sessionID != "" {
		a.mu.Unlock()
		return nil
	}
	if err := a.loadLocked(); err != nil {
		a.mu.Unlock()
		return err
	}
	cwd := filepath.Join(a.history.Root(), "workspace")
	if strings.TrimSpace(a.cfg.ACPCwd) != "" {
		resolved, err := expandUserPath(a.cfg.ACPCwd)
		if err != nil {
			a.mu.Unlock()
			return err
		}
		cwd = resolved
	}
	remoteCWD, err := SessionCWD(a.cfg, cwd)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		a.mu.Unlock()
		return err
	}
	backend := a.state.Backends[a.backendKey]
	if backend == nil {
		backend = &BackendState{}
		a.state.Backends[a.backendKey] = backend
	}
	if backend.CWD != "" && backend.CWD != remoteCWD {
		a.mu.Unlock()
		return fmt.Errorf("ACP 会话的主机工作目录已改变，请恢复原工作目录再连接。")
	}
	storedSession := a.sessionID
	a.mu.Unlock()

	toolServer := NewToolMCPServer(a.tools)
	if err := toolServer.Start(); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = toolServer.Close(context.Background())
		_ = a.bridge.Stop(context.Background(), 2)
		return err
	}
	mount := toolServer.Mount()
	port := toolServer.Port()
	capabilities, err := a.bridge.Initialize(ctx, a.cfg, cwd, "maibot-agent", &port)
	if err != nil {
		return fail(err)
	}
	sessionCaps, _ := capabilities["sessionCapabilities"].(map[string]any)
	if storedSession != "" {
		if _, ok := sessionCaps["resume"]; !ok {
			return fail(fmt.Errorf("外部 Agent 未声明 session/resume 能力，不能恢复已记录的会话"))
		}
	}
	session, err := a.bridge.OpenSession(ctx, remoteCWD, mount, storedSession, a.cfg.ACPTimeoutSec)
	if err != nil {
		return fail(err)
	}
	sessionID, _ := session["sessionId"].(string)
	if storedSession != "" {
		sessionID = storedSession
	}
	if strings.TrimSpace(sessionID) == "" {
		return fail(fmt.Errorf("ACP session/new 未返回 sessionId"))
	}
	a.mu.Lock()
	a.sessionID = sessionID
	a.connectedID = sessionID
	a.cwd = remoteCWD
	a.capabilities = capabilities
	backend.CWD = remoteCWD
	a.toolServer = toolServer
	_ = a.saveLocked()
	a.mu.Unlock()

	a.bridge.SetUpdateHandler(a.onUpdate)
	if a.cfg.ACPModel != "" {
		if err := a.selectModel(ctx, session, sessionID); err != nil {
			return fail(err)
		}
	}
	return nil
}

func (a *Agent) selectModel(ctx context.Context, session map[string]any, sessionID string) error {
	options, _ := session["configOptions"].([]any)
	modelID := ""
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if option["id"] == "model" {
			modelID, _ = option["id"].(string)
			break
		}
	}
	if modelID == "" {
		return fmt.Errorf("外部 Agent 未提供 model 配置选项")
	}
	if _, err := a.bridge.Request(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  modelID,
		"value":     a.cfg.ACPModel,
	}, a.cfg.ACPTimeoutSec); err != nil {
		return err
	}
	return nil
}

func (a *Agent) onUpdate(ctx context.Context, params map[string]any) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionID == "" || stringValue(params["sessionId"]) != a.sessionID {
		return
	}
	update, _ := params["update"].(map[string]any)
	kind := stringValue(update["sessionUpdate"])
	switch kind {
	case "agent_message_chunk", "agent_thought_chunk":
		content, _ := update["content"].(map[string]any)
		name := "assistant"
		if kind == "agent_thought_chunk" {
			name = "reasoning"
		}
		// 对齐 Python ContextItemMeta.create(logical_turn_id=str(update.get("messageId", sessionId)))
		turnID := stringValue(update["messageId"])
		if turnID == "" {
			turnID = stringValue(params["sessionId"])
		}
		switch stringValue(content["type"]) {
		case "text":
			text := stringValue(content["text"])
			if text == "" {
				return
			}
			record, err := NewRecord("step", "", []ContextItem{{Type: "text", Name: name, Text: text, LogicalTurnID: turnID}}, a.history)
			if err == nil {
				_ = a.history.Append(record)
			}
		case "image":
			item := ContextItem{Type: "image", Name: name, Data: stringValue(content["data"]), MIMEType: stringValue(content["mimeType"]), LogicalTurnID: turnID}
			record, err := NewRecord("step", "", []ContextItem{item}, a.history)
			if err == nil {
				_ = a.history.Append(record)
			}
		}
	case "tool_call", "tool_call_update":
		status := stringValue(update["status"])
		if status == "" {
			status = "pending"
		}
		if status == "pending" || status == "in_progress" {
			a.agentState = "tool"
		} else if a.state.Backends[a.backendKey].Pending != nil {
			a.agentState = "thinking"
		} else {
			a.agentState = "idle"
		}
		title := stringValue(update["title"])
		if title == "" {
			title = stringValue(update["toolCallId"])
		}
		record, err := NewRecord("step", "", []ContextItem{{Type: "tool", Name: status, Text: title}}, a.history)
		if err == nil {
			_ = a.history.Append(record)
		}
	}
}

// RunTurn 对齐 run_turn；返回停止原因；失败时返回 *TurnError
func (a *Agent) RunTurn(ctx context.Context) (*TurnResult, error) {
	if err := a.Connect(ctx); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if err := a.loadLocked(); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	backend := a.state.Backends[a.backendKey]
	if backend == nil {
		backend = &BackendState{}
		a.state.Backends[a.backendKey] = backend
	}
	if backend.Pending != nil {
		uncertain := map[string]bool{}
		for _, id := range backend.Pending.InputIDs {
			uncertain[id] = true
		}
		kept := a.state.Inbox[:0]
		for _, record := range a.state.Inbox {
			if !uncertain[record.ID] {
				kept = append(kept, record)
			}
		}
		a.state.Inbox = kept
		warning, err := NewRecord("step", "", []ContextItem{{Type: "text", Name: "system", Text: "上次 ACP 回合在进程中断前未确认结束，执行结果未知，未重发原任务。请检查外部会话及原桌面操作 ID。"}}, a.history)
		if err != nil {
			a.mu.Unlock()
			return nil, err
		}
		_ = a.history.Append(warning)
		backend.Pending = nil
		_ = a.saveLocked()
	}
	batch := append([]HistoryRecord(nil), a.state.Inbox...)
	if len(batch) == 0 {
		a.mu.Unlock()
		return &TurnResult{StopReason: "empty"}, nil
	}
	allItems := make([]ContextItem, 0)
	ids := make([]string, 0, len(batch))
	for _, record := range batch {
		ids = append(ids, record.ID)
		allItems = append(allItems, record.Items...)
	}
	items, err := a.history.Unpack(allItems)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	policy := ""
	if a.runtime != nil {
		policy = a.runtime.Policy()
	}
	policyKey := sha256Hex(policy)
	contextText := ""
	if a.runtime != nil {
		contextText, err = a.runtime.ContextText(a.cwd)
		if err != nil {
			a.mu.Unlock()
			return nil, err
		}
	}
	var prompt []any
	if backend.Policy != policyKey && policy != "" {
		prompt = append(prompt, map[string]any{"type": "text", "text": policy})
	}
	if contextText != "" {
		prompt = append(prompt, map[string]any{"type": "text", "text": contextText})
	}
	imageCapable := promptImageSupported(a.capabilities)
	for _, item := range items {
		switch item.Type {
		case "image":
			if !imageCapable {
				a.mu.Unlock()
				return nil, fmt.Errorf("ACP 当前模型路由未声明图片输入能力，任务保留在队列中")
			}
			prompt = append(prompt, map[string]any{
				"type":     "image",
				"data":     item.Data,
				"mimeType": item.MIMEType,
			})
		default:
			if item.Text != "" {
				prompt = append(prompt, map[string]any{"type": "text", "text": item.Text})
			}
		}
	}
	backend.Pending = &PendingTurn{InputIDs: ids}
	if err := a.saveLocked(); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	sessionID := a.sessionID
	a.mu.Unlock()

	if err := a.history.Append(batch...); err != nil {
		return nil, err
	}
	a.mu.Lock()
	remaining := a.state.Inbox[:0]
	for _, record := range a.state.Inbox {
		drop := false
		for _, id := range ids {
			if record.ID == id {
				drop = true
				break
			}
		}
		if !drop {
			remaining = append(remaining, record)
		}
	}
	a.state.Inbox = remaining
	a.agentState = "thinking"
	_ = a.saveLocked()
	a.mu.Unlock()

	raw, err := a.bridge.Request(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    prompt,
	}, 0)
	if err != nil {
		cancelled := ctx.Err() != nil
		a.mu.Lock()
		if cancelled {
			backend.StopReason = "cancelled"
		} else {
			backend.StopReason = "error"
		}
		backend.Pending = nil
		a.agentState = "idle"
		_ = a.saveLocked()
		a.mu.Unlock()
		if cancelled {
			_ = a.bridge.Notify(context.Background(), "session/cancel", map[string]any{"sessionId": sessionID})
			return nil, ctx.Err()
		}
		return nil, &TurnError{Message: err.Error(), ReplyTargets: replyTargets(batch), Err: err}
	}
	result, _ := raw.(map[string]any)
	stopReason := stringValue(result["stopReason"])
	a.mu.Lock()
	backend.Policy = policyKey
	backend.StopReason = stopReason
	backend.Pending = nil
	a.agentState = "idle"
	_ = a.saveLocked()
	a.mu.Unlock()
	if stopReason != "end_turn" {
		return &TurnResult{StopReason: stopReason}, &TurnError{
			Message:      fmt.Sprintf("外接 Agent 未正常完成：%s，已有输出已保留，原任务不会自动重发", stopReason),
			ReplyTargets: replyTargets(batch),
		}
	}
	return &TurnResult{StopReason: stopReason}, nil
}

// TurnResult 对齐 session/prompt 的 stopReason
type TurnResult struct {
	StopReason string
}

// Close 保存状态，关闭 MCP 与 ACP 连接；session/close 仅在能力声明时调用
func (a *Agent) Close(ctx context.Context) error {
	a.mu.Lock()
	if err := a.loadLocked(); err != nil {
		a.mu.Unlock()
		return err
	}
	_ = a.saveLocked()
	toolServer := a.toolServer
	sessionID := a.connectedID
	capabilities := a.capabilities
	a.toolServer = nil
	a.connectedID = ""
	a.agentState = "stopped"
	a.mu.Unlock()

	var firstErr error
	if toolServer != nil {
		if err := toolServer.Close(ctx); err != nil {
			firstErr = err
		}
	}
	if sessionID != "" {
		sessionCaps, _ := capabilities["sessionCapabilities"].(map[string]any)
		if _, ok := sessionCaps["close"]; ok && a.bridge.Connected() {
			timeout := a.cfg.ACPTimeoutSec
			if timeout > 2 {
				timeout = 2
			}
			_, _ = a.bridge.Request(ctx, "session/close", map[string]any{"sessionId": sessionID}, timeout)
		}
	}
	if err := a.bridge.Stop(ctx, a.cfg.ACPTimeoutSec); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func promptImageSupported(capabilities map[string]any) bool {
	promptCaps, _ := capabilities["promptCapabilities"].(map[string]any)
	value, _ := promptCaps["image"].(bool)
	return value
}

func replyTargets(records []HistoryRecord) map[string][]string {
	out := map[string][]string{}
	for _, record := range records {
		if record.ReplyTo == "" {
			continue
		}
		out[record.ReplyTo] = append(out[record.ReplyTo], record.ID)
	}
	return out
}

func backendStateKey(cfg Config) string {
	if cfg.Connection == ConnectionDSHDesktop {
		return "acp_desktop:" + strings.ToLower(filepath.Clean(DesktopManifestPath(cfg)))
	}
	return "acp"
}

func imageContextItem(ref string) ContextItem {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "data:") {
		if comma := strings.IndexByte(ref, ','); comma >= 0 {
			meta := ref[5:comma]
			mime := strings.TrimSuffix(meta, ";base64")
			return ContextItem{Type: "image", Data: ref[comma+1:], MIMEType: mime}
		}
	}
	return ContextItem{Type: "image", Path: ref, MIMEType: mimeForPath(ref)}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func stringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
