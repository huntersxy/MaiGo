package focus

import (
	"container/list"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/maisaka"
)

// MaxSeenMessages 已见消息键上限
const MaxSeenMessages = 20000

// GroupMember Focus 共享组成员的最小运行面
type GroupMember interface {
	SessionID() string
	SessionName() string
	IsGroupSession() bool
	IsRunning() bool
	MaxContextSize() int

	ChatHistory() []maisaka.ContextMessage
	AppendChatHistory(messages ...maisaka.ContextMessage)
	TrimChatHistory(keep int)
	FindSourceMessageByID(messageID string) (maisaka.ContextMessage, bool)
	CollectPendingMessages() []maisaka.ContextMessage
	ClearForcedTurnState()
	EnterStopState()
	EnterRunningState()
	SetAgentState(state string)
	UpdateStageStatus(stage, detail string)
	WaitForMessageQuietPeriod(ctx context.Context)

	ListTools(ctx context.Context) ([]maisaka.ToolSpec, error)
	GetToolSpec(ctx context.Context, name string) (*maisaka.ToolSpec, bool, error)
	InvokeTool(ctx context.Context, invocation maisaka.ToolInvocation) maisaka.ToolExecutionResult
	ExpandMessageIDAliases(arguments map[string]any) map[string]any
	UpdateDeferredToolSpecs(specs []maisaka.ToolSpec)
	DiscoveredToolNames() map[string]struct{}

	SetFocusGroup(group *GroupRuntime)
	FocusGroup() *GroupRuntime
}

// Member GroupMember 的简写别名，管理器与工具路由使用
type Member = GroupMember

// ReferenceProvider 可选：复用成员聊天的记忆与人物注入
type ReferenceProvider interface {
	BuildPlannerInjectedUserMessages(profile *maisaka.ContextMessage, sources []maisaka.ContextMessage, deferredNotice string) []string
	RefreshJargonReferenceMessage() *maisaka.ContextMessage
}

// ToolEffectRecorder 可选：记录工具副作用与媒体消息
type ToolEffectRecorder interface {
	RecordToolExecutionEffects(invocation maisaka.ToolInvocation, result maisaka.ToolExecutionResult, spec *maisaka.ToolSpec)
	BuildToolResultHistoryContent(call maisaka.ToolCall, result maisaka.ToolExecutionResult, condensed string) string
	AppendToolResultMediaMessages(call maisaka.ToolCall, result maisaka.ToolExecutionResult) []maisaka.ContextMessage
}

// ToolResultCondenser 可选：按成员运行时判断并整理超长工具结果
// 判定依赖成员自己的 condense 配置与 web_result_preview 元数据，由成员侧实现。
type ToolResultCondenser interface {
	CondenseToolResult(ctx context.Context, call maisaka.ToolCall, result maisaka.ToolExecutionResult, latestThought string) string
}

// LearningTrigger 可选：裁剪学习触发
type LearningTrigger interface {
	TriggerTrimmedHistoryLearning(messages []maisaka.ContextMessage)
}

// PlannerRequest 组级 Planner 请求
type PlannerRequest struct {
	History               []maisaka.ContextMessage
	Tools                 []maisaka.ToolSpec
	DeferredToolsReminder string
	InjectedUserMessages  []string
	TailUserMessages      []string
	MaxContextSize        int
	UseTokenLimit         bool
	MaxContextTokens      int
	LogicalTurnID         string
	// CaptureSnapshot 宿主在请求成功后回传 Planner 请求快照，供压缩复用前缀
	CaptureSnapshot func(*maisaka.PlannerRequestSnapshot)
}

// PlannerStep 执行一次组级 Planner 请求
type PlannerStep func(ctx context.Context, request PlannerRequest) (maisaka.ChatResponse, error)

// SummaryBuilder 共享上下文压缩
type SummaryBuilder func(ctx context.Context, input SummaryInput) (*maisaka.ContextMessage, error)

// SummaryInput 压缩输入
type SummaryInput struct {
	Sources       []maisaka.ContextMessage
	Previous      *maisaka.ContextMessage
	Recent        []maisaka.ContextMessage
	SourceContext string
	SessionID     string
	// Snapshot 本轮 Planner 请求快照；为空时不生成摘要
	Snapshot *maisaka.PlannerRequestSnapshot
}

