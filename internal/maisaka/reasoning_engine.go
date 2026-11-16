package maisaka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/common/reasoningtrace"
	"MaiGo/internal/maisaka/turn_trigger"
)

// StopAfterExecutionPauseReason 工具要求执行后停止
const StopAfterExecutionPauseReason = "stop_after_execution"

// ToolInvocation 一次统一工具调用
type ToolInvocation struct {
	ToolName      string
	Arguments     map[string]any
	CallID        string
	LatestThought string
	// SessionID / StreamID 目标聊天；Focus 组级调用显式指定
	SessionID string
	StreamID  string
	// LogicalTurnID 当前完整模型—工具—模型循环的标识
	LogicalTurnID string
	// Metadata 宿主附加信息，含 focus_scope_id 等
	Metadata map[string]any
}

// ToolRuntime 统一工具执行端口，由宿主适配 internal/tools
type ToolRuntime interface {
	// AvailableTools 返回当前 Action Loop 可见工具
	AvailableTools(ctx context.Context) ([]ToolSpec, error)
	// Invoke 执行一次工具调用
	Invoke(ctx context.Context, invocation ToolInvocation) (ToolExecutionResult, error)
	// AfterExecution 执行工具后置副作用，例如工具记录落库
	AfterExecution(ctx context.Context, invocation ToolInvocation, result ToolExecutionResult)
}

// ReplyExecutor Planner 正文直接触发回复的端口
type ReplyExecutor interface {
	ReplyFromContent(ctx context.Context, content string, target *ContextMessage, replyToolArgs map[string]any) ToolExecutionResult
}

// ChatLoopRunner 主对话循环端口
type ChatLoopRunner interface {
	ChatLoopStep(ctx context.Context, chatHistory []ContextMessage, requestKind string, maxContextSize int, tools []ToolSpec, systemPrompt, finalUserMessage string, injected, tail []string) (ChatResponse, error)
}

// TurnStartContext 一次内部循环触发的前置上下文
type TurnStartContext struct {
	CachedMessages       []ContextMessage
	TriggerMessage       *ContextMessage
	TimeoutTriggered     bool
	ProactiveTriggered   bool
	SilentReplyFrequency bool
	LogicalTurnID        string
}

// ReasoningEngine 负责内部思考、推理与工具执行
type ReasoningEngine struct {
	runtime *Runtime

	lastReasoningContent   string
	activeLogicalTurnID    string
	turnPassiveReplyer     *bool
	turnLightweightPrivate bool
}

// NewReasoningEngine 创建推理引擎
func NewReasoningEngine(runtime *Runtime) *ReasoningEngine {
	return &ReasoningEngine{runtime: runtime}
}

// PassiveReplyerEnabled 一轮思考内固定实验模式，避免热重载使 Prompt、工具与输出处理不一致
func (e *ReasoningEngine) PassiveReplyerEnabled() bool {
	if e.turnPassiveReplyer != nil {
		return *e.turnPassiveReplyer
	}
	return false
}

// LastReasoningContent 返回最近一轮思考文本
func (e *ReasoningEngine) LastReasoningContent() string { return e.lastReasoningContent }

// ActiveLogicalTurnID 返回当前完整模型—工具—模型循环的逻辑轮次 ID
func (e *ReasoningEngine) ActiveLogicalTurnID() string { return e.activeLogicalTurnID }

// RunTurnOnce 以显式快照执行一次完整内部思考轮次，供宿主主链路按批次驱动
func (e *ReasoningEngine) RunTurnOnce(ctx context.Context, trigger *ContextMessage, cached []ContextMessage, logicalTurnID string) error {
	runtime := e.runtime
	if trigger == nil && len(cached) > 0 {
		last := cached[len(cached)-1]
		trigger = &last
	}
	startContext := TurnStartContext{
		CachedMessages: cached,
		TriggerMessage: trigger,
		LogicalTurnID:  logicalTurnID,
	}
	if startContext.TriggerMessage == nil {
		return fmt.Errorf("Maisaka 思考轮次缺少触发消息")
	}
	if runtime.IsReplyFrequencySilent() {
		e.handleSilentTurn(ctx, startContext)
		return nil
	}
	return e.runTurn(ctx, startContext)
}

// RunQueuedTurn 取出一个已经排队的内部触发并同步执行，供宿主主动触发消费
// 返回值表示是否真的取到并执行了触发；没有排队触发时返回 false
func (e *ReasoningEngine) RunQueuedTurn(ctx context.Context) (bool, error) {
	runtime := e.runtime
	var queued TurnTriggerSource
	select {
	case queued = <-runtime.internalTurnQueue:
	default:
		return false, nil
	}
	startContext := e.PrepareTurnStartContext(ctx, queued)
	if startContext.TriggerMessage == nil {
		return false, nil
	}
	if startContext.SilentReplyFrequency {
		e.handleSilentTurn(ctx, startContext)
		return true, nil
	}
	return true, e.runTurn(ctx, startContext)
}

// RunLoop 独立消费消息批次，并执行对应的内部思考轮次
func (e *ReasoningEngine) RunLoop(ctx context.Context) error {
	runtime := e.runtime
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-runtime.ctx.Done():
			return nil
		case queued := <-runtime.internalTurnQueue:
			startContext := e.PrepareTurnStartContext(ctx, queued)
			if startContext.TriggerMessage == nil {
				continue
			}
			if startContext.SilentReplyFrequency {
				e.handleSilentTurn(ctx, startContext)
				continue
			}
			if err := e.runTurn(ctx, startContext); err != nil {
				runtime.UpdateStageStatus("错误", err.Error())
				return err
			}
		}
	}
}

func (e *ReasoningEngine) runTurn(ctx context.Context, startContext TurnStartContext) error {
	runtime := e.runtime
	cachedMessages := startContext.CachedMessages
	triggerMessage := startContext.TriggerMessage

	runtime.setRunning(true)
	e.turnLightweightPrivate = runtime.LightweightPrivate()
	passiveReplyer := runtime.opts.PassiveReplyer || e.turnLightweightPrivate
	e.turnPassiveReplyer = &passiveReplyer
	if chatLoop, ok := runtime.opts.ChatLoop.(*ChatLoopService); ok {
		chatLoop.PassiveReplyerOverride = &passiveReplyer
		lightweight := e.turnLightweightPrivate
		chatLoop.LightweightPrivateOverride = &lightweight
	}
	defer func() {
		e.turnPassiveReplyer = nil
		e.turnLightweightPrivate = false
		if chatLoop, ok := runtime.opts.ChatLoop.(*ChatLoopService); ok {
			chatLoop.PassiveReplyerOverride = nil
			chatLoop.LightweightPrivateOverride = nil
		}
		e.activeLogicalTurnID = ""
		if runtime.AgentState() == turn_trigger.StateRunning {
			runtime.EnterStopState()
		}
		if runtime.AgentState() == turn_trigger.StateWait {
			runtime.UpdateStageStatus("等待", "等待后继续思考")
			return
		}
		runtime.UpdateStageStatus("空闲", "本轮处理结束")
	}()

	e.activeLogicalTurnID = startContext.LogicalTurnID
	if e.activeLogicalTurnID == "" {
		e.activeLogicalTurnID = newRunID()
	}
	if reason := runtime.ConsumeForcedTurnReason(); reason != "" {
		zap.L().Info(runtime.logPrefix + " " + reason)
	}

	for roundIndex := 0; roundIndex < runtime.maxInternalRounds; roundIndex++ {
		// 工具执行或整理上下文期间收到的消息，在下一次模型请求前补齐
		if roundIndex > 0 {
			pendingRoundMessages := e.ingestPendingMessages(ctx)
			if len(pendingRoundMessages) > 0 {
				cachedMessages = pendingRoundMessages
				last := pendingRoundMessages[len(pendingRoundMessages)-1]
				triggerMessage = &last
			}
		}
		cycleDetail, roundText := e.startCycleRound(roundIndex)
		state := &CycleRuntimeState{CycleDetail: cycleDetail}
		exit, retryMessages, err := e.runCycleRound(ctx, cycleDetail, roundText, roundIndex, triggerMessage, cachedMessages, state)
		e.finalizeCycle(ctx, cycleDetail, roundIndex, state)
		if err != nil {
			return err
		}
		if len(retryMessages) > 0 {
			cachedMessages = retryMessages
			last := retryMessages[len(retryMessages)-1]
			triggerMessage = &last
			continue
		}
		if exit {
			break
		}
	}
	return nil
}

