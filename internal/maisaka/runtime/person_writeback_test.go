package runtime

import (
	"context"
	"testing"

	"MaiGo/internal/common/database"
	"MaiGo/internal/platform_io/drivers/onebot"
)

func TestNotifySentMessageAttachesSessionID(t *testing.T) {
	agent := &Agent{index: newSessionIndex()}
	agent.index.setChatSessionID(memory.ConversationKindGroup, 42, "session-42")
	var got string
	agent.SetSentMessageHandler(func(_ context.Context, msg *onebot.ConversationMessage) error {
		got = msg.SessionID
		return nil
	})
	agent.notifySentMessage(context.Background(), &onebot.ConversationMessage{
		ConversationKind: memory.ConversationKindGroup,
		TargetID:         42,
	})
	if got != "session-42" {
		t.Fatalf("出站消息应带上会话号: got=%q", got)
	}
}
