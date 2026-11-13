package storage

// A_memorix V5 操作审计，对齐 Python metadata_store.py 的 record_v5_operation

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// RecordV5Operation 记录一条 V5 操作审计
func (s *Store) RecordV5Operation(action string, target string, resolvedHashes []string, reason string, updatedBy string, result map[string]any, conn *Conn) (map[string]any, error) {
	operationID := "v5_" + uuid.NewString()
	operationID = strings.ReplaceAll(operationID, "-", "")
	createdAt := float64(time.Now().UnixNano()) / 1e9
	hashes := splitNonEmptyTokens(resolvedHashes)
	trimmedAction := strings.TrimSpace(action)
	trimmedTarget := strings.TrimSpace(target)
	trimmedReason := strings.TrimSpace(reason)
	trimmedUpdatedBy := strings.TrimSpace(updatedBy)
	if result == nil {
		result = map[string]any{}
	}
	payload := map[string]any{
		"operation_id":    operationID,
		"action":          trimmedAction,
		"target":          trimmedTarget,
		"reason":          trimmedReason,
		"updated_by":      trimmedUpdatedBy,
		"created_at":      createdAt,
		"resolved_hashes": hashes,
		"result":          result,
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	const statement = "INSERT INTO memory_v5_operations " +
		"(operation_id, action, target, reason, updated_by, created_at, resolved_hashes_json, result_json) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	if _, err := c.Exec(statement, operationID, trimmedAction, optionalString(trimmedTarget), optionalString(trimmedReason),
		optionalString(trimmedUpdatedBy), createdAt, jsonDumps(hashes), jsonDumps(result)); err != nil {
		return nil, err
	}
	if conn == nil {
		if err := c.Commit(); err != nil {
			return nil, err
		}
	}
	return payload, nil
}
