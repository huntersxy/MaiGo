package runtime

// 内部任务消息的会话条目，对齐 Python InternalContextMessage

import (
	"strings"
	"time"

	"maibot-go/internal/common/media"

	"github.com/cloudwego/eino/schema"
)

// AppendInternalTaskMessage 把内部任务消息加入待处理工作集
// 与助手回复不同，内部任务属于模型可见的用户内容，图片引用可以再次传给其他聊天
func (s *Session) AppendInternalTaskMessage(message *schema.AgenticMessage, transcriptID uint, at time.Time, mediaRefs []media.Ref) *sessionUser {
	if message == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		at = time.Now()
	}
	user := &sessionUser{
		Ref: internalTaskMessageID(message), Text: agenticMessageText(message), Msg: message,
		IsAgentMessage: true, IsInternalTask: true,
		MediaRefs:    append([]media.Ref(nil), mediaRefs...),
		TranscriptID: transcriptID, ReceivedAt: time.Now(), At: at, Order: s.nextOrderLocked(),
	}
	for _, item := range mediaRefs {
		s.media.register(item)
	}
	s.pending = append(s.pending, user)
	return user
}

// internalTaskMessageID 读取内部任务消息持久化的任务编号
func internalTaskMessageID(message *schema.AgenticMessage) string {
	if message == nil || message.Extra == nil {
		return ""
	}
	value, _ := message.Extra["message_id"].(string)
	return strings.TrimSpace(value)
}
