package services

// 同实例聊天流记忆归属复制；内容与向量保持稳定，覆盖操作可事务性恢复。
// 对齐 A_memorix/core/runtime/services/chat_migration_service.py。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
)

// ChatMemoryMigration 聊天流长期记忆迁移
type ChatMemoryMigration struct {
	Store *storage.Store
}

func memJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func memQuery(store *storage.Store, query string, args ...any) ([]map[string]any, error) {
	return store.Query(query, args...)
}

func memRows(tx *storage.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			value := values[index]
			if bytes, ok := value.([]byte); ok {
				value = string(bytes)
			}
			row[column] = value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func memString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func memInt(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

// Snapshot 读取来源与目标聊天流的记忆归属
func (m *ChatMemoryMigration) Snapshot(source, target string) (map[string]any, error) {
	allParagraphs, err := memQuery(m.Store,
		"SELECT * FROM paragraphs WHERE COALESCE(is_deleted, 0)=0 ORDER BY hash")
	if err != nil {
		return nil, err
	}
	paragraphs := make([]map[string]any, 0)
	for _, row := range allParagraphs {
		kind, ids := paragraphScopeIdentityOf(row)
		if kind != "chat" {
			continue
		}
		_, fromSource := ids[source]
		_, fromTarget := ids[target]
		if !fromSource && !fromTarget {
			continue
		}
		scopes := make([]string, 0, len(ids))
		for id := range ids {
			scopes = append(scopes, id)
		}
		sort.Strings(scopes)
		paragraphs = append(paragraphs, map[string]any{"row": row, "scopes": scopes})
	}
	images, err := memQuery(m.Store,
		"SELECT * FROM image_occurrences WHERE scope_type='chat' AND chat_id IN (?, ?) AND status='active' ORDER BY occurrence_id",
		source, target)
	if err != nil {
		return nil, err
	}
	refs, err := memQuery(m.Store,
		"SELECT * FROM external_memory_refs WHERE json_valid(metadata_json) AND json_extract(metadata_json, '$.chat_id') IN (?, ?) ORDER BY external_id",
		source, target)
	if err != nil {
		return nil, err
	}
	checkpoints, err := memQuery(m.Store,
		"SELECT * FROM chat_summary_checkpoints WHERE chat_id IN (?, ?) ORDER BY external_id", source, target)
	if err != nil {
		return nil, err
	}
	episodes, err := memQuery(m.Store,
		"SELECT * FROM episodes WHERE source IN (?, ?) ORDER BY episode_id",
		"chat_summary:"+source, "chat_summary:"+target)
	if err != nil {
		return nil, err
	}
	facts, err := memQuery(m.Store,
		"SELECT * FROM fact_claims WHERE scope_type='chat' AND scope_id IN (?, ?) ORDER BY claim_id",
		source, target)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"paragraphs": paragraphs, "images": images, "refs": refs, "checkpoints": checkpoints,
		"episodes": episodes, "facts": facts,
	}
	episodeIDs := make([]any, 0, len(episodes))
	for _, row := range episodes {
		episodeIDs = append(episodeIDs, row["episode_id"])
	}
	claimIDs := make([]any, 0, len(facts))
	for _, row := range facts {
		claimIDs = append(claimIDs, row["claim_id"])
	}
	imageIDs := make([]any, 0, len(images))
	for _, row := range images {
		imageIDs = append(imageIDs, row["occurrence_id"])
	}
	children := []struct {
		name   string
		column string
		ids    []any
	}{
		{"episode_paragraphs", "episode_id", episodeIDs},
		{"fact_evidence", "claim_id", claimIDs},
		{"image_observations", "occurrence_id", imageIDs},
		{"image_memory_links", "occurrence_id", imageIDs},
	}
	for _, child := range children {
		rows, queryErr := memQuery(m.Store,
			fmt.Sprintf("SELECT * FROM %s WHERE %s IN (SELECT value FROM json_each(?)) ORDER BY rowid",
				child.name, child.column), memJSON(child.ids))
		if queryErr != nil {
			return nil, queryErr
		}
		result[child.name] = rows
	}
	transitions, err := memQuery(m.Store,
		"SELECT * FROM fact_transitions WHERE old_claim_id IN (SELECT value FROM json_each(?)) OR new_claim_id IN (SELECT value FROM json_each(?)) ORDER BY transition_id",
		memJSON(claimIDs), memJSON(claimIDs))
	if err != nil {
		return nil, err
	}
	result["fact_transitions"] = transitions
	sourceParagraphs, targetParagraphs := 0, 0
	for _, item := range paragraphs {
		scopes, _ := item["scopes"].([]string)
		for _, scope := range scopes {
			if scope == source {
				sourceParagraphs++
			}
			if scope == target {
				targetParagraphs++
			}
		}
	}
	sourceImages, targetImages, sourceFacts, targetFacts, sourceEpisodes, targetEpisodes := 0, 0, 0, 0, 0, 0
	for _, row := range images {
		if memString(row["chat_id"]) == source {
			sourceImages++
		} else if memString(row["chat_id"]) == target {
			targetImages++
		}
	}
	for _, row := range facts {
		if memString(row["scope_id"]) == source {
			sourceFacts++
		} else if memString(row["scope_id"]) == target {
			targetFacts++
		}
	}
	for _, row := range episodes {
		if memString(row["source"]) == "chat_summary:"+source {
			sourceEpisodes++
		} else if memString(row["source"]) == "chat_summary:"+target {
			targetEpisodes++
		}
	}
	result["fingerprint"] = digestText(memJSON(result))
	result["counts"] = map[string]any{
		"source": sourceParagraphs, "target": targetParagraphs,
		"source_images": sourceImages, "target_images": targetImages,
		"source_facts": sourceFacts, "target_facts": targetFacts,
		"source_episodes": sourceEpisodes, "target_episodes": targetEpisodes,
	}
	return result, nil
}

// Apply 复制记忆归属并记录可恢复变更
func (m *ChatMemoryMigration) Apply(jobID, source, target, expected string) (map[string]any, error) {
	var counts map[string]any
	err := m.Store.Transaction(true, func(tx *storage.Tx) error {
		if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS chat_scope_transfers (id TEXT PRIMARY KEY, status TEXT NOT NULL, changes TEXT NOT NULL)"); err != nil {
			return err
		}
		previous, err := memRows(tx, "SELECT status FROM chat_scope_transfers WHERE id=?", jobID)
		if err != nil {
			return err
		}
		if len(previous) > 0 {
			if memString(previous[0]["status"]) == "applied" {
				counts = map[string]any{}
				return nil
			}
			return fmt.Errorf("该记忆迁移已恢复，请重新创建迁移预览")
		}
		snapshot, err := m.Snapshot(source, target)
		if err != nil {
			return err
		}
		if memString(snapshot["fingerprint"]) != expected {
			return fmt.Errorf("长期记忆已变化，请重新预览")
		}
		changes := make([]map[string]any, 0)
		update := func(table, key string, before, patch map[string]any) error {
			columns := memSortedKeys(patch)
			assignments := make([]string, len(columns))
			values := make([]any, 0, len(columns)+1)
			for index, column := range columns {
				assignments[index] = quoteMem(column) + "=?"
				values = append(values, patch[column])
			}
			values = append(values, before[key])
			if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE %s=?", quoteMem(table),
				strings.Join(assignments, ","), quoteMem(key)), values...); err != nil {
				return err
			}
			beforeSnapshot := make(map[string]any, len(patch))
			for _, column := range columns {
				beforeSnapshot[column] = before[column]
			}
			changes = append(changes, map[string]any{
				"table": table, "key": key, "id": before[key], "before": beforeSnapshot, "after": patch,
			})
			return nil
		}
		insert := func(table string, keys []string, row map[string]any) error {
			if err := memInsert(tx, table, row); err != nil {
				return err
			}
			keyValues := make(map[string]any, len(keys))
			for _, key := range keys {
				keyValues[key] = row[key]
			}
			changes = append(changes, map[string]any{
				"table": table, "keys": keyValues, "before": nil, "after": row,
			})
			return nil
		}
		remove := func(table string, keys []string, row map[string]any) error {
			where := make([]string, 0, len(keys))
			values := make([]any, 0, len(keys))
			identity := make(map[string]any, len(keys))
			for _, key := range keys {
				where = append(where, quoteMem(key)+"=?")
				values = append(values, row[key])
				identity[key] = row[key]
			}
			if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", quoteMem(table),
				strings.Join(where, " AND ")), values...); err != nil {
				return err
			}
			changes = append(changes, map[string]any{
				"table": table, "keys": identity, "before": row, "after": nil,
			})
			return nil
		}

		paragraphs, _ := snapshot["paragraphs"].([]map[string]any)
		for _, item := range paragraphs {
			row, _ := item["row"].(map[string]any)
			scopes := make(map[string]struct{})
			if values, ok := item["scopes"].([]string); ok {
				for _, value := range values {
					scopes[value] = struct{}{}
				}
			}
			delete(scopes, target)
			if _, ok := scopes[source]; ok {
				scopes[target] = struct{}{}
			}
			metadata := map[string]any{}
			_ = json.Unmarshal([]byte(memString(row["metadata"])), &metadata)
			if metadata == nil {
				metadata = map[string]any{}
			}
			for _, key := range []string{"chat_id", "session_id", "stream_id", "chat_ids", "session_ids", "stream_ids"} {
				delete(metadata, key)
			}
			scopeList := make([]string, 0, len(scopes))
			for scope := range scopes {
				scopeList = append(scopeList, scope)
			}
			sort.Strings(scopeList)
			metadata["scope_type"] = "chat"
			metadata["chat_ids"] = scopeList
			patch := map[string]any{"metadata": memJSON(metadata)}
			if len(scopes) == 0 {
				archive := fmt.Sprintf("migration_archive:%s:%s", jobID, memString(row["hash"]))
				patch["source"] = archive
				metadata["source"] = archive
				patch["metadata"] = memJSON(metadata)
			} else if _, ok := scopes[source]; ok {
				metadata["chat_id"] = source
			} else {
				metadata["chat_id"] = scopeList[0]
			}
			if _, ok := patch["source"]; !ok {
				patch["metadata"] = memJSON(metadata)
			}
			if err := update("paragraphs", "hash", row, patch); err != nil {
				return err
			}
		}

		episodeIDs := make(map[string]string)
		episodes, _ := snapshot["episodes"].([]map[string]any)
		for _, row := range episodes {
			if memString(row["source"]) == "chat_summary:"+source {
				episodeIDs[memString(row["episode_id"])] = fmt.Sprintf("migration_%s_%s", jobID, memString(row["episode_id"]))
			}
		}
		for _, row := range episodes {
			if memString(row["source"]) == "chat_summary:"+target {
				if err := update("episodes", "episode_id", row, map[string]any{"source": "migration_archive:" + jobID}); err != nil {
					return err
				}
				continue
			}
			clone := cloneMemRow(row)
			clone["episode_id"] = episodeIDs[memString(row["episode_id"])]
			clone["source"] = "chat_summary:" + target
			if err := insert("episodes", []string{"episode_id"}, clone); err != nil {
				return err
			}
		}
		episodeParagraphs, _ := snapshot["episode_paragraphs"].([]map[string]any)
		for _, row := range episodeParagraphs {
			if _, ok := episodeIDs[memString(row["episode_id"])]; !ok {
				continue
			}
			clone := cloneMemRow(row)
			clone["episode_id"] = episodeIDs[memString(row["episode_id"])]
			if err := insert("episode_paragraphs", []string{"episode_id", "paragraph_hash"}, clone); err != nil {
				return err
			}
		}

		claimIDs := make(map[string]string)
		facts, _ := snapshot["facts"].([]map[string]any)
		for _, row := range facts {
			if memString(row["scope_id"]) != source {
				continue
			}
			claimID, err := storage.ClaimIdentity("chat", target, memString(row["fact_key"]),
				memString(row["value_normalized"]), memString(row["polarity"]))
			if err != nil {
				return err
			}
			claimIDs[memString(row["claim_id"])] = claimID
		}
		removedClaims := make(map[string]struct{})
		for _, row := range facts {
			if memString(row["scope_id"]) == target {
				removedClaims[memString(row["claim_id"])] = struct{}{}
			}
		}
		transitions, _ := snapshot["fact_transitions"].([]map[string]any)
		for _, row := range transitions {
			_, oldRemoved := removedClaims[memString(row["old_claim_id"])]
			_, newRemoved := removedClaims[memString(row["new_claim_id"])]
			if oldRemoved || newRemoved {
				if err := remove("fact_transitions", []string{"transition_id"}, row); err != nil {
					return err
				}
			}
		}
		evidence, _ := snapshot["fact_evidence"].([]map[string]any)
		for _, row := range evidence {
			if _, removed := removedClaims[memString(row["claim_id"])]; removed {
				if err := remove("fact_evidence",
					[]string{"claim_id", "evidence_type", "evidence_id", "stance"}, row); err != nil {
					return err
				}
			}
		}
		for _, row := range facts {
			if memString(row["scope_id"]) == target {
				if err := remove("fact_claims", []string{"claim_id"}, row); err != nil {
					return err
				}
			}
		}
		for _, row := range facts {
			if memString(row["scope_id"]) != source {
				continue
			}
			conflictGroup, err := storage.ConflictGroup("chat", target, memString(row["fact_key"]),
				memString(row["value_normalized"]), memString(row["cardinality"]))
			if err != nil {
				return err
			}
			clone := cloneMemRow(row)
			clone["claim_id"] = claimIDs[memString(row["claim_id"])]
			clone["scope_id"] = target
			clone["conflict_group"] = conflictGroup
			if err := insert("fact_claims", []string{"claim_id"}, clone); err != nil {
				return err
			}
		}
		for _, row := range evidence {
			if _, ok := claimIDs[memString(row["claim_id"])]; !ok {
				continue
			}
			clone := cloneMemRow(row)
			clone["claim_id"] = claimIDs[memString(row["claim_id"])]
			if replacement, ok := episodeIDs[memString(row["evidence_id"])]; ok {
				clone["evidence_id"] = replacement
			}
			if err := insert("fact_evidence",
				[]string{"claim_id", "evidence_type", "evidence_id", "stance"}, clone); err != nil {
				return err
			}
		}
		for _, row := range transitions {
			oldClaim, oldOK := claimIDs[memString(row["old_claim_id"])]
			newClaim, newOK := claimIDs[memString(row["new_claim_id"])]
			if !oldOK && !newOK {
				continue
			}
			clone := cloneMemRow(row)
			if oldOK {
				clone["old_claim_id"] = oldClaim
			}
			if newOK {
				clone["new_claim_id"] = newClaim
			}
			next, err := memRows(tx, "SELECT COALESCE(MAX(transition_id),0)+1 AS next FROM fact_transitions")
			if err != nil {
				return err
			}
			if len(next) > 0 {
				clone["transition_id"] = next[0]["next"]
			}
			if err := insert("fact_transitions", []string{"transition_id"}, clone); err != nil {
				return err
			}
		}

		images, _ := snapshot["images"].([]map[string]any)
		observations, _ := snapshot["image_observations"].([]map[string]any)
		links, _ := snapshot["image_memory_links"].([]map[string]any)
		for _, row := range images {
			if memString(row["chat_id"]) == target {
				if err := update("image_occurrences", "occurrence_id", row, map[string]any{"scope_type": "archive"}); err != nil {
					return err
				}
				continue
			}
			clone := cloneMemRow(row)
			clone["occurrence_id"] = fmt.Sprintf("migration_%s_%s", jobID, memString(row["occurrence_id"]))
			clone["chat_id"] = target
			clone["external_ref"] = "migration_" + jobID + "_" + memString(row["external_ref"])
			clone["installation_id"] = ""
			if err := insert("image_occurrences", []string{"occurrence_id"}, clone); err != nil {
				return err
			}
			for _, child := range []struct {
				table string
				key   string
			}{{"image_observations", "observation_id"}, {"image_memory_links", "link_id"}} {
				rows := observations
				if child.table == "image_memory_links" {
					rows = links
				}
				for _, item := range rows {
					if memString(item["occurrence_id"]) != memString(row["occurrence_id"]) {
						continue
					}
					copied := cloneMemRow(item)
					copied[child.key] = fmt.Sprintf("migration_%s_%s", jobID, memString(item[child.key]))
					copied["occurrence_id"] = clone["occurrence_id"]
					if child.table == "image_observations" {
						for _, field := range []string{"supersedes_id", "superseded_by"} {
							if value := memString(item[field]); value != "" {
								copied[field] = fmt.Sprintf("migration_%s_%s", jobID, value)
							}
						}
					}
					if child.table == "image_memory_links" && memString(item["target_type"]) == "episode" {
						if replacement, ok := episodeIDs[memString(item["target_id"])]; ok {
							copied["target_id"] = replacement
						}
					}
					if err := insert(child.table, []string{child.key}, copied); err != nil {
						return err
					}
				}
			}
		}

		refs, _ := snapshot["refs"].([]map[string]any)
		checkpoints, _ := snapshot["checkpoints"].([]map[string]any)
		for _, row := range refs {
			metadata := map[string]any{}
			_ = json.Unmarshal([]byte(memString(row["metadata_json"])), &metadata)
			owner := memString(metadata["chat_id"])
			if owner == target {
				metadata["chat_id"] = "migration_archive:" + jobID
				if err := update("external_memory_refs", "external_id", row, map[string]any{"metadata_json": memJSON(metadata)}); err != nil {
					return err
				}
				continue
			}
			clone := cloneMemRow(row)
			clone["external_id"] = "migration_" + jobID + "_" + memString(row["external_id"])
			metadata["chat_id"] = target
			clone["metadata_json"] = memJSON(metadata)
			if err := insert("external_memory_refs", []string{"external_id"}, clone); err != nil {
				return err
			}
		}
		for _, row := range checkpoints {
			if memString(row["chat_id"]) == target {
				if err := update("chat_summary_checkpoints", "external_id", row,
					map[string]any{"chat_id": "migration_archive:" + jobID}); err != nil {
					return err
				}
				continue
			}
			clone := cloneMemRow(row)
			clone["external_id"] = "migration_" + jobID + "_" + memString(row["external_id"])
			clone["chat_id"] = target
			if err := insert("chat_summary_checkpoints", []string{"external_id"}, clone); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("INSERT INTO chat_scope_transfers VALUES (?, 'applied', ?)", jobID, memJSON(changes)); err != nil {
			return err
		}
		resultCounts, _ := snapshot["counts"].(map[string]any)
		counts = resultCounts
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "counts": counts}, nil
}

