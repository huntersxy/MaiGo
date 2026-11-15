package maisaka

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/maisaka/turn_trigger"
)

// scoredToolSpec tool_search 打分结果
type scoredToolSpec struct {
	score int
	name  string
	spec  ToolSpec
}

// RegisterMessage 缓存一条新消息并唤醒主循环
func (r *Runtime) RegisterMessage(ctx context.Context, message ContextMessage) error {
	return r.registerMessage(ctx, message)
}

func (r *Runtime) registerMessage(ctx context.Context, message ContextMessage) error {
	if message.SessionID == "" {
		message.SessionID = r.sessionID
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now()
	}
	if message.ReceivedAt == nil {
		received := message.Timestamp
		message.ReceivedAt = &received
	}
	// 对齐 Python：静默窗口以主程序记录的 received_at 起算，不把媒体预处理耗时计入批次
	receivedAt := *message.ReceivedAt
	r.observeMessageDebounce(message, receivedAt)
	r.updateMessageTriggerState(&message)

	r.mu.Lock()
	r.messageCache = append(r.messageCache, message)
	if receivedAt.After(r.lastMessageReceivedAt) {
		r.lastMessageReceivedAt = receivedAt
	}
	r.mu.Unlock()

	r.emitMonitorMessageIngested(message)
	if r.FocusModeActive() {
		// Focus 组由组级前台 Planner 统一消费，成员不再单独调度
		return nil
	}
	r.scheduler.ScheduleMessageTurn()
	return nil
}

// updateMessageTriggerState 补齐消息中的 @/提及 标记，并在命中时启用强制触发
func (r *Runtime) updateMessageTriggerState(message *ContextMessage) {
	mentioned, at := false, false
	boost := 0.0
	if r.opts.MentionDetector != nil {
		mentioned, at, boost = r.opts.MentionDetector(*message)
	}
	if at {
		message.MetadataBoolSet("is_at", true)
	}
	if mentioned {
		message.MetadataBoolSet("is_mentioned", true)
	}
	shouldForce := boost >= 1.0 ||
		(at && r.opts.InevitableAtReply != nil && r.opts.InevitableAtReply()) ||
		(mentioned && r.opts.MentionedBotReply != nil && r.opts.MentionedBotReply())
	if !shouldForce || (!at && !mentioned) {
		return
	}
	r.armForcedTurn(message, at, mentioned)
}

// armForcedTurn 在检测到 @ 或提及时强制下一轮思考绕过普通频率阈值
func (r *Runtime) armForcedTurn(message *ContextMessage, isAt, isMentioned bool) {
	triggerReason := "触发消息"
	switch {
	case isAt:
		triggerReason = "@消息"
	case isMentioned:
		triggerReason = "提及消息"
	}
	r.mu.Lock()
	wasArmed := r.forcedTurnEnabled
	r.forcedTurnEnabled = true
	r.forcedTurnMessageID = message.MessageID
	r.forcedTurnReason = triggerReason
	r.mu.Unlock()
	r.scheduler.IdleBackoff().Reset()
	if wasArmed {
		zap.L().Info(r.logPrefix+" 检测到新的"+triggerReason+"，刷新强制 continue 状态", zap.String("消息编号", message.MessageID))
		return
	}
	zap.L().Info(r.logPrefix+" 检测到"+triggerReason+"，下一轮 Planner 将强制触发", zap.String("消息编号", message.MessageID))
}

// HasPendingMessages 是否仍有未消费消息
func (r *Runtime) HasPendingMessages() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastProcessedIndex < len(r.messageCache)
}

// PendingMessageCount 统计当前尚未进入内部循环的新消息数量
func (r *Runtime) PendingMessageCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastProcessedIndex >= len(r.messageCache) {
		return 0
	}
	return len(r.messageCache) - r.lastProcessedIndex
}

// PendingMessages 返回尚未处理的待处理消息，实现 turn_trigger.RuntimeView
func (r *Runtime) PendingMessages() []turn_trigger.PendingMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastProcessedIndex >= len(r.messageCache) {
		return nil
	}
	pending := r.messageCache[r.lastProcessedIndex:]
	out := make([]turn_trigger.PendingMessage, 0, len(pending))
	for _, message := range pending {
		out = append(out, turn_trigger.PendingMessage{
			MessageID:          message.MessageID,
			Platform:           message.Platform,
			UserID:             message.UserID,
			ProcessedPlainText: message.Content,
			IsAt:               message.MetadataBool("is_at"),
			IsMentioned:        message.MetadataBool("is_mentioned"),
			ReceivedAt:         message.ReceivedAt,
		})
	}
	return out
}

