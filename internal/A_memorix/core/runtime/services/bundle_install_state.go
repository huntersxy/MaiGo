package services

// 完整记忆包状态重映射与写入，对齐 bundle_admin_service.py 的 _remap_full_state 与 _insert_full_state

import (
	"fmt"
	"reflect"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// buildFullStateTables 按固定表顺序复制 state.tables
func buildFullStateTables(raw map[string]any) map[string][]map[string]any {
	tables := make(map[string][]map[string]any, len(fullStateTableOrder))
	for _, table := range fullStateTableOrder {
		rows := make([]map[string]any, 0, len(asList(raw[table])))
		for _, rowValue := range asList(raw[table]) {
			row := asMap(rowValue)
			if row == nil {
				continue
			}
			rows = append(rows, copyBundleRow(row))
		}
		tables[table] = rows
	}
	return tables
}

// copyBundleRow 复制一行，避免重映射污染调用方数据
func copyBundleRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}

// validateFullStateHashes 校验完整包哈希与内容一致
func (s *BundleAdminService) validateFullStateHashes(tables map[string][]map[string]any) error {
	for _, row := range tables["paragraphs"] {
		expected := utils.ComputeParagraphHash(asString(row["content"]))
		if asString(row["hash"]) != expected {
			return fmt.Errorf("完整包包含段落内容与稳定哈希不一致的记录")
		}
	}
	for _, row := range tables["entities"] {
		expected := utils.ComputeEntityHash(asString(row["name"]))
		if asString(row["hash"]) != expected {
			return fmt.Errorf("完整包包含实体名称与稳定哈希不一致的记录")
		}
	}
	for _, row := range tables["relations"] {
		expected, err := s.Meta().ComputeRelationHash(asString(row["subject"]), asString(row["predicate"]), asString(row["object"]))
		if err != nil {
			return err
		}
		if asString(row["hash"]) != expected {
			return fmt.Errorf("完整包包含关系内容与稳定哈希不一致的记录")
		}
	}
	return nil
}

// fullStateOriginalChatIDs 提取完整包段落中记录的原始聊天流 ID
func fullStateOriginalChatIDs(paragraphs []map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	for _, row := range paragraphs {
		metadata := jsonObjectAny(row["metadata"])
		for _, key := range []string{"chat_ids", "session_ids", "stream_ids"} {
			collectBundleStringTokens(metadata[key], out)
		}
		for _, key := range []string{"chat_id", "session_id", "stream_id"} {
			value := strings.TrimSpace(asString(metadata[key]))
			if value != "" {
				out[value] = struct{}{}
			}
		}
		source := strings.TrimSpace(asString(row["source"]))
		if strings.HasPrefix(source, "chat_summary:") {
			value := strings.TrimSpace(strings.TrimPrefix(source, "chat_summary:"))
			if value != "" {
				out[value] = struct{}{}
			}
		}
	}
	return out
}

// collectBundleStringTokens 递归收集字符串值，对齐 _string_tokens
func collectBundleStringTokens(value any, out map[string]struct{}) {
	switch typed := value.(type) {
	case string:
		token := strings.TrimSpace(typed)
		if token != "" {
			out[token] = struct{}{}
		}
	case []any:
		for _, item := range typed {
			collectBundleStringTokens(item, out)
		}
	case map[string]any:
		for _, item := range typed {
			collectBundleStringTokens(item, out)
		}
	}
}

