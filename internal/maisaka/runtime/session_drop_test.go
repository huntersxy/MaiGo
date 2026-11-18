package runtime

import (
	"testing"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
)

func TestSessionStoreDropRemovesAndRecreates(t *testing.T) {
	store := newSessionStore(nil)
	first := store.get(memory.ConversationKindGroup, 42)
	if first == nil {
		t.Fatal("get 应创建会话")
	}
	first.notes = []string{"keep-me"}
	if !store.Drop(memory.ConversationKindGroup, 42) {
		t.Fatal("Drop 应成功")
	}
	if store.Drop(memory.ConversationKindGroup, 42) {
		t.Fatal("重复 Drop 应返回 false")
	}
	second := store.get(memory.ConversationKindGroup, 42)
	if second == nil {
		t.Fatal("Drop 后 get 应重建")
	}
	if second == first {
		t.Fatal("重建会话应是新实例")
	}
	if len(second.notes) != 0 {
		t.Fatalf("重建会话不应保留旧 notes: %#v", second.notes)
	}
}

func TestChatSessionKindTarget(t *testing.T) {
	kind, id, ok := chatSessionKindTarget(true, "1001", "9")
	if !ok || kind != memory.ConversationKindGroup || id != 1001 {
		t.Fatalf("群聊解析失败: kind=%s id=%d ok=%v", kind, id, ok)
	}
	kind, id, ok = chatSessionKindTarget(false, "1001", "9")
	if !ok || kind != memory.ConversationKindPrivate || id != 9 {
		t.Fatalf("私聊解析失败: kind=%s id=%d ok=%v", kind, id, ok)
	}
	if _, _, ok = chatSessionKindTarget(true, "", "9"); ok {
		t.Fatal("缺 groupID 应失败")
	}
}

func TestAgentDropConversationClearsArrivalSeq(t *testing.T) {
	a := &Agent{sessions: newSessionStore(nil)}
	key := chatsession.ConversationKey(memory.ConversationKindPrivate, 7)
	a.arrivalSeq.Store(key, "seq")
	_ = a.sessions.get(memory.ConversationKindPrivate, 7)
	if !a.DropConversation(memory.ConversationKindPrivate, 7) {
		t.Fatal("DropConversation 应成功")
	}
	if _, ok := a.arrivalSeq.Load(key); ok {
		t.Fatal("arrivalSeq 应被清除")
	}
	if len(a.sessions.all()) != 0 {
		t.Fatal("Drop 后 sessionStore 应为空")
	}
	if _, err := a.SessionRuntime("any"); err == nil {
		t.Fatal("淘汰后 SessionRuntime 不应命中旧运行时")
	}
	if a.DropChatSession(false, "", "7") {
		t.Fatal("会话已删后 DropChatSession 应返回 false")
	}
}