// CollectPendingMessages 从消息缓存中收集一批尚未处理的消息
func (r *Runtime) CollectPendingMessages() []ContextMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	startIndex := r.lastProcessedIndex
	if startIndex >= len(r.messageCache) {
		return nil
	}
	pending := make([]ContextMessage, len(r.messageCache)-startIndex)
	copy(pending, r.messageCache[startIndex:])
	r.lastProcessedIndex = len(r.messageCache)
	if r.opts.Debouncer != nil {
		r.opts.Debouncer.Consume()
	}
	r.pruneProcessedMessageCacheLocked()
	return pending
}

func (r *Runtime) pruneProcessedMessageCacheLocked() {
	if len(r.messageCache) <= MaxRetainedMessageCacheSize {
		return
	}
	drop := len(r.messageCache) - MaxRetainedMessageCacheSize
	kept := make([]ContextMessage, 0, MaxRetainedMessageCacheSize)
	kept = append(kept, r.messageCache[drop:]...)
	r.messageCache = kept
	r.lastProcessedIndex = max(0, r.lastProcessedIndex-drop)
}

// ScheduleMessageTurn 为当前待处理消息安排一次内部 turn
func (r *Runtime) ScheduleMessageTurn() {
	if r.FocusModeActive() {
		return
	}
	r.scheduler.ScheduleMessageTurn()
}

// MessageTurnScheduled 是否已排入 message turn
func (r *Runtime) MessageTurnScheduled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.messageTurnScheduled
}

// EnqueueMessageTurn 投递一次消息触发的内部循环
func (r *Runtime) EnqueueMessageTurn() {
	r.cancelDeferredMessageTurnTask()
	r.mu.Lock()
	r.messageTurnScheduled = true
	r.mu.Unlock()
	r.pushTrigger(TriggerMessage)
}

func (r *Runtime) pushTrigger(trigger TurnTriggerSource) {
	select {
	case r.internalTurnQueue <- trigger:
	default:
		// 队列满时退化为记录，主循环在下一轮合并处理
		r.turnQueueMu.Lock()
		r.pendingTurnTriggers = append(r.pendingTurnTriggers, trigger)
		r.turnQueueMu.Unlock()
	}
}

// TakeTrigger 阻塞读取一次内部循环触发
func (r *Runtime) TakeTrigger(ctx context.Context) (TurnTriggerSource, bool) {
	select {
	case trigger := <-r.internalTurnQueue:
		return trigger, true
	case <-ctx.Done():
		return "", false
	}
}

