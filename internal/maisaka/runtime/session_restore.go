package runtime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/config"
	"MaiGo/internal/platform_io/drivers/onebot"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

// restoreSessions 先恢复持久化工作集，没有工作集时才回退到最近原文
func (a *Agent) restoreSessions() error {
	groupIDs, err := a.memory.ListActiveGroupIDs(a.ctx)
	if err != nil {
		return fmt.Errorf("读取可恢复群聊失败: %w", err)
	}
	for _, groupID := range groupIDs {
		if err := a.restoreTranscriptSession(memory.ConversationKindGroup, groupID); err != nil {
			return fmt.Errorf("恢复群 %d 工作集失败: %w", groupID, err)
		}
	}
	privateTargets, err := a.memory.ListConversationTargets(a.ctx, memory.ConversationKindPrivate, true)
	if err != nil {
		return fmt.Errorf("读取可恢复私聊失败: %w", err)
	}
	for _, target := range privateTargets {
		if err := a.restoreTranscriptSession(memory.ConversationKindPrivate, target.TargetID); err != nil {
			return fmt.Errorf("恢复好友 %d 工作集失败: %w", target.TargetID, err)
		}
	}
	return nil
}

// restoreTranscriptSession 读取检查点、保留尾部和工作集 pending
func (a *Agent) restoreTranscriptSession(kind string, targetID int64) error {
	session := a.sessions.get(kind, targetID)
	sessionID, sessionName, includeChatID := a.plannerSessionInfo(session)
	checkpoint, err := a.memory.GetCheckpoint(a.ctx, kind, targetID)
	if err != nil {
		return err
	}
	var after uint64
	if checkpoint != nil && checkpoint.ThroughVisibleSeq != nil {
		after = *checkpoint.ThroughVisibleSeq
	}
	visible, err := a.memory.VisibleTranscript(a.ctx, kind, targetID, after)
	if err != nil {
		return err
	}
	pending, err := a.memory.PendingInputs(a.ctx, kind, targetID)
	if err != nil {
		return err
	}
	pendingReferences, err := a.memory.PendingReferences(a.ctx, kind, targetID)
	if err != nil {
		return err
	}
	if len(visible) == 0 && len(pending) == 0 && len(pendingReferences) == 0 {
		// checkpoint 覆盖了全部可见条目时，磁盘上的工作集处于「已消费」状态；
		// 进程刚启动时内存工作集为空，直接返回会让模型看不到任何聊天记录。
		// 对齐 Python _restore_recent_context_from_db：内存为空就补最近窗口。
		logs, getErr := a.memory.GetRecentMessagesScope(a.ctx, kind, targetID, 0, restoreMessageLimit, 0)
		if getErr != nil {
			return getErr
		}
		a.restoreSession(kind, targetID, trimRestoreMessages(logs, a.SelfID(), a.contextWindow()))
		return nil
	}

	visibleRows, deferred := splitRestoredEntries(visible)
	toolCalls := map[string]bool{}
	for _, entry := range visible {
		if entry.EntryKind == "tool" {
			toolCalls[toolCallKey(entry.RunID, entry.ToolCallID)] = true
		}
	}
	assistantEntries := make([]memory.TranscriptEntry, 0, len(visibleRows)+len(deferred))
	assistantEntries = append(assistantEntries, visibleRows...)
	assistantEntries = append(assistantEntries, deferred...)
	missing, err := a.collectMissingToolResults(session, assistantEntries, toolCalls)
	if err != nil {
		return err
	}

	for _, entry := range visibleRows {
		if entry.EntryKind != "user" {
			if err := a.restoreSeenEntry(session, &entry, missing); err != nil {
				return err
			}
			continue
		}
		if entry.MessageLogID == nil {
			continue
		}
		log, getErr := a.memory.GetMessageLogByInternalID(*entry.MessageLogID)
		if getErr != nil {
			continue
		}
		msg := a.conversationMessageFromLog(*log)
		if msg == nil {
			continue
		}
		var payload schema.AgenticMessage
		if sonic.Unmarshal(entry.Payload, &payload) != nil {
			payload = *msgToPayloadWithSession(msg, sessionID, sessionName, includeChatID)
		} else {
			// transcript 不存图片字节，按落库的媒体引用重新装配
			payload = attachRestoredMedia(payload, log.MediaRefs)
		}
		session.RestoreUser(msg, log.ID, log.MediaRefs, entry.ID, valueOrZero(entry.VisibleSeq), true, &payload)
	}

	unvisible := append([]memory.TranscriptEntry(nil), deferred...)
	unvisible = append(unvisible, pendingReferences...)
	sort.SliceStable(unvisible, func(i, j int) bool { return unvisible[i].ID < unvisible[j].ID })
	for _, entry := range unvisible {
		if entry.EntryKind == "internal_reference" {
			var payload schema.AgenticMessage
			if sonic.Unmarshal(entry.Payload, &payload) != nil {
				continue
			}
			var receipt memory.ReferenceReceipt
			if raw, ok := payload.Extra["reference_receipts"]; ok {
				data, _ := sonic.Marshal(raw)
				_ = sonic.Unmarshal(data, &receipt)
			}
			session.RestorePendingReference(&payload, entry.ID, &receipt)
			continue
		}
		if err := a.restoreSeenEntry(session, &entry, missing); err != nil {
			return err
		}
	}

	for _, entry := range pending {
		if entry.InputSeq == nil {
			continue
		}
		if entry.EntryKind == "agent_message" {
			var payload schema.AgenticMessage
			if sonic.Unmarshal(entry.Payload, &payload) == nil {
				session.RestorePendingAgentMessage(&payload, entry.ID)
			}
			continue
		}
		if entry.EntryKind == "observation" {
			var payload schema.AgenticMessage
			if sonic.Unmarshal(entry.Payload, &payload) == nil {
				session.RestorePendingObservation(&payload, entry.ID)
			}
			continue
		}
		if entry.MessageLogID == nil {
			continue
		}
		log, getErr := a.memory.GetMessageLogByInternalID(*entry.MessageLogID)
		if getErr != nil {
			continue
		}
		msg := a.conversationMessageFromLog(*log)
		if msg == nil {
			continue
		}
		var payload schema.AgenticMessage
		if sonic.Unmarshal(entry.Payload, &payload) != nil {
			payload = *msgToPayloadWithSession(msg, sessionID, sessionName, includeChatID)
		}
		session.RestoreUser(msg, log.ID, log.MediaRefs, entry.ID, 0, false, &payload)
	}
	return nil
}

