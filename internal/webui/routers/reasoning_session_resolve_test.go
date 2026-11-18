package webuiapi

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	chatsession "MaiGo/internal/chat/session"
)

func TestResolveReasoningSessionInfoByName(t *testing.T) {
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

	targetKey := reasoningSessionTargetKey{Platform: "qq", ChatType: "group", TargetID: groupID}
	candidates := loadReasoningSessionCandidatesByTarget(db, map[reasoningSessionTargetKey]struct{}{targetKey: {}})
	info := resolveReasoningSessionInfo("qq_group_"+groupID, map[[2]string]struct{}{}, candidates, db)
	if info.Platform != "qq" || info.ChatType != "group" || info.TargetID != groupID {
		t.Fatalf("会话身份 = %+v", info)
	}
	if info.ResolvedSessionID == nil || *info.ResolvedSessionID != sessionID {
		t.Fatalf("resolved_session_id = %v", info.ResolvedSessionID)
	}
	if info.DisplayName != groupName {
		t.Fatalf("display_name = %q, 期望 %q", info.DisplayName, groupName)
	}
	if !isGroupReasoningSession("qq_group_"+groupID, info) {
		t.Fatal("群聊目录应判定为群聊")
	}
}