// DeferMessageTurnCheck 延迟后重新检查是否应投递消息触发
func (r *Runtime) DeferMessageTurnCheck(delaySeconds float64) {
	r.cancelDeferredMessageTurnTask()
	r.mu.Lock()
	r.deferredTurnGen++
	gen := r.deferredTurnGen
	r.mu.Unlock()
	// 与 Python 的 asyncio.create_task 等价：立即返回，延迟结束后再检查
	go func() {
		timer := time.NewTimer(time.Duration(delaySeconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-r.ctx.Done():
			return
		case <-timer.C:
		}
		r.mu.Lock()
		if !r.running || r.deferredTurnGen != gen {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		r.scheduler.ScheduleMessageTurn()
	}()
}

func (r *Runtime) cancelDeferredMessageTurnTask() {
	r.mu.Lock()
	if r.deferredTurnCancel != nil {
		r.deferredTurnCancel()
		r.deferredTurnCancel = nil
	}
	r.mu.Unlock()
}

// MarkMessageTurnUnscheduled 释放当前消息触发占位，允许后续重新调度
func (r *Runtime) MarkMessageTurnUnscheduled() {
	r.mu.Lock()
	r.messageTurnScheduled = false
	r.mu.Unlock()
}

// MessageQuietRemainingSeconds 返回本批消息距离防抖截止还需等待的秒数
func (r *Runtime) MessageQuietRemainingSeconds() float64 {
	if r.opts.Debouncer == nil || !r.isMessageDebounceEnabled() {
		return 0
	}
	remaining := r.opts.Debouncer.Remaining(time.Now())
	if remaining <= 0 {
		return 0
	}
	return remaining.Seconds()
}

func (r *Runtime) isMessageDebounceEnabled() bool { return r.debounceMode() != "off" }

func (r *Runtime) debounceMode() string {
	if r.opts.DebounceMode == nil {
		return "off"
	}
	return strings.ToLower(strings.TrimSpace(r.opts.DebounceMode(r.opts.IsGroupSession)))
}

// WaitForMessageQuietPeriod 等待消息批次允许判断；新消息会唤醒等待并重算
func (r *Runtime) WaitForMessageQuietPeriod(ctx context.Context) {
	for {
		remaining := r.MessageQuietRemainingSeconds()
		if remaining <= 0 {
			return
		}
		step := time.Duration(remaining * float64(time.Second))
		if step > 500*time.Millisecond {
			step = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-r.ctx.Done():
			return
		case <-time.After(step):
		}
	}
}

// observeMessageDebounce 在触发调度之前更新截止时间；开关热重载时清除旧批次
func (r *Runtime) observeMessageDebounce(message ContextMessage, receivedAt time.Time) {
	if r.opts.Debouncer == nil {
		return
	}
	mode := r.debounceMode()
	enabled := mode != "off"
	r.mu.Lock()
	if enabled != r.messageDebounceWasEnabled {
		r.opts.Debouncer.Consume()
		r.messageDebounceWasEnabled = enabled
	}
	r.mu.Unlock()
	if mode == "off" {
		return
	}
	if message.MetadataBool("is_notify") || message.MetadataBool("is_command") || r.isOnlineBotSelf(message.Platform, message.UserID) {
		return
	}
	kind, length := messageShape(message)
	wall := time.Now()
	reason := r.opts.Debouncer.Observe(DebounceObserve{
		Author:      message.UserID,
		IsBareImage: kind == 1 && length == 0,
		IsPureEmoji: kind == 2 && length == 0,
		Mode:        mode,
		AtBot:       message.MetadataBool("is_at") || message.MetadataBool("is_mentioned"),
		ReceivedAt:  receivedAt,
		WallTime:    wall,
		Now:         wall,
	})
	if deadline, ok := r.opts.Debouncer.Deadline(); ok {
		remain := deadline.Sub(time.Now())
		if remain < 0 {
			remain = 0
		}
		zap.L().Info(fmt.Sprintf("%s 消息防抖：档位=%s，规则=%s，剩余等待=%.3f 秒，待处理=%d 条，消息编号=%s",
			r.logPrefix, debounceModeLabel(mode), reason, remain.Seconds(), r.PendingMessageCount()+1, message.MessageID))
	}
}

func (r *Runtime) isOnlineBotSelf(platform, userID string) bool {
	if r == nil || r.opts.OnlineBotAccountIDs == nil {
		return false
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	ids := r.opts.OnlineBotAccountIDs(platform)
	if len(ids) == 0 {
		return false
	}
	_, ok := ids[userID]
	return ok
}

func debounceModeLabel(mode string) string {
	switch mode {
	case "fast":
		return "快速"
	case "natural":
		return "自然"
	case "steady":
		return "沉稳"
	default:
		return mode
	}
}

// EnterStopState 切换到停止状态
func (r *Runtime) EnterStopState() { r.enterStopState() }

func (r *Runtime) enterStopState() {
	r.mu.Lock()
	r.agentState = turn_trigger.StateStop
	r.mu.Unlock()
	r.resetConsecutiveWaitCount("stop_state")
	r.clearWaitState()
}

// EnterRunningState 切换到运行状态，不清理 wait 工具挂起信息
func (r *Runtime) EnterRunningState() {
	r.mu.Lock()
	r.agentState = turn_trigger.StateRunning
	r.mu.Unlock()
}

func (r *Runtime) clearWaitState() {
	r.mu.Lock()
	r.pendingWaitToolCallID = ""
	r.pendingWaitLogicalTurnID = ""
	r.pendingWaitStartedAt = nil
	r.pendingWaitSeconds = nil
	cancel := r.waitTimeoutCancel
	r.waitTimeoutCancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runtime) resetConsecutiveWaitCount(reason string) {
	r.mu.Lock()
	count := r.consecutiveWaitCount
	r.consecutiveWaitCount = 0
	r.mu.Unlock()
	if count > 0 {
		zap.L().Debug(r.logPrefix+" 连续 wait 计数已重置", zap.Int("原计数", count), zap.String("原因", reason))
	}
}

// EnterCountedWaitState 进入 wait 状态并返回连续次数；多次等待由下一轮上下文提醒
func (r *Runtime) EnterCountedWaitState(seconds *float64, toolCallID, logicalTurnID string) (int, error) {
	if err := r.EnterWaitState(seconds, toolCallID, logicalTurnID); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.consecutiveWaitCount++
	count := r.consecutiveWaitCount
	r.mu.Unlock()
	return count, nil
}

// ResumeFromWaitForProactiveTrigger 主动触发到来时从 wait 状态恢复运行
func (r *Runtime) ResumeFromWaitForProactiveTrigger() {
	if r.AgentState() != turn_trigger.StateWait {
		return
	}
	r.EnterRunningState()
}

// EnterWaitState 切换到等待状态
func (r *Runtime) EnterWaitState(seconds *float64, toolCallID, logicalTurnID string) error {
	if toolCallID == "" || logicalTurnID == "" {
		return fmt.Errorf("进入 wait 状态必须绑定非空 tool_call_id 和 logical_turn_id")
	}
	now := time.Now()
	r.mu.Lock()
	r.agentState = turn_trigger.StateWait
	r.pendingWaitToolCallID = toolCallID
	r.pendingWaitLogicalTurnID = logicalTurnID
	r.pendingWaitStartedAt = &now
	r.pendingWaitSeconds = seconds
	r.messageTurnScheduled = false
	r.mu.Unlock()
	r.cancelDeferredMessageTurnTask()
	r.CancelWaitTimeout()
	if seconds != nil {
		r.scheduleWaitTimeout(*seconds, toolCallID)
	}
	if r.PlannerMessageSteering() && r.HasPendingMessages() {
		// 工具执行期间收到的消息也能唤醒刚进入的 wait
		r.ScheduleMessageTurn()
	}
	return nil
}

// CancelWaitTimeout 取消当前 wait 对应的超时任务
func (r *Runtime) CancelWaitTimeout() { r.cancelWaitTimeoutTask() }

func (r *Runtime) cancelWaitTimeoutTask() {
	r.mu.Lock()
	cancel := r.waitTimeoutCancel
	r.waitTimeoutCancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runtime) scheduleWaitTimeout(seconds float64, toolCallID string) {
	ctx, cancel := context.WithCancel(r.ctx)
	r.mu.Lock()
	r.waitTimeoutCancel = cancel
	r.mu.Unlock()
	go func() {
		defer cancel()
		timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.mu.Lock()
		stillWaiting := r.running &&
			r.agentState == turn_trigger.StateWait &&
			r.pendingWaitToolCallID == toolCallID
		r.mu.Unlock()
		if !stillWaiting {
			return
		}
		zap.L().Debug(r.logPrefix + " Maisaka 等待已超时")
		r.EnterRunningState()
		r.pushTrigger(TriggerTimeout)
	}()
}

// ConsumePendingWaitState 消费当前 wait 挂起状态，供 wait 完成消息回填
func (r *Runtime) ConsumePendingWaitState() (toolCallID, logicalTurnID string, elapsedSeconds float64, requestedSeconds *float64, err error) {
	r.mu.Lock()
	toolCallID = r.pendingWaitToolCallID
	logicalTurnID = r.pendingWaitLogicalTurnID
	startedAt := r.pendingWaitStartedAt
	requestedSeconds = r.pendingWaitSeconds
	r.mu.Unlock()
	if toolCallID == "" || logicalTurnID == "" {
		return "", "", 0, nil, fmt.Errorf("不存在可补齐的 wait 工具调用")
	}
	if startedAt != nil {
		elapsedSeconds = time.Since(*startedAt).Seconds()
		if elapsedSeconds < 0 {
			elapsedSeconds = 0
		}
	}
	r.cancelWaitTimeoutTask()
	r.mu.Lock()
	r.pendingWaitToolCallID = ""
	r.pendingWaitLogicalTurnID = ""
	r.pendingWaitStartedAt = nil
	r.pendingWaitSeconds = nil
	r.mu.Unlock()
	return toolCallID, logicalTurnID, elapsedSeconds, requestedSeconds, nil
}

// HasPendingWaitToolCall 返回当前是否存在等待完成后需要回填的 wait 工具调用
func (r *Runtime) HasPendingWaitToolCall() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingWaitToolCallID != ""
}

// PendingWaitToolCallIDs 返回历史清理期间允许暂时未闭合的 wait 调用 ID
func (r *Runtime) PendingWaitToolCallIDs() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingWaitToolCallID == "" {
		return map[string]struct{}{}
	}
	return map[string]struct{}{r.pendingWaitToolCallID: {}}
}

// SetCurrentActionTools 记录当前 Action Loop 已实际暴露给 planner 的工具声明
func (r *Runtime) SetCurrentActionTools(toolSpecs []ToolSpec) {
	names := map[string]struct{}{}
	specs := map[string]ToolSpec{}
	for _, spec := range toolSpecs {
		normalized := strings.TrimSpace(spec.Name)
		if normalized == "" {
			continue
		}
		names[normalized] = struct{}{}
		specs[normalized] = spec
	}
	r.mu.Lock()
	r.currentActionToolNames = names
	r.currentActionToolSpecs = specs
	r.mu.Unlock()
}

// SetCurrentActionToolNames 兼容仅设置工具名的调用
func (r *Runtime) SetCurrentActionToolNames(toolNames []string) {
	specs := make([]ToolSpec, 0, len(toolNames))
	for _, name := range toolNames {
		specs = append(specs, ToolSpec{Name: name})
	}
	r.SetCurrentActionTools(specs)
}

// CurrentActionToolSpec 读取当前 Action Loop 暴露的工具声明
func (r *Runtime) CurrentActionToolSpec(toolName string) *ToolSpec {
	normalized := strings.TrimSpace(toolName)
	if normalized == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.currentActionToolSpecs[normalized]
	if !ok {
		return nil
	}
	copied := spec
	return &copied
}

// IsActionToolCurrentlyAvailable 判断指定工具在当前 Action Loop 轮次中是否真实可用
func (r *Runtime) IsActionToolCurrentlyAvailable(toolName string) bool {
	normalized := strings.TrimSpace(toolName)
	if normalized == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.currentActionToolNames[normalized]
	return ok
}

// UpdateDeferredToolSpecs 刷新当前会话的 deferred tools 池，并清理失效的已发现工具
func (r *Runtime) UpdateDeferredToolSpecs(specs []ToolSpec) {
	next := map[string]ToolSpec{}
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		next[name] = spec
	}
	r.mu.Lock()
	r.deferredToolSpecs = next
	for name := range r.discoveredToolNames {
		if _, ok := next[name]; !ok {
			delete(r.discoveredToolNames, name)
		}
	}
	r.mu.Unlock()
}

