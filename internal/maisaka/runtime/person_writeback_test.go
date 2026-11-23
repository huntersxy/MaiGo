package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/database/migrations"
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

func TestExternalAgentSendNotifiesMemoryAfterEchoWasStored(t *testing.T) {
	root := t.TempDir()
	db, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := migration.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	manager, err := memory.NewManager(db, root)
	if err != nil {
		t.Fatal(err)
	}
	const kind, target, sessionID = memory.ConversationKindGroup, 42, "session-42"
	agent := &Agent{memory: manager, sessions: newSessionStore(manager), index: newSessionIndex(), selfIDValue: 9, selfExpires: time.Now().Add(time.Minute)}
	agent.index.setChatSessionID(kind, target, sessionID)
	var notified int
	agent.SetSentMessageHandler(func(ctx context.Context, msg *onebot.ConversationMessage) error {
		notified++
		count, err := manager.CountMessagesBySessionID(ctx, msg.SessionID, nil)
		if count != 1 || err != nil {
			t.Errorf("summary trigger must see the persisted message, count=%d err=%v", count, err)
		}
		return nil
	})
	msg := onebot.ConversationMessage{Platform: "qq", ExternalTargetID: "group-42",
		ConversationKind: kind, TargetID: target, MessageID: 77, UserID: 9, Time: time.Now()}
	agent.commitConversationMessageLocked(t.Context(), commitItem{msg: &msg}, sessionID)
	if notified != 0 {
		t.Fatal("adapter echo notified memory")
	}
	agent.commitConversationMessageLocked(t.Context(), commitItem{msg: &msg, sentByAgent: true}, sessionID)
	if notified != 1 {
		t.Fatalf("local send triggered memory %d times", notified)
	}
}
