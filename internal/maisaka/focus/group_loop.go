package focus

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/llm_models/contract"
	"MaiGo/internal/maisaka"

	"go.uber.org/zap"
)

// isRecoverablePlannerError 只恢复已知模型请求失败；保存或上下文一致性错误仍交由外层停止
func isRecoverablePlannerError(err error) bool {
	var empty *contract.EmptyResponseError
	var timeout *contract.TaskTimeoutError
	var network *contract.NetworkError
	var status *contract.HTTPStatusError
	var parse *contract.ParseError
	return errors.As(err, &empty) || errors.As(err, &timeout) || errors.As(err, &network) ||
		errors.As(err, &status) || errors.As(err, &parse)
}

// Run 共享 Planner 主循环
func (g *GroupRuntime) Run() {
	defer func() {
		g.mu.Lock()
		g.workerAlive = false
		g.mu.Unlock()
		_ = g.Checkpoint(context.Background())
		g.workerWG.Done()
	}()
	for {
		g.mu.Lock()
		if g.closed || len(g.members) == 0 {
			g.mu.Unlock()
			return
		}
		if g.plannerRetryAfter != nil {
			if g.sequence <= *g.plannerRetryAfter {
				g.mu.Unlock()
				select {
				case <-g.ctx.Done():
					return
				case <-g.wake:
				}
				continue
			}
			g.plannerRetryAfter = nil
		}
		if g.waitUntil != nil {
			remaining := *g.waitUntil - nowSeconds()
			g.mu.Unlock()
			if remaining <= 0 {
				g.finishWait("等待到期")
				continue
			}
			g.setStage("等待", "整组暂停，@ 可以提前唤醒")
			timer := time.NewTimer(time.Duration(remaining * float64(time.Second)))
			select {
			case <-g.ctx.Done():
				timer.Stop()
				return
			case <-g.wake:
				timer.Stop()
			case <-timer.C:
				g.finishWait("等待到期")
			}
			continue
		}
		restoredPending := false
		for _, entry := range g.entries {
			for sessionID, sequence := range entry.Progress() {
				if _, ok := g.members[sessionID]; ok && sequence > g.considered[sessionID] {
					restoredPending = true
					break
				}
			}
			if restoredPending {
				break
			}
		}
		readyInputs := false
		for sessionID := range g.ready {
			if _, ok := g.members[sessionID]; !ok {
				continue
			}
			if len(g.pending[sessionID]) > 0 {
				readyInputs = true
				break
			}
		}
		if !readyInputs && !restoredPending {
			g.state = "stop"
			members := make([]GroupMember, 0, len(g.members))
			for _, member := range g.members {
				members = append(members, member)
			}
			g.mu.Unlock()
			for _, member := range members {
				member.SetAgentState("stop")
				member.UpdateStageStatus("空闲", "Focus 共享组等待新消息")
			}
			select {
			case <-g.ctx.Done():
				return
			case <-g.wake:
			}
			continue
		}
		g.mu.Unlock()
		if err := g.runTurn(g.ctx); err != nil {
			if g.ctx.Err() != nil {
				return
			}
			g.mu.Lock()
			g.failed = true
			g.mu.Unlock()
			g.setStage("失败", "共享 Planner 运行失败，输入已保留；请检查日志并重启运行时")
			return
		}
	}
}