// PlannerSnapshotInfo 组级 Planner 监控快照
type PlannerSnapshotInfo struct {
	SessionID        string
	RunID            string
	ScopeID          string
	Cycle            int
	Response         maisaka.ChatResponse
	Tools            []map[string]any
	AgentState       string
	EndReason        string
	ActiveToolCallID string
	SessionIDs       []string
	PlannerStartedAt float64
	PlannerEndedAt   float64
}

// FocusEmitter 监控事件出口
type FocusEmitter interface {
	PlannerSnapshot(eventType string, info PlannerSnapshotInfo)
	FlowStep(sessionID, runID string, cycle int, step, title, detail string)
}

// GroupOptions 共享组装配参数
type GroupOptions struct {
	Planner          PlannerStep
	Summary          SummaryBuilder
	Emitter          FocusEmitter
	MaxContextSize   func() int
	UseTokenLimit    func() bool
	MaxContextTokens func() int
	StoreRoot        string
	// Prompts 提示词来源，用于连续等待提醒
	Prompts maisaka.PromptSource
}

// GroupRuntime 每个 Focus 共享组唯一的前台 Planner
type GroupRuntime struct {
	scopeID string
	opts    GroupOptions

	mu                      sync.Mutex
	members                 map[string]GroupMember
	entries                 []*Entry
	pending                 map[string][]*Entry
	ready                   map[string]struct{}
	debouncing              map[string]struct{}
	seen                    *seenSet
	considered              map[string]int
	sequence                int
	generation              int
	closed                  bool
	failed                  bool
	hydrating               bool
	restored                map[string]struct{}
	waitUntil               *float64
	waitSession             string
	waitToken               string
	waits                   int
	sent                    []*Entry
	activeBatch             *Entry
	post                    []maisaka.ContextMessage
	runID                   string
	cycle                   int
	state                   string
	plannerSnapshot         *maisaka.PlannerRequestSnapshot
	lastPlannerPromptTokens int
	// plannerRetryAfter 失败请求没有确认输入进度；收到更新的序列号前不重试
	plannerRetryAfter *int
	startedAt         float64
	endedAt           float64
	waiters           []chan struct{}

	wake        chan struct{}
	workerAlive bool
	workerWG    sync.WaitGroup
	// debounceWG / flushWG 跟踪异步去抖与检查点协程，Stop 时确定性等待，避免写盘落在测试 TempDir 清理之后
	debounceWG sync.WaitGroup
	flushWG    sync.WaitGroup
	stopDone   chan struct{} // Stop 完成信号，重复 Stop 等待同一轮清理
	ctx        context.Context
	cancel     context.CancelFunc

	tools          *FocusToolRouter
	store          *CheckpointStore
	checkpointRoot string
	checkpointMu   sync.Mutex
	dirty          bool
	flushing       bool
}

func tokenLimit(provider func() int) int {
	if provider == nil {
		return 0
	}
	return provider()
}

// NewGroupRuntime 构造共享组运行时
func NewGroupRuntime(scopeID string, opts GroupOptions) *GroupRuntime {
	if opts.StoreRoot == "" {
		opts.StoreRoot = DefaultCheckpointRoot
	}
	group := &GroupRuntime{
		scopeID:        scopeID,
		opts:           opts,
		members:        map[string]GroupMember{},
		pending:        map[string][]*Entry{},
		ready:          map[string]struct{}{},
		debouncing:     map[string]struct{}{},
		seen:           newSeenSet(MaxSeenMessages),
		considered:     map[string]int{},
		restored:       map[string]struct{}{},
		state:          "stop",
		wake:           make(chan struct{}, 1),
		checkpointRoot: opts.StoreRoot,
	}
	group.store = NewCheckpointStore(scopeID, opts.StoreRoot)
	group.tools = NewFocusToolRouter(group)
	group.ctx, group.cancel = context.WithCancel(context.Background())
	group.runID = newRunID()
	return group
}

// ScopeID 共享组身份
func (g *GroupRuntime) ScopeID() string { return g.scopeID }

// Store 检查点存储
func (g *GroupRuntime) Store() *CheckpointStore { return g.store }

// Tools 组级工具路由
func (g *GroupRuntime) Tools() *FocusToolRouter { return g.tools }

// Members 成员快照
func (g *GroupRuntime) Members() map[string]GroupMember {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]GroupMember, len(g.members))
	for key, member := range g.members {
		out[key] = member
	}
	return out
}