// SyncDiscoveredDeferredToolsWithContext 根据当前实际上下文中的 tool_search 调用链同步已发现 deferred tools
func (r *Runtime) SyncDiscoveredDeferredToolsWithContext(selectedHistory []ContextMessage) {
	visible := r.extractVisibleToolSearchDiscoveries(selectedHistory)
	r.mu.Lock()
	r.discoveredToolNames = map[string]struct{}{}
	for name := range visible {
		if _, ok := r.deferredToolSpecs[name]; ok {
			r.discoveredToolNames[name] = struct{}{}
		}
	}
	r.mu.Unlock()
}

func (r *Runtime) extractVisibleToolSearchDiscoveries(selectedHistory []ContextMessage) map[string]struct{} {
	toolSearchCallIDs := map[string]struct{}{}
	for _, message := range selectedHistory {
		if message.Role != RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Name == ToolToolSearchName && call.CallID != "" {
				toolSearchCallIDs[call.CallID] = struct{}{}
			}
		}
	}
	discovered := map[string]struct{}{}
	for _, message := range selectedHistory {
		if message.Role != RoleTool || message.ToolName != ToolToolSearchName {
			continue
		}
		if _, ok := toolSearchCallIDs[message.ToolCallID]; !ok {
			continue
		}
		if message.MetadataBool("success") == false && message.Metadata != nil {
			if success, ok := message.Metadata["success"].(bool); ok && !success {
				continue
			}
		}
		for name := range r.parseToolSearchResultToolNames(message.Content) {
			discovered[name] = struct{}{}
		}
	}
	return discovered
}