// runTurn 一轮内部思考上限内的完整共享轮次
func (g *GroupRuntime) runTurn(ctx context.Context) error {
	for round := 0; round < maisaka.MaxInternalRounds; round++ {
		g.mu.Lock()
		if g.closed || g.waitUntil != nil {
			g.mu.Unlock()
			return nil
		}
		g.mu.Unlock()
		g.takeReadyInputs()
		if err := g.Compact(ctx); err != nil {
			return err
		}
		if _, err := g.tools.Refresh(ctx); err != nil {
			return err
		}
		g.mu.Lock()
		generation := g.generation
		g.mu.Unlock()
		references := g.buildReferences(ctx)
		g.takeReadyInputs()

		g.mu.Lock()
		related := make([]string, 0, len(g.members))
		for sessionID := range g.members {
			related = append(related, sessionID)
		}
		sort.Strings(related)
		covered := make(map[string]int, len(g.entries))
		for _, entry := range g.entries {
			for sessionID, sequence := range entry.Progress() {
				if sequence > covered[sessionID] {
					covered[sessionID] = sequence
				}
			}
		}
		history := g.historyLocked()
		g.cycle++
		cycle := g.cycle
		logicalTurnID := newRunID()
		waits := g.waits
		requestSequence := g.sequence
		g.mu.Unlock()

		g.setStage("Planner", fmt.Sprintf("汇合 %d 个聊天，等待模型响应", len(related)))
		started := nowSeconds()
		g.mu.Lock()
		g.startedAt = started
		g.mu.Unlock()
		total := 0
		for _, message := range history {
			if message.CountInContext {
				total++
			}
		}
		maxContextSize := g.MaxContextSize()
		if total+1 > maxContextSize {
			maxContextSize = total + 1
		}
		injected := make([]string, 0, len(references)+2)
		injected = append(injected, g.Roster(), g.tools.DeferredNotice())
		injected = append(injected, references...)
		var capturedSnapshot *maisaka.PlannerRequestSnapshot
		request := PlannerRequest{
			History:              history,
			Tools:                g.toolSpecs(),
			InjectedUserMessages: injected,
			TailUserMessages:     maisaka.BuildConsecutiveWaitReminders(g.promptSource(), waits),
			MaxContextSize:       maxContextSize,
			LogicalTurnID:        logicalTurnID,
			CaptureSnapshot: func(snapshot *maisaka.PlannerRequestSnapshot) {
				capturedSnapshot = snapshot
			},
		}
		if g.opts.Planner == nil {
			return fmt.Errorf("Focus 共享组 Planner 端口未初始化")
		}
		response, err := g.opts.Planner(ctx, request)
		g.mu.Lock()
		g.endedAt = nowSeconds()
		g.mu.Unlock()
		if err != nil {
			if !isRecoverablePlannerError(err) {
				return err
			}
			g.mu.Lock()
			g.plannerSnapshot = nil
			g.plannerRetryAfter = &requestSequence
			g.mu.Unlock()
			g.setStageState("请求失败", fmt.Sprintf("输入已保留，等待新消息后继续；%v", err), "stop")
			zap.L().Error("Focus 共享 Planner 请求失败，等待新输入",
				zap.String("scope_id", g.scopeID), zap.Error(err))
			_ = g.Checkpoint(ctx)
			return nil
		}
		g.mu.Lock()
		stale := generation != g.generation || g.closed
		if stale {
			g.plannerSnapshot = nil
		}
		if capturedSnapshot != nil && !stale {
			g.plannerSnapshot = capturedSnapshot
		}
		g.mu.Unlock()
		if stale {
			return nil
		}
		g.mu.Lock()
		for sessionID, sequence := range covered {
			if sequence > g.considered[sessionID] {
				g.considered[sessionID] = sequence
			}
		}
		for _, entry := range g.entries {
			for sessionID, records := range entry.Inputs {
				kept := records[:0]
				for _, record := range records {
					if record.Sequence > g.considered[sessionID] {
						kept = append(kept, record)
					}
				}
				entry.Inputs[sessionID] = kept
			}
		}
		batch := NewEntry(response.RawMessages, related...)
		g.entries = append(g.entries, batch)
		g.mu.Unlock()
		if err := g.Checkpoint(ctx); err != nil {
			return err
		}
		activeToolCallID := ""
		if len(response.ToolCalls) > 0 {
			activeToolCallID = response.ToolCalls[0].CallID
		}
		g.emitResponse("planner.progress", response, nil, started, false, activeToolCallID, cycle)
		monitorTools, paused := g.executeTools(ctx, response, batch, logicalTurnID, generation)
		g.mu.Lock()
		if len(g.sent) > 0 {
			g.entries = append(g.entries, g.sent...)
			g.sent = nil
		}
		g.mu.Unlock()
		g.emitResponse("planner.finalized", response, monitorTools, started, paused, "", cycle)
		if err := g.Checkpoint(ctx); err != nil {
			return err
		}
		if paused || len(response.ToolCalls) == 0 {
			g.mu.Lock()
			if g.waitUntil == nil {
				g.waits = 0
			}
			g.mu.Unlock()
			return nil
		}
	}
	return nil
}