// ValidMembers 当前仍有效的成员
func (g *GroupRuntime) ValidMembers() map[string]GroupMember {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]GroupMember, len(g.members))
	for key, member := range g.members {
		if g.isMemberValidLocked(member) {
			out[key] = member
		}
	}
	return out
}

// History 共享时间线
func (g *GroupRuntime) History() []maisaka.ContextMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.historyLocked()
}

func (g *GroupRuntime) historyLocked() []maisaka.ContextMessage {
	out := make([]maisaka.ContextMessage, 0, len(g.entries))
	for _, entry := range g.entries {
		out = append(out, entry.Messages...)
	}
	return out
}

// MaxContextSize 组级上下文上限
func (g *GroupRuntime) MaxContextSize() int {
	if g.opts.MaxContextSize != nil {
		if size := g.opts.MaxContextSize(); size >= 2 {
			return size
		}
	}
	return 2
}

// State 运行态：failed / wait / stop / running
func (g *GroupRuntime) State() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed {
		return "failed"
	}
	if g.waitUntil != nil {
		return "wait"
	}
	return g.state
}

// Restore 从检查点恢复时间线
func (g *GroupRuntime) Restore(ctx context.Context) error {
	payload, err := g.store.Load()
	if err != nil || payload == nil {
		return err
	}
	entries := DecodeCheckpointEntries(payload)
	g.mu.Lock()
	g.entries = entries
	for _, entry := range entries {
		for sessionID := range entry.SessionIDs {
			g.restored[sessionID] = struct{}{}
		}
	}
	g.sequence = payload.Sequence
	g.considered = payload.Considered
	if g.considered == nil {
		g.considered = map[string]int{}
	}
	g.seen = newSeenSet(MaxSeenMessages)
	for _, key := range payload.Seen {
		g.seen.add(key)
	}
	g.waitUntil = payload.WaitUntil
	g.waitSession = ""
	if payload.WaitSessionID != nil {
		g.waitSession = *payload.WaitSessionID
	}
	g.waitToken = ""
	if payload.WaitToken != nil {
		g.waitToken = *payload.WaitToken
	}
	if _, ok := g.restored[g.waitSession]; !ok {
		g.waitUntil = nil
		g.waitSession = ""
		g.waitToken = ""
	}
	g.waits = payload.ConsecutiveWaits
	for _, entry := range entries {
		for sessionID, sequence := range entry.Progress() {
			if sequence > g.considered[sessionID] {
				g.ready[sessionID] = struct{}{}
			}
		}
	}
	g.mu.Unlock()
	_ = ctx
	return nil
}

// Checkpoint 持久化当前状态
func (g *GroupRuntime) Checkpoint(ctx context.Context) error {
	g.checkpointMu.Lock()
	defer g.checkpointMu.Unlock()
	g.mu.Lock()
	entries := g.closedEntriesLocked()
	pending := make([]*Entry, 0)
	for _, batch := range g.pending {
		pending = append(pending, batch...)
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].Sequence < pending[j].Sequence })
	entries = append(entries, pending...)
	entries = append(entries, g.sent...)
	state := CheckpointState{
		Sequence:         g.sequence,
		Members:          g.checkpointMembersLocked(),
		Considered:       copyIntMap(g.considered),
		Seen:             g.seen.keys(),
		WaitUntil:        g.waitUntil,
		WaitSessionID:    stringPtrOrNil(g.waitSession),
		WaitToken:        stringPtrOrNil(g.waitToken),
		ConsecutiveWaits: g.waits,
	}
	g.mu.Unlock()
	_ = ctx
	return g.store.Save(entries, state)
}

// CheckpointMemberProvider 可选：成员向检查点提供重建会话所需的身份
type CheckpointMemberProvider interface {
	CheckpointMember() CheckpointMember
}

