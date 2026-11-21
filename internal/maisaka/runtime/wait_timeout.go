package runtime

import (
	"fmt"
	"time"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type waitTimeoutHandle struct {
	timer      *time.Timer
	generation uint64
}

func (a *Agent) beginConversationWait(kind string, targetID int64, seconds int) {
	toolCallID := fmt.Sprintf("wait:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	logicalTurnID := fmt.Sprintf("turn:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	a.beginConversationWaitWithIDs(kind, targetID, seconds, toolCallID, logicalTurnID)
}

// beginConversationWaitWithIDs 使用 Maisaka 工具调用的真实 call_id 进入等待状态
func (a *Agent) beginConversationWaitWithIDs(kind string, targetID int64, seconds int, toolCallID, logicalTurnID string) {
	if _, err := a.beginConversationWaitCountedWithIDs(kind, targetID, seconds, toolCallID, logicalTurnID); err != nil {
		zap.L().Warn("Maisaka wait 状态进入失败", zap.String("conversation_kind", kind), zap.Int64("target_id", targetID), zap.Error(err))
	}
}

// beginConversationWaitCounted 进入等待状态并返回连续 wait 次数；供 wait 工具结构化结果使用
func (a *Agent) beginConversationWaitCounted(kind string, targetID int64, seconds int) (int, error) {
	toolCallID := fmt.Sprintf("wait:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	logicalTurnID := fmt.Sprintf("turn:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	return a.beginConversationWaitCountedWithIDs(kind, targetID, seconds, toolCallID, logicalTurnID)
}

// beginConversationWaitCountedWithIDs 使用真实 tool_call_id 进入计数等待状态
func (a *Agent) beginConversationWaitCountedWithIDs(kind string, targetID int64, seconds int, toolCallID, logicalTurnID string) (int, error) {
	if toolCallID == "" {
		toolCallID = fmt.Sprintf("wait:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	}
	if logicalTurnID == "" {
		logicalTurnID = fmt.Sprintf("turn:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	}
	session := a.sessions.get(kind, targetID)
	if runtime := a.attachMaisakaRuntime(kind, targetID, session); runtime != nil {
		waitSeconds := float64(seconds)
		return runtime.EnterCountedWaitState(&waitSeconds, toolCallID, logicalTurnID)
	}
	return 0, nil
}

func (a *Agent) armWaitTimeout(kind string, targetID int64, delay time.Duration, generation uint64) {
	key := chatsession.ConversationKey(kind, targetID)
	a.waitTimeoutMu.Lock()
	defer a.waitTimeoutMu.Unlock()
	if a.waitTimeouts == nil {
		a.waitTimeouts = map[string]*waitTimeoutHandle{}
	}
	if prev := a.waitTimeouts[key]; prev != nil && prev.timer != nil {
		prev.timer.Stop()
	}
	if delay <= 0 {
		delete(a.waitTimeouts, key)
		return
	}
	handle := &waitTimeoutHandle{generation: generation}
	handle.timer = time.AfterFunc(delay, func() {
		a.onWaitTimeout(kind, targetID, generation)
	})
	a.waitTimeouts[key] = handle
}

func (a *Agent) cancelWaitTimeout(kind string, targetID int64) {
	key := chatsession.ConversationKey(kind, targetID)
	a.waitTimeoutMu.Lock()
	defer a.waitTimeoutMu.Unlock()
	if a.waitTimeouts == nil {
		return
	}
	if prev := a.waitTimeouts[key]; prev != nil && prev.timer != nil {
		prev.timer.Stop()
	}
	delete(a.waitTimeouts, key)
}

func (a *Agent) cancelAllWaitTimeouts() {
	a.waitTimeoutMu.Lock()
	defer a.waitTimeoutMu.Unlock()
	for key, handle := range a.waitTimeouts {
		if handle != nil && handle.timer != nil {
			handle.timer.Stop()
		}
		delete(a.waitTimeouts, key)
	}
}

func (a *Agent) onWaitTimeout(kind string, targetID int64, generation uint64) {
	if a.stopping.Load() || a.ctx.Err() != nil {
		return
	}
	session := a.sessions.get(kind, targetID)
	hasNew := sessionHasActionableUserSinceWait(session)
	if !a.completeConversationWait(kind, targetID, generation, hasNew) {
		return
	}

	switch kind {
	case memory.ConversationKindGroup:
		if a.groupConcurrency.IsRunning(targetID) {
			return
		}
		a.groupConcurrency.Submit(targetID)
	case memory.ConversationKindPrivate:
		if a.privateConcurrency.IsRunning(targetID) {
			return
		}
		a.privateConcurrency.Submit(targetID)
	}
}

func (a *Agent) completeConversationWait(kind string, targetID int64, generation uint64, hasNew bool) bool {
	elapsed, requested, ok := a.turnSchedule.finishWait(kind, targetID, generation)
	if !ok {
		return false
	}
	a.cancelWaitTimeout(kind, targetID)
	session := a.sessions.get(kind, targetID)
	if runtime := session.MaisakaRuntime(); runtime != nil {
		callID, turnID, _, _, err := runtime.ConsumePendingWaitState()
		if err == nil {
			a.enqueueWaitToolResult(kind, targetID, session, callID, turnID, elapsed, requested, hasNew)
		}
	}
	return true
}

func sessionHasActionableUserSinceWait(session *Session) bool {
	if session == nil {
		return false
	}
	for _, user := range session.PendingMessages() {
		if user == nil || user.IsAgentMessage {
			continue
		}
		return true
	}
	return false
}

// enqueueWaitToolResult 把 wait 完成回填为原始 tool_call_id 的工具结果
func (a *Agent) enqueueWaitToolResult(kind string, targetID int64, session *Session, toolCallID, logicalTurnID string, elapsed, requested time.Duration, hasNewMessages bool) {
	if session == nil || a.memory == nil {
		return
	}
	requestedText := ""
	if requested > 0 {
		requestedText = fmt.Sprintf("，原计划等待 %.1f 秒", requested.Seconds())
	}
	var content string
	if hasNewMessages {
		content = fmt.Sprintf(
			"等待已结束，实际等待 %.1f 秒%s，期间收到了新的用户输入。请结合这些新消息继续下一轮思考。",
			elapsed.Seconds(), requestedText,
		)
	} else if requested > 0 && elapsed >= requested {
		content = fmt.Sprintf(
			"等待已超时，实际等待 %.1f 秒%s，期间没有收到新的用户输入。请基于现有上下文继续下一轮思考。",
			elapsed.Seconds(), requestedText,
		)
	} else {
		content = fmt.Sprintf("等待已结束，实际等待 %.1f 秒%s。请基于现有上下文继续下一轮思考。", elapsed.Seconds(), requestedText)
	}
	if toolCallID == "" {
		toolCallID = fmt.Sprintf("wait:%s:%d:%d", kind, targetID, time.Now().UnixNano())
	}
	payload, err := sonic.Marshal(&schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		Extra: map[string]any{
			"source": "tool_result", "tool_status": "succeeded", "logical_turn_id": logicalTurnID,
		},
		ContentBlocks: []*schema.ContentBlock{{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: toolCallID, Name: "wait",
				Content: []*schema.FunctionToolResultContentBlock{{
					Type: schema.FunctionToolResultContentBlockTypeText,
					Text: &schema.UserInputText{Text: content},
				}},
			},
		}},
	})
	if err != nil {
		return
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	entry := &memory.TranscriptEntry{
		ConversationKind: kind, TargetID: targetID, EntryKind: "tool",
		RunID: maisakaTranscriptRunID, ToolCallID: toolCallID, ToolName: "wait",
		ToolStatus: "succeeded", Payload: payload,
	}
	if err := a.memory.AppendTranscript(ctx, entry); err != nil {
		zap.L().Warn("保存 wait 工具结果失败", zap.Error(err))
		return
	}
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: toolCallID, Name: "wait",
				Content: []*schema.FunctionToolResultContentBlock{{
					Type: schema.FunctionToolResultContentBlockTypeText,
					Text: &schema.UserInputText{Text: content},
				}},
			},
		}},
	}
	msg.Extra = map[string]any{"kind": "wait_tool_result", "source": "internal", "logical_turn_id": logicalTurnID}
	session.AppendAgentMessage(msg, entry.ID, time.Now())
}
