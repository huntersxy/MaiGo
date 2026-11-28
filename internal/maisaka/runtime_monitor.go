package maisaka

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/maisaka/monitor"
	"MaiGo/internal/maisaka/turn_trigger"
)

// UpdateStageStatus 更新当前会话的阶段状态并广播 stage.status
//
// stage 变化时重置 stage_started_at，与 Python MaisakaStageStatusBoard.update 一致；
// wait 状态自动带上 wait_until，避免前端需要额外推算。
func (r *Runtime) UpdateStageStatus(stage, detail string) {
	r.mu.Lock()
	now := float64(time.Now().UnixNano()) / 1e9
	if r.stage != stage || r.stageStartedAt == 0 {
		r.stageStartedAt = now
	}
	r.stage = stage
	r.stageDetail = detail
	roundText := ""
	if r.currentCycleDetail != nil {
		roundText = r.currentCycleDetail.RoundText
	}
	agentState := string(r.agentState)
	var waitUntil *float64
	if r.pendingWaitStartedAt != nil && r.pendingWaitSeconds != nil && r.agentState == turn_trigger.StateWait {
		value := float64(r.pendingWaitStartedAt.UnixNano())/1e9 + *r.pendingWaitSeconds
		waitUntil = &value
	}
	stageStartedAt := r.stageStartedAt
	sessionName := r.sessionName
	sessionID := r.sessionID
	r.mu.Unlock()
	monitor.UpdateStageStatusGlobal(monitor.StageStatus{
		SessionID:      sessionID,
		SessionName:    sessionName,
		Stage:          stage,
		Detail:         detail,
		RoundText:      roundText,
		AgentState:     agentState,
		WaitUntil:      waitUntil,
		StageStartedAt: stageStartedAt,
	})
}

// Stage 当前阶段名
func (r *Runtime) Stage() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stage, r.stageDetail
}

func (r *Runtime) currentCycleID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cycleCounter
}

func (r *Runtime) emitMonitorSessionStart() {
	if r.opts.Monitor == nil {
		return
	}
	r.opts.Monitor.SessionStart(r.monitorRunID, r.sessionID, r.sessionName, r.opts.IsGroupSession)
}

func (r *Runtime) emitMonitorMessageIngested(message ContextMessage) {
	if r.opts.Monitor == nil {
		return
	}
	r.opts.Monitor.MessageIngested(r.sessionID, message)
}

// EmitMonitorMessageSent 异步广播 MaiSaka 自己发出的消息，供 WebUI 实时展示
func (r *Runtime) EmitMonitorMessageSent(message ContextMessage, speakerName, sourceKind string) {
	if r.opts.Monitor == nil {
		return
	}
	r.opts.Monitor.MessageSent(r.sessionID, speakerName, sourceKind, message)
}

// EmitMonitorMessageUpdated 图片识别完成后，原地刷新监控面板中的消息内容
func (r *Runtime) EmitMonitorMessageUpdated(message ContextMessage, refreshKey [2]string) {
	if r.opts.Monitor == nil {
		return
	}
	r.opts.Monitor.MessageUpdated(r.sessionID, refreshKey, message)
}

func (r *Runtime) emitFlowStep(step, title, detail string, cycleID int) {
	if r.opts.Monitor == nil {
		return
	}
	if cycleID == 0 {
		cycleID = r.currentCycleID()
	}
	r.opts.Monitor.FlowStep(r.sessionID, step, title, detail, cycleID)
}

func (r *Runtime) emitPlannerProgress(cycleDetail CycleDetail, state *CycleRuntimeState, tools []map[string]any, activeToolCallID string) {
	if r.opts.Monitor == nil {
		return
	}
	r.opts.Monitor.PlannerProgress(r.sessionID, r.buildPlannerMonitorPayload(cycleDetail, state, tools, activeToolCallID, false))
}

func (r *Runtime) emitPlannerFinalized(cycleDetail CycleDetail, state *CycleRuntimeState) {
	if state == nil {
		return
	}
	// 先构造完整快照，Monitor 与终端面板共用；监控未接入时也要能展示思考过程
	payload := r.buildPlannerMonitorPayload(cycleDetail, state, state.ToolMonitorResults, "", true)
	if r.opts.Monitor != nil {
		r.opts.Monitor.PlannerFinalized(r.sessionID, payload)
	}
	r.emitPlannerTerminalPanel(payload, state, cycleDetail)
}