func (g *GroupRuntime) checkpointMembersLocked() map[string]CheckpointMember {
	if len(g.members) == 0 {
		return nil
	}
	out := make(map[string]CheckpointMember, len(g.members))
	for sessionID, member := range g.members {
		if provider, ok := member.(CheckpointMemberProvider); ok {
			info := provider.CheckpointMember()
			info.SessionID = firstNonEmptyValue(info.SessionID, sessionID)
			out[sessionID] = info
			continue
		}
		out[sessionID] = CheckpointMember{SessionID: sessionID, SessionName: member.SessionName()}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// closedEntriesLocked 快照时闭合未完成工具调用
func (g *GroupRuntime) closedEntriesLocked() []*Entry {
	entries := make([]*Entry, 0, len(g.entries))
	for _, entry := range g.entries {
		clone := NewEntry(append([]maisaka.ContextMessage(nil), entry.Messages...), entry.SortedSessionIDs()...)
		clone.Sequence = entry.Sequence
		clone.Watermarks = copyIntMap(entry.Watermarks)
		clone.Inputs = copyInputs(entry.Inputs)
		completed := map[string]struct{}{}
		for _, message := range clone.Messages {
			if message.Role == maisaka.RoleTool && message.ToolCallID != "" {
				completed[message.ToolCallID] = struct{}{}
			}
		}
		for _, message := range clone.Messages {
			if message.Role != maisaka.RoleAssistant {
				continue
			}
			for _, call := range message.ToolCalls {
				if _, ok := completed[call.CallID]; ok {
					continue
				}
				clone.Messages = append(clone.Messages, maisaka.ContextMessage{
					Role:          maisaka.RoleTool,
					Content:       "运行进程在工具批次中断；此调用的完成状态需要核实，禁止自动重放。",
					Source:        maisaka.SourceToolResult,
					Timestamp:     time.Now(),
					ToolCallID:    call.CallID,
					LogicalTurnID: message.LogicalTurnID,
					ToolName:      call.Name,
					Metadata:      map[string]any{"success": false, "execution_state": "unknown"},
				})
			}
		}
		if entry == g.activeBatch {
			clone.Messages = append(clone.Messages, g.post...)
		}
		entries = append(entries, clone)
	}
	return entries
}

// AddMember 挂入成员并恢复它自己的工具媒体与表情映射
func (g *GroupRuntime) AddMember(member GroupMember) {
	if member == nil {
		return
	}
	g.mu.Lock()
	g.members[member.SessionID()] = member
	for _, message := range g.historyLocked() {
		kind, _ := message.Metadata["focus_kind"].(string)
		if (kind == "media" || kind == "emoji") && message.SessionID == member.SessionID() {
			member.AppendChatHistory(message)
		}
	}
	pendingForMember := false
	for _, entry := range g.entries {
		if entry.Progress()[member.SessionID()] > g.considered[member.SessionID()] {
			g.ready[member.SessionID()] = struct{}{}
			pendingForMember = true
			break
		}
	}
	waiting := g.waitUntil != nil
	g.mu.Unlock()
	if pendingForMember || waiting {
		g.signal()
		g.EnsureWorker()
	}
}

// IsMemberValid 成员是否仍属于本组且 Focus 仍启用
func (g *GroupRuntime) IsMemberValid(member GroupMember) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.isMemberValidLocked(member)
}

func (g *GroupRuntime) isMemberValidLocked(member GroupMember) bool {
	if member == nil || !member.IsRunning() || member.FocusGroup() != g {
		return false
	}
	return true
}

// RemoveMember 摘除成员
func (g *GroupRuntime) RemoveMember(ctx context.Context, sessionID string, clearContext bool) error {
	g.mu.Lock()
	g.generation++
	// 成员变化后旧 Planner 前缀不再覆盖当前共享组，压缩必须重新等一次请求
	g.plannerSnapshot = nil
	g.mu.Unlock()
	g.cancelWorker()
	g.mu.Lock()
	delete(g.debouncing, sessionID)
	delete(g.members, sessionID)
	pending := g.pending[sessionID]
	delete(g.pending, sessionID)
	if !clearContext {
		g.entries = append(g.entries, pending...)
		kept := make([]*Entry, 0, len(g.sent))
		for _, entry := range g.sent {
			if _, ok := entry.SessionIDs[sessionID]; ok {
				g.entries = append(g.entries, entry)
				continue
			}
			kept = append(kept, entry)
		}
		g.sent = kept
	}
	delete(g.ready, sessionID)
	if clearContext {
		kept := g.entries[:0]
		for _, entry := range g.entries {
			if _, ok := entry.SessionIDs[sessionID]; ok {
				continue
			}
			kept = append(kept, entry)
		}
		g.entries = kept
		delete(g.considered, sessionID)
		g.seen.removePrefix(sessionID + "\x00")
	}
	if g.waitSession == sessionID {
		g.finishWaitLocked("发起等待的聊天已退出共享组")
	}
	membersLeft := len(g.members) > 0
	g.mu.Unlock()
	if err := g.Checkpoint(ctx); err != nil {
		return err
	}
	if membersLeft {
		g.signal()
		g.EnsureWorker()
	}
	return nil
}

// hasSession 时间线或待处理批次里是否还有该会话
func (g *GroupRuntime) hasSession(sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.members[sessionID]; ok {
		return true
	}
	for _, entry := range g.entries {
		if _, ok := entry.SessionIDs[sessionID]; ok {
			return true
		}
	}
	for _, entry := range g.pending[sessionID] {
		if _, ok := entry.SessionIDs[sessionID]; ok {
			return true
		}
	}
	return false
}

// RegisterMessage 登记一条真实入站消息
func (g *GroupRuntime) RegisterMessage(ctx context.Context, member GroupMember, message maisaka.ContextMessage) error {
	if !g.remember(member.SessionID(), message.MessageID) {
		return nil
	}
	g.mu.Lock()
	g.queueEntryLocked(member.SessionID(), message)
	awakened := isAtMessage(message) && g.waitUntil != nil
	if awakened {
		g.finishWaitLocked(member.SessionName() + " 收到 @")
	}
	delete(g.ready, member.SessionID())
	if !g.closed {
		if _, debouncing := g.debouncing[member.SessionID()]; !debouncing {
			g.debouncing[member.SessionID()] = struct{}{}
			g.debounceWG.Add(1)
			go g.debounce(member)
		}
	}
	if awakened {
		for sessionID := range g.pending {
			g.ready[sessionID] = struct{}{}
		}
	} else if isAtMessage(message) {
		g.ready[member.SessionID()] = struct{}{}
		g.signalLocked()
	}
	g.mu.Unlock()
	if err := g.Checkpoint(ctx); err != nil {
		return err
	}
	g.EnsureWorker()
	return nil
}

func (g *GroupRuntime) debounce(member GroupMember) {
	defer g.debounceWG.Done()
	member.WaitForMessageQuietPeriod(g.ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.debouncing, member.SessionID())
	if _, ok := g.members[member.SessionID()]; ok {
		g.ready[member.SessionID()] = struct{}{}
		g.signalLocked()
	}
}

func (g *GroupRuntime) remember(sessionID, messageID string) bool {
	if messageID == "" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seen.add(sessionID + "\x00" + messageID)
}

// HasMessage 是否已经见过该消息
func (g *GroupRuntime) HasMessage(sessionID, messageID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seen.has(sessionID + "\x00" + messageID)
}

// queueEntryLocked 单条消息作为一个不可拆分片段
func (g *GroupRuntime) queueEntryLocked(sessionID string, message maisaka.ContextMessage) {
	g.sequence++
	entry := NewEntry([]maisaka.ContextMessage{message}, sessionID)
	entry.Sequence = g.sequence
	if message.CountInContext && message.MessageID != "" {
		entry.Inputs[sessionID] = []InputRecord{{Sequence: g.sequence, MessageID: message.MessageID}}
	}
	g.pending[sessionID] = append(g.pending[sessionID], entry)
}

// RegisterProactive 登记主动任务事件
func (g *GroupRuntime) RegisterProactive(member GroupMember, trigger maisaka.ContextMessage) {
	if !g.remember(member.SessionID(), "internal:"+trigger.MessageID) {
		return
	}
	text := trigger.Content
	for _, message := range member.ChatHistory() {
		if message.MessageID == trigger.MessageID && message.Role == maisaka.RoleUser {
			text = message.Content
			break
		}
	}
	notice := maisaka.ContextMessage{
		Role:      maisaka.RoleReference,
		Source:    maisaka.SourceReference,
		Content:   fmt.Sprintf("<focus_event session_id=\"%s\" chat_name=\"%s\">\n%s\n</focus_event>", escapeAttribute(member.SessionID()), escapeAttribute(member.SessionName()), text),
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"display_prefix": "[聊天任务事件]",
			"focus_kind":     "reference",
			"remaining_uses": nil,
		},
	}
	g.mu.Lock()
	g.queueEntryLocked(member.SessionID(), notice)
	g.ready[member.SessionID()] = struct{}{}
	g.signalLocked()
	g.mu.Unlock()
	g.EnsureWorker()
	g.scheduleCheckpoint()
}