func (e *ReasoningEngine) runCycleRound(ctx context.Context, cycleDetail CycleDetail, roundText string, roundIndex int, trigger *ContextMessage, cached []ContextMessage, state *CycleRuntimeState) (bool, []ContextMessage, error) {
	runtime := e.runtime
	// 每轮 Planner 请求前先用已完成识图结果刷新历史占位，对应 _refresh_visual_placeholders_for_cycle
	runtime.RefreshVisualPlaceholders(ctx, false)
	e.PostProcessChatHistoryAfterCycle(ctx, cycleDetail, true, false)
	runtime.StartPlannerContinuation()
	source := cached
	if len(source) == 0 && trigger != nil {
		source = []ContextMessage{*trigger}
	}
	started := time.Now()
	state.PlannerStartedAt = &started
	response, err := e.runPlannerRequest(ctx, trigger, source, roundIndex, roundText, state)
	ended := time.Now()
	state.PlannerEndedAt = &ended
	state.PlannerDurationMs = ended.Sub(started).Milliseconds()
	if err != nil {
		if errors.Is(err, ErrPlannerInterrupted) {
			state.PlannerInterrupted = true
			state.CycleEnd = CycleEnd{Reason: "planner_interrupted", Detail: "Planner 被新消息打断，当前轮结束。"}
			interrupted, extraLines, retryMessages := e.handlePlannerInterrupt(ctx, err, roundIndex, roundText, state)
			state.Response = interrupted
			state.PlannerExtraLines = extraLines
			if len(retryMessages) == 0 {
				return true, nil, nil
			}
			return false, retryMessages, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			state.CycleEnd = CycleEnd{Reason: "cancelled", Detail: "思考任务已取消，本轮结束。"}
			if ctx.Err() != nil {
				return true, nil, ctx.Err()
			}
			return true, nil, err
		}
		state.CycleEnd = CycleEnd{Reason: "cycle_error", Detail: fmt.Sprintf("本轮处理异常：%v", err)}
		return true, nil, err
	}
	state.Response = response
	return e.handlePlannerResponseActions(ctx, response, cycleDetail, state), nil, nil
}

// handlePlannerInterrupt 对齐 _handle_planner_interrupt：
// 广播打断状态，构造中断响应；有可展示片段时先写入历史，再决定是否并入新消息重试
func (e *ReasoningEngine) handlePlannerInterrupt(ctx context.Context, err error, roundIndex int, roundText string, state *CycleRuntimeState) (ChatResponse, []string, []ContextMessage) {
	runtime := e.runtime
	runtime.UpdateStageStatus("Planner 已打断", "请求被外部信号中断")
	interruptedAt := time.Now()
	partial := plannerPartialOutput(err)
	partialText := partial.DisplayText()
	interruptedText := "Planner 收到新消息，开始重新决策"
	if partialText != "" {
		interruptedText = "[Planner 已被新消息打断，以下为未完成输出]\n\n" + partialText
	} else {
		interruptedText += "（中断前尚未收到可展示的输出片段）"
	}
	response := ChatResponse{
		Content:              interruptedText,
		SelectedHistoryCount: len(runtime.History()),
		ToolCount:            state.ActionToolCount,
		RawMessages: []ContextMessage{{
			Role:          RoleAssistant,
			Content:       interruptedText,
			Timestamp:     interruptedAt,
			SessionID:     runtime.SessionID(),
			LogicalTurnID: e.activeLogicalTurnID,
		}},
	}
	if partial != nil {
		response.ModelName = partial.ModelName
	}
	if partialText != "" {
		// 工具参数片段只作为文本保留，不创建可执行的 tool_call
		runtime.AppendHistory(response.RawMessages[0])
	}
	startedAt := interruptedAt
	if state.PlannerStartedAt != nil {
		startedAt = *state.PlannerStartedAt
	}
	elapsed := interruptedAt.Sub(startedAt).Seconds()
	extraLines := []string{
		"状态：已被新消息打断",
		"打断位置：Planner 请求流式响应阶段",
		fmt.Sprintf("打断耗时：%.3f 秒", elapsed),
	}
	zap.L().Info(fmt.Sprintf("%s Planner 打断成功: 回合=%d 开始时间=%.3f 打断时间=%.3f 耗时=%.3f 秒",
		runtime.logPrefix, roundIndex+1,
		float64(startedAt.UnixNano())/1e9, float64(interruptedAt.UnixNano())/1e9, elapsed))
	if roundIndex+1 >= runtime.maxInternalRounds || !runtime.HasPendingMessages() {
		return response, extraLines, nil
	}
	retryMessages := e.ingestPendingMessages(ctx)
	if len(retryMessages) == 0 {
		return response, extraLines, nil
	}
	zap.L().Info(fmt.Sprintf("%s 保持活跃状态，直接重试 Planner: 回合=%d", runtime.logPrefix, roundIndex+1))
	return response, extraLines, retryMessages
}

func plannerPartialOutput(err error) *InterruptedStreamOutput {
	var interruptErr *PlannerInterruptError
	if errors.As(err, &interruptErr) {
		return interruptErr.Partial
	}
	return nil
}

// finalizeCycle 渲染并广播一次内部循环的收尾信息
func (e *ReasoningEngine) finalizeCycle(ctx context.Context, cycleDetail CycleDetail, roundIndex int, state *CycleRuntimeState) {
	runtime := e.runtime
	// 对齐 _end_cycle：收尾先执行消费引用与压缩，再结束本轮
	e.PostProcessChatHistoryAfterCycle(ctx, cycleDetail, true, true)
	end := state.CycleEnd
	if roundIndex+1 >= runtime.maxInternalRounds && (end.Reason == "continue" || end.Reason == "tool_continue") {
		end = CycleEnd{Reason: CycleEndMaxRounds, Detail: fmt.Sprintf("达到内部思考轮次上限 %d，结束本轮。", runtime.maxInternalRounds)}
	}
	if end.Reason == "" {
		end = CycleEnd{Reason: CycleEndPlannerNoTool, Detail: "Planner本轮思考结束。"}
	}
	state.CycleEnd = end
	runtime.emitPlannerFinalized(cycleDetail, state)
	runtime.scheduler.IdleBackoff().RecordCycleResult(end.Reason)
}

