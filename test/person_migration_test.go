package test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"MaiGo/internal/A_memorix/core/storage"
	messagereceive "MaiGo/internal/chat/message_receive"
	"MaiGo/internal/common/data_models/messagecomponent"
	memory "MaiGo/internal/common/database"
	migration "MaiGo/internal/common/database/migrations"
	runtime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/person_info/identity"
	"MaiGo/internal/services"
)

func TestPersonMigrationCreatesIndividualSchema(t *testing.T) {
	db, err := memory.OpenDB(filepath.Join(t.TempDir(), "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	if err := migration.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	var version, legacyColumns int
	if err := connection.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('person_info') WHERE name IN ('name_reason','memory_points')`).Scan(&legacyColumns); err != nil {
		t.Fatal(err)
	}
	if version != 58 || legacyColumns != 0 {
		t.Fatalf("version=%d legacyColumns=%d", version, legacyColumns)
	}
	if _, err := connection.Exec(`INSERT INTO person_info (person_id, platform, user_id) VALUES ('person','qq','100')`); err == nil || !strings.Contains(err.Error(), "individual_id") {
		t.Fatalf("missing Individual must be rejected, got %v", err)
	}
}

func TestPersonMigrationRollsBackMainAndInheritsEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	open := func(path string) *sql.DB {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	exec := func(db *sql.DB, query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	main := open("data/MaiBot.db")
	memorix := open("data/a-memorix/metadata/metadata.db")
	source, target := identity.PersonID("qq", "100"), identity.PersonID("qq", "openid")
	exec(main, `CREATE TABLE person_info (id INTEGER PRIMARY KEY, platform TEXT, user_id TEXT, person_id TEXT UNIQUE)`)
	exec(main, `INSERT INTO person_info VALUES (1,'qq','100',?)`, source)
	exec(memorix, `CREATE TABLE fact_claims (claim_id TEXT PRIMARY KEY, scope_type TEXT, scope_id TEXT, fact_key TEXT,
		value_normalized TEXT, polarity TEXT, cardinality TEXT, conflict_group TEXT, status TEXT)`)
	exec(memorix, `CREATE TABLE fact_evidence (claim_id TEXT, evidence_type TEXT, evidence_id TEXT, stance TEXT, metadata_json TEXT,
		PRIMARY KEY(claim_id,evidence_type,evidence_id,stance))`)
	sourceClaim, err := storage.ClaimIdentity("person", source, "favorite_color", "green", "affirm")
	if err != nil {
		t.Fatal(err)
	}
	targetClaim, err := storage.ClaimIdentity("person", target, "favorite_color", "green", "affirm")
	if err != nil {
		t.Fatal(err)
	}
	exec(memorix, `INSERT INTO fact_claims VALUES (?,'person',?,'favorite_color','green','affirm','single','source-group','active')`, sourceClaim, source)
	exec(memorix, `INSERT INTO fact_evidence VALUES (?,'message','old-message','supports','{"user_id":"100"}')`, sourceClaim)
	exec(memorix, `CREATE TRIGGER reject_fact_copy BEFORE INSERT ON fact_claims BEGIN SELECT RAISE(ABORT,'copy rejected'); END`)
	plan, err := services.PreviewPersonMigration(ctx, root, "qq", "100", "openid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.ExecutePersonMigration(ctx, root, plan); err == nil || !strings.Contains(err.Error(), "copy rejected") {
		t.Fatalf("expected memory write failure, got %v", err)
	}
	var user string
	if err := main.QueryRow(`SELECT user_id FROM person_info WHERE id=1`).Scan(&user); err != nil || user != "100" {
		t.Fatalf("main row did not roll back: user=%q err=%v", user, err)
	}
	state, err := identity.Load(ctx, main, root)
	if err != nil || state.ResolveUserID("qq", "100") != "100" {
		t.Fatalf("identity did not roll back: state=%#v err=%v", state, err)
	}
	exec(memorix, `DROP TRIGGER reject_fact_copy`)
	plan, err = services.PreviewPersonMigration(ctx, root, "qq", "100", "openid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.ExecutePersonMigration(ctx, root, plan); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ claim, user, person string }{{sourceClaim, "100", source}, {targetClaim, "openid", target}} {
		var scope, evidence string
		if err := memorix.QueryRow(`SELECT scope_id, json_extract(metadata_json,'$.user_id') FROM fact_claims JOIN fact_evidence USING(claim_id) WHERE claim_id=?`, expected.claim).Scan(&scope, &evidence); err != nil || scope != expected.person || evidence != expected.user {
			t.Fatalf("claim=%s scope=%q evidence=%q err=%v", expected.claim, scope, evidence, err)
		}
	}
}

func TestPersonMigrationPreservesStructuredForwardIdentities(t *testing.T) {
	forward := func(user string, content ...any) map[string]any {
		return map[string]any{"type": "forward", "data": []any{map[string]any{"user_id": user, "content": content}}}
	}
	segment := forward("100", forward("200",
		map[string]any{"type": "at", "data": map[string]any{"target_user_id": "100"}},
		map[string]any{"type": "reply", "data": map[string]any{"target_message_id": "message", "target_message_sender_id": "200"}},
	))
	message, err := runtime.DecodeExternalPluginMessage(map[string]any{"platform": "qq", "user_id": "100", "raw_message": []any{segment}})
	if err != nil {
		t.Fatal(err)
	}
	original := message.MessageParts[0].Forward
	if original == nil {
		t.Fatal("missing forwarded nodes")
	}
	state := identity.State{Aliases: map[string]map[string]string{"qq": {"100": "first-openid", "200": "second-openid"}}}
	normalized := identity.NormalizeComponents([]messagecomponent.Component{*original}, state, "qq")[0].(messagecomponent.ForwardNodeComponent)
	message.MessageParts[0].Forward = &normalized
	_, _, drafts := messagereceive.ParseMessageContent(context.Background(), message, nil)
	if len(drafts) != 2 || drafts[1].Parent != drafts[0].Handle {
		t.Fatalf("missing nested draft: %#v", drafts)
	}
	sequence, err := messagecomponent.DecodeDBRecord(messagereceive.BuildRawContent(message, nil))
	if err != nil {
		t.Fatal(err)
	}
	stored := sequence.Components[0].(messagecomponent.ForwardNodeComponent).ForwardComponents[0]
	nested := stored.Content[0].(messagecomponent.ForwardNodeComponent).ForwardComponents[0]
	at := nested.Content[0].(messagecomponent.AtComponent)
	reply := nested.Content[1].(messagecomponent.ReplyComponent)
	if *stored.UserID != "first-openid" || *nested.UserID != "second-openid" || at.TargetUserID != "first-openid" || *reply.SenderID != "second-openid" || *original.ForwardComponents[0].UserID != "100" {
		t.Fatalf("unexpected identities: stored=%#v nested=%#v at=%#v reply=%#v", stored, nested, at, reply)
	}
}

func TestPersonMigrationRechecksConflictsAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	directory := filepath.Join(root, "data")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE person_info (id INTEGER PRIMARY KEY, platform TEXT, user_id TEXT, person_id TEXT UNIQUE,
		individual_id TEXT, user_nickname TEXT, know_counts INTEGER, group_cardname TEXT)`)
	exec(`CREATE TABLE chat_sessions (session_id TEXT PRIMARY KEY, user_id TEXT)`)
	exec(`INSERT INTO chat_sessions VALUES ('old-session', '100')`)
	exec(`INSERT INTO person_info VALUES (1,'qq','100',?,'source-owner','Alice',2,'[]'),
		(2,'qq','openid',?,'target-owner','Bob',5,'[]')`, identity.PersonID("qq", "100"), identity.PersonID("qq", "openid"))
	preview := func(decisions map[string]string) services.PersonMigrationPlan {
		t.Helper()
		plan, err := services.PreviewPersonMigration(ctx, root, "qq", "100", "openid", decisions)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	initial := preview(nil)
	if initial.Ready || len(initial.Conflicts) != 2 {
		t.Fatalf("unexpected preview: %#v", initial.Conflicts)
	}
	decisions := map[string]string{}
	for _, conflict := range initial.Conflicts {
		decisions[conflict.ID] = "target"
	}
	reviewed := preview(decisions)
	exec(`UPDATE person_info SET user_nickname='Changed' WHERE user_id='openid'`)
	if _, err := services.ExecutePersonMigration(ctx, root, reviewed); !errors.Is(err, services.ErrPersonMigrationStale) {
		t.Fatalf("changed conflict should be stale, got %v", err)
	}
	reviewed = preview(decisions)
	path := filepath.Join(root, "plan.json")
	if err := services.WritePersonMigrationJSON(path, reviewed); err != nil {
		t.Fatal(err)
	}
	var saved services.PersonMigrationPlan
	if err := services.ReadPersonMigrationJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE person_info SET know_counts=7 WHERE user_id='100'`)
	job, err := services.ExecutePersonMigration(ctx, root, saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := services.RecoverPersonMigration(ctx, root, job); err != nil {
		t.Fatal(err)
	}
	var owner, nickname, historicalUser string
	var count, people int
	if err := db.QueryRow(`SELECT individual_id,user_nickname,know_counts FROM person_info WHERE user_id='openid'`).Scan(&owner, &nickname, &count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM person_info`).Scan(&people); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT user_id FROM chat_sessions WHERE session_id='old-session'`).Scan(&historicalUser); err != nil {
		t.Fatal(err)
	}
	state, err := identity.Load(ctx, db, root)
	if err != nil {
		t.Fatal(err)
	}
	if owner != "target-owner" || nickname != "Changed" || count != 12 || people != 1 || historicalUser != "100" || state.ResolveUserID("qq", "100") != "openid" {
		t.Fatalf("owner=%q nickname=%q count=%d people=%d historicalUser=%q aliases=%#v", owner, nickname, count, people, historicalUser, state.Aliases)
	}
}