// RecordSentMessage 记录发言，延后到工具批次结束后追加
func (g *GroupRuntime) RecordSentMessage(member GroupMember, message maisaka.ContextMessage) {
	if member == nil {
		return
	}
	g.RecordSentMessageForSession(member.SessionID(), message)
}

// RecordSentMessageForSession 按会话编号记录发言，供只有 runtime 的场景复用
func (g *GroupRuntime) RecordSentMessageForSession(sessionID string, message maisaka.ContextMessage) {
	sessionID = strings.TrimSpace(sessionID)
	if g == nil || sessionID == "" {
		return
	}
	g.mu.Lock()
	g.sent = append(g.sent, NewEntry([]maisaka.ContextMessage{message}, sessionID))
	g.mu.Unlock()
	g.scheduleCheckpoint()
}

// scheduleCheckpoint 同步回调也要持久化
func (g *GroupRuntime) scheduleCheckpoint() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.dirty = true
	if g.flushing {
		g.mu.Unlock()
		return
	}
	g.flushing = true
	g.flushWG.Add(1)
	g.mu.Unlock()
	go g.flushCallbacks()
}

func (g *GroupRuntime) flushCallbacks() {
	defer g.flushWG.Done()
	for {
		g.mu.Lock()
		if !g.dirty {
			g.flushing = false
			g.mu.Unlock()
			return
		}
		g.dirty = false
		g.mu.Unlock()
		if err := g.Checkpoint(g.ctx); err != nil {
			g.mu.Lock()
			g.failed = true
			g.generation++
			g.flushing = false
			g.mu.Unlock()
			g.cancelWorker()
			g.setStage("失败", "后台事件保存失败，已停止共享 Planner；请检查日志")
			return
		}
	}
}