// toolSpecs 当前路由下的工具声明
func (g *GroupRuntime) toolSpecs() []maisaka.ToolSpec {
	specs := make([]maisaka.ToolSpec, 0)
	for _, route := range g.tools.Routes() {
		specs = append(specs, route.Spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// promptSource 提示词来源，用于连续等待提醒
func (g *GroupRuntime) promptSource() maisaka.PromptSource {
	return g.opts.Prompts
}

// buildReferences 沿用各聊天的自动记忆/人物注入，并为每份参考信息标明真实来源
func (g *GroupRuntime) buildReferences(ctx context.Context) []string {
	g.mu.Lock()
	considered := copyIntMap(g.considered)
	entries := append([]*Entry(nil), g.entries...)
	members := make(map[string]GroupMember, len(g.members))
	for sessionID, member := range g.members {
		members[sessionID] = member
	}
	g.mu.Unlock()
	grouped := map[string][]maisaka.ContextMessage{}
	for _, entry := range entries {
		for _, message := range entry.Messages {
			sessionID := message.SessionID
			if sessionID == "" {
				continue
			}
			if _, ok := members[sessionID]; !ok {
				continue
			}
			if entry.Progress()[sessionID] <= considered[sessionID] {
				continue
			}
			grouped[sessionID] = append(grouped[sessionID], message)
		}
	}
	sessionIDs := make([]string, 0, len(grouped))
	for sessionID := range grouped {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs)
	references := make([]string, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		member := members[sessionID]
		sources := grouped[sessionID]
		provider, ok := member.(ReferenceProvider)
		if !ok {
			continue
		}
		profile := sources[len(sources)-1]
		injected := provider.BuildPlannerInjectedUserMessages(&profile, sources, "")
		if jargon := provider.RefreshJargonReferenceMessage(); jargon != nil && jargon.Content != "" {
			injected = append(injected, jargon.Content)
		}
		if len(injected) == 0 {
			continue
		}
		references = append(references, fmt.Sprintf("<focus_reference session_id=\"%s\">\n%s\n</focus_reference>", escapeAttribute(sessionID), strings.Join(injected, "\n")))
	}
	_ = ctx
	return references
}

// executeTools 按输出顺序执行本批次工具
func (g *GroupRuntime) executeTools(ctx context.Context, response maisaka.ChatResponse, batch *Entry, logicalTurnID string, generation int) ([]map[string]any, bool) {
	monitor := make([]map[string]any, 0, len(response.ToolCalls))
	paused := false
	g.mu.Lock()
	g.activeBatch = batch
	g.post = nil
	g.mu.Unlock()
	callTurns := map[string]string{}
	for _, message := range response.RawMessages {
		for _, call := range message.ToolCalls {
			callTurns[call.CallID] = message.LogicalTurnID
		}
	}
	for index, call := range response.ToolCalls {
		toolStarted := nowSeconds()
		var member GroupMember
		var spec *maisaka.ToolSpec
		var condensed string
		var result maisaka.ToolExecutionResult
		g.mu.Lock()
		stale := generation != g.generation || g.closed
		g.mu.Unlock()
		if paused || stale {
			result = maisaka.ToolExecutionResult{
				ToolName: call.Name,
				Success:  false,
				Content:  "本批次已暂停或会话归属已失效，此工具未执行。",
				Message:  "本批次已暂停或会话归属已失效，此工具未执行。",
			}
		} else {
			sessionID, _ := call.Arguments["session_id"].(string)
			g.setStage("工具执行", fmt.Sprintf("%s → %s", call.Name, sessionID))
			invocation, invokedResult, invokedMember, invokedSpec := g.tools.Invoke(ctx, call.Name, call.Arguments, call.CallID, response.Content)
			result = invokedResult
			member = invokedMember
			spec = invokedSpec
			if member != nil {
				if recorder, ok := member.(ToolEffectRecorder); ok {
					recorder.RecordToolExecutionEffects(invocation, result, spec)
				}
				if condenser, ok := member.(ToolResultCondenser); ok && call.Name != "wait" {
					if condensedText := condenser.CondenseToolResult(ctx, call, result, response.Content); condensedText != "" {
						condensed = condensedText
					}
				}
				if call.Name != "wait" {
					g.mu.Lock()
					g.waits = 0
					g.mu.Unlock()
				}
			}
		}
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		result.Metadata["focus_scope_id"] = g.scopeID
		content := condensed
		if content == "" {
			content = result.Content
		}
		if content == "" {
			if result.Success {
				content = "工具执行成功。"
			} else {
				content = "工具执行失败。"
			}
		}
		if member != nil {
			if recorder, ok := member.(ToolEffectRecorder); ok {
				if built := recorder.BuildToolResultHistoryContent(call, result, condensed); built != "" {
					content = built
				}
			}
		}
		toolMessage := maisaka.ContextMessage{
			Role:          maisaka.RoleTool,
			Content:       content,
			Source:        maisaka.SourceToolResult,
			Timestamp:     time.Now(),
			ToolCallID:    call.CallID,
			LogicalTurnID: firstNonEmptyValue(callTurns[call.CallID], logicalTurnID),
			ToolName:      call.Name,
			Metadata:      result.Metadata,
			SessionID:     sessionOf(call, member),
		}
		if _, ok := result.Metadata["success"].(bool); !ok {
			toolMessage.Metadata["success"] = result.Success
		}
		batch.Messages = append(batch.Messages, toolMessage)
		if member != nil {
			if recorder, ok := member.(ToolEffectRecorder); ok {
				media := recorder.AppendToolResultMediaMessages(call, result)
				g.mu.Lock()
				g.post = append(g.post, media...)
				g.mu.Unlock()
			}
		}
		// 对齐 Python _execute_tools：工具的 post_history_messages
		// 也要进共享历史，并在媒体消息之后追加，保持 function call/result 原子段完整。
		if len(result.AppendPostHistory) > 0 {
			g.mu.Lock()
			g.post = append(g.post, result.AppendPostHistory...)
			g.mu.Unlock()
		}
		item := map[string]any{
			"tool_call_id":  call.CallID,
			"tool_name":     call.Name,
			"tool_args":     call.Arguments,
			"session_id":    call.Arguments["session_id"],
			"session_name":  sessionNameOf(member),
			"success":       result.Success,
			"error_message": result.Message,
			"summary":       content,
			"started_at":    toolStarted,
			"ended_at":      nowSeconds(),
			"duration_ms":   (nowSeconds() - toolStarted) * 1000,
			"detail":        result.Metadata["monitor_detail"],
			"card":          result.Metadata["monitor_card"],
			"sub_cards":     result.Metadata["monitor_sub_cards"],
		}
		monitor = append(monitor, item)
		_ = g.Checkpoint(ctx)
		pauseExecution, _ := result.Metadata["pause_execution"].(bool)
		if result.Success && (pauseExecution || result.PauseTool != "") {
			paused = true
		}
		nextCall := ""
		if index+1 < len(response.ToolCalls) && !paused {
			nextCall = response.ToolCalls[index+1].CallID
		}
		g.emitResponse("planner.progress", response, monitor, g.startedAt, paused, nextCall, g.cycle)
	}
	g.mu.Lock()
	batch.Messages = append(batch.Messages, g.post...)
	g.activeBatch = nil
	g.post = nil
	g.mu.Unlock()
	return monitor, paused
}

func sessionOf(call maisaka.ToolCall, member GroupMember) string {
	if value, ok := call.Arguments["session_id"].(string); ok && value != "" {
		return value
	}
	if member != nil {
		return member.SessionID()
	}
	return ""
}

func sessionNameOf(member GroupMember) string {
	if member == nil {
		return ""
	}
	return member.SessionName()
}

// emitResponse 推送组级监控快照
func (g *GroupRuntime) emitResponse(eventType string, response maisaka.ChatResponse, tools []map[string]any, started float64, paused bool, activeToolCallID string, cycle int) {
	if g.opts.Emitter == nil {
		return
	}
	g.mu.Lock()
	memberIDs := make([]string, 0, len(g.members))
	for sessionID := range g.members {
		memberIDs = append(memberIDs, sessionID)
	}
	sort.Strings(memberIDs)
	state := g.state
	endReason := "tool_continue"
	finished := paused || len(response.ToolCalls) == 0
	if g.waitUntil != nil {
		endReason = "tool_pause:wait"
	} else if finished {
		endReason = "planner_no_tool_end"
	}
	g.mu.Unlock()
	if len(memberIDs) == 0 {
		return
	}
	g.opts.Emitter.PlannerSnapshot(eventType, PlannerSnapshotInfo{
		SessionID:        memberIDs[0],
		RunID:            g.runID,
		ScopeID:          g.scopeID,
		Cycle:            cycle,
		Response:         response,
		Tools:            tools,
		AgentState:       state,
		EndReason:        endReason,
		ActiveToolCallID: activeToolCallID,
		SessionIDs:       memberIDs,
		PlannerStartedAt: g.startedAt,
		PlannerEndedAt:   g.endedAt,
	})
}

// Compact 共享时间线以完整片段为单位压缩
func (g *GroupRuntime) Compact(ctx context.Context) error {
	if g.opts.Summary == nil {
		return nil
	}
	g.mu.Lock()
	plannerSnapshot := g.plannerSnapshot
	snapshot := append([]*Entry(nil), g.entries...)
	generation := g.generation
	maxContextSize := g.MaxContextSize()
	g.mu.Unlock()
	if plannerSnapshot == nil {
		return nil
	}
	size := 0
	for _, entry := range snapshot {
		size += entry.Size()
	}
	if size <= maxContextSize {
		return nil
	}
	split := 0
	remaining := size
	target := maxContextSize / 2
	if target < 1 {
		target = 1
	}
	for split < len(snapshot) && remaining > target {
		remaining -= snapshot[split].Size()
		split++
	}
	if split <= 0 {
		return nil
	}
	old := snapshot[:split]
	related := map[string]struct{}{}
	watermarks := map[string]int{}
	inputs := map[string][]InputRecord{}
	sources := make([]maisaka.ContextMessage, 0)
	for _, entry := range old {
		for sessionID := range entry.SessionIDs {
			related[sessionID] = struct{}{}
		}
		for sessionID, sequence := range entry.Progress() {
			if sequence > watermarks[sessionID] {
				watermarks[sessionID] = sequence
			}
		}
		for sessionID, records := range entry.Inputs {
			inputs[sessionID] = append(inputs[sessionID], records...)
		}
		for _, message := range entry.Messages {
			if IsSummaryMessage(message) {
				continue
			}
			sources = append(sources, message)
		}
	}
	recent := make([]maisaka.ContextMessage, 0)
	for _, entry := range snapshot[split:] {
		recent = append(recent, entry.Messages...)
	}
	previous := (*maisaka.ContextMessage)(nil)
	for entryIndex := len(snapshot) - 1; entryIndex >= 0 && previous == nil; entryIndex-- {
		messages := snapshot[entryIndex].Messages
		for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
			if IsSummaryMessage(messages[messageIndex]) {
				copy := messages[messageIndex]
				previous = &copy
				break
			}
		}
	}
	sourceContext := make([]string, 0, len(snapshot))
	for _, entry := range snapshot {
		parts := make([]string, 0, len(entry.Messages))
		for _, message := range entry.Messages {
			parts = append(parts, fmt.Sprintf("('%s', '%s')", message.Timestamp.Format("2006-01-02T15:04:05.999999"), message.Source))
		}
		sourceContext = append(sourceContext, fmt.Sprintf("session_ids=%s; messages=[%s]", pythonStringList(entry.SortedSessionIDs()), strings.Join(parts, ", ")))
	}
	g.setStage("上下文压缩", "复用 Planner 请求快照并保留会话来源与近期原文")
	summary, summaryErr := g.opts.Summary(ctx, SummaryInput{
		Sources:       sources,
		Previous:      previous,
		Recent:        recent,
		SourceContext: strings.Join(sourceContext, "\n"),
		SessionID:     g.scopeID,
		Snapshot:      plannerSnapshot,
	})
	if summaryErr != nil {
		summary = nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if generation != g.generation || g.closed || len(snapshot) != len(g.entries) {
		return nil
	}
	for index, entry := range snapshot {
		if index >= len(g.entries) || g.entries[index] != entry {
			return nil
		}
	}
	g.plannerSnapshot = nil
	var replacement *Entry
	if summary != nil {
		sourceNames := make([]string, 0, len(related))
		for sessionID := range related {
			if member := g.members[sessionID]; member != nil {
				sourceNames = append(sourceNames, member.SessionName())
			} else {
				sourceNames = append(sourceNames, sessionID)
			}
		}
		sort.Strings(sourceNames)
		summary.Metadata["display_prefix"] = fmt.Sprintf("[Focus 上下文状态摘要 来源：%s]", strings.Join(sourceNames, "、"))
		replacement = NewEntry([]maisaka.ContextMessage{*summary})
	} else {
		summaries := make([]maisaka.ContextMessage, 0)
		for _, entry := range old {
			for _, message := range entry.Messages {
				if IsSummaryMessage(message) {
					summaries = append(summaries, message)
				}
			}
		}
		replacement = NewEntry(summaries)
	}
	for sessionID := range related {
		replacement.AddSessionID(sessionID)
	}
	replacement.Watermarks = watermarks
	replacement.Inputs = inputs
	updated := make([]*Entry, 0, len(g.entries)-split+1)
	updated = append(updated, replacement)
	updated = append(updated, g.entries[split:]...)
	g.entries = updated
	step := "focus.context.trimmed"
	title := "共享上下文裁切"
	detail := fmt.Sprintf("压缩失败，已丢弃 %d 个旧片段，保留近期完整轮次并继续运行。", len(old))
	if summary != nil {
		step = "focus.context.compacted"
		title = "共享上下文压缩"
		detail = fmt.Sprintf("已压缩 %d 个完整片段，保留会话来源与近期完整轮次。", len(old))
	}
	sessionID := ""
	for key := range g.members {
		sessionID = key
		break
	}
	emitter := g.opts.Emitter
	runID := g.runID
	cycle := g.cycle
	members := make(map[string]GroupMember, len(g.members))
	for key, member := range g.members {
		members[key] = member
	}
	g.mu.Unlock()
	if emitter != nil && sessionID != "" {
		emitter.FlowStep(sessionID, runID, cycle, step, title, detail)
	}
	for key, member := range members {
		own := make([]maisaka.ContextMessage, 0)
		for _, entry := range old {
			for _, message := range entry.Messages {
				if message.SessionID == key {
					own = append(own, message)
				}
			}
		}
		if trigger, ok := member.(LearningTrigger); ok && len(own) > 0 {
			trigger.TriggerTrimmedHistoryLearning(own)
		}
		keep := 20
		if size := member.MaxContextSize() * 2; size > keep {
			keep = size
		}
		member.TrimChatHistory(keep)
	}
	g.mu.Lock()
	return nil
}

func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// pythonStringList 以 Python repr 形式输出字符串列表，供摘要 source_context 对齐
func pythonStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+strings.ReplaceAll(value, "'", "\\'")+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
