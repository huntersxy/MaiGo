package webuiapi

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	chatsession "MaiGo/internal/chat/session"
)

func TestResolveReasoningSessionInfoByHashDirectory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chatsession.Record{}); err != nil {
		t.Fatal(err)
	}
	sessionID := "e90f5a34f0f8450bed46a357ebcea2cb"
	groupID, groupName := "994571792", "测试群"
	record := chatsession.Record{
		SessionID: sessionID, Platform: "qq", GroupID: &groupID, GroupName: &groupName,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}

	info := resolveReasoningSessionInfo(sessionID, map[[2]string]struct{}{}, nil, db)
	if info.Platform != "qq" || info.ChatType != "group" || info.TargetID != groupID {
		t.Fatalf("会话身份 = %+v", info)
	}
	if info.ResolvedSessionID == nil || *info.ResolvedSessionID != sessionID {
		t.Fatalf("resolved_session_id = %v", info.ResolvedSessionID)
	}
	if info.DisplayName != groupName {
		t.Fatalf("display_name = %q, 期望 %q", info.DisplayName, groupName)
	}
	if !isGroupReasoningSession(sessionID, info) {
		t.Fatal("哈希群聊目录应判定为群聊")
	}
}

func TestResolveReasoningSessionInfoKeepsUnknownName(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chatsession.Record{}); err != nil {
		t.Fatal(err)
	}
	name := "0123456789abcdef0123456789abcdef"
	info := resolveReasoningSessionInfo(name, map[[2]string]struct{}{}, nil, db)
	if info.ResolvedSessionID != nil {
		t.Fatalf("未知会话不应解析: %+v", info)
	}
	if info.DisplayName != name {
		t.Fatalf("display_name = %q", info.DisplayName)
	}
}
