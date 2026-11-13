package runtime

import (
	"context"
	"strconv"
	"strings"

	"maibot-go/internal/maisaka"
	"maibot-go/internal/maisaka/focus"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// focusCheckpointMaxMessages 检查点保留的最近消息数量，和 Python 增量水位对齐
const focusCheckpointMaxMessages = 40

// focusCheckpointMessages 取会话最近可见消息转成 Maisaka 上下文消息
func focusCheckpointMessages(session *Session) ([]maisaka.ContextMessage, int) {
	if session == nil {
		return nil, 0
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	start := len(session.seen) - focusCheckpointMaxMessages
	if start < 0 {
		start = 0
	}
	out := make([]maisaka.ContextMessage, 0, len(session.seen)-start)
	var sequence int
	for _, entry := range session.seen[start:] {
		if entry.visibleSeq > uint64(sequence) {
			sequence = int(entry.visibleSeq)
		}
		if entry.msg == nil {
			continue
		}
		messageID := strconv.FormatUint(entry.visibleSeq, 10)
		role := maisaka.RoleUser
		if entry.user != nil {
			if entry.user.Ref != "" {
				messageID = entry.user.Ref
			}
			// 内部任务消息对齐 Python InternalContextMessage，仍按 user 角色投影
			if entry.user.IsAgentMessage && !entry.user.IsInternalTask {
				role = maisaka.RoleAssistant
			}
		}
		message := maisaka.ContextMessage{
			Role:           role,
			Content:        agenticMessageText(entry.msg),
			Source:         maisaka.SourceUser,
			CountInContext: entry.visibleSeq > 0,
			MessageID:      messageID,
		}
		if entry.user != nil {
			message.UserID = strconv.FormatInt(entry.user.SenderID, 10)
			message.Timestamp = entry.user.At
			receivedAt := entry.user.ReceivedAt
			message.ReceivedAt = &receivedAt
		}
		out = append(out, message)
	}
	return out, sequence
}

// saveFocusCheckpoint 在 Focus 启用时把最近消息水位落盘
func (a *Agent) saveFocusCheckpoint(ctx context.Context, session *Session) {
	if a == nil || session == nil {
		return
	}
	session.mu.Lock()
	grouped := session.maisakaFocusGroup != nil
	session.mu.Unlock()
	if grouped {
		return
	}
	settings, _, ok := a.focusExperimentalSnapshot()
	if !ok {
		return
	}
	info, ok := a.focusSessionInfo(ctx, session)
	if !ok || !focus.IsEnabledForSession(settings, info) {
		return
	}
	messages, sequence := focusCheckpointMessages(session)
	if len(messages) == 0 {
		return
	}
	scopeID, err := focus.GetFocusScopeKey(settings, info)
	if err != nil {
		zap.L().Warn("Focus 共享组身份解析失败", zap.String("session_id", info.SessionID), zap.Error(err))
		return
	}
	store := focus.NewCheckpointStore(scopeID, focus.DefaultCheckpointRoot)
	entry := focus.NewEntry(messages, info.SessionID)
	entry.Sequence = sequence
	entry.Watermarks[info.SessionID] = sequence
	state := focus.CheckpointState{
		Sequence:   sequence,
		Considered: map[string]int{info.SessionID: sequence},
		Seen:       focusSeenKeys(info.SessionID, messages),
	}
	if err := store.Save([]*focus.Entry{entry}, state); err != nil {
		zap.L().Warn("Focus 检查点写入失败", zap.String("session_id", info.SessionID), zap.String("scope_id", scopeID), zap.Error(err))
	}
}

// focusSeenKeys 生成去重键，和 Python seen 的 session_id+\0+message_id 对齐
func focusSeenKeys(sessionID string, messages []maisaka.ContextMessage) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.MessageID == "" {
			continue
		}
		out = append(out, sessionID+"\x00"+message.MessageID)
	}
	return out
}

// focusActiveForSession 当前会话是否处于 Focus 模式，用于跳过空闲退避
func (a *Agent) focusActiveForSession(session *Session) bool {
	if a == nil || session == nil {
		return false
	}
	settings, _, ok := a.focusExperimentalSnapshot()
	if !ok {
		return false
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	info, ok := a.focusSessionInfo(ctx, session)
	if !ok {
		return false
	}
	return focus.IsEnabledForSession(settings, info)
}

// loadFocusCheckpointMessages 读取检查点中的历史消息，供新进程恢复
func loadFocusCheckpointMessages(settings focus.Settings, info focus.SessionInfo) ([]maisaka.ContextMessage, int) {
	if !focus.IsEnabledForSession(settings, info) {
		return nil, 0
	}
	scopeID, err := focus.GetFocusScopeKey(settings, info)
	if err != nil {
		return nil, 0
	}
	store := focus.NewCheckpointStore(scopeID, focus.DefaultCheckpointRoot)
	checkpoint, err := store.Load()
	if err != nil || checkpoint == nil {
		if err != nil {
			zap.L().Warn("Focus 检查点读取失败", zap.String("session_id", info.SessionID), zap.Error(err))
		}
		return nil, 0
	}
	messages := make([]maisaka.ContextMessage, 0, len(checkpoint.Entries))
	for _, entry := range checkpoint.Entries {
		for _, record := range entry.Messages {
			messages = append(messages, focus.DeserializeMessage(record))
		}
	}
	return messages, checkpoint.Sequence
}

// restoreFocusCheckpoint 把检查点历史补进会话，已有内存历史时不覆盖
func (a *Agent) restoreFocusCheckpoint(ctx context.Context, session *Session) {
	if a == nil || session == nil {
		return
	}
	session.mu.Lock()
	hasHistory := len(session.seen) > 0
	session.mu.Unlock()
	if hasHistory {
		return
	}
	settings, _, ok := a.focusExperimentalSnapshot()
	if !ok {
		return
	}
	info, ok := a.focusSessionInfo(ctx, session)
	if !ok {
		return
	}
	restored := 0
	messages, sequence := loadFocusCheckpointMessages(settings, info)
	base := sequence - len(messages) + 1
	if base < 1 {
		base = 1
	}
	for index, message := range messages {
		text := strings.TrimSpace(message.Content)
		if text == "" || message.Role != maisaka.RoleUser {
			continue
		}
		senderID := int64(0)
		if message.UserID != "" {
			senderID, _ = strconv.ParseInt(message.UserID, 10, 64)
		}
		visibleSeq := base + index
		if visibleSeq < 1 {
			visibleSeq = 1
		}
		session.RestoreVisibleMessage(schema.UserAgenticMessage(text), message.Timestamp, senderID, message.MessageID, uint64(visibleSeq))
		restored++
	}
	if restored > 0 {
		zap.L().Info("Focus 检查点已恢复", zap.String("session_id", info.SessionID), zap.Int("messages", restored))
	}
}