// remapFullState 重写包内 scope、chat 与派发主键，对齐 _remap_full_state
func (s *BundleAdminService) remapFullState(tables map[string][]map[string]any, packageID, installationID, scopeType, chatID string, originalChatIDs map[string]struct{}) map[string][]map[string]any {
	for _, paragraph := range tables["paragraphs"] {
		paragraph["vector_index"] = nil
		metadata := jsonObjectAny(paragraph["metadata"])
		metadata["scope_type"] = scopeType
		metadata["knowledge_package_id"] = packageID
		metadata["knowledge_package_installation_id"] = installationID
		if scopeType == "chat" {
			metadata["chat_id"] = chatID
		} else {
			delete(metadata, "chat_id")
		}
		paragraph["metadata"] = canonicalJSONText(metadata)
		source := asString(paragraph["source"])
		if scopeType == "chat" && strings.HasPrefix(source, "chat_summary:") {
			paragraph["source"] = "chat_summary:" + chatID
		}
	}
	for _, table := range []string{"entities", "relations", "deleted_relations"} {
		for _, row := range tables[table] {
			row["vector_index"] = nil
		}
	}

	episodeIDMap := map[string]string{}
	for _, episode := range tables["episodes"] {
		source := asString(episode["source"])
		if scopeType != "chat" || !strings.HasPrefix(source, "chat_summary:") {
			continue
		}
		oldEpisodeID := asString(episode["episode_id"])
		newSource := "chat_summary:" + chatID
		evidenceIDs := jsonListAny(episode["evidence_ids_json"])
		seed := map[string]any{
			"source":  newSource,
			"hashes":  evidenceIDs,
			"version": asString(episode["segmentation_version"]),
		}
		newEpisodeID, err := utils.ComputeHash(canonicalJSONText(seed))
		if err != nil {
			continue
		}
		episodeIDMap[oldEpisodeID] = newEpisodeID
		episode["episode_id"] = newEpisodeID
		episode["source"] = newSource
		episode["input_fingerprint"] = ""
	}
	for _, link := range tables["episode_paragraphs"] {
		oldEpisodeID := asString(link["episode_id"])
		if mapped, ok := episodeIDMap[oldEpisodeID]; ok {
			link["episode_id"] = mapped
		}
	}

	claimIDMap := map[string]string{}
	if scopeType == "chat" {
		for _, claim := range tables["fact_claims"] {
			if strings.ToLower(strings.TrimSpace(asString(claim["scope_type"]))) != "chat" {
				continue
			}
			scopeID := strings.TrimSpace(asString(claim["scope_id"]))
			if _, ok := originalChatIDs[scopeID]; !ok {
				continue
			}
			oldClaimID := asString(claim["claim_id"])
			claim["scope_id"] = chatID
			claimID := sha256Hex(canonicalJSONBytes(map[string]any{
				"scope_type": "chat",
				"scope_id":   chatID,
				"fact_key":   asString(claim["fact_key"]),
				"value":      asString(claim["value_normalized"]),
				"polarity":   asString(claim["polarity"]),
			}))
			claim["claim_id"] = claimID
			conflictPayload := map[string]any{
				"scope_type": "chat",
				"scope_id":   chatID,
				"fact_key":   asString(claim["fact_key"]),
			}
			if asString(claim["cardinality"]) == "set" {
				conflictPayload["value"] = asString(claim["value_normalized"])
			}
			claim["conflict_group"] = sha256Hex(canonicalJSONBytes(conflictPayload))
			claimIDMap[oldClaimID] = claimID
		}
	}
	for _, evidence := range tables["fact_evidence"] {
		oldClaimID := asString(evidence["claim_id"])
		if mapped, ok := claimIDMap[oldClaimID]; ok {
			evidence["claim_id"] = mapped
		}
		oldEvidenceID := asString(evidence["evidence_id"])
		if mapped, ok := episodeIDMap[oldEvidenceID]; ok {
			evidence["evidence_id"] = mapped
		}
	}
	for _, transition := range tables["fact_transitions"] {
		for _, key := range []string{"old_claim_id", "new_claim_id"} {
			oldClaimID := asString(transition[key])
			if oldClaimID == "" {
				continue
			}
			if mapped, ok := claimIDMap[oldClaimID]; ok {
				transition[key] = mapped
			}
		}
		oldEvidenceID := asString(transition["evidence_id"])
		if mapped, ok := episodeIDMap[oldEvidenceID]; ok {
			transition["evidence_id"] = mapped
		}
	}
	for _, snapshot := range tables["person_profile_snapshots"] {
		evidenceIDs := jsonListAny(snapshot["evidence_ids_json"])
		factClaimIDs := jsonListAny(snapshot["fact_claim_ids_json"])
		remappedEvidenceIDs := replaceBundleJSONTokens(evidenceIDs, episodeIDMap)
		remappedFactClaimIDs := replaceBundleJSONTokens(factClaimIDs, claimIDMap)
		if !reflect.DeepEqual(remappedEvidenceIDs, evidenceIDs) || !reflect.DeepEqual(remappedFactClaimIDs, factClaimIDs) {
			snapshot["evidence_fingerprint"] = ""
		}
		snapshot["evidence_ids_json"] = canonicalJSONText(remappedEvidenceIDs)
		snapshot["fact_claim_ids_json"] = canonicalJSONText(remappedFactClaimIDs)
	}
	return tables
}