// PrepareTurnStartContext 消费触发信号并准备本轮初始消息与触发消息
func (e *ReasoningEngine) PrepareTurnStartContext(ctx context.Context, queued TurnTriggerSource) TurnStartContext {
	runtime := e.runtime
	messageTriggered, timeoutTriggered, proactiveTriggered := e.drainReadyTurnTriggers(queued)
	silent := runtime.IsReplyFrequencySilent()

	if runtime.AgentState() == turn_trigger.StateWait &&
		!(timeoutTriggered || proactiveTriggered) &&
		!silent &&
		!(runtime.PlannerMessageSteering() && messageTriggered && runtime.HasPendingMessages()) {
		runtime.MarkMessageTurnUnscheduled()
		zap.L().Debug(runtime.logPrefix + " 当前仍处于 wait 状态，忽略消息触发并继续等待超时")
		return TurnStartContext{TimeoutTriggered: timeoutTriggered, ProactiveTriggered: proactiveTriggered, SilentReplyFrequency: silent}
	}

	if runtime.HasPendingMessages() {
		// 消息、timeout、主动任务都可能带走消息缓存，不能绕过当前批次的防抖等待
		runtime.WaitForMessageQuietPeriod(ctx)
	}
	if messageTriggered {
		runtime.MarkMessageTurnUnscheduled()
	}

	var cached []ContextMessage
	if runtime.HasPendingMessages() {
		cached = runtime.CollectPendingMessages()
	}
	continuationLogicalTurnID := ""

	switch {
	case len(cached) > 0:
		runtime.EnterRunningState()
		runtime.UpdateStageStatus("消息整理", fmt.Sprintf("待处理消息 %d 条", len(cached)))
		if runtime.HasPendingWaitToolCall() {
			waitMessage := e.BuildWaitCompletedMessage(true)
			continuationLogicalTurnID = waitMessage.LogicalTurnID
			runtime.AppendHistory(waitMessage)
		}
		e.ingestMessages(cached)
		last := cached[len(cached)-1]
		return TurnStartContext{
			CachedMessages:       cached,
			TriggerMessage:       &last,
			TimeoutTriggered:     timeoutTriggered,
			ProactiveTriggered:   proactiveTriggered,
			SilentReplyFrequency: silent,
			LogicalTurnID:        continuationLogicalTurnID,
		}
	case proactiveTriggered:
		trigger := runtime.consumeProactiveTriggerMessage()
		proactiveLogicalTurnID := runtime.consumeProactiveLogicalTurnID()
		if trigger == nil {
			zap.L().Warn(runtime.logPrefix + " 主动触发缺少对应的触发消息，跳过本轮")
			return TurnStartContext{TimeoutTriggered: timeoutTriggered, ProactiveTriggered: proactiveTriggered, SilentReplyFrequency: silent}
		}
		runtime.MarkMessageTurnUnscheduled()
		// 主动任务排队后，上一轮仍可能进入 wait；消费时必须再次恢复运行状态
		runtime.EnterRunningState()
		// 触发消息不并入上下文；改为回填真实用户消息作为可回复引用目标
		// 对齐 Python src/maisaka/runtime.py:375-446 与 reasoning_engine.py:827-843
		runtime.RestoreProactiveUserContext(ctx)
		runtime.emitFlowStep("turn.proactive", "主动思考触发", "按已安排的主动触发继续判断当前对话。", 0)
		if runtime.HasPendingWaitToolCall() {
			waitMessage := e.BuildWaitCompletedMessage(false)
			continuationLogicalTurnID = waitMessage.LogicalTurnID
			runtime.AppendHistory(waitMessage)
		} else if proactiveLogicalTurnID != "" {
			continuationLogicalTurnID = proactiveLogicalTurnID
		}
		return TurnStartContext{
			TriggerMessage:       trigger,
			TimeoutTriggered:     timeoutTriggered,
			ProactiveTriggered:   proactiveTriggered,
			SilentReplyFrequency: silent,
			LogicalTurnID:        continuationLogicalTurnID,
		}
	case timeoutTriggered && runtime.HasPendingWaitToolCall():
		runtime.mu.Lock()
		var trigger *ContextMessage
		if len(runtime.messageCache) > 0 {
			last := runtime.messageCache[len(runtime.messageCache)-1]
			trigger = &last
		}
		runtime.mu.Unlock()
		if trigger == nil {
			zap.L().Warn(runtime.logPrefix + " wait 超时后没有可复用的触发消息，跳过本轮")
			return TurnStartContext{TimeoutTriggered: timeoutTriggered, ProactiveTriggered: proactiveTriggered, SilentReplyFrequency: silent}
		}
		zap.L().Info(runtime.logPrefix + " 等待结束，再看一眼！")
		waitMessage := e.BuildWaitCompletedMessage(false)
		continuationLogicalTurnID = waitMessage.LogicalTurnID
		runtime.AppendHistory(waitMessage)
		return TurnStartContext{
			TriggerMessage:       trigger,
			TimeoutTriggered:     timeoutTriggered,
			ProactiveTriggered:   proactiveTriggered,
			SilentReplyFrequency: silent,
			LogicalTurnID:        continuationLogicalTurnID,
		}
	default:
		zap.L().Debug(runtime.logPrefix + " 消息已由当前内部轮次处理，忽略过期的轮次触发")
		return TurnStartContext{TimeoutTriggered: timeoutTriggered, ProactiveTriggered: proactiveTriggered, SilentReplyFrequency: silent}
	}
}

func (e *ReasoningEngine) drainReadyTurnTriggers(queued TurnTriggerSource) (message, timeout, proactive bool) {
	runtime := e.runtime
	message = queued == TriggerMessage
	timeout = queued == TriggerTimeout
	proactive = queued == TriggerProactive
	for {
		select {
		case next := <-runtime.internalTurnQueue:
			switch next {
			case TriggerMessage:
				message = true
			case TriggerTimeout:
				timeout = true
			case TriggerProactive:
				proactive = true
			}
		default:
			return
		}
	}
}

// BuildWaitCompletedMessage 构造 wait 完成后的工具结果消息
func (e *ReasoningEngine) BuildWaitCompletedMessage(hasNewMessages bool) ContextMessage {
	afterReply := e.runtime.PendingWaitAfterReply()
	toolCallID, logicalTurnID, elapsed, requested, _ := e.runtime.ConsumePendingWaitState()
	waitState, _ := json.Marshal(map[string]any{
		"elapsed_seconds":   roundTo(elapsed, 1),
		"requested_seconds": requested,
		"has_new_messages":  hasNewMessages,
	})
	content := e.renderPlannerPrompt("planner_wait_completed", map[string]string{
		"wait_state": string(waitState),
	}, "等待结束，请基于现有上下文继续思考。")
	if afterReply {
		return ContextMessage{
			Role:          RoleReference,
			Content:       content,
			Timestamp:     nowTime(),
			Source:        SourceReference,
			LogicalTurnID: logicalTurnID,
			Metadata: map[string]any{
				"display_prefix": "[回复后等待结束]",
				"remaining_uses": nil,
			},
		}
	}
	return ContextMessage{
		Role:          RoleTool,
		Content:       content,
		ToolCallID:    toolCallID,
		ToolName:      "wait",
		Timestamp:     nowTime(),
		Source:        SourceToolResult,
		LogicalTurnID: logicalTurnID,
	}
}

func (e *ReasoningEngine) handleSilentTurn(ctx context.Context, startContext TurnStartContext) {
	runtime := e.runtime
	runtime.ClearForcedTurnState()
	if startContext.ProactiveTriggered {
		runtime.consumeProactiveTriggerMessage()
		runtime.consumeProactiveLogicalTurnID()
	}
	e.PostProcessChatHistoryAfterCycle(ctx, CycleDetail{CycleID: runtime.cycleCounter}, false, true)
	runtime.EnterStopState()
	silentReason := "回复频率为 0"
	if !runtime.IsGroupSession() {
		silentReason = "私聊回复已暂停"
	}
	runtime.UpdateStageStatus("空闲", silentReason+"，已静默接收消息")

	labels := make([]string, 0, 3)
	if len(startContext.CachedMessages) > 0 {
		labels = append(labels, fmt.Sprintf("消息=%d", len(startContext.CachedMessages)))
	}
	if startContext.TimeoutTriggered {
		labels = append(labels, "wait_timeout")
	}
	if startContext.ProactiveTriggered {
		labels = append(labels, "proactive")
	}
	triggerText := strings.Join(labels, " ")
	if triggerText == "" {
		triggerText = "无新消息"
	}
	zap.L().Info(fmt.Sprintf("%s %s，静默接收并完成历史维护，不进入 Planner；%s", runtime.logPrefix, silentReason, triggerText))
	runtime.emitFlowStep("turn.silent", "本轮静默处理", silentReason+"，已接收消息并维护历史，不进入思考。", 0)
}

func (e *ReasoningEngine) ingestPendingMessages(ctx context.Context) []ContextMessage {
	runtime := e.runtime
	if !runtime.HasPendingMessages() {
		return nil
	}
	runtime.WaitForMessageQuietPeriod(ctx)
	cached := runtime.CollectPendingMessages()
	if len(cached) == 0 {
		return nil
	}
	runtime.EnterRunningState()
	e.ingestMessages(cached)
	return cached
}

func (e *ReasoningEngine) ingestMessages(messages []ContextMessage) {
	runtime := e.runtime
	for _, message := range messages {
		history := message
		history.CountInContext = true
		history.Source = firstNonEmpty(history.Source, SourceUser)
		runtime.AppendHistory(history)
	}
}

func (e *ReasoningEngine) startCycleRound(roundIndex int) (CycleDetail, string) {
	runtime := e.runtime
	runtime.mu.Lock()
	runtime.cycleCounter++
	cycleID := runtime.cycleCounter
	runtime.mu.Unlock()
	roundText := fmt.Sprintf("第 %d 轮思考", roundIndex+1)
	return CycleDetail{
		CycleID:     cycleID,
		StartedAt:   time.Now(),
		RoundText:   roundText,
		TriggerKind: string(TriggerMessage),
		LogicalTurn: e.activeLogicalTurnID,
	}, roundText
}