func splitRestoredEntries(entries []memory.TranscriptEntry) (visible, deferred []memory.TranscriptEntry) {
	for _, entry := range entries {
		if entry.VisibleSeq == nil {
			deferred = append(deferred, entry)
			continue
		}
		visible = append(visible, entry)
	}
	return visible, deferred
}

// collectMissingToolResults 为缺少回执的 assistant 工具调用补 not_executed 行
func (a *Agent) collectMissingToolResults(session *Session, entries []memory.TranscriptEntry, toolCalls map[string]bool) (map[uint][]*memory.TranscriptEntry, error) {
	missing := map[uint][]*memory.TranscriptEntry{}
	for _, entry := range entries {
		if entry.EntryKind != "assistant" {
			continue
		}
		var message schema.AgenticMessage
		if sonic.Unmarshal(entry.Payload, &message) != nil {
			continue
		}
		for _, block := range message.ContentBlocks {
			if block == nil || block.FunctionToolCall == nil {
				continue
			}
			call := block.FunctionToolCall
			key := toolCallKey(entry.RunID, call.CallID)
			if toolCalls[key] {
				continue
			}
			assistantID := entry.ID
			toolEntry, created, err := session.AppendNotExecutedTool(a.ctx, entry.RunID, call.CallID, call.Name)
			if err != nil {
				return nil, err
			}
			toolCalls[key] = true
			if created && toolEntry != nil {
				missing[assistantID] = append(missing[assistantID], toolEntry)
			}
		}
	}
	return missing, nil
}

func (a *Agent) restoreSeenEntry(session *Session, entry *memory.TranscriptEntry, missing map[uint][]*memory.TranscriptEntry) error {
	if entry.EntryKind == "tool" && (entry.ToolStatus == "running" || entry.ToolStatus == "planned") {
		if err := session.RepairToolTranscript(a.ctx, entry, "unknown", "工具执行因进程退出而中断，没有可复用结果，请勿自动重放。"); err != nil {
			return err
		}
	}
	var message schema.AgenticMessage
	if sonic.Unmarshal(entry.Payload, &message) == nil {
		session.AppendSeenMessage(&message, entry.ID, valueOrZero(entry.VisibleSeq))
	}
	for _, extra := range missing[entry.ID] {
		var repaired schema.AgenticMessage
		if sonic.Unmarshal(extra.Payload, &repaired) == nil {
			session.AppendSeenMessage(&repaired, extra.ID, 0)
		}
	}
	return nil
}

func toolCallKey(runID, callID string) string {
	return runID + "\x00" + callID
}

