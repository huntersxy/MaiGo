package services

// 图节点重命名与边权重更新，对齐 Python graph_admin_service.py 的写入段

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// AliasRebuilder 是关系哈希别名重建端口，避免服务层依赖存储包内部返回类型
type AliasRebuilder interface {
	RebuildRelationHashAliases(conn *storage.Conn) error
}

// VectorInvalidator 是重命名后旧向量失效端口
type VectorInvalidator interface {
	DeleteVectorsByType(ctx context.Context, entityHashes []string, relationHashes []string) (map[string]any, error)
}

// errRenameAbort 用于在事务内提前终止并保留业务结果
var errRenameAbort = errors.New("rename aborted")

// renameNode 对齐 _rename_node
func (s *GraphAdminService) renameNode(ctx context.Context, oldName string, newName string, reason string, updatedBy string, recordOperation bool) map[string]any {
	oldToken := strings.TrimSpace(oldName)
	newToken := strings.TrimSpace(newName)
	if oldToken == "" || newToken == "" {
		return map[string]any{"success": false, "error": "old_name/new_name 不能为空"}
	}
	if oldToken == newToken {
		result := map[string]any{"success": true, "renamed": false, "old_name": oldToken, "new_name": newToken}
		if recordOperation {
			operation, err := s.Meta().RecordV5Operation("graph_rename_node", newToken, []string{}, reason, updatedBy, result, nil)
			if err != nil {
				return map[string]any{"success": false, "error": err.Error()}
			}
			result["operation"] = operation
		}
		return result
	}

	oldHash := utils.ComputeEntityHash(oldToken)
	targetHash := utils.ComputeEntityHash(newToken)
	businessResult := map[string]any{}
	resolvedTargetHash := targetHash
	relationHashMap := map[string]string{}
	oldRelationHashes := []string{}
	projectionJobs := []map[string]any{}
	var operation map[string]any

	txErr := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		oldRow, err := queryRow(tx, "SELECT * FROM entities WHERE hash = ? OR LOWER(TRIM(name)) = LOWER(TRIM(?)) LIMIT 1", oldHash, oldToken)
		if err != nil {
			return err
		}
		if oldRow == nil {
			businessResult = map[string]any{"success": false, "error": "原节点不存在"}
			return errRenameAbort
		}
		oldHash = rowString(oldRow, "hash")

		episodeSources := []string{}
		if rows, err := queryRows(tx,
			`SELECT DISTINCT p.source AS source FROM paragraph_entities pe JOIN paragraphs p ON p.hash = pe.paragraph_hash
			 WHERE pe.entity_hash = ? AND p.source IS NOT NULL AND TRIM(p.source) != '' AND COALESCE(p.is_deleted, 0) = 0`, oldHash); err == nil {
			for _, row := range rows {
				episodeSources = append(episodeSources, rowString(row, "source"))
			}
		}

		targetRow, err := queryRow(tx, "SELECT * FROM entities WHERE hash = ? OR LOWER(TRIM(name)) = LOWER(TRIM(?)) LIMIT 1", targetHash, newToken)
		if err != nil {
			return err
		}
		targetSameAsOld := targetRow != nil && rowString(targetRow, "hash") == oldHash

		now := float64(0)
		if targetSameAsOld {
			if _, err := tx.Exec("UPDATE entities SET name = ?, vector_index = NULL, is_deleted = 0, deleted_at = NULL WHERE hash = ?", newToken, oldHash); err != nil {
				return err
			}
			resolvedTargetHash = oldHash
		} else if targetRow == nil {
			if _, err := tx.Exec(
				"INSERT INTO entities (hash, name, vector_index, appearance_count, created_at, metadata, is_deleted) VALUES (?, ?, NULL, ?, ?, ?, 0)",
				targetHash, newToken, rowInt(oldRow, "appearance_count"), now, asString(oldRow["metadata"])); err != nil {
				return err
			}
			resolvedTargetHash = targetHash
		} else {
			resolvedTargetHash = rowString(targetRow, "hash")
			if _, err := tx.Exec(
				"UPDATE entities SET name = ?, appearance_count = COALESCE(appearance_count, 0) + ?, is_deleted = 0, deleted_at = NULL WHERE hash = ?",
				newToken, rowInt(oldRow, "appearance_count"), resolvedTargetHash); err != nil {
				return err
			}
		}

		if resolvedTargetHash != oldHash {
			if _, err := tx.Exec("UPDATE OR IGNORE paragraph_entities SET entity_hash = ? WHERE entity_hash = ?", resolvedTargetHash, oldHash); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM paragraph_entities WHERE entity_hash = ?", oldHash); err != nil {
				return err
			}
		}

		relationRows, err := queryRows(tx,
			`SELECT * FROM relations WHERE LOWER(TRIM(subject)) = LOWER(TRIM(?)) OR LOWER(TRIM(object)) = LOWER(TRIM(?)) ORDER BY created_at ASC`,
			oldToken, oldToken)
		if err != nil {
			return err
		}
		for _, row := range relationRows {
			oldRelationHash := rowString(row, "hash")
			subject := rowString(row, "subject")
			predicate := rowString(row, "predicate")
			object := rowString(row, "object")
			if strings.EqualFold(strings.TrimSpace(subject), oldToken) {
				subject = newToken
			}
			if strings.EqualFold(strings.TrimSpace(object), oldToken) {
				object = newToken
			}
			projectionJobs = append(projectionJobs, map[string]any{
				"relation_hash": oldRelationHash, "subject": subject, "object": object,
				"desired_lifecycle_revision": rowInt(row, "lifecycle_revision"),
			})
			newRelationHash, err := s.Meta().ComputeRelationHash(subject, predicate, object)
			if err != nil {
				return err
			}
			relationHashMap[oldRelationHash] = newRelationHash
			oldRelationHashes = append(oldRelationHashes, oldRelationHash)
			projectionJobs = append(projectionJobs, map[string]any{
				"relation_hash": newRelationHash, "subject": subject, "object": object,
			})
			if newRelationHash == oldRelationHash {
				if _, err := tx.Exec(
					`UPDATE relations SET subject = ?, object = ?, vector_index = NULL, vector_state = 'none',
					 vector_updated_at = NULL, vector_error = NULL, vector_retry_count = 0 WHERE hash = ?`,
					subject, object, oldRelationHash); err != nil {
					return err
				}
				continue
			}
			relationCopy := map[string]any{}
			for key, value := range row {
				relationCopy[key] = value
			}
			relationCopy["hash"] = newRelationHash
			relationCopy["subject"] = subject
			relationCopy["object"] = object
			relationCopy["vector_index"] = nil
			relationCopy["vector_state"] = "none"
			relationCopy["vector_updated_at"] = nil
			relationCopy["vector_error"] = nil
			relationCopy["vector_retry_count"] = 0
			if err := insertRowTx(tx, "relations", relationCopy); err != nil {
				return err
			}
			if _, err := tx.Exec("UPDATE OR IGNORE paragraph_relations SET relation_hash = ? WHERE relation_hash = ?", newRelationHash, oldRelationHash); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM paragraph_relations WHERE relation_hash = ?", oldRelationHash); err != nil {
				return err
			}
			if hasTableTx(tx) {
				if _, err := tx.Exec("UPDATE OR IGNORE graph_edge_relation_map SET relation_hash = ? WHERE relation_hash = ?", newRelationHash, oldRelationHash); err != nil {
					return err
				}
				if _, err := tx.Exec("DELETE FROM graph_edge_relation_map WHERE relation_hash = ?", oldRelationHash); err != nil {
					return err
				}
			}
			if _, err := tx.Exec("DELETE FROM relations WHERE hash = ?", oldRelationHash); err != nil {
				return err
			}
		}

		if resolvedTargetHash != oldHash {
			if _, err := tx.Exec("DELETE FROM entities WHERE hash = ?", oldHash); err != nil {
				return err
			}
		}
		if len(episodeSources) > 0 {
			s.Meta().EnqueueEpisodeSourceRebuilds(dedupeStrings(episodeSources), "entity_renamed", storage.EpisodeEnqueueOptions{Conn: tx.Conn()})
		}
		if s.aliasRebuilder != nil {
			if err := s.aliasRebuilder.RebuildRelationHashAliases(tx.Conn()); err != nil {
				return err
			}
		}
		if _, err := s.Meta().ReenqueueAuthoritativeRelationGraphProjectionJobs(projectionJobs, nil); err != nil {
			return err
		}
		businessResult = map[string]any{
			"success":           true,
			"renamed":           true,
			"old_name":          oldToken,
			"new_name":          newToken,
			"entity_hash":       resolvedTargetHash,
			"relation_hash_map": relationHashMap,
		}
		if recordOperation {
			hashes := append([]string{resolvedTargetHash}, mapValues(relationHashMap)...)
			operation, err = s.Meta().RecordV5Operation("graph_rename_node", resolvedTargetHash, hashes, reason, updatedBy, businessResult, tx.Conn())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil && !errors.Is(txErr, errRenameAbort) {
		return map[string]any{"success": false, "error": fmt.Sprintf("rename failed: %v", txErr)}
	}
	if len(businessResult) > 0 {
		return businessResult
	}

	invalidation := map[string]any{"status": "invalidated", "entity_hashes": []string{resolvedTargetHash}, "relation_hashes": mapValues(relationHashMap)}
	if s.vectorInvalidator != nil {
		invalidated, err := s.vectorInvalidator.DeleteVectorsByType(ctx, []string{oldHash}, oldRelationHashes)
		if err != nil {
			invalidation = map[string]any{"status": "failed", "error": err.Error(),
				"entity_hashes": []string{resolvedTargetHash}, "relation_hashes": mapValues(relationHashMap)}
		} else if invalidated != nil {
			invalidation = map[string]any{"status": "invalidated", "entity_hashes": []string{resolvedTargetHash},
				"relation_hashes": mapValues(relationHashMap), "details": invalidated}
		}
	}
	projection := s.publishProjection(ctx, len(projectionJobs))
	result := map[string]any{}
	for key, value := range businessResult {
		result[key] = value
	}
	if operation != nil {
		result["operation"] = operation
	}
	result["projection"] = projection
	result["vector_projection"] = invalidation
	return result
}