// EnsureWorker 确保共享 Planner 工作协程在运行
func (g *GroupRuntime) EnsureWorker() {
	g.mu.Lock()
	if g.closed || g.failed || g.hydrating || g.workerAlive {
		g.mu.Unlock()
		return
	}
	g.workerAlive = true
	g.workerWG.Add(1)
	g.mu.Unlock()
	go g.Run()
}

func (g *GroupRuntime) signal() {
	g.mu.Lock()
	g.signalLocked()
	g.mu.Unlock()
}

func (g *GroupRuntime) signalLocked() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// setStage 同步共享组与成员阶段
func (g *GroupRuntime) setStage(stage, detail string) {
	g.setStageState(stage, detail, "running")
}

// setStageState 指定共享组状态；请求失败时保持 stop，等待新输入再继续
func (g *GroupRuntime) setStageState(stage, detail, agentState string) {
	g.mu.Lock()
	if g.failed {
		g.state = "failed"
	} else if g.waitUntil != nil {
		g.state = "wait"
	} else if g.workerAlive {
		g.state = agentState
	}
	waiting := g.waitUntil != nil
	members := make([]GroupMember, 0, len(g.members))
	for _, member := range g.members {
		members = append(members, member)
	}
	g.mu.Unlock()
	for _, member := range members {
		if waiting {
			member.SetAgentState("wait")
		} else {
			member.SetAgentState(agentState)
		}
		member.UpdateStageStatus(stage, "Focus 共享组 · "+detail)
	}
}

// takeReadyInputs 取出就绪输入并推进成员进度
func (g *GroupRuntime) takeReadyInputs() []*Entry {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.takeReadyInputsLocked()
}

func (g *GroupRuntime) takeReadyInputsLocked() []*Entry {
	entries := make([]*Entry, 0)
	for sessionID := range g.ready {
		member, ok := g.members[sessionID]
		if !ok {
			continue
		}
		delete(g.ready, sessionID)
		entries = append(entries, g.pending[sessionID]...)
		delete(g.pending, sessionID)
		member.CollectPendingMessages()
		member.ClearForcedTurnState()
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Sequence < entries[j].Sequence })
	g.entries = append(g.entries, entries...)
	if len(g.sent) > 0 {
		g.entries = append(g.entries, g.sent...)
		g.sent = nil
	}
	return entries
}

