package services

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	chatsession "MaiGo/internal/chat/session"
	memory "MaiGo/internal/common/database"
)

func TestSharingGroupScopeAndRevision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chatsession.Record{}, &memory.SharingGroup{}, &memory.SharingGroupMember{},
		&memory.SharingGroupRule{}, &memory.SharingGroupCapability{}, &memory.SharingGroupMigration{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := chatsession.Record{SessionID: "source", Platform: "qq", UserID: strPtr("1001")}
	target := chatsession.Record{SessionID: "target", Platform: "qq", UserID: strPtr("1002")}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := InitializeSharingGroups(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	group, err := SaveSharingGroup(ctx, SharingGroupInput{
		Name: "测试组", Color: "#39845A", Enabled: true,
		MemberIDs:    []string{"source"},
		Rules:        []SharingMemberRule{{Platform: "qq", ItemID: "1002", RuleType: "private"}},
		Capabilities: map[string]bool{"expression": true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := SaveSharingGroup(ctx, SharingGroupInput{Name: "空组", Color: "#397FAE", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ListSharingGroups(ctx) {
		if item.ID != empty.ID {
			continue
		}
		if item.MemberIDs == nil || item.Rules == nil || item.ResolvedIDs == nil {
			t.Fatalf("空组集合必须编码为数组: %#v", item)
		}
	}
	if err := DeleteSharingGroup(ctx, empty.ID, empty.Revision); err != nil {
		t.Fatal(err)
	}
	ids, global, err := ResolveSharingScope("source", "expression")
	if err != nil {
		t.Fatal(err)
	}
	if global {
		t.Fatal("不应是全局范围")
	}
	if _, ok := ids["target"]; !ok {
		t.Fatalf("规则命中的目标未纳入范围: %#v", ids)
	}
	revision := group.Revision + 1
	if _, err := SaveSharingGroup(ctx, SharingGroupInput{
		Name: "改名", Color: "#397FAE", Enabled: true, Revision: &revision,
		MemberIDs:    []string{"source"},
		Rules:        []SharingMemberRule{{Platform: "qq", ItemID: "1002", RuleType: "private"}},
		Capabilities: map[string]bool{"expression": true},
	}, &group.ID); err == nil {
		t.Fatal("过期 revision 应拒绝更新")
	}
	if err := DeleteSharingGroup(ctx, group.ID, group.Revision); err != nil {
		t.Fatal(err)
	}
}

func strPtr(value string) *string { return &value }