// buildPlannerMonitorPayload 构造 emit_planner_snapshot 的载荷结构
//
// 顶层 session/cycle/run 标识 + request/planner/tools/final_state 四个区块，
// 与 Python monitor.events.emit_planner_snapshot 一致；progress 不带请求区块。
func (r *Runtime) buildPlannerMonitorPayload(cycleDetail CycleDetail, state *CycleRuntimeState, tools []map[string]any, activeToolCallID string, finalized bool) map[string]any {
	input := monitor.PlannerSnapshotInput{
		SessionID:        r.sessionID,
		RunID:            r.monitorRunID,
		CycleID:          cycleDetail.CycleID,
		Tools:            tools,
		ActiveToolCallID: activeToolCallID,
	}
	if state != nil {
		response := state.Response
		input.Content = response.Content
		input.Reasoning = response.Reasoning
		input.ModelName = response.ModelName
		input.ToolCalls = plannerToolCallObjects(response.ToolCalls)
		input.NativeToolCalls = nativeToolCallObjects(response.NativeToolCalls)
		input.PromptTokens = response.Usage.PromptTokens
		input.CompletionTokens = response.Usage.CompletionTokens
		input.PromptCacheHit = response.Usage.CacheHitTokens
		input.PromptCacheMiss = response.Usage.CacheMissTokens
		input.DurationMS = float64(state.PlannerDurationMs)
		input.PlannerActionJudge = response.PlannerActionJudge
		input.Interrupted = state.PlannerInterrupted
		input.StartedAt = monitor.Seconds(derefTime(state.PlannerStartedAt))
		input.EndedAt = monitor.Seconds(derefTime(state.PlannerEndedAt))
		if finalized {
			input.AgentState = string(r.AgentState())
			input.EndReason = state.CycleEnd.Reason
			input.EndDetail = state.CycleEnd.Detail
			if snapshot := r.PlannerRequestSnapshot(); snapshot != nil {
				input.Request = &monitor.PlannerRequest{
					Messages:             plannerRequestMessageObjects(snapshot.Items),
					SelectedHistoryCount: response.SelectedHistoryCount,
					ToolCount:            response.ToolCount,
					ContextSections:      plannerContextSectionObjects(response.ContextSections),
				}
			}
		}
	}
	return monitor.BuildPlannerSnapshotPayload(input, finalized)
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// plannerToolCallObjects 把内部工具调用投影为监控序列化输入
func plannerToolCallObjects(calls []ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		source := "response"
		sourceLabel := "正文调用"
		if call.FromReasoning {
			source = "reasoning"
			sourceLabel = "推理中调用"
		}
		if value, ok := call.ExtraContent["tool_call_source"].(string); ok && strings.TrimSpace(value) != "" {
			source = strings.TrimSpace(value)
			sourceLabel = ""
		}
		if value, ok := call.ExtraContent["tool_call_source_label"].(string); ok && strings.TrimSpace(value) != "" {
			sourceLabel = strings.TrimSpace(value)
		}
		entry := map[string]any{
			"id":           call.CallID,
			"name":         call.Name,
			"arguments":    call.Arguments,
			"source":       source,
			"source_label": sourceLabel,
		}
		if len(call.ExtraContent) > 0 {
			entry["extra_content"] = call.ExtraContent
		}
		out = append(out, entry)
	}
	return out
}

// nativeToolCallObjects 把原生工具摘要投影为监控序列化输入
func nativeToolCallObjects(calls []NativeToolCallSummary) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		details := make([]any, 0, len(call.Details))
		for _, item := range call.Details {
			details = append(details, item)
		}
		out = append(out, map[string]any{
			"tool_type":    call.ToolType,
			"call_id":      call.CallID,
			"status":       call.Status,
			"action_type":  call.ActionType,
			"details":      details,
			"source_count": call.SourceCount,
		})
	}
	return out
}

// plannerRequestMessageObjects 把请求 Item 投影为监控消息结构
func plannerRequestMessageObjects(items []ContextItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{"role": string(item.Role), "content": item.Text}
		if item.ToolCallID != "" {
			entry["tool_call_id"] = item.ToolCallID
		}
		if len(item.ToolCalls) > 0 {
			entry["tool_calls"] = plannerToolCallObjects(item.ToolCalls)
		}
		out = append(out, entry)
	}
	return out
}

// plannerContextSectionObjects 把提示词分段用量投影为监控序列化输入
func plannerContextSectionObjects(sections []ContextSectionUsage) []map[string]any {
	out := make([]map[string]any, 0, len(sections))
	for _, section := range sections {
		out = append(out, map[string]any{
			"key":   section.Key,
			"chars": section.Chars,
			"count": section.Count,
		})
	}
	return out
}

// setRunning 标记内部循环是否运行中
func (r *Runtime) setRunning(running bool) {
	r.mu.Lock()
	r.running = running
	r.mu.Unlock()
}

// IsRunning 内部循环是否运行中
func (r *Runtime) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Start 启动运行时主循环
func (r *Runtime) Start() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go func() {
		if err := r.engine.RunLoop(r.ctx); err != nil && r.ctx.Err() == nil {
			zap.L().Error(r.logPrefix+" Maisaka 内部循环异常退出", zap.Error(err))
		}
	}()
}