// Restore 回滚一次已应用的记忆迁移
func (m *ChatMemoryMigration) Restore(jobID string) (map[string]any, error) {
	err := m.Store.Transaction(true, func(tx *storage.Tx) error {
		if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS chat_scope_transfers (id TEXT PRIMARY KEY, status TEXT NOT NULL, changes TEXT NOT NULL)"); err != nil {
			return err
		}
		record, err := memRows(tx, "SELECT status, changes FROM chat_scope_transfers WHERE id=?", jobID)
		if err != nil {
			return err
		}
		if len(record) == 0 || memString(record[0]["status"]) == "restored" {
			return nil
		}
		var changes []map[string]any
		if err := json.Unmarshal([]byte(memString(record[0]["changes"])), &changes); err != nil {
			return err
		}
		for index := len(changes) - 1; index >= 0; index-- {
			change := changes[index]
			table := memString(change["table"])
			keys := map[string]any{}
			if raw, ok := change["keys"].(map[string]any); ok {
				keys = raw
			} else {
				keys[memString(change["key"])] = change["id"]
			}
			keyNames := memSortedKeys(keys)
			where := make([]string, len(keyNames))
			values := make([]any, len(keyNames))
			for position, name := range keyNames {
				where[position] = quoteMem(name) + "=?"
				values[position] = keys[name]
			}
			rows, err := memRows(tx, fmt.Sprintf("SELECT * FROM %s WHERE %s", quoteMem(table),
				strings.Join(where, " AND ")), values...)
			if err != nil {
				return err
			}
			after, hasAfter := change["after"].(map[string]any)
			before, hasBefore := change["before"].(map[string]any)
			if !hasAfter || after == nil {
				if len(rows) > 0 {
					return fmt.Errorf("记忆已在迁移后被修改，不能自动覆盖恢复")
				}
				if hasBefore && before != nil {
					if err := memInsert(tx, table, before); err != nil {
						return err
					}
				}
				continue
			}
			if len(rows) == 0 {
				return fmt.Errorf("记忆已在迁移后被修改，不能自动覆盖恢复")
			}
			for name, value := range after {
				if !memValueEqual(rows[0][name], value) {
					return fmt.Errorf("记忆已在迁移后被修改，不能自动覆盖恢复")
				}
			}
			if !hasBefore || before == nil {
				if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", quoteMem(table),
					strings.Join(where, " AND ")), values...); err != nil {
					return err
				}
				continue
			}
			columns := memSortedKeys(before)
			assignments := make([]string, len(columns))
			updateValues := make([]any, 0, len(columns)+len(values))
			for position, name := range columns {
				assignments[position] = quoteMem(name) + "=?"
				updateValues = append(updateValues, before[name])
			}
			updateValues = append(updateValues, values...)
			if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE %s", quoteMem(table),
				strings.Join(assignments, ","), strings.Join(where, " AND ")), updateValues...); err != nil {
				return err
			}
		}
		_, err = tx.Exec("UPDATE chat_scope_transfers SET status='restored' WHERE id=?", jobID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func quoteMem(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func memSortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMemRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}

func memInsert(tx *storage.Tx, table string, row map[string]any) error {
	columns := memSortedKeys(row)
	quoted := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	values := make([]any, len(columns))
	for index, column := range columns {
		quoted[index] = quoteMem(column)
		placeholders[index] = "?"
		values[index] = row[column]
	}
	_, err := tx.Exec(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteMem(table),
		strings.Join(quoted, ","), strings.Join(placeholders, ",")), values...)
	return err
}

func memValueEqual(left, right any) bool {
	leftNumber, leftOK := memNumber(left)
	rightNumber, rightOK := memNumber(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	return memString(left) == memString(right)
}

func memNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	default:
		return 0, false
	}
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