func (r *Runtime) parseToolSearchResultToolNames(content string) map[string]struct{} {
	discovered := map[string]struct{}{}
	r.mu.Lock()
	specs := r.deferredToolSpecs
	r.mu.Unlock()
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line[2:], "（", 2)[0])
		if _, ok := specs[name]; ok {
			discovered[name] = struct{}{}
		}
	}
	return discovered
}

// DiscoveredDeferredToolSpecs 返回当前会话中已发现、且仍然有效的 deferred tools
func (r *Runtime) DiscoveredDeferredToolSpecs() []ToolSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ToolSpec, 0, len(r.discoveredToolNames))
	for name, spec := range r.deferredToolSpecs {
		if _, ok := r.discoveredToolNames[name]; ok {
			out = append(out, spec)
		}
	}
	return out
}

// BuildDeferredToolsReminder 构造供 planner 使用的 deferred tools 提示消息
func (r *Runtime) BuildDeferredToolsReminder() string {
	r.mu.Lock()
	undiscovered := make([]ToolSpec, 0, len(r.deferredToolSpecs))
	for name, spec := range r.deferredToolSpecs {
		if _, ok := r.discoveredToolNames[name]; ok {
			continue
		}
		undiscovered = append(undiscovered, spec)
	}
	r.mu.Unlock()
	if len(undiscovered) == 0 {
		return ""
	}
	lines := make([]string, 0, len(undiscovered)+5)
	lines = append(lines, "<system-reminder>", "以下工具当前未直接暴露给你，但可以通过 tool_search 工具发现并在后续轮次中使用：")
	for index, spec := range undiscovered {
		name := strings.TrimSpace(spec.Name)
		description := strings.TrimSpace(spec.Description)
		if description != "" {
			lines = append(lines, fmt.Sprintf("%d. %s: %s", index+1, name, description))
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, name))
	}
	lines = append(lines, "", "如需其中某个工具，请先调用 tool_search。tool_search 只负责发现工具，不直接执行。", "</system-reminder>")
	return strings.Join(lines, "\n")
}

