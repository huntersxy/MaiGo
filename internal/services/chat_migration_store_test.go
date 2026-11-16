package services

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestChatMigrationSnapshotAndApply(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE chat_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL UNIQUE,
			created_timestamp DATETIME NOT NULL, platform TEXT NOT NULL, user_id TEXT, group_id TEXT,
			account_id TEXT, scope TEXT, nickname TEXT, group_name TEXT
		)`,
		`CREATE TABLE mai_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, message_id INTEGER NOT NULL,
			session_id TEXT, processed_plain_text TEXT, timestamp DATETIME NOT NULL
		)`,
		`CREATE TABLE expressions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, situation TEXT NOT NULL, style TEXT NOT NULL,
			content_list TEXT NOT NULL DEFAULT '[]', count INTEGER NOT NULL DEFAULT 0,
			session_id TEXT, checked BOOLEAN NOT NULL DEFAULT false
		)`,
		`CREATE TABLE jargons (
			id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL, meaning TEXT NOT NULL,
			session_id_dict TEXT NOT NULL DEFAULT '{}', is_global BOOLEAN NOT NULL DEFAULT false
		)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed := []string{
		`INSERT INTO chat_sessions (session_id, created_timestamp, platform, user_id) VALUES ('source', CURRENT_TIMESTAMP, 'qq', '1001')`,
		`INSERT INTO chat_sessions (session_id, created_timestamp, platform, user_id) VALUES ('target', CURRENT_TIMESTAMP, 'qq', '1002')`,
		`INSERT INTO mai_messages (message_id, session_id, processed_plain_text, timestamp) VALUES (1, 'source', 'hello', CURRENT_TIMESTAMP)`,
		`INSERT INTO expressions (situation, style, content_list, session_id) VALUES ('闲聊', '简短', '["你好"]', 'source')`,
		`INSERT INTO jargons (content, meaning, session_id_dict) VALUES ('梗', '意思', '{"source": 2}')`,
	}
	for _, statement := range seed {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := NewMigrationStore(db)
	ctx := context.Background()
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx, "source", "target")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.SourceRows(snapshot, "mai_messages", "source")) != 1 {
		t.Fatal("来源消息未进入快照")
	}
	fingerprint := snapshot["fingerprint"].(string)
	jobID, err := store.Create(ctx, "source", "target", map[string]any{"fingerprint": fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]string{}
	for _, conflict := range snapshot["conflicts"].([]map[string]any) {
		decisions[conflict["id"].(string)] = "both"
	}
	counts, err := store.Apply(ctx, jobID, fingerprint, decisions)
	if err != nil {
		t.Fatal(err)
	}
	if counts["mai_messages"] != 1 || counts["expressions"] != 1 || counts["jargons"] != 1 {
		t.Fatalf("复制计数不符: %#v", counts)
	}
	var messageCount int64
	if err := db.Raw("SELECT COUNT(*) FROM mai_messages WHERE session_id = 'target'").Scan(&messageCount).Error; err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 {
		t.Fatalf("目标消息数量 = %d", messageCount)
	}
	var dict string
	if err := db.Raw("SELECT session_id_dict FROM jargons WHERE session_id_dict LIKE '%target%'").Scan(&dict).Error; err != nil {
		t.Fatal(err)
	}
	if dict == "" {
		t.Fatal("黑话未复制到目标作用域")
	}
	var ledgerCount int64
	if err := db.Raw("SELECT COUNT(*) FROM chat_migration_copies").Scan(&ledgerCount).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 3 {
		t.Fatalf("迁移账本行数 = %d", ledgerCount)
	}
}
