package migration

import (
	"context"
	"fmt"
	"strings"
)

func migrateV35(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "maisaka_monitor_events")
	if err != nil {
		return err
	}
	ddl := "CREATE TABLE maisaka_monitor_events (" +
		"event_id " + serialPK(q) + ", " +
		"event_type TEXT NOT NULL, " +
		"session_id TEXT NOT NULL DEFAULT '', " +
		"timestamp DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"schema_version INTEGER NOT NULL DEFAULT 1, " +
		"payload_json TEXT NOT NULL DEFAULT '{}', " +
		"created_at " + timestampType(q) + " NOT NULL DEFAULT CURRENT_TIMESTAMP)"
	if !exists {
		if err := q.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	indexes := [][2]string{
		{"ix_maisaka_monitor_events_event_type", "event_type"},
		{"ix_maisaka_monitor_events_session_id", "session_id"},
		{"ix_maisaka_monitor_events_session_event", "session_id,event_id"},
		{"ix_maisaka_monitor_events_type_event", "event_type,event_id"},
		{"ix_maisaka_monitor_events_timestamp", "timestamp"},
		{"ix_maisaka_monitor_events_created_at", "created_at"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "maisaka_monitor_events", item[1]); err != nil {
			return err
		}
	}
	return nil
}

func migrateV36(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "mai_messages")
	if err != nil || !exists {
		return err
	}
	return ensureIndex(ctx, q, "ix_message_logs_platform_external_message_id", "mai_messages", "platform,external_message_id")
}

func migrateV37(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "maisaka_reply_effects")
	if err != nil {
		return err
	}
	ddl := "CREATE TABLE maisaka_reply_effects (" +
		"effect_id TEXT PRIMARY KEY, " +
		"session_id TEXT NOT NULL DEFAULT '', " +
		"session_name TEXT NOT NULL DEFAULT '', " +
		"chat_type TEXT NOT NULL DEFAULT 'group', " +
		"status TEXT NOT NULL DEFAULT '', " +
		"created_at " + timestampType(q) + ", " +
		"finalized_at " + timestampType(q) + ", " +
		"strategy_primary TEXT NOT NULL DEFAULT 'other', " +
		"model_name TEXT NOT NULL DEFAULT '', " +
		"prompt_fingerprint TEXT NOT NULL DEFAULT '', " +
		"scorer_version INTEGER NOT NULL DEFAULT 2, " +
		"response_score DOUBLE PRECISION, " +
		"reception_score DOUBLE PRECISION, " +
		"conversation_score DOUBLE PRECISION, " +
		"raw_score DOUBLE PRECISION, " +
		"relative_score DOUBLE PRECISION, " +
		"confidence DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"record_json TEXT NOT NULL DEFAULT '{}')"
	if !exists {
		if err := q.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	indexes := [][2]string{
		{"ix_maisaka_reply_effects_session_id", "session_id"},
		{"ix_maisaka_reply_effects_status", "status"},
		{"ix_maisaka_reply_effects_created_at", "created_at"},
		{"ix_maisaka_reply_effects_finalized_at", "finalized_at"},
		{"ix_maisaka_reply_effects_session_finalized", "session_id,finalized_at"},
		{"ix_maisaka_reply_effects_strategy_finalized", "strategy_primary,finalized_at"},
		{"ix_maisaka_reply_effects_model_prompt", "model_name,prompt_fingerprint"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "maisaka_reply_effects", item[1]); err != nil {
			return err
		}
	}
	return nil
}

func migrateV38(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "maisaka_reply_effects")
	if err != nil || !exists {
		return err
	}
	if err := ensureColumn(ctx, q, "maisaka_reply_effects", "request_fingerprint", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := backfillReplyFingerprints(ctx, q); err != nil {
		return err
	}
	return ensureIndex(ctx, q, "ix_reply_effect_request_fingerprint", "maisaka_reply_effects", "request_fingerprint")
}