// Roster 共享组会话说明
func (g *GroupRuntime) Roster() string {
	g.mu.Lock()
	members := make([]GroupMember, 0, len(g.members))
	for _, member := range g.members {
		if g.isMemberValidLocked(member) {
			members = append(members, member)
		}
	}
	g.mu.Unlock()
	sort.Slice(members, func(i, j int) bool { return members[i].SessionID() < members[j].SessionID() })
	lines := make([]string, 0, len(members))
	for _, member := range members {
		instructions := memberInstructions(member)
		if strings.TrimSpace(instructions) == "" {
			instructions = "无额外注意信息"
		}
		lines = append(lines, fmt.Sprintf("在 %s(%s) 中的注意信息：%s", member.SessionName(), member.SessionID(), instructions))
	}
	return strings.Join(lines, "\n")
}

// memberInstructions 可选注入：成员额外注意信息
func memberInstructions(member GroupMember) string {
	if provider, ok := member.(interface{ ChatInstructions() string }); ok {
		return provider.ChatInstructions()
	}
	return ""
}

// ExecuteControl 处理 wait 控制工具；整组等待状态由 applyWaitControl 统一落地
func (g *GroupRuntime) ExecuteControl(name string, arguments map[string]any, _ string) maisaka.ToolExecutionResult {
	if name != "wait" {
		return maisaka.ToolExecutionResult{ToolName: name, Success: false, Content: "当前只支持 wait 控制工具。", Message: "当前只支持 wait 控制工具。"}
	}
	result := maisaka.ToolExecutionResult{ToolName: name, Success: true, Metadata: map[string]any{"pause_execution": true}}
	if raw, exists := arguments["seconds"]; exists {
		seconds, isInt := toInt(raw)
		if !isInt || seconds < 0 {
			return maisaka.ToolExecutionResult{ToolName: name, Success: false, Content: "seconds 必须是非负整数。", Message: "seconds 必须是非负整数。"}
		}
		result.Metadata["wait_seconds"] = seconds
	}
	return result
}

// applyReplyControl 成功回复后的控制作用于整个共享组，复用 reply 已校验的行为与等待时长
func (g *GroupRuntime) applyReplyControl(result maisaka.ToolExecutionResult, sessionID string) {
	if !result.Success {
		return
	}
	action, _ := result.Metadata["after_reply"].(string)
	if strings.TrimSpace(action) == "" || strings.TrimSpace(action) == "continue" {
		return
	}
	g.applyWaitControl(result, sessionID)
}

// applyWaitControl 将显式 wait 和回复后的暂停统一应用到整个共享组
func (g *GroupRuntime) applyWaitControl(result maisaka.ToolExecutionResult, sessionID string) {
	if !result.Success {
		return
	}
	seconds, timed := metadataIntValue(result.Metadata["wait_seconds"])
	if timed {
		g.mu.Lock()
		g.waits++
		until := nowSeconds() + float64(seconds)
		g.waitUntil = &until
		g.waitSession = sessionID
		g.waitToken = newRunID()
		hasPendingAt := false
		for _, batch := range g.pending {
			for _, entry := range batch {
				for _, message := range entry.Messages {
					if isAtMessage(message) {
						hasPendingAt = true
						break
					}
				}
			}
		}
		content := fmt.Sprintf("已为整个 Focus 共享组登记等待 %d 秒；@ 可以提前唤醒。", seconds)
		if hasPendingAt {
			g.finishWaitLocked("等待登记前已有尚未提交的 @ 消息")
			for memberID := range g.pending {
				g.ready[memberID] = struct{}{}
			}
			content = "已登记整组等待，但已有尚未提交的 @ 消息，立即继续下一次思考。"
		}
		g.mu.Unlock()
		result.Content = joinToolResultContent(result.Content, content)
		result.Message = result.Content
	} else {
		g.mu.Lock()
		g.waits = 0
		g.mu.Unlock()
		result.Content = joinToolResultContent(result.Content, "已结束整个 Focus 共享组的本轮思考。")
		result.Message = result.Content
	}
	result.Metadata["pause_execution"] = true
}

func metadataIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func joinToolResultContent(current, extra string) string {
	current = strings.TrimSpace(current)
	extra = strings.TrimSpace(extra)
	if current == "" {
		return extra
	}
	if extra == "" {
		return current
	}
	return current + "\n" + extra
}

// finishWait 结束整组等待并通知发起方
func (g *GroupRuntime) finishWait(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.finishWaitLocked(reason)
}

