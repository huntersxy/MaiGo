package test

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	memory "MaiGo/internal/common/database"
	migrations "MaiGo/internal/common/database/migrations"
	personinfo "MaiGo/internal/person_info"
	"MaiGo/internal/platform_io/drivers/snowluma"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestMentionDisplayNameUsesGroupCardThenNickname(t *testing.T) {
	root := t.TempDir()
	db, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	manager, err := memory.NewManager(db, filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	registry := &personinfo.Registry{}
	registry.SetMemory(manager)
	insertMentionProfile(t, manager, "10001", "成员昵称", "1079643584", "群名片")
	insertMentionProfile(t, manager, "10002", "10002", "1079643584", "10002")

	if got := registry.MentionDisplayName(context.Background(), "qq", "10001", "1079643584"); got != "群名片" {
		t.Fatalf("expected current-group card, got %q", got)
	}
	if got := registry.MentionDisplayName(context.Background(), "qq", "10001", "other-group"); got != "成员昵称" {
		t.Fatalf("expected profile nickname, got %q", got)
	}
	if got := registry.MentionDisplayName(context.Background(), "qq", "10002", "1079643584"); got != "" {
		t.Fatalf("numeric ID fallback must not be treated as a name, got %q", got)
	}
	if got := registry.MentionDisplayName(context.Background(), "qq", "10003", "1079643584"); got != "" {
		t.Fatalf("unknown users must have no resolved name, got %q", got)
	}
}

func insertMentionProfile(t *testing.T, manager *memory.Manager, userID, nickname, groupID, card string) {
	t.Helper()
	profileID := personinfo.PersonID("qq", userID)
	individualID := personinfo.PersonID("individual", userID)
	groupCards, err := json.Marshal([]personinfo.GroupCardEntry{{GroupID: groupID, GroupCardname: card}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.GetDB().Exec(`INSERT INTO individuals (individual_id, display_name, is_known, profile_sources_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		individualID, nickname, true, "[]", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.GetDB().Exec(`INSERT INTO person_info (is_known, person_id, individual_id, platform, user_id, user_nickname, group_cardname, know_counts) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		true, profileID, individualID, "qq", userID, nickname, string(groupCards), 1).Error; err != nil {
		t.Fatal(err)
	}
}

func TestSnowLumaAtWithoutNameKeepsIDOutOfNickname(t *testing.T) {
	message := snowluma.NormalizeInboundMessage(map[string]any{
		"message_type": "group",
		"message_id":   "1",
		"user_id":      "10",
		"group_id":     "20",
		"self_id":      "99",
		"message": []any{map[string]any{
			"type": "at",
			"data": map[string]any{"qq": "10001"},
		}},
	}, "99", "scope")
	segments, ok := message["raw_message"].([]any)
	if !ok || len(segments) != 1 {
		t.Fatal("expected one normalized mention segment")
	}
	segment, ok := segments[0].(map[string]any)
	if !ok {
		t.Fatal("expected a mention segment map")
	}
	data, ok := segment["data"].(map[string]any)
	if !ok || strings.TrimSpace(asTestString(data["target_user_nickname"])) != "" {
		t.Fatal("missing nickname must not be populated with the target ID")
	}
	if message["is_mentioned"] != false {
		t.Fatal("mentioning another user must not mark the bot as mentioned")
	}
}

func TestSnowLumaConnectionErrorRedactsAccessToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.WarnLevel)
	restoreLogger := zap.ReplaceGlobals(zap.New(core))
	defer restoreLogger()
	const token = "fixture/token value"
	transport := snowluma.NewTransport(nil, nil, nil)
	transport.Configure(snowluma.ClientConfig{Server: host, Port: port, Token: token, ReconnectDelaySec: 60}, nil)
	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for logs.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transport.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	entries := logs.All()
	if len(entries) == 0 {
		t.Fatal("expected a connection error log")
	}
	fields := entries[0].ContextMap()
	logged := asTestString(fields["url"]) + " " + asTestString(fields["error"])
	if strings.Contains(logged, token) || strings.Contains(logged, url.QueryEscape(token)) {
		t.Fatal("connection error log exposed the access token")
	}
	if !strings.Contains(asTestString(fields["url"]), "access_token=***") {
		t.Fatal("connection URL must show a masked token")
	}
}

func asTestString(value any) string {
	text, _ := value.(string)
	return text
}