func valueOrZero(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

// fillSeenFromLogs 把最近窗口直接接进内存工作集，不写 transcript
//
// 只在 transcript 已有历史时使用：这些消息早已落库，重复 seed 会违反唯一约束，
// 但内存工作集必须补齐，否则重启后的首个请求看不到任何聊天记录。
func (a *Agent) fillSeenFromLogs(session *Session, logs []memory.MessageLog, users map[uint]*sessionUser, selfID int64) {
	if session == nil || len(logs) == 0 {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	for _, log := range logs {
		if log.UserID == selfID {
			payload := assistantHistoryMessage(log)
			if payload == nil {
				continue
			}
			session.seen = append(session.seen, sessionEntry{msg: payload})
			continue
		}
		user := users[log.ID]
		if user == nil {
			continue
		}
		session.seen = append(session.seen, sessionEntry{user: user, msg: user.Msg})
	}
}

// restoreSession 在缺少工作集时把近期原文接成可见工作集，不伪造工具轨迹
func (a *Agent) restoreSession(kind string, targetID int64, logs []memory.MessageLog) {
	if len(logs) == 0 {
		return
	}
	session := a.sessions.get(kind, targetID)
	seeds := make([]memory.TranscriptSeed, 0, len(logs))
	users := map[uint]*sessionUser{}
	selfID := a.SelfID()
	sessionID, sessionName, includeChatID := a.plannerSessionInfo(session)
	for _, log := range logs {
		var payloadMessage *schema.AgenticMessage
		if log.UserID == selfID {
			payloadMessage = assistantHistoryMessage(log)
		} else {
			msg := a.conversationMessageFromLog(log)
			payloadMessage = msgToPayloadWithSession(msg, sessionID, sessionName, includeChatID)
			if payloadMessage == nil {
				continue
			}
			session.mu.Lock()
			user := session.newUserLocked(msg, log.ID, log.MediaRefs)
			session.mu.Unlock()
			payloadMessage = user.Msg
			users[log.ID] = user
		}
		payload, err := sonic.Marshal(payloadMessage)
		if err != nil {
			continue
		}
		entryKind := "user"
		if log.UserID == selfID {
			entryKind = "assistant"
		}
		seeds = append(seeds, memory.TranscriptSeed{EntryKind: entryKind, MessageLogID: log.ID, Payload: payload, CreatedAt: log.Timestamp})
	}
	entries, created, err := a.memory.SeedTranscriptIfEmpty(a.ctx, kind, targetID, seeds)
	if err != nil {
		return
	}
	if !created {
		// transcript 已有历史（例如进程重启）：不再重复 seed，但必须把最近窗口
		// 灌进内存工作集，否则首个请求的上下文为空。对齐 Python 的启动恢复语义。
		a.fillSeenFromLogs(session, logs, users, selfID)
		return
	}
	// 对齐 Python _restore_recent_context_from_db：恢复历史后追加一条离线间隔提示
	if reference := buildContextRestoreReferenceMessage(logs[len(logs)-1].Timestamp, time.Now()); reference != nil {
		session.mu.Lock()
		session.seen = append(session.seen, sessionEntry{msg: reference})
		session.mu.Unlock()
	}
	for _, entry := range entries {
		if entry.EntryKind == "user" {
			var logID uint
			if entry.MessageLogID != nil {
				logID = *entry.MessageLogID
			}
			user := users[logID]
			if user == nil {
				continue
			}
			user.TranscriptID = entry.ID
			session.mu.Lock()
			session.seen = append(session.seen, sessionEntry{user: user, msg: user.Msg, transcriptID: entry.ID, visibleSeq: valueOrZero(entry.VisibleSeq)})
			session.mu.Unlock()
			continue
		}
		var message schema.AgenticMessage
		if sonic.Unmarshal(entry.Payload, &message) == nil {
			session.AppendSeenMessage(&message, entry.ID, valueOrZero(entry.VisibleSeq))
		}
	}
}

// trimRestoreMessages 首次升级时只接续预算内的近期原文，优先保留最新消息
func trimRestoreMessages(logs []memory.MessageLog, selfID int64, window int) []memory.MessageLog {
	if len(logs) == 0 {
		return logs
	}
	cfg := config.Snapshot()
	if window <= 0 || cfg.Compactor.RecentTailRatio <= 0 {
		return logs
	}
	budget := int(float64(window) * cfg.Compactor.RecentTailRatio)
	if budget <= 0 {
		return logs
	}
	keepFrom := len(logs)
	total := 0
	for i := len(logs) - 1; i >= 0; i-- {
		var payload *schema.AgenticMessage
		if logs[i].UserID == selfID {
			payload = assistantHistoryMessage(logs[i])
		} else {
			payload = msgToPayload(messageLogToSessionMessage(logs[i]))
		}
		cost := messageTokens(payload)
		if keepFrom < len(logs) && total+cost > budget {
			break
		}
		total += cost
		keepFrom = i
	}
	return logs[keepFrom:]
}

func assistantHistoryMessage(log memory.MessageLog) *schema.AgenticMessage {
	text := strings.TrimSpace(log.ProcessedPlainText)
	if text == "" {
		text = strings.TrimSpace(log.ProcessedPlainText)
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeAssistantGenText, AssistantGenText: &schema.AssistantGenText{Text: text}}}}
}