// Stop 停止运行时主循环
func (r *Runtime) Stop() {
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
	r.scheduler.Close()
	r.cancel()
	if r.subagentTasks != nil {
		r.subagentTasks.CancelAll()
	}
	if r.opts.Learners != nil {
		r.opts.Learners.CancelTrimmedHistoryLearning()
	}
	r.enterStopState()
}

// ConsumeProactiveTriggerMessage 消费当前主动触发消息
func (r *Runtime) consumeProactiveTriggerMessage() *ContextMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	message := r.proactiveTriggerMessage
	r.proactiveTriggerMessage = nil
	return message
}

// ConsumeProactiveTriggerMessage 导出给工具层使用
func (r *Runtime) ConsumeProactiveTriggerMessage() *ContextMessage {
	return r.consumeProactiveTriggerMessage()
}

func (r *Runtime) consumeProactiveLogicalTurnID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	turnID := r.proactiveLogicalTurnID
	r.proactiveLogicalTurnID = ""
	return turnID
}

// QueueProactiveTurn 设置主动触发消息，并投递 proactive 内部循环
// 处于 Focus 共享组时改由 Focus 登记事件，对齐 Python src/maisaka/runtime.py:824-838
func (r *Runtime) QueueProactiveTurn(triggerMessage ContextMessage, logicalTurnID string) {
	if r.opts.FocusProactiveRegistrar != nil && r.opts.FocusProactiveRegistrar(triggerMessage) {
		return
	}
	r.mu.Lock()
	r.proactiveTriggerMessage = &triggerMessage
	r.proactiveLogicalTurnID = logicalTurnID
	r.messageTurnScheduled = true
	r.mu.Unlock()
	r.ResumeFromWaitForProactiveTrigger()
	r.pushTrigger(NewInternalTurnTrigger(TriggerProactive))
}

// ToolResultCondenseConfig 返回当前生效的工具结果整理开关与字符阈值，阈值 <=0 表示关闭
// 对齐 Python src/config/official_configs.py:1029-1055 与 src/maisaka/tool_result_condenser.py:55-66
func (r *Runtime) ToolResultCondenseConfig() (bool, int) {
	if r == nil {
		return false, 0
	}
	if r.opts.ToolResultCondense != nil {
		return r.opts.ToolResultCondense()
	}
	r.mu.Lock()
	threshold := r.toolResultCondenseThreshold
	r.mu.Unlock()
	return threshold > 0, threshold
}

// ShouldCondense 判断一次工具结果是否需要交给子代理整理
// alreadyPreviewed 对齐 Python metadata["web_result_preview"] is True：已预览结果不再二次整理
func (r *Runtime) ShouldCondense(toolName, content string, success bool, alreadyPreviewed bool) bool {
	enabled, threshold := r.ToolResultCondenseConfig()
	if !enabled || threshold <= 0 {
		return false
	}
	return ShouldCondenseToolResult(toolName, content, success, alreadyPreviewed, threshold)
}

// SetToolResultCondenseThreshold 设置工具结果整理阈值，0 表示关闭
// 仅在没有 ToolResultCondense 提供者时生效，提供者存在时每次读取实时配置
func (r *Runtime) SetToolResultCondenseThreshold(threshold int) {
	r.mu.Lock()
	r.toolResultCondenseThreshold = threshold
	r.mu.Unlock()
}

// BuildMonitorMessageContent 生成监控面板使用的消息可见文本
//
// 对齐 Python build_monitor_message_content：优先用 processed_plain_text（纯正文），
// 不能把 Planner 的 <message …> 前缀带进面板。Go 侧 Content 保存的是前缀+正文，
// 这里剥掉前缀后返回正文。
func BuildMonitorMessageContent(message ContextMessage) string {
	text := strings.TrimSpace(stripPlannerPrefix(message.Content))
	if text != "" {
		return text
	}
	if len(message.Images) > 0 {
		return imagePlaceholderText(message.Images)
	}
	return fmt.Sprintf("[%s]", firstNonEmpty(message.Role.String(), "message"))
}

// stripPlannerPrefix 去掉 Planner XML 前缀，只保留消息正文
func stripPlannerPrefix(content string) string {
	trimmed := strings.TrimLeft(content, " \t")
	if !strings.HasPrefix(trimmed, "<message ") && !strings.HasPrefix(trimmed, "<message>") {
		return content
	}
	if idx := strings.Index(trimmed, ">\n"); idx >= 0 {
		return trimmed[idx+2:]
	}
	return content
}

// RoleString 角色可读名
func (role ContextRole) String() string { return string(role) }

// Now 供测试注入
var now = time.Now

func nowTime() time.Time { return now() }