func (g *GroupRuntime) finishWaitLocked(reason string) {
	sessionID := g.waitSession
	g.waitUntil = nil
	g.waitSession = ""
	g.waitToken = ""
	if sessionID != "" {
		notice := maisaka.ContextMessage{
			Role:      maisaka.RoleReference,
			Source:    maisaka.SourceReference,
			Content:   fmt.Sprintf("session_id=%s 发起的整组等待已结束：%s。请结合所有聊天的新消息继续判断。", sessionID, reason),
			Timestamp: time.Now(),
			Metadata: map[string]any{
				"display_prefix": "[Focus 等待结束]",
				"focus_kind":     "reference",
				"remaining_uses": nil,
			},
		}
		g.queueEntryLocked(sessionID, notice)
		g.ready[sessionID] = struct{}{}
	}
	g.signalLocked()
}

// UnprocessedMessages 交接到普通循环的未判断真实输入
func (g *GroupRuntime) UnprocessedMessages(sessionID string) ([]maisaka.ContextMessage, error) {
	g.mu.Lock()
	entries := make([]*Entry, 0, len(g.entries))
	entries = append(entries, g.entries...)
	entries = append(entries, g.pending[sessionID]...)
	considered := g.considered[sessionID]
	member := g.members[sessionID]
	g.mu.Unlock()
	if member == nil {
		return nil, nil
	}
	records := make([]InputRecord, 0)
	for _, entry := range entries {
		for _, record := range entry.Inputs[sessionID] {
			if record.Sequence > considered {
				records = append(records, record)
			}
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Sequence == records[j].Sequence {
			return records[i].MessageID < records[j].MessageID
		}
		return records[i].Sequence < records[j].Sequence
	})
	messages := make([]maisaka.ContextMessage, 0, len(records))
	for _, record := range records {
		message, ok := member.FindSourceMessageByID(record.MessageID)
		if !ok {
			return nil, fmt.Errorf("Focus 待处理消息不存在：session_id=%s message_id=%s", sessionID, record.MessageID)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// Stop 关闭共享组；等待后台去抖与检查点协程退出后再做最后一次落盘，保证调用返回后不再有写盘
func (g *GroupRuntime) Stop() {
	g.mu.Lock()
	if g.closed {
		done := g.stopDone
		g.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	g.closed = true
	done := make(chan struct{})
	g.stopDone = done
	g.mu.Unlock()
	g.cancelWorker()
	if g.cancel != nil {
		g.cancel()
	}
	g.workerWG.Wait()
	g.debounceWG.Wait()
	g.flushWG.Wait()
	_ = g.Checkpoint(context.Background())
	close(done)
}

// cancelWorker 取消当前 Planner 请求并闭合未完成批次
func (g *GroupRuntime) cancelWorker() {
	g.mu.Lock()
	g.generation++
	g.activeBatch = nil
	g.post = nil
	g.mu.Unlock()
}

// seenSet 有上限的已见键集合
type seenSet struct {
	limit int
	order *list.List
	items map[string]*list.Element
}

func newSeenSet(limit int) *seenSet {
	return &seenSet{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

func (s *seenSet) add(key string) bool {
	if s == nil {
		return true
	}
	if _, ok := s.items[key]; ok {
		return false
	}
	s.items[key] = s.order.PushBack(key)
	for s.order.Len() > s.limit {
		front := s.order.Front()
		if front == nil {
			break
		}
		s.order.Remove(front)
		delete(s.items, front.Value.(string))
	}
	return true
}

func (s *seenSet) has(key string) bool {
	if s == nil {
		return false
	}
	_, ok := s.items[key]
	return ok
}

func (s *seenSet) keys() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.items))
	for element := s.order.Front(); element != nil; element = element.Next() {
		out = append(out, element.Value.(string))
	}
	return out
}

func (s *seenSet) removePrefix(prefix string) {
	if s == nil {
		return
	}
	for key, element := range s.items {
		if strings.HasPrefix(key, prefix) {
			s.order.Remove(element)
			delete(s.items, key)
		}
	}
}

func isAtMessage(message maisaka.ContextMessage) bool {
	if message.Metadata == nil {
		return false
	}
	value, ok := message.Metadata["is_at"].(bool)
	return ok && value
}

func escapeAttribute(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#x27;")
	return replacer.Replace(value)
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func copyIntMap(source map[string]int) map[string]int {
	if source == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyInputs(source map[string][]InputRecord) map[string][]InputRecord {
	out := make(map[string][]InputRecord, len(source))
	for key, records := range source {
		out[key] = append([]InputRecord(nil), records...)
	}
	return out
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
