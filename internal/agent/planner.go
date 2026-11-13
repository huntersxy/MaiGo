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

// PlannerSnapshot 对齐 ACPPlannerSnapshot；调用方传入深拷贝内容
type PlannerSnapshot struct {
	Config         Config
	ContextItems   []map[string]any
	HistoryItemIDs []string
	Tools          []ToolDefinition
}

// PlannerToolCall 外部 Planner 提交的工具决策，尚未执行
type PlannerToolCall struct {
	CallID string         `json:"call_id"`
	Name   string         `json:"name"`
	Args   map[string]any `json:"args"`
}

// PlannerResult 对齐 LLMResponseResult 的可移植子集
type PlannerResult struct {
	Response   string            `json:"response"`
	Reasoning  string            `json:"reasoning"`
	ToolCalls  []PlannerToolCall `json:"tool_calls"`
	ModelName  string            `json:"model_name"`
	StopReason string            `json:"stop_reason"`
}

// PlannerOptions Planner 依赖
type PlannerOptions struct {
	DataDir      string
	RenderPrompt func(name string, vars map[string]string) (string, error)
}

type plannerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// PlannerBridge 实验性外接 Planner 桥
type PlannerBridge struct {
	root   string
	opts   PlannerOptions
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
	active map[string]*plannerRun
}

// NewPlannerBridge 创建 Planner 桥
func NewPlannerBridge(opts PlannerOptions) *PlannerBridge {
	if strings.TrimSpace(opts.DataDir) == "" {
		opts.DataDir = filepath.Join("data", "agent")
	}
	return &PlannerBridge{root: opts.DataDir, opts: opts, locks: map[string]*sync.Mutex{}, active: map[string]*plannerRun{}}
}

// PlannerDecisions 对齐 PlannerDecisions：MCP 回执只确认决策提交
type PlannerDecisions struct {
	mu     sync.Mutex
	specs  []ToolDefinition
	calls  []PlannerToolCall
	active bool
}

// NewPlannerDecisions 根据工具定义构造决策提供者
func NewPlannerDecisions(specs []ToolDefinition) *PlannerDecisions {
	return &PlannerDecisions{specs: specs}
}

// ProviderName 实现 ToolProvider
func (p *PlannerDecisions) ProviderName() string { return "acp_planner" }

// Tools 实现 ToolProvider
func (p *PlannerDecisions) Tools() []ToolDefinition {
	out := make([]ToolDefinition, 0, len(p.specs))
	for _, spec := range p.specs {
		tool := spec
		out = append(out, ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
				_ = ctx
				p.mu.Lock()
				defer p.mu.Unlock()
				if !p.active {
					return ToolResult{}, fmt.Errorf("Planner 回合已结束，不能提交工具决策")
				}
				if err := validateRequired(tool.InputSchema, args); err != nil {
					return ToolResult{IsError: true, Text: err.Error()}, nil
				}
				call := PlannerToolCall{CallID: randomRecordID(), Name: tool.Name, Args: args}
				p.calls = append(p.calls, call)
				return ToolResult{Text: fmt.Sprintf("Request %s accepted, NOT executed. End this turn and wait for the actual result.", call.CallID)}, nil
			},
		})
	}
	return out
}

func (p *PlannerDecisions) setActive(active bool) {
	p.mu.Lock()
	p.active = active
	if active {
		p.calls = nil
	}
	p.mu.Unlock()
}

func (p *PlannerDecisions) snapshotCalls() []PlannerToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PlannerToolCall(nil), p.calls...)
}