// replaceBundleJSONTokens 递归替换 JSON 文本中的 ID
func replaceBundleJSONTokens(value any, replacements map[string]string) any {
	switch typed := value.(type) {
	case string:
		if mapped, ok := replacements[typed]; ok {
			return mapped
		}
		return typed
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, replaceBundleJSONTokens(item, replacements))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = replaceBundleJSONTokens(item, replacements)
		}
		return out
	default:
		return value
	}
}

// canonicalJSONBytes 返回紧凑 JSON 字节
func canonicalJSONBytes(value any) []byte {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return nil
	}
	return encoded
}

// insertFullStateTx 逐表写入完整包状态并登记资源归属
func (s *BundleAdminService) insertFullStateTx(tx *storage.Tx, tables map[string][]map[string]any, installationID string) (map[string]int, error) {
	inserted := make(map[string]int, len(fullStateTableOrder))
	for _, table := range fullStateTableOrder {
		rows := tables[table]
		inserted[table] = 0
		for _, sourceRow := range rows {
			row := copyBundleRow(sourceRow)
			if table == "person_profile_snapshots" {
				delete(row, "snapshot_id")
			} else if table == "fact_transitions" {
				delete(row, "transition_id")
			}
			created, lastID, err := insertRowTxResult(tx, table, row)
			if err != nil {
				return nil, err
			}
			identity := copyBundleRow(sourceRow)
			switch table {
			case "person_profile_snapshots":
				if created {
					identity["snapshot_id"] = lastID
				} else {
					existing, err := queryRows(tx,
						"SELECT snapshot_id FROM person_profile_snapshots WHERE person_id = ? AND profile_version = ? LIMIT 1",
						sourceRow["person_id"], sourceRow["profile_version"])
					if err != nil {
						return nil, err
					}
					if len(existing) == 0 {
						return nil, fmt.Errorf("无法解析完整记忆包中的人物画像快照主键")
					}
					identity["snapshot_id"] = existing[0]["snapshot_id"]
				}
			case "fact_transitions":
				if !created {
					return nil, fmt.Errorf("完整记忆包中的事实迁移记录未能写入")
				}
				identity["transition_id"] = lastID
			}
			if created {
				inserted[table]++
			}
			if err := s.registerPackageResource(tx, installationID, table, bundleResourceKey(table, identity), created); err != nil {
				return nil, err
			}
		}
	}
	return inserted, nil
}

// insertRowTxResult 按实际列插入一行并返回是否新建与自增主键
func insertRowTxResult(tx *storage.Tx, table string, row map[string]any) (bool, int64, error) {
	columnRows, err := queryRows(tx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, 0, err
	}
	columns := make([]string, 0, len(columnRows))
	values := make([]any, 0, len(columnRows))
	placeholders := make([]string, 0, len(columnRows))
	for _, columnRow := range columnRows {
		name := rowString(columnRow, "name")
		if _, ok := row[name]; !ok {
			continue
		}
		columns = append(columns, name)
		values = append(values, row[name])
		placeholders = append(placeholders, "?")
	}
	if len(columns) == 0 {
		return false, 0, nil
	}
	statement := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	result, err := tx.Exec(statement, values...)
	if err != nil {
		return false, 0, err
	}
	lastID, _ := result.LastInsertId()
	return rowsCreated(result), lastID, nil
}