// updateEdgeWeight 对齐 _update_edge_weight
func (s *GraphAdminService) updateEdgeWeight(ctx context.Context, relationHash string, subject string, object string, weight float64, reason string, updatedBy string) map[string]any {
	hashToken := strings.TrimSpace(relationHash)
	subjectName := strings.TrimSpace(subject)
	objectName := strings.TrimSpace(object)
	if hashToken == "" && (subjectName == "" || objectName == "") {
		return map[string]any{"success": false, "error": "hash 或 subject/object 不能为空"}
	}
	result := map[string]any{}
	txErr := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		var row map[string]any
		var err error
		if hashToken != "" {
			row, err = queryRow(tx, "SELECT hash, confidence, subject, predicate, object FROM relations WHERE hash = ?", hashToken)
		} else {
			row, err = queryRow(tx,
				`SELECT hash, confidence, subject, predicate, object FROM relations
				 WHERE LOWER(TRIM(subject)) = LOWER(TRIM(?)) AND LOWER(TRIM(object)) = LOWER(TRIM(?))
				 AND (is_inactive IS NULL OR is_inactive = 0) ORDER BY confidence DESC, created_at DESC LIMIT 1`,
				subjectName, objectName)
		}
		if err != nil {
			return err
		}
		if row == nil {
			result = map[string]any{"success": false, "error": "未找到可更新的关系"}
			return errRenameAbort
		}
		resolvedHash := rowString(row, "hash")
		if _, err := tx.Exec("UPDATE relations SET confidence = ? WHERE hash = ?", weight, resolvedHash); err != nil {
			return err
		}
		business := map[string]any{
			"success":    true,
			"hash":       resolvedHash,
			"subject":    rowString(row, "subject"),
			"predicate":  rowString(row, "predicate"),
			"object":     rowString(row, "object"),
			"weight":     weight,
			"confidence": weight,
		}
		operation, err := s.Meta().RecordV5Operation("graph_update_relation_confidence", resolvedHash,
			[]string{resolvedHash}, reason, updatedBy, business, tx.Conn())
		if err != nil {
			return err
		}
		business["operation"] = operation
		result = business
		return nil
	})
	if txErr != nil && !errors.Is(txErr, errRenameAbort) {
		return map[string]any{"success": false, "error": txErr.Error()}
	}
	if len(result) == 0 {
		return map[string]any{"success": false, "error": "未找到可更新的关系"}
	}
	if pyTruthy(result["success"]) {
		if graphStore := s.Graph(); graphStore != nil {
			_ = graphStore.Save(s.DataDir())
		}
		queued := 0
		if _, err := s.Meta().ReenqueueAuthoritativeRelationGraphProjectionJobs([]map[string]any{{
			"relation_hash": result["hash"], "subject": result["subject"], "object": result["object"],
		}}, nil); err == nil {
			queued = 1
		}
		result["projection"] = s.publishProjection(ctx, queued)
	}
	return result
}

// hasTableTx 判断关系图映射表是否存在
func hasTableTx(tx *storage.Tx) bool {
	row := tx.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'graph_edge_relation_map'")
	var name string
	if err := row.Scan(&name); err != nil {
		return false
	}
	return name != ""
}

// queryRow 在事务内查询单行
func queryRow(tx *storage.Tx, statement string, params ...any) (map[string]any, error) {
	rows, err := queryRows(tx, statement, params...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// queryRows 在事务内查询多行并转成对象列表
func queryRows(tx *storage.Tx, statement string, params ...any) ([]map[string]any, error) {
	rows, err := tx.Query(statement, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
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
			row[column] = values[index]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// insertRowTx 按实际列插入一行，对齐 Python 按 PRAGMA table_info 过滤列的行为
func insertRowTx(tx *storage.Tx, table string, row map[string]any) error {
	columnRows, err := queryRows(tx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
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
		return fmt.Errorf("表 %s 没有可写入的列", table)
	}
	statement := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	_, err = tx.Exec(statement, values...)
	return err
}

// mapValues 返回映射的值列表，按同一排序保持稳定
func mapValues(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}