func msgToPayload(msg *onebot.ConversationMessage) *schema.AgenticMessage {
	return msgToPayloadWithSession(msg, "", "", false)
}

// msgToPayloadWithSession 按 Planner 前缀格式构造用户消息
// 对齐 Python src/maisaka/context/planner_messages.py:17-84 与 history.py:23-38：
// 前缀拼在正文之前，属性顺序 msg_id → session_id/chat_name → quote → time → user → group_card → is_self_message
func msgToPayloadWithSession(msg *onebot.ConversationMessage, sessionID, sessionName string, includeChatID bool) *schema.AgenticMessage {
	if msg == nil {
		return nil
	}
	body := strings.TrimSpace(msg.FinalContent)
	if body == "" {
		body = strings.TrimSpace(msg.Content)
	}
	messageID := strconv.FormatInt(msg.MessageID, 10)
	if msg.MessageID == 0 {
		if noticeID := onebot.NoticeIDFromMessage(msg); noticeID != "" {
			messageID = noticeID
		}
	}
	prefix := plannerPrefixForMessage(msg, sessionID, sessionName, includeChatID, false)
	message := schema.UserAgenticMessage(prefix + body)
	message.Extra = map[string]any{
		"message_id":          messageID,
		"sender_id":           msg.UserID,
		"sender_name":         msg.Nickname,
		"nickname":            msg.Nickname,
		"timestamp_unix_ms":   msg.Time.UnixMilli(),
		"received_at_unix_ms": msg.ReceivedAt.UnixMilli(),
		"is_mentioned":        msg.IsMentioned,
		"is_notify":           msg.IsNotify,
	}
	return message
}

// attachRestoredMedia 把媒体引用重新装配为图片块，补回落库时剥离的字节
func attachRestoredMedia(payload schema.AgenticMessage, mediaRefs []media.Ref) schema.AgenticMessage {
	if len(mediaRefs) == 0 {
		return payload
	}
	blocks := make([]*schema.ContentBlock, 0, len(payload.ContentBlocks)+len(mediaRefs))
	blocks = append(blocks, payload.ContentBlocks...)
	for _, ref := range mediaRefs {
		if len(blocks) > 8 {
			break
		}
		image := mediaRefImageBlock(ref)
		if image == nil {
			continue
		}
		blocks = append(blocks, schema.NewContentBlock(image))
	}
	if len(blocks) == len(payload.ContentBlocks) {
		return payload
	}
	payload.ContentBlocks = blocks
	return payload
}

// conversationMessageFromLog 恢复消息时补齐引用目标的发送者，供自动参考选择
func (a *Agent) conversationMessageFromLog(log memory.MessageLog) *onebot.ConversationMessage {
	msg := messageLogToSessionMessage(log)
	if msg == nil {
		return nil
	}
	a.noteConversationPlatform(msg)
	a.rememberExternalIdentities(msg)
	if log.ReplyTo == nil {
		return msg
	}
	reply, err := a.memory.WithContext(a.ctx).GetMessageLogByScope(log.ConversationKind, log.TargetID, *log.ReplyTo)
	if err == nil && reply != nil {
		msg.Reply.SenderID = reply.UserID
	}
	return msg
}

// messageLogToSessionMessage 把消息库记录转成会话消息
func messageLogToSessionMessage(log memory.MessageLog) *onebot.ConversationMessage {
	var reply *onebot.ReplyInfo
	if log.ReplyTo != nil {
		reply = &onebot.ReplyInfo{MessageID: *log.ReplyTo}
	}
	return &onebot.ConversationMessage{
		Platform:          log.Platform,
		AccountID:         log.AccountID,
		Scope:             log.Scope,
		MessageID:         log.MessageID,
		ConversationKind:  log.ConversationKind,
		TargetID:          log.TargetID,
		UserID:            log.UserID,
		ExternalMessageID: log.ExternalMessageID,
		ExternalTargetID:  log.ExternalTargetID,
		ExternalUserID:    log.ExternalUserID,
		Nickname:          log.UserNickname,
		Content:           log.ProcessedPlainText,
		FinalContent:      log.ProcessedPlainText,
		IsMentioned:       log.IsMentioned,
		IsNotify:          strings.HasPrefix(log.ExternalMessageID, "napcat-notice-"),
		Time:              log.Timestamp,
		Reply:             reply,
	}
}