// SearchDeferredToolSpecs 按名称或简要描述搜索 deferred tools
func (r *Runtime) SearchDeferredToolSpecs(query string, limit int) []ToolSpec {
	normalizedQuery := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if normalizedQuery == "" {
		return nil
	}
	terms := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(normalizedQuery))
	r.mu.Lock()
	specs := make(map[string]ToolSpec, len(r.deferredToolSpecs))
	for name, spec := range r.deferredToolSpecs {
		specs[name] = spec
	}
	r.mu.Unlock()

	matches := make([]scoredToolSpec, 0, len(specs))
	for name, spec := range specs {
		lowerName := strings.ToLower(name)
		lowerDescription := strings.ToLower(spec.Description)
		score := 0
		if normalizedQuery == lowerName {
			score += 1000
		}
		if strings.HasPrefix(lowerName, normalizedQuery) {
			score += 300
		}
		if strings.Contains(lowerName, normalizedQuery) {
			score += 200
		}
		if strings.Contains(lowerDescription, normalizedQuery) {
			score += 100
		}
		for _, term := range terms {
			if strings.Contains(lowerName, term) {
				score += 25
			}
			if strings.Contains(lowerDescription, term) {
				score += 10
			}
		}
		if score <= 0 {
			continue
		}
		matches = append(matches, scoredToolSpec{score: score, name: name, spec: spec})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].name < matches[j].name
	})
	if limit < 1 {
		limit = 1
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]ToolSpec, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.spec)
	}
	return out
}

// DiscoverDeferredTools 将指定 deferred tools 标记为已发现，并返回本次新发现的工具名
func (r *Runtime) DiscoverDeferredTools(toolNames []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newly []string
	for _, raw := range toolNames {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := r.deferredToolSpecs[name]; !ok {
			continue
		}
		if _, ok := r.discoveredToolNames[name]; ok {
			continue
		}
		r.discoveredToolNames[name] = struct{}{}
		newly = append(newly, name)
	}
	return newly
}