// backfillReplyFingerprints 对齐 v37_to_v38.py：重算请求指纹与稳定 Prompt 版本指纹
func backfillReplyFingerprints(ctx context.Context, q querier) error {
	rows, err := q.Query(ctx,
		"SELECT effect_id, COALESCE(request_fingerprint,''), COALESCE(prompt_fingerprint,''), CASE WHEN COALESCE(record_json,'')='' THEN '{}' ELSE record_json END FROM maisaka_reply_effects")
	if err != nil {
		return err
	}
	type pending struct {
		id         string
		recordJSON string
		requestFP  string
		promptFP   string
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		var raw string
		if err := rows.Scan(&item.id, &item.requestFP, &item.promptFP, &raw); err != nil {
			rows.Close()
			return err
		}
		record, err := decodeRecordObject(raw)
		if err != nil {
			rows.Close()
			return err
		}
		reply := mapValue(record["reply"])
		metadata := mapValue(reply["reply_metadata"])
		_, calculatedRequestFingerprint, promptVersionFingerprint := extractGenerationFingerprints(metadata)
		requestFingerprint := item.requestFP
		if requestFingerprint == "" {
			requestFingerprint = item.promptFP
			if requestFingerprint == "" {
				requestFingerprint = calculatedRequestFingerprint
			}
			item.promptFP = promptVersionFingerprint
		}
		reply["request_fingerprint"] = requestFingerprint
		reply["prompt_fingerprint"] = item.promptFP
		record["reply"] = reply
		encoded, err := marshalRecordJSON(record)
		if err != nil {
			rows.Close()
			return err
		}
		item.recordJSON = encoded
		item.requestFP = requestFingerprint
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		if err := q.Exec(ctx,
			"UPDATE maisaka_reply_effects SET request_fingerprint=?, prompt_fingerprint=?, record_json=? WHERE effect_id=?",
			item.requestFP, item.promptFP, item.recordJSON, item.id); err != nil {
			return err
		}
	}
	return nil
}

func migrateV39(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "maisaka_reply_effects")
	if err != nil || !exists {
		return err
	}
	if err := ensureColumn(ctx, q, "maisaka_reply_effects", "record_blob", blobType(q)); err != nil {
		return err
	}
	rows, err := q.Query(ctx, "SELECT effect_id, CASE WHEN COALESCE(record_json,'')='' THEN '{}' ELSE record_json END, record_blob FROM maisaka_reply_effects")
	if err != nil {
		return err
	}
	type pending struct {
		id   string
		blob []byte
	}
	items := make([]pending, 0)
	for rows.Next() {
		var id, raw string
		var blob []byte
		if err := rows.Scan(&id, &raw, &blob); err != nil {
			rows.Close()
			return err
		}
		if len(blob) > 0 {
			if _, err := decodeRecordPayload(raw, blob); err != nil {
				rows.Close()
				return err
			}
			continue
		}
		payload, err := decodeRecordPayload(raw, nil)
		if err != nil {
			rows.Close()
			return err
		}
		encoded, err := encodeRecordPayload(payload)
		if err != nil {
			rows.Close()
			return err
		}
		decoded, err := decodeRecordPayload("{}", encoded)
		if err != nil {
			rows.Close()
			return err
		}
		if !recordsEqual(decoded, payload) {
			rows.Close()
			return fmt.Errorf("回复效果记录压缩校验失败：%s", id)
		}
		items = append(items, pending{id: id, blob: encoded})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		if err := q.Exec(ctx, "UPDATE maisaka_reply_effects SET record_json='{}', record_blob=? WHERE effect_id=?", item.blob, item.id); err != nil {
			return err
		}
	}
	files, err := replyEffectMirrorFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := compressReplyEffectMirror(file); err != nil {
			return err
		}
	}
	if q.Dialect() == DialectSQLite {
		return scheduleMigrationMaintenance(ctx, q, func(ctx context.Context, q querier) error {
			return q.Exec(ctx, "VACUUM")
		})
	}
	return nil
}

func migrateV40(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "bot_platform_accounts")
	if err != nil {
		return err
	}
	ddl := "CREATE TABLE bot_platform_accounts (" +
		"id " + serialPK(q) + ", " +
		"platform TEXT NOT NULL, " +
		"account_id TEXT NOT NULL, " +
		"disabled BOOLEAN NOT NULL DEFAULT FALSE, " +
		"first_seen_at " + timestampType(q) + ", " +
		"last_seen_at " + timestampType(q) + ", " +
		"disabled_at " + timestampType(q) + ", " +
		"last_source TEXT NOT NULL DEFAULT '', " +
		"last_adapter_id TEXT, " +
		"last_plugin_id TEXT, " +
		"last_gateway_name TEXT)"
	if !exists {
		if err := q.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	indexes := [][2]string{
		{"ix_bot_platform_accounts_platform", "platform"},
		{"ix_bot_platform_accounts_account_id", "account_id"},
		{"ix_bot_platform_accounts_disabled", "disabled"},
		{"ix_bot_platform_accounts_last_seen_at", "last_seen_at"},
	}
	for _, item := range indexes {
		if err := ensureIndex(ctx, q, item[0], "bot_platform_accounts", item[1]); err != nil {
			return err
		}
	}
	// 唯一约束用命名唯一索引而非表级 UNIQUE：AutoMigrate 重建该表时 glebarez 会把 DDL 里的
	// UNIQUE 关键字误解析为列名，导致 bot_platform_accounts__temp 缺列报错
	if err := ensureUniqueIndex(ctx, q, "uq_bot_platform_accounts_platform_account", "bot_platform_accounts", "platform,account_id"); err != nil {
		return err
	}
	return nil
}