func (e *ReasoningEngine) runPlannerRequest(ctx context.Context, trigger *ContextMessage, source []ContextMessage, roundIndex int, roundText string, state *CycleRuntimeState) (ChatResponse, error) {
	runtime := e.runtime
	runtime.UpdateStageStatus("Planner", roundText)
	tools, deferredNotice, err := e.buildActionToolDefinitions(ctx)
	if err != nil {
		return ChatResponse{}, err
	}
	state.ActionToolCount = len(tools)
	injected := e.buildPlannerInjectedUserMessages(ctx, trigger, source, deferredNotice, roundText)
	state.ReplyTargetMessage = lastReplyTargetMessage(runtime.History(), runtime.SessionID())
	finalReminder := e.plannerFinalUserReminder()
	loop := e.chatLoop()
	interrupt := make(chan struct{})
	controller := runtime.PlannerInterrupt()
	controller.Bind(interrupt)
	defer controller.Unbind(interrupt)
	if setter, ok := loop.(interface{ SetInterruptFlag(<-chan struct{}) }); ok {
		setter.SetInterruptFlag(interrupt)
		defer setter.SetInterruptFlag(nil)
	}
	if service, ok := loop.(*ChatLoopService); ok && service != nil {
		service.LogicalTurnID = e.activeLogicalTurnID
		defer func() { service.LogicalTurnID = "" }()
	}
	// 覆盖整理上下文到绑定中断标记之间收到消息的窗口
	if runtime.PlannerMessageSteering() && runtime.HasPendingMessages() {
		controller.Request()
	}
	return loop.ChatLoopStep(ctx, runtime.History(), "planner", runtime.MaxContextSize(), tools, "", finalReminder, injected, nil)
}

func (e *ReasoningEngine) buildActionToolDefinitions(ctx context.Context) ([]ToolSpec, string, error) {
	runtime := e.runtime
	if runtime.opts.ToolSearchTailLoading != nil {
		runtime.SetToolSearchTailLoading(runtime.opts.ToolSearchTailLoading())
	}
	if runtime.opts.Tools == nil {
		runtime.UpdateDeferredToolSpecs(nil)
		runtime.SetCurrentActionTools(nil)
		return nil, "", nil
	}
	tools, err := runtime.opts.Tools.AvailableTools(ctx)
	if err != nil {
		return nil, "", err
	}
	visibleBuiltin := make([]ToolSpec, 0, len(tools))
	deferredSpecs := make([]ToolSpec, 0, len(tools))
	passiveReplyer := e.PassiveReplyerEnabled()
	for _, spec := range tools {
		if e.turnLightweightPrivate {
			if spec.Provider != BuiltinProviderName || spec.Name == "read_tool_result" {
				// 扩展工具按需发现；原始结果回查供联网工具返回 result_id 后继续读取。
				deferredSpecs = append(deferredSpecs, spec)
			} else if IsLightweightTool(spec.Name) {
				visibleBuiltin = append(visibleBuiltin, spec)
			}
			continue
		}
		if spec.Provider == BuiltinProviderName {
			if !IsBuiltinToolInActionStage(spec, passiveReplyer) {
				continue
			}
			switch GetBuiltinToolVisibility(spec) {
			case BuiltinVisible:
				visibleBuiltin = append(visibleBuiltin, spec)
			case BuiltinDeferred:
				deferredSpecs = append(deferredSpecs, spec)
			}
			continue
		}
		if visibility, _ := spec.Metadata["visibility"].(string); strings.EqualFold(strings.TrimSpace(visibility), "visible") {
			visibleBuiltin = append(visibleBuiltin, spec)
			continue
		}
		deferredSpecs = append(deferredSpecs, spec)
	}
	runtime.UpdateDeferredToolSpecs(deferredSpecs)
	selectedHistory := runtime.History()
	if loop, ok := runtime.opts.ChatLoop.(*ChatLoopService); ok && loop != nil {
		if selected, _ := loop.SelectLLMContextMessages(runtime.History(), "planner", runtime.MaxContextSize()); len(selected) > 0 {
			selectedHistory = selected
		}
	}
	runtime.SyncDiscoveredDeferredToolsWithContext(selectedHistory)
	discoveredSpecs := runtime.DiscoveredDeferredToolSpecs()
	activeSpecs := make([]ToolSpec, 0, len(visibleBuiltin)+len(discoveredSpecs))
	activeSpecs = append(activeSpecs, visibleBuiltin...)
	activeSpecs = append(activeSpecs, discoveredSpecs...)
	runtime.SetCurrentActionTools(activeSpecs)

	definitions := visibleBuiltin
	if e.turnLightweightPrivate {
		definitions = BuildLightweightToolSpecs(visibleBuiltin)
	}
	if !runtime.ToolSearchTailLoading() {
		definitions = append(append([]ToolSpec(nil), definitions...), discoveredSpecs...)
	}
	return definitions, runtime.BuildDeferredToolsReminder(), nil
}

func (e *ReasoningEngine) buildPlannerInjectedUserMessages(ctx context.Context, trigger *ContextMessage, source []ContextMessage, deferredNotice, roundText string) []string {
	injected := make([]string, 0, 6)
	if deferredNotice != "" {
		injected = append(injected, deferredNotice)
	}
	if e.turnLightweightPrivate {
		return injected
	}
	if hook := e.runtime.opts.PlannerExtraInjections; hook != nil {
		for _, extra := range hook(ctx, trigger, source) {
			if strings.TrimSpace(extra) != "" {
				injected = append(injected, extra)
			}
		}
	}
	if pending := e.runtime.PendingMessageCount(); pending > 0 {
		injected = append(injected, fmt.Sprintf("本轮开始后又有 %d 条新消息，已经并入上下文。", pending))
	}
	return injected
}

// plannerFinalUserReminder 选择当前 Planner 请求的末尾提醒
func (e *ReasoningEngine) plannerFinalUserReminder() string {
	if loop, ok := e.chatLoop().(*ChatLoopService); ok && loop != nil {
		return loop.BuildPlannerFinalUserReminder()
	}
	if e.runtime.FocusModeActive() {
		return fmt.Sprintf(FocusPlannerFinalUserReminderTemplate, "")
	}
	return PlannerFinalUserReminder
}

func (e *ReasoningEngine) chatLoop() ChatLoopRunner {
	if loop, ok := e.runtime.opts.ChatLoop.(ChatLoopRunner); ok && loop != nil {
		return loop
	}
	return defaultChatLoop{e.runtime}
}