// GeneratePlannerResponse 对齐 generate_planner_response
func (p *PlannerBridge) GeneratePlannerResponse(ctx context.Context, snapshot PlannerSnapshot, chatID string) (*PlannerResult, error) {
	if strings.TrimSpace(chatID) == "" {
		return nil, fmt.Errorf("外接 Planner 需要 chat_id")
	}
	cfg := snapshot.Config
	lock := p.chatLock(chatID)
	lock.Lock()
	defer lock.Unlock()
	runCtx, cancel := context.WithCancel(ctx)
	run := &plannerRun{cancel: cancel, done: make(chan struct{})}
	p.mu.Lock()
	if p.active == nil {
		p.active = map[string]*plannerRun{}
	}
	p.active[chatID] = run
	p.mu.Unlock()
	defer func() {
		cancel()
		close(run.done)
		p.mu.Lock()
		if p.active[chatID] == run {
			delete(p.active, chatID)
		}
		p.mu.Unlock()
	}()
	ctx = runCtx

	state, err := p.loadState(chatID)
	if err != nil {
		return nil, err
	}
	cwd := filepath.Join(p.root, "workspace")
	if strings.TrimSpace(cfg.ACPCwd) != "" {
		resolved, err := expandUserPath(cfg.ACPCwd)
		if err != nil {
			return nil, err
		}
		cwd = resolved
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return nil, err
	}
	remoteCWD, err := SessionCWD(cfg, cwd)
	if err != nil {
		return nil, err
	}
	identity := plannerIdentity(cfg, cwd, remoteCWD)
	if len(state.Identity) > 0 && !sameIdentity(state.Identity, identity) {
		return nil, fmt.Errorf("外接 Planner 连接归属已改变，请恢复原连接配置，或清空聊天后建立新会话")
	}
	if state.SessionID != "" && state.Identity == nil {
		state.Identity = identity
	}

	delta, seen, systemKeys := selectDelta(snapshot.ContextItems, snapshot.HistoryItemIDs, state.Seen, state.System, state.Transient)
	policy := p.plannerPolicy()
	policyKey := sha256Hex(policy)

	// 工具决策由外部 Planner 通过 MCP 提交
	decisions := NewPlannerDecisions(snapshot.Tools)
	toolServer := NewToolMCPServer([]ToolProvider{decisions})
	if err := toolServer.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = toolServer.Close(context.Background()) }()
	mount := toolServer.Mount()
	port := toolServer.Port()

	bridge := NewBridge()
	defer func() { _ = bridge.Stop(context.Background(), cfg.ACPTimeoutSec) }()
	capabilities, err := bridge.Initialize(ctx, cfg, cwd, "maibot-planner", &port)
	if err != nil {
		return nil, err
	}
	sessionCaps, _ := capabilities["sessionCapabilities"].(map[string]any)
	if _, ok := sessionCaps["resume"]; !ok {
		return nil, fmt.Errorf("外接 Planner 需要 session/resume 能力以恢复持久会话")
	}
	session, err := bridge.OpenSession(ctx, remoteCWD, mount, state.SessionID, cfg.ACPTimeoutSec)
	if err != nil {
		return nil, err
	}
	externalSession := state.SessionID
	if externalSession == "" {
		externalSession, _ = session["sessionId"].(string)
		if strings.TrimSpace(externalSession) == "" {
			return nil, fmt.Errorf("ACP session/new 未返回 sessionId")
		}
		state.SessionID = externalSession
		state.Identity = identity
		if err := p.saveState(chatID, state); err != nil {
			return nil, err
		}
	}
	if cfg.ACPModel != "" {
		if err := selectSessionModel(ctx, bridge, cfg, session, externalSession); err != nil {
			return nil, err
		}
	}

	var text, thoughts []string
	bridge.SetUpdateHandler(func(_ context.Context, params map[string]any) {
		if stringValue(params["sessionId"]) != externalSession {
			return
		}
		update, _ := params["update"].(map[string]any)
		if stringValue(update["sessionUpdate"]) != "agent_message_chunk" && stringValue(update["sessionUpdate"]) != "agent_thought_chunk" {
			return
		}
		content, _ := update["content"].(map[string]any)
		if stringValue(content["type"]) != "text" {
			return
		}
		if stringValue(update["sessionUpdate"]) == "agent_message_chunk" {
			text = append(text, stringValue(content["text"]))
		} else {
			thoughts = append(thoughts, stringValue(content["text"]))
		}
	})

	prompt, err := buildPlannerPrompt(delta, policy, policyKey, state, capabilities, p.opts.RenderPrompt)
	if err != nil {
		return nil, err
	}
	state.Pending = true
	if err := p.saveState(chatID, state); err != nil {
		return nil, err
	}
	decisions.setActive(true)
	raw, err := bridge.Request(ctx, "session/prompt", map[string]any{
		"sessionId": externalSession,
		"prompt":    prompt,
	}, 0)
	decisions.setActive(false)
	if err != nil {
		if ctx.Err() != nil {
			_ = bridge.Notify(context.Background(), "session/cancel", map[string]any{"sessionId": externalSession})
		}
		return nil, err
	}
	result, _ := raw.(map[string]any)
	stopReason := stringValue(result["stopReason"])
	if stopReason != "end_turn" {
		return nil, fmt.Errorf("外接 Planner 未正常完成：%s", stopReason)
	}
	state.Seen = seen
	state.Policy = policyKey
	state.System = systemKeys
	state.Transient = transientCounts(snapshot.ContextItems, snapshot.HistoryItemIDs)
	state.Pending = false
	if err := p.saveState(chatID, state); err != nil {
		return nil, err
	}
	model := cfg.ACPModel
	if model == "" {
		model = string(cfg.Connection)
	}
	if _, ok := sessionCaps["close"]; ok {
		timeout := cfg.ACPTimeoutSec
		if timeout > 2 {
			timeout = 2
		}
		_, _ = bridge.Request(context.Background(), "session/close", map[string]any{"sessionId": externalSession}, timeout)
	}
	return &PlannerResult{
		Response:   strings.Join(text, ""),
		Reasoning:  strings.Join(thoughts, ""),
		ToolCalls:  decisions.snapshotCalls(),
		ModelName:  "acp:" + model,
		StopReason: stopReason,
	}, nil
}