func migrateV41(ctx context.Context, q querier) error {
	hasMessages, err := tableExists(ctx, q, "mai_messages")
	if err != nil || !hasMessages {
		return err
	}
	hasSessions, err := tableExists(ctx, q, "chat_sessions")
	if err != nil || !hasSessions {
		return err
	}
	for _, column := range []string{"account_id", "scope"} {
		statement := "UPDATE mai_messages SET " + column + "=COALESCE((SELECT cs." + column +
			" FROM chat_sessions cs WHERE cs.session_id=mai_messages.session_id),'') " +
			"WHERE COALESCE(mai_messages." + column + ",'')='' AND mai_messages.session_id IS NOT NULL AND mai_messages.session_id<>''"
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

var behaviorTables = []string{
	"behavior_experience_paths_v21", "behavior_scene_nodes_v22", "behavior_scene_tag_clusters_v22",
	"behavior_scene_clusters_v23", "behavior_actions_v24", "behavior_experience_paths_v24",
	"behavior_outcomes_v24", "behavior_scene_clusters_v27", "behavior_offline_import_records",
	"behavior_pattern_scene_links", "behavior_experience_scene_links", "behavior_scene_node_tags",
	"behavior_scene_action_edges", "behavior_action_outcome_edges", "behavior_scene_edges",
	"behavior_patterns", "behavior_experience_paths", "behavior_scene_clusters",
	"behavior_scene_tag_clusters", "behavior_scene_nodes", "behavior_action_nodes",
	"behavior_outcome_nodes", "behavior_actions", "behavior_outcomes",
}

func migrateV42(ctx context.Context, q querier) error {
	for _, table := range behaviorTables {
		if err := dropTable(ctx, q, table); err != nil {
			return err
		}
	}
	return nil
}

func migrateV43(ctx context.Context, q querier) error {
	return scheduleMigrationMaintenance(ctx, q, func(ctx context.Context, q querier) error {
		if err := q.Exec(ctx, "VACUUM"); err != nil {
			return err
		}
		if q.Dialect() != DialectSQLite {
			return nil
		}
		var busy, logFrames, checkpointed int
		if err := q.QueryRow(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
			return err
		}
		if busy != 0 {
			return fmt.Errorf("数据库瘦身后的 WAL 回写被占用，未完成空间回收")
		}
		return nil
	})
}

func migrateV44(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "model_task_usage")
	if err != nil {
		return err
	}
	ddl := "CREATE TABLE model_task_usage (" +
		"bucket_time " + timestampType(q) + " NOT NULL, " +
		"task_name TEXT NOT NULL, " +
		"model_name TEXT NOT NULL, " +
		"model_identifier TEXT NOT NULL, " +
		"provider_name TEXT NOT NULL, " +
		"request_count INTEGER NOT NULL DEFAULT 0, " +
		"failure_count INTEGER NOT NULL DEFAULT 0, " +
		"success_duration DOUBLE PRECISION NOT NULL DEFAULT 0, " +
		"PRIMARY KEY(bucket_time,task_name,model_name,model_identifier,provider_name))"
	if !exists {
		if err := q.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return ensureIndex(ctx, q, "ix_model_task_usage_bucket_time", "model_task_usage", "bucket_time")
}

func migrateV45(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "mai_messages")
	if err != nil || !exists {
		return err
	}
	if err := ensureColumn(ctx, q, "mai_messages", "received_at", timestampType(q)); err != nil {
		return err
	}
	return ensureIndex(ctx, q, "ix_message_logs_received_at", "mai_messages", "received_at")
}

func migrateV46(ctx context.Context, q querier) error {
	for _, table := range []string{"file_assets", "file_bindings", "file_parses"} {
		exists, err := tableExists(ctx, q, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("v46 需要 v17 文件表 %s", table)
		}
	}
	return nil
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		if text := stringValue(value); text != "" {
			return text
		}
	}
	return ""
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	case nil:
		return ""
	default:
		return ""
	}
}