// renderPlannerPrompt 读取并渲染 Planner 指令模板；缺失时使用兼容文案
func (e *ReasoningEngine) renderPlannerPrompt(name string, vars map[string]string, fallback string) string {
	source := e.runtime.opts.Prompts
	if source == nil {
		return fallback
	}
	if renderer, ok := source.(PromptRenderer); ok {
		if text, err := renderer.Render(name, vars); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	if text, err := source.Get(name); err == nil && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	return fallback
}

// GetPlannerContent 只提取 Planner 正文，不将 Provider 原生 reasoning 传给工具和 replyer
func GetPlannerContent(response ChatResponse) string {
	return strings.TrimSpace(response.Content)
}

func cycleEndForPauseTool(pauseToolName string) CycleEnd {
	switch pauseToolName {
	case StopAfterExecutionPauseReason:
		return CycleEnd{Reason: CycleEndToolStopAfterExec, Detail: "插件工具请求在当前批次执行完成后结束 Planner，并等待新消息。"}
	case "wait:end":
		return CycleEnd{Reason: CycleEndPlannerNoTool, Detail: "暂时不行动，等待新消息"}
	case "wait":
		return CycleEnd{Reason: CycleEndToolPauseWait, Detail: "Planner 调用 wait，本轮暂停并在等待结束后继续判断。"}
	case "reply:wait:end":
		return CycleEnd{Reason: CycleEndPlannerNoTool, Detail: "回复已发送，after_reply=wait 未指定时间，结束本轮思考。"}
	case "reply:wait":
		return CycleEnd{Reason: CycleEndToolPauseWait, Detail: "回复已发送，after_reply=wait 等待后继续判断。"}
	case "":
		return CycleEnd{Reason: CycleEndToolPause, Detail: "工具要求暂停当前思考循环。"}
	default:
		return CycleEnd{Reason: CycleEndToolPause + ":" + pauseToolName, Detail: fmt.Sprintf("工具 %s 要求暂停当前思考循环。", pauseToolName)}
	}
}

// handlePlannerResponseActions 处理 Planner 响应中的工具调用，或无工具输出策略
func (e *ReasoningEngine) handlePlannerResponseActions(ctx context.Context, response ChatResponse, cycleDetail CycleDetail, state *CycleRuntimeState) bool {
	runtime := e.runtime
	plannerContent := GetPlannerContent(response)
	// 新消息到达时必须先穿过防抖；旧的等待/静默决策不能盖过已就绪的新输入。
	waitingDecision := len(response.ToolCalls) == 0 && !(e.PassiveReplyerEnabled() && plannerContent != "")
	if !waitingDecision {
		for _, call := range response.ToolCalls {
			if call.Name == "wait" {
				waitingDecision = true
				break
			}
		}
	}
	if waitingDecision && runtime.HasPendingMessages() {
		runtime.WaitForMessageQuietPeriod(ctx)
		if runtime.HasPendingMessages() {
			reminder := e.renderPlannerPrompt("planner_replan", nil, "有新消息，请根据新消息内容重新思考后规划新的行动")
			runtime.AppendHistory(ContextMessage{
				Role:          RoleUser,
				Content:       reminder,
				Timestamp:     nowTime(),
				SessionID:     runtime.SessionID(),
				LogicalTurnID: e.activeLogicalTurnID,
			})
			state.CycleEnd = CycleEnd{Reason: "tool_continue", Detail: "新消息已就绪，跳过等待决策并重新规划。"}
			return false
		}
	}
	if len(response.ToolCalls) > 0 && e.shouldReplaceReasoning(plannerContent) {
		plannerContent = "我应该根据我上面思考的内容进行反思，重新思考我下一步的行动，我需要分析当前场景，对话，然后直接输出我的想法："
		response.Content = plannerContent
		zap.L().Info(runtime.logPrefix + " 当前思考与上一轮过于相似，已替换为重新思考提示")
	}
	e.lastReasoningContent = plannerContent
	state.LastThought = plannerContent
	// 无参数 wait 是终止信号：不写入工具调用，也不执行同批其它工具
	if hasTerminalWaitToolCall(response.ToolCalls) {
		e.endPlannerNoToolCycle(state, "状态：已结束本轮思考")
		state.CycleEnd = cycleEndForPauseTool("wait:end")
		state.ToolResultSummaries = nil
		state.ToolMonitorResults = nil
		runtime.emitPlannerProgress(cycleDetail, state, nil, "")
		return true
	}
	for _, message := range response.RawMessages {
		if message.SessionID == "" {
			message.SessionID = runtime.sessionID
		}
		if message.Timestamp.IsZero() {
			message.Timestamp = nowTime()
		}
		message.LogicalTurnID = firstNonEmpty(message.LogicalTurnID, e.activeLogicalTurnID)
		runtime.AppendHistory(message)
	}

	if len(response.ToolCalls) > 0 {
		shouldPause, pauseTool, summaries, monitors := e.handleToolCalls(ctx, response.ToolCalls, plannerContent, cycleDetail, state)
		state.ToolResultSummaries = summaries
		state.ToolMonitorResults = monitors
		if shouldPause {
			state.CycleEnd = cycleEndForPauseTool(pauseTool)
			return true
		}
		state.CycleEnd = CycleEnd{Reason: "tool_continue", Detail: "Planner 工具执行完成，继续下一轮内部思考。"}
		return false
	}

	if e.PassiveReplyerEnabled() && plannerContent != "" {
		e.replyFromPlannerContent(ctx, plannerContent, cycleDetail, state)
		e.endPlannerNoToolCycle(state, fmt.Sprintf("状态：%s", state.CycleEnd.Detail))
		return true
	}
	if e.PassiveReplyerEnabled() {
		zap.L().Warn(runtime.logPrefix + " Planner 未返回正文或工具调用，不启动回复")
	}
	e.endPlannerNoToolCycle(state, "状态：已结束本轮思考")
	zap.L().Info(runtime.logPrefix + " Planner 已结束本轮思考")
	state.CycleEnd = CycleEnd{Reason: CycleEndPlannerNoTool, Detail: "Planner本轮思考结束。"}
	state.ToolResultSummaries = nil
	state.ToolMonitorResults = nil
	return true
}

func hasTerminalWaitToolCall(toolCalls []ToolCall) bool {
	for _, call := range toolCalls {
		if strings.TrimSpace(call.Name) == "wait" && len(call.Arguments) == 0 {
			return true
		}
	}
	return false
}

// EndPlannerNoToolCycle 供无工具输出或终止型 wait 复用收尾逻辑
func (e *ReasoningEngine) EndPlannerNoToolCycle(statusLine string) {
	e.endPlannerNoToolCycle(nil, statusLine)
}

func (e *ReasoningEngine) endPlannerNoToolCycle(state *CycleRuntimeState, statusLine string) {
	runtime := e.runtime
	runtime.EndPlannerContinuation()
	runtime.resetConsecutiveWaitCount(CycleEndPlannerNoTool)
	runtime.EnterStopState()
	state.PlannerExtraLines = append(state.PlannerExtraLines, statusLine)
}

func (e *ReasoningEngine) replyFromPlannerContent(ctx context.Context, content string, cycleDetail CycleDetail, state *CycleRuntimeState) {
	runtime := e.runtime
	started := time.Now()
	executor := runtime.opts.Reply
	if executor == nil {
		state.CycleEnd = CycleEnd{Reason: "planner_reply_failed", Detail: "回复失败：缺少回复执行端口。"}
		return
	}
	runtime.UpdateStageStatus("Replyer", "根据 Planner 正文生成可见回复")
	result := executor.ReplyFromContent(ctx, content, state.ReplyTargetMessage, plannerReplyArguments(content, state.ReplyTargetMessage))
	summary := result.Content
	state.ToolResultSummaries = []string{summary}
	state.ToolMonitorResults = []map[string]any{{
		"tool_name":   "reply",
		"success":     result.Success,
		"summary":     summary,
		"duration_ms": time.Since(started).Milliseconds(),
	}}
	if result.Success {
		state.CycleEnd = CycleEnd{Reason: "planner_reply_end", Detail: "回复完成，已结束本轮思考。"}
		return
	}
	zap.L().Error(runtime.logPrefix + " Planner 正文触发回复失败：" + summary)
	state.CycleEnd = CycleEnd{Reason: "planner_reply_failed", Detail: "回复失败，已结束本轮思考：" + summary}
}

// appendDeferredToolParameterHint 给未展开工具的失败结果补充参数查看提示
func appendDeferredToolParameterHint(result ToolExecutionResult) ToolExecutionResult {
	const hint = "请通过 tool_search 查看具体的工具参数后再重试。"
	if result.Success {
		return result
	}
	if strings.TrimSpace(result.Message) != "" {
		if !strings.Contains(result.Message, hint) {
			result.Message += "\n" + hint
		}
		return result
	}
	if strings.TrimSpace(result.Content) != "" {
		if !strings.Contains(result.Content, hint) {
			result.Content += "\n" + hint
		}
		return result
	}
	result.Message = hint
	return result
}

// handleToolCalls 执行一批统一工具调用
func (e *ReasoningEngine) handleToolCalls(ctx context.Context, toolCalls []ToolCall, latestThought string, cycleDetail CycleDetail, state *CycleRuntimeState) (bool, string, []string, []map[string]any) {
	runtime := e.runtime
	summaries := make([]string, 0, len(toolCalls))
	monitorResults := make([]map[string]any, 0, len(toolCalls))
	shouldStopAfterExecution := false
	deferredPostHistory := make([]ContextMessage, 0, len(toolCalls))
	// 同一批工具调用共用本轮 trace，供预览记录互相索引
	traceCtx := reasoningtrace.WithTraceID(ctx, reasoningtrace.NewTraceID())
	replyPause := ""
	var replyWaitSeconds *int
	replyPauseCallID := ""

	for index, call := range toolCalls {
		e.logToolCallSource(call, index+1, len(toolCalls))
		invocation := ToolInvocation{
			ToolName:      call.Name,
			Arguments:     call.Arguments,
			CallID:        call.CallID,
			LogicalTurnID: e.activeLogicalTurnID,
			LatestThought: latestThought,
			SessionID:     runtime.sessionID,
			StreamID:      runtime.sessionID,
		}
		runtime.UpdateStageStatus("工具执行 · "+call.Name, fmt.Sprintf("第 %d/%d 个工具", index+1, len(toolCalls)))
		started := time.Now()
		result := ToolExecutionResult{Success: false, Content: "统一工具注册表尚未初始化。"}
		isUnexpandedTool := !runtime.IsActionToolCurrentlyAvailable(call.Name)
		if replyPause != "" {
			// 同批调用都必须有回执，但回复要求结束后不能再执行其余工具
			result = ToolExecutionResult{
				ToolName: call.Name,
				Success:  false,
				Message:  fmt.Sprintf("未执行：前面的 reply 已选择 after_reply=%s。", replyPause),
			}
		} else if e.PassiveReplyerEnabled() && call.Name == "reply" {
			result = ToolExecutionResult{
				ToolName: "reply",
				Success:  false,
				Message:  "被动式 replyer 不调用 reply；决定发言时请直接输出回复指导，不要调用工具。",
			}
		} else if e.turnLightweightPrivate && isUnexpandedTool {
			result = ToolExecutionResult{
				ToolName: call.Name,
				Success:  false,
				Message:  "轻量模式未提供该工具；请使用当前可用工具或输出回复指导。",
			}
		} else if runtime.opts.Tools != nil {
			result = e.invokeTool(traceCtx, invocation)
		}
		if isUnexpandedTool && !result.Success && (!e.turnLightweightPrivate || runtime.HasDeferredToolSpec(call.Name)) {
			result = appendDeferredToolParameterHint(result)
		}
		duration := time.Since(started).Milliseconds()
		if call.Name != "wait" {
			runtime.resetConsecutiveWaitCount("tool:" + call.Name)
		}
		if runtime.opts.Tools != nil {
			runtime.opts.Tools.AfterExecution(traceCtx, invocation, result)
		}
		// 对齐 Python reasoning_engine.py:2125 先落库工具记录并处理副作用，再整理与追加历史
		e.recordToolExecution(traceCtx, invocation, result, runtime.CurrentActionToolSpec(call.Name))

		condensed := ""
		rawContent := result.Content
		alreadyPreviewed, _ := result.Metadata["web_result_preview"].(bool)
		if !e.turnLightweightPrivate && runtime.ShouldCondense(call.Name, rawContent, result.Success, alreadyPreviewed) {
			runtime.UpdateStageStatus("工具结果整理 · "+call.Name, fmt.Sprintf("原始结果 %d 字", len([]rune(rawContent))))
			condensed = CondenseToolResult(ctx, runtime, call, rawContent, latestThought)
		}
		e.AppendToolExecutionResult(call, result, condensed, false)
		deferredPostHistory = append(deferredPostHistory, result.AppendPostHistory...)
		summary := BuildToolResultSummary(call, result, condensed)
		e.appendToolResultMediaMessages(call, result)
		summaries = append(summaries, summary)
		monitorResults = append(monitorResults, buildToolMonitorResult(call, invocation, result, runtime.CurrentActionToolSpec(call.Name), float64(duration), summary))
		nextID := nextCallID(toolCalls, index)
		if replyPause != "" {
			nextID = ""
		}
		runtime.emitPlannerProgress(cycleDetail, state, monitorResults, nextID)

		if result.Success && call.Name == "reply" && !e.PassiveReplyerEnabled() {
			afterReply := "continue"
			if value, ok := result.Metadata["after_reply"].(string); ok && strings.TrimSpace(value) != "" {
				afterReply = strings.TrimSpace(value)
			}
			if afterReply == "wait" {
				replyPause = afterReply
				replyPauseCallID = call.CallID
				switch seconds := result.Metadata["wait_seconds"].(type) {
				case int:
					value := seconds
					replyWaitSeconds = &value
				case int64:
					value := int(seconds)
					replyWaitSeconds = &value
				case float64:
					value := int(seconds)
					replyWaitSeconds = &value
				}
			}
		}
		if !result.Success && call.Name == "reply" {
			zap.L().Warn(runtime.logPrefix + " 回复工具未生成可见消息，将继续下一轮循环")
		}
		if result.Success && result.StopAfterExecution {
			shouldStopAfterExecution = true
		}
		// 对齐 Python：pause_execution 立即暂停本批，stop_after_execution 等整批结束
		if pauseExecution, ok := result.Metadata["pause_execution"].(bool); ok && pauseExecution {
			e.appendToolPostHistoryMessages(deferredPostHistory)
			return true, call.Name, summaries, monitorResults
		}
	}
	e.appendToolPostHistoryMessages(deferredPostHistory)
	if replyPause != "" {
		if replyWaitSeconds == nil {
			runtime.EndPlannerContinuation()
			runtime.EnterStopState()
			return true, "reply:wait:end", summaries, monitorResults
		}
		seconds := float64(*replyWaitSeconds)
		if _, err := runtime.EnterCountedWaitState(&seconds, replyPauseCallID, e.activeLogicalTurnID, true); err != nil {
			zap.L().Warn(runtime.logPrefix + " 进入 reply 等待状态失败: " + err.Error())
		}
		return true, "reply:wait", summaries, monitorResults
	}
	if shouldStopAfterExecution {
		runtime.EndPlannerContinuation()
		runtime.resetConsecutiveWaitCount(CycleEndToolStopAfterExec)
		runtime.EnterStopState()
		return true, StopAfterExecutionPauseReason, summaries, monitorResults
	}
	return false, "", summaries, monitorResults
}

// appendToolResultMediaMessages 把工具结果中的媒体项拆成紧跟其后的 user 上下文消息
func (e *ReasoningEngine) appendToolResultMediaMessages(call ToolCall, result ToolExecutionResult) {
	if len(result.Items) == 0 {
		return
	}
	callID := toolResultMediaCallID(call)
	for index, item := range result.Items {
		mediaType := strings.ToLower(strings.TrimSpace(item.Type))
		switch mediaType {
		case "image", "audio", "resource_link", "resource", "binary", "emoji", "media", "picture":
		default:
			continue
		}
		if strings.TrimSpace(item.Data) == "" && strings.TrimSpace(item.Text) == "" && strings.TrimSpace(item.MIMEType) == "" &&
			toolResultMediaMetadataString(item.Metadata, "uri") == "" && toolResultMediaMetadataString(item.Metadata, "name") == "" &&
			toolResultMediaMetadataString(item.Metadata, "description") == "" {
			continue
		}
		position := index + 1
		mediaIndex := fmt.Sprintf("tool_result:%s:%d", callID, position)
		message := ContextMessage{
			Role:           RoleUser,
			Content:        e.buildToolResultMediaVisibleText(mediaIndex, mediaType, item),
			Source:         SourceReference,
			MessageID:      mediaIndex,
			MediaIndex:     mediaIndex,
			ToolCallID:     call.CallID,
			ToolName:       call.Name,
			SessionID:      e.runtime.SessionID(),
			Timestamp:      time.Now(),
			CountInContext: false,
			Metadata: map[string]any{
				"kind":         ToolResultMediaKind,
				"tool_call_id": call.CallID,
				"tool_name":    call.Name,
				"media_type":   mediaType,
			},
		}
		if mediaType == "image" || (mediaType == "binary" && strings.HasPrefix(strings.ToLower(item.MIMEType), "image/")) {
			if data := strings.TrimSpace(item.Data); data != "" {
				message.Images = []ImageComponent{{
					MediaRef:    data,
					Format:      strings.TrimSpace(item.MIMEType),
					Description: strings.TrimSpace(item.Text),
				}}
			}
		}
		if mediaType == "emoji" {
			if data := strings.TrimSpace(item.Data); data != "" {
				message.Emojis = []ImageComponent{{
					MediaRef:    data,
					Format:      strings.TrimSpace(item.MIMEType),
					Description: strings.TrimSpace(item.Text),
					Placeholder: "[表情包]",
				}}
			}
		}
		if len(message.Images) > 0 {
			e.runtime.ScheduleToolResultMediaImageRecognition(message.Images, mediaIndex)
		}
		if len(message.Emojis) > 0 {
			e.runtime.ScheduleToolResultMediaEmojiRecognition(message.Emojis, mediaIndex)
		}
		e.runtime.AppendHistory(message)
	}
}

// toolResultMediaTypes 对齐 Python TOOL_RESULT_MEDIA_TYPES
var toolResultMediaTypes = map[string]struct{}{
	"image":         {},
	"audio":         {},
	"resource_link": {},
	"resource":      {},
	"binary":        {},
}

type toolResultMediaPosition struct {
	position int
	item     ToolResultItem
}

// collectToolResultMediaItems 对齐 Python _iter_tool_result_media_items
func collectToolResultMediaItems(result ToolExecutionResult) []toolResultMediaPosition {
	if len(result.Items) == 0 {
		return nil
	}
	items := make([]toolResultMediaPosition, 0, len(result.Items))
	for index, item := range result.Items {
		if _, ok := toolResultMediaTypes[strings.TrimSpace(item.Type)]; !ok {
			continue
		}
		if strings.TrimSpace(item.Data) == "" && strings.TrimSpace(item.MIMEType) == "" &&
			toolResultMediaMetadataString(item.Metadata, "uri") == "" &&
			toolResultMediaMetadataString(item.Metadata, "name") == "" &&
			toolResultMediaMetadataString(item.Metadata, "description") == "" {
			continue
		}
		items = append(items, toolResultMediaPosition{position: index + 1, item: item})
	}
	return items
}

func toolResultMediaCallID(call ToolCall) string {
	callID := strings.TrimSpace(call.CallID)
	if callID == "" {
		callID = firstNonEmpty(strings.TrimSpace(call.Name), "tool")
	}
	return callID
}

func toolResultMediaIndex(call ToolCall, position int) string {
	return fmt.Sprintf("tool_result:%s:%d", toolResultMediaCallID(call), position)
}

// buildToolResultMediaAttrs 对齐 Python _build_tool_result_media_xml_attrs
func buildToolResultMediaAttrs(call ToolCall, position int, item ToolResultItem) string {
	contentType := strings.TrimSpace(item.Type)
	if contentType == "" {
		contentType = "unknown"
	}
	values := [][2]string{
		{"msg_id", toolResultMediaIndex(call, position)},
		{"type", contentType},
		{"mime", strings.TrimSpace(item.MIMEType)},
		{"name", toolResultMediaMetadataString(item.Metadata, "name")},
		{"context_key", toolResultMediaMetadataString(item.Metadata, "context_key")},
		{"source_url", toolResultMediaMetadataString(item.Metadata, "source_url")},
	}
	parts := make([]string, 0, len(values))
	for _, pair := range values {
		if pair[1] == "" {
			continue
		}
		parts = append(parts, pair[0]+`="`+escapeMediaAttribute(pair[1])+`"`)
	}
	return strings.Join(parts, " ")
}

// buildToolResultHistoryContent 对齐 Python _build_tool_result_history_content
func buildToolResultHistoryContent(call ToolCall, result ToolExecutionResult, content string) string {
	mediaItems := collectToolResultMediaItems(result)
	if len(mediaItems) == 0 {
		return content
	}
	lines := make([]string, 0, len(mediaItems)+2)
	lines = append(lines, "<tool_result_media_list>")
	for _, media := range mediaItems {
		attrs := buildToolResultMediaAttrs(call, media.position, media.item)
		if attrs != "" {
			lines = append(lines, "  <media "+attrs+" />")
		} else {
			lines = append(lines, "  <media />")
		}
	}
	lines = append(lines, "</tool_result_media_list>")
	block := strings.Join(lines, "\n")
	if trimmed := strings.TrimSpace(content); trimmed != "" {
		return trimmed + "\n\n" + block
	}
	return block
}

// buildToolResultMediaVisibleText 构造工具媒体消息的可读正文
func (e *ReasoningEngine) buildToolResultMediaVisibleText(mediaIndex string, mediaType string, item ToolResultItem) string {
	parts := []string{fmt.Sprintf(`<tool_result_media msg_id="%s" />`, escapeMediaAttribute(mediaIndex))}
	if description := describeToolResultMediaItem(item); description != "" {
		parts = append(parts, description)
	}
	if mediaType == "image" || (mediaType == "binary" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.MIMEType)), "image/")) {
		parts = append(parts, "[图片]")
	}
	return strings.Join(parts, "\n")
}