// ResetPlannerSession 对齐 reset_planner_session：取消任务并删除状态
func (p *PlannerBridge) ResetPlannerSession(chatID string) error {
	p.mu.Lock()
	run := p.active[chatID]
	p.mu.Unlock()
	if run != nil {
		run.cancel()
		<-run.done
	}
	path := p.statePath(chatID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (p *PlannerBridge) plannerPolicy() string {
	if p.opts.RenderPrompt == nil {
		return ""
	}
	text, err := p.opts.RenderPrompt("agent_planner", nil)
	if err != nil {
		return ""
	}
	return text
}

func (p *PlannerBridge) chatLock(chatID string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	lock := p.locks[chatID]
	if lock == nil {
		lock = &sync.Mutex{}
		p.locks[chatID] = lock
	}
	return lock
}

type plannerStateFile struct {
	ChatID    string         `json:"chat_id"`
	Seen      map[string]int `json:"seen"`
	Policy    string         `json:"policy"`
	Pending   bool           `json:"pending"`
	System    []string       `json:"system"`
	Transient map[string]int `json:"transient,omitempty"`
	SessionID string         `json:"session_id"`
	Identity  map[string]any `json:"identity,omitempty"`
}

func (p *PlannerBridge) statePath(chatID string) string {
	sum := sha256.Sum256([]byte(chatID))
	return filepath.Join(p.root, "planner", hex.EncodeToString(sum[:])+".json")
}

func (p *PlannerBridge) loadState(chatID string) (*plannerStateFile, error) {
	state := &plannerStateFile{ChatID: chatID, Seen: map[string]int{}, Transient: map[string]int{}}
	raw, err := os.ReadFile(p.statePath(chatID))
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := sonic.ConfigStd.Unmarshal(raw, state); err != nil {
			return nil, fmt.Errorf("解析 Planner 状态失败: %w", err)
		}
	}
	if state.ChatID != "" && state.ChatID != chatID {
		return nil, fmt.Errorf("外接会话归属不匹配")
	}
	if state.Seen == nil {
		state.Seen = map[string]int{}
	}
	state.ChatID = chatID
	return state, nil
}

func (p *PlannerBridge) saveState(chatID string, state *plannerStateFile) error {
	path := p.statePath(chatID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := sonic.ConfigStd.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func plannerIdentity(cfg Config, cwd, remoteCWD string) map[string]any {
	identity := map[string]any{"connection": string(cfg.Connection), "cwd": cwd}
	if cfg.ACPRemoteCwd != "" {
		identity["remote_cwd"] = remoteCWD
	}
	if cfg.Connection == ConnectionDSHDesktop {
		identity["manifest"] = DesktopManifestPath(cfg)
	} else {
		args := cfg.ACPArgs
		if cfg.Connection == ConnectionDSH {
			args = []string{"--profile", "acp"}
		}
		identity["command"] = cfg.ACPCommand
		identity["args"] = args
		identity["env_hash"] = sha256Hex(canonicalJSON(cfg.ACPEnv))
	}
	return identity
}

func sameIdentity(a, b map[string]any) bool {
	return canonicalJSON(a) == canonicalJSON(b)
}

func canonicalJSON(value any) string {
	raw, err := sonic.ConfigStd.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func buildPlannerPrompt(delta []map[string]any, policy string, policyKey string, state *plannerStateFile, capabilities map[string]any, render func(string, map[string]string) (string, error)) ([]any, error) {
	var prompt []any
	if policy != "" && state.Policy != policyKey {
		prompt = append(prompt, map[string]any{"type": "text", "text": policy})
	}
	imageSupported := promptImageSupported(capabilities)
	for _, item := range delta {
		cloned := cloneMapAny(item)
		parts, _ := cloned["parts"].([]any)
		var images []any
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			if stringValue(part["type"]) != "image" {
				continue
			}
			if !imageSupported {
				return nil, fmt.Errorf("外接 Planner 未声明图片输入能力")
			}
			data, _ := part["image_base64"].(string)
			format, _ := part["image_format"].(string)
			delete(part, "image_base64")
			delete(part, "image_format")
			part["text"] = "Image attached after this context item."
			images = append(images, map[string]any{"type": "image", "data": data, "mimeType": "image/" + format})
		}
		raw, err := sonic.ConfigStd.Marshal(cloned)
		if err != nil {
			return nil, err
		}
		prompt = append(prompt, map[string]any{"type": "text", "text": string(raw)})
		prompt = append(prompt, images...)
	}
	if state.Pending {
		notice := "上一轮未完成。没有实际执行结果的工具请求不能视为已执行；请结合最新信息重新判断。"
		if render != nil {
			if text, err := render("agent_planner_resume", nil); err == nil && strings.TrimSpace(text) != "" {
				notice = text
			}
		}
		prompt = append([]any{map[string]any{"type": "text", "text": notice}}, prompt...)
	}
	if len(prompt) == 0 {
		notice := "请根据已有信息继续。"
		if render != nil {
			if text, err := render("agent_planner_continue", nil); err == nil && strings.TrimSpace(text) != "" {
				notice = text
			}
		}
		prompt = append(prompt, map[string]any{"type": "text", "text": notice})
	}
	return prompt, nil
}

func selectDelta(items []map[string]any, historyIDs []string, seen map[string]int, previousSystem []string, previousTransient map[string]int) ([]map[string]any, map[string]int, []string) {
	updated := cloneIntMap(seen)
	if updated == nil {
		updated = map[string]int{}
	}
	stable := make(map[string]bool, len(historyIDs))
	for _, id := range historyIDs {
		stable[id] = true
	}
	keys := make([]string, len(items))
	stableFlags := make([]bool, len(items))
	systemKeys := make([]string, 0)
	for index, item := range items {
		meta, _ := item["meta"].(map[string]any)
		historyID, _ := meta["item_id"].(string)
		stableFlags[index] = stable[historyID] && historyID != ""
		keys[index] = contentKey(item, historyID, stableFlags[index])
		if strings.EqualFold(stringValue(item["role"]), "system") {
			systemKeys = append(systemKeys, contentKey(item, "", false))
		}
	}
	systemChanged := !equalStrings(systemKeys, previousSystem)
	counts := map[string]int{}
	delta := make([]map[string]any, 0)
	for index, item := range items {
		key := keys[index]
		counts[key]++
		source := seen
		if !stableFlags[index] && previousTransient != nil {
			source = previousTransient
		}
		if counts[key] > source[key] || (systemChanged && strings.EqualFold(stringValue(item["role"]), "system")) {
			delta = append(delta, item)
		}
		if counts[key] > updated[key] {
			updated[key] = counts[key]
		}
	}
	return delta, updated, systemKeys
}

func contentKey(item map[string]any, historyID string, stable bool) string {
	cloned := cloneMapAny(item)
	delete(cloned, "meta")
	if stable && historyID != "" {
		cloned["history_id"] = historyID
	}
	return sha256Hex(canonicalJSON(cloned))
}

func transientCounts(items []map[string]any, historyIDs []string) map[string]int {
	stable := make(map[string]bool, len(historyIDs))
	for _, id := range historyIDs {
		stable[id] = true
	}
	counts := map[string]int{}
	for _, item := range items {
		meta, _ := item["meta"].(map[string]any)
		historyID, _ := meta["item_id"].(string)
		if historyID != "" && stable[historyID] {
			continue
		}
		counts[contentKey(item, "", false)]++
	}
	return counts
}

func cloneMapAny(value map[string]any) map[string]any {
	raw, err := sonic.ConfigStd.Marshal(value)
	if err != nil {
		return value
	}
	out := map[string]any{}
	if err := sonic.ConfigStd.Unmarshal(raw, &out); err != nil {
		return value
	}
	return out
}

func cloneIntMap(value map[string]int) map[string]int {
	if value == nil {
		return nil
	}
	out := make(map[string]int, len(value))
	for key, number := range value {
		out[key] = number
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func validateRequired(schema map[string]any, args map[string]any) error {
	required, _ := schema["required"].([]any)
	for _, raw := range required {
		name, _ := raw.(string)
		if name == "" {
			continue
		}
		if _, ok := args[name]; !ok {
			return fmt.Errorf("Planner 工具参数缺少 %s", name)
		}
	}
	return nil
}

func selectSessionModel(ctx context.Context, bridge *Bridge, cfg Config, session map[string]any, sessionID string) error {
	options, _ := session["configOptions"].([]any)
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if option["id"] == "model" {
			_, err := bridge.Request(ctx, "session/set_config_option", map[string]any{
				"sessionId": sessionID,
				"configId":  "model",
				"value":     cfg.ACPModel,
			}, cfg.ACPTimeoutSec)
			return err
		}
	}
	return fmt.Errorf("外部 Agent 未提供 model 配置选项")
}

// ContentKey 对齐 agent/planner_state.py `content_keys` 的单条计算：
// 去掉 meta 后按稳定 ID 附加 history_id，再对规范化 JSON 取 sha256。
func ContentKey(item map[string]any, historyID string, stable bool) string {
	return contentKey(item, historyID, stable)
}