// describeToolResultMediaItem 对齐 Python _describe_tool_result_media_item
func describeToolResultMediaItem(item ToolResultItem) string {
	contentType := strings.TrimSpace(item.Type)
	if contentType == "" {
		contentType = "unknown"
	}
	parts := []string{contentType}
	if mimeType := strings.TrimSpace(item.MIMEType); mimeType != "" {
		parts = append(parts, mimeType)
	}
	if name := toolResultMediaMetadataString(item.Metadata, "name"); name != "" {
		parts = append(parts, name)
	}
	if contextKey := toolResultMediaMetadataString(item.Metadata, "context_key"); contextKey != "" {
		parts = append(parts, "context_key="+contextKey)
	}
	if sourceURL := toolResultMediaMetadataString(item.Metadata, "source_url"); sourceURL != "" {
		parts = append(parts, "source_url="+sourceURL)
	}
	return strings.Join(parts, " / ")
}

func toolResultMediaMetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch value.(type) {
	case map[string]any, []any:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// escapeMediaAttribute 转义工具媒体 XML 属性
func escapeMediaAttribute(value string) string {
	return html.EscapeString(strings.TrimSpace(value))
}

func (e *ReasoningEngine) invokeTool(ctx context.Context, invocation ToolInvocation) ToolExecutionResult {
	result, err := e.runtime.opts.Tools.Invoke(ctx, invocation)
	if err != nil {
		// 工具本身失败要暴露给 Planner，不伪造成功
		return ToolExecutionResult{Success: false, Content: fmt.Sprintf("工具执行失败：%v", err)}
	}
	return result
}

func nextCallID(calls []ToolCall, index int) string {
	if index+1 < len(calls) {
		return calls[index+1].CallID
	}
	return ""
}

// AppendToolExecutionResult 将统一工具执行结果写回 Maisaka 历史
// appendPostHistory=false 时由调用方在整批工具结束后统一追加，对齐 Python 的延迟写入
func (e *ReasoningEngine) AppendToolExecutionResult(call ToolCall, result ToolExecutionResult, condensedContent string, appendPostHistory bool) {
	runtime := e.runtime
	// wait 成功后会暂停循环，真正的结果由等待结束时的 BuildWaitCompletedMessage 补写
	// 对齐 Python HISTORY_DEFERRED_TOOL_RESULT_NAMES，避免同一 call_id 出现两条工具结果
	if call.Name == "wait" && result.Success && (result.PauseTool == "wait" || toolPauseExecution(result.Metadata)) {
		return
	}
	content := result.Content
	if condensedContent != "" {
		content = condensedContent
	}
	if result.Message != "" {
		if content != "" {
			content += "\n"
		}
		content += result.Message
	}
	content = buildToolResultHistoryContent(call, result, content)
	if strings.TrimSpace(content) == "" {
		if result.Success {
			content = "工具执行成功。"
		} else {
			content = "工具 " + call.Name + " 执行失败。"
		}
	}
	message := ContextMessage{
		Role:          RoleTool,
		Content:       content,
		ToolCallID:    call.CallID,
		ToolName:      call.Name,
		Timestamp:     nowTime(),
		Source:        SourceToolResult,
		LogicalTurnID: e.activeLogicalTurnID,
	}
	message.MetadataBoolSet("success", result.Success)
	runtime.AppendHistory(message)
	if !appendPostHistory {
		return
	}
	e.appendToolPostHistoryMessages(result.AppendPostHistory)
}

// appendToolPostHistoryMessages 对齐 Python _append_tool_post_history_messages：按 message_id 去重
func (e *ReasoningEngine) appendToolPostHistoryMessages(messages []ContextMessage) {
	if len(messages) == 0 {
		return
	}
	seen := map[string]struct{}{}
	for _, historyMessage := range e.runtime.History() {
		if id := strings.TrimSpace(historyMessage.MessageID); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, message := range messages {
		id := strings.TrimSpace(message.MessageID)
		if id != "" {
			if _, exists := seen[id]; exists {
				continue
			}
		}
		message.LogicalTurnID = e.activeLogicalTurnID
		e.runtime.AppendHistory(message)
		if id != "" {
			seen[id] = struct{}{}
		}
	}
}

// toolPauseExecution 判断工具结果是否要求暂停执行循环
func toolPauseExecution(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	value, ok := metadata["pause_execution"].(bool)
	return ok && value
}

// BuildToolResultSummary 构建终端和监控共用的摘要，优先展示写入 Planner 历史的整理结果
func BuildToolResultSummary(call ToolCall, result ToolExecutionResult, condensed string) string {
	content := result.Content
	if condensed != "" {
		content = condensed
	}
	content = strings.TrimSpace(content)
	// 对齐 Python _build_tool_result_summary：content 为空时回落到错误原因，
	// 否则失败工具只会显示「执行失败」，丢掉可诊断的真实原因
	if content == "" {
		content = strings.TrimSpace(result.Message)
	}
	runes := []rune(content)
	if len(runes) > 180 {
		content = string(runes[:180]) + "…"
	}
	status := "成功"
	if !result.Success {
		status = "失败"
	}
	if content == "" {
		return fmt.Sprintf("%s: 执行%s", call.Name, status)
	}
	return fmt.Sprintf("%s: %s %s", call.Name, status, content)
}

// PostProcessChatHistoryAfterCycle 每轮结束后统一执行引用消费、结构归一与上下文裁切
// 对齐 Python _post_process_chat_history_after_cycle：摘要成功才前置新摘要并替换旧摘要，
// 摘要失败仍提交裁切；快照缺失时放弃提交本批裁切，等待下一次 Planner 请求
func (e *ReasoningEngine) PostProcessChatHistoryAfterCycle(ctx context.Context, cycleDetail CycleDetail, enableCompaction, consumeReferences bool) {
	runtime := e.runtime
	originalHistory := runtime.History()
	if len(originalHistory) == 0 {
		return
	}
	revision := runtime.HistoryRevision()
	processor := runtime.opts.HistoryPostProcessor
	if processor == nil {
		zap.L().Warn(runtime.logPrefix + " 历史后处理端口未配置，跳过本轮上下文裁切")
		return
	}
	result := processor(originalHistory, HistoryPostProcessOptions{
		MaxContextSize:    runtime.MaxContextSize(),
		PendingCallIDs:    runtime.PendingWaitToolCallIDs(),
		ConsumeReferences: consumeReferences,
	})
	if result.ChangedCount <= 0 {
		return
	}
	finalHistory := result.History
	trimmedChatCount := 0
	for _, message := range result.RemovedMessages {
		if message.CountInContext {
			trimmedChatCount++
		}
	}
	if len(result.RemovedMessages) > 0 && enableCompaction {
		if runtime.opts.Compactor == nil {
			zap.L().Warn(runtime.logPrefix + " 历史压缩端口未初始化，直接裁切本批旧原文")
		} else {
			snapshot := runtime.PlannerRequestSnapshot()
			if snapshot == nil {
				// 冷启动或上次摘要提交后必须先完成一次 Planner 请求，才能复用真实前缀
				zap.L().Warn(runtime.logPrefix + " 缺少 Planner 请求快照，取消提交本批上下文裁切")
				return
			}
			summary, err := runtime.opts.Compactor.Summarize(ctx, HistoryCompactionInput{
				Removed:   result.RemovedMessages,
				Recent:    messagesWithoutContextSummary(finalHistory),
				Previous:  lastContextSummary(originalHistory),
				Snapshot:  snapshot,
				SessionID: runtime.sessionID,
				Timestamp: result.RemovedMessages[len(result.RemovedMessages)-1].Timestamp,
			})
			if err != nil {
				zap.L().Warn(runtime.logPrefix+" 上下文压缩失败，将直接裁切本批旧原文", zap.Error(err))
			} else if summary != nil {
				finalHistory = messagesWithoutContextSummary(finalHistory)
				finalHistory = append([]ContextMessage{*summary}, finalHistory...)
				zap.L().Info(fmt.Sprintf("%s 上下文压缩完成: 压缩=%d 保留原文=%d 摘要字符=%d",
					runtime.logPrefix, trimmedChatCount, result.RemainingContextCount, len([]rune(summary.Content))))
			}
		}
	}
	// /clear 或其他异步操作可能在模型请求期间改变历史，禁止用旧快照覆盖新状态
	if runtime.HistoryRevision() != revision {
		zap.L().Info(runtime.logPrefix + " 压缩期间上下文已改变，取消提交旧快照")
		return
	}
	runtime.SetHistory(finalHistory)
	if len(result.RemovedMessages) > 0 {
		runtime.ClearPlannerRequestSnapshot()
	}
	if trimmedChatCount <= 0 || runtime.opts.Learners == nil {
		return
	}
	// 只把真正被裁掉的真实聊天消息交给学习，避免重复学习完整历史
	if err := runtime.opts.Learners.TriggerTrimmedHistoryLearning(ctx, runtime.sessionID, result.RemovedMessages, nil); err != nil {
		zap.L().Warn(runtime.logPrefix+" 裁切历史学习提交失败", zap.Error(err))
	}
}

// messagesWithoutContextSummary 过滤常驻上下文摘要，供摘要请求只携带原文
func messagesWithoutContextSummary(history []ContextMessage) []ContextMessage {
	out := make([]ContextMessage, 0, len(history))
	for _, message := range history {
		if IsContextSummaryMessage(message) {
			continue
		}
		out = append(out, message)
	}
	return out
}

// lastContextSummary 取最近一条常驻上下文摘要，作为本次压缩的前置摘要
func lastContextSummary(history []ContextMessage) *ContextMessage {
	for index := len(history) - 1; index >= 0; index-- {
		if IsContextSummaryMessage(history[index]) {
			message := history[index]
			return &message
		}
	}
	return nil
}

// plannerReplyArguments 把 Planner 正文转成 reply 工具参数，供 Replyer 选择表达与目标
func plannerReplyArguments(content string, target *ContextMessage) map[string]any {
	args := map[string]any{"reply_reference": content}
	if target != nil && target.MessageID != "" {
		args["msg_id"] = target.MessageID
	}
	return args
}

// lastReplyTargetMessage 取当前会话最后一条真实用户消息，作为本轮回复引用目标
func lastReplyTargetMessage(history []ContextMessage, sessionID string) *ContextMessage {
	for i := len(history) - 1; i >= 0; i-- {
		message := history[i]
		if message.Role != RoleUser || strings.TrimSpace(message.MessageID) == "" {
			continue
		}
		if message.Source != SourceUser && message.Source != "" {
			continue
		}
		if sessionID != "" && message.SessionID != "" && message.SessionID != sessionID {
			continue
		}
		target := message
		return &target
	}
	return nil
}

func (e *ReasoningEngine) shouldReplaceReasoning(currentContent string) bool {
	previous := strings.TrimSpace(e.lastReasoningContent)
	current := strings.TrimSpace(currentContent)
	if previous == "" || current == "" {
		return false
	}
	return calculateSimilarity(previous, current) > 0.9
}

// calculateSimilarity 对齐 difflib.SequenceMatcher(None, a, b).ratio()：2*M/T
func calculateSimilarity(left, right string) float64 {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	total := len(leftRunes) + len(rightRunes)
	if total == 0 {
		return 1
	}
	b2j := buildSequenceMatcherIndex(rightRunes)
	matched := 0
	var walk func(alo, ahi, blo, bhi int)
	walk = func(alo, ahi, blo, bhi int) {
		i, j, size := findLongestMatch(leftRunes, alo, ahi, rightRunes, blo, bhi, b2j)
		if size == 0 {
			return
		}
		matched += size
		walk(alo, i, blo, j)
		walk(i+size, ahi, j+size, bhi)
	}
	walk(0, len(leftRunes), 0, len(rightRunes))
	return 2 * float64(matched) / float64(total)
}

// buildSequenceMatcherIndex 对齐 difflib b2j，含 autojunk 的 popular 元素剔除
func buildSequenceMatcherIndex(sequence []rune) map[rune][]int {
	index := make(map[rune][]int, len(sequence))
	for position, value := range sequence {
		index[value] = append(index[value], position)
	}
	if len(sequence) >= 200 {
		threshold := len(sequence)/100 + 1
		for value, positions := range index {
			if len(positions) > threshold {
				delete(index, value)
			}
		}
	}
	return index
}

// findLongestMatch 对齐 difflib find_longest_match（不延伸 junk 边界）
func findLongestMatch(a []rune, alo, ahi int, b []rune, blo, bhi int, b2j map[rune][]int) (int, int, int) {
	bestI, bestJ, bestSize := alo, blo, 0
	previous := map[int]int{}
	for i := alo; i < ahi; i++ {
		current := map[int]int{}
		for _, j := range b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			size := previous[j-1] + 1
			current[j] = size
			if size > bestSize {
				bestI, bestJ, bestSize = i-size+1, j-size+1, size
			}
		}
		previous = current
	}
	return bestI, bestJ, bestSize
}

func roundTo(value float64, places int) float64 {
	factor := math.Pow(10, float64(places))
	return math.Round(value*factor) / factor
}

// defaultChatLoop 未注入对话循环端口时的兜底：直接暴露协议错误
type defaultChatLoop struct{ runtime *Runtime }

func (d defaultChatLoop) ChatLoopStep(ctx context.Context, chatHistory []ContextMessage, requestKind string, maxContextSize int, tools []ToolSpec, systemPrompt, finalUserMessage string, injected, tail []string) (ChatResponse, error) {
	return ChatResponse{}, fmt.Errorf("Maisaka 对话循环端口未初始化")
}
