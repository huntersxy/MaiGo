package storage

// A_memorix 关系生命周期证据观测，对齐 Python metadata_store.py 的 _observe_relation_evidence

import (
	"fmt"
	"strings"
)

// ObserveRelationEvidence 新证据建立关联时重置关系生命周期，调用方负责事务边界
func (s *Store) ObserveRelationEvidence(hashes []string, observedAt float64, conn *Conn) {
	normalized := make([]string, 0, len(hashes))
	for _, item := range hashes {
		token := strings.TrimSpace(item)
		if token != "" {
			normalized = append(normalized, token)
		}
	}
	normalized = dedupeStrings(normalized)
	if len(normalized) == 0 {
		return
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return
	}
	for start := 0; start < len(normalized); start += 500 {
		end := start + 500
		if end > len(normalized) {
			end = len(normalized)
		}
		chunk := normalized[start:end]
		query := fmt.Sprintf(`UPDATE relations
			SET retention_strength = 1.0,
				retention_anchor_at = MAX(
					COALESCE(retention_anchor_at, ?),
					COALESCE(inactive_since, ?),
					COALESCE(last_accessed, ?),
					COALESCE(last_reinforced, ?),
					COALESCE(last_access_reinforced_at, ?),
					?
				),
				next_lifecycle_at = MAX(
					COALESCE(retention_anchor_at, ?),
					COALESCE(inactive_since, ?),
					COALESCE(last_accessed, ?),
					COALESCE(last_reinforced, ?),
					COALESCE(last_access_reinforced_at, ?),
					?
				),
				reinforcement_count = COALESCE(reinforcement_count, 0) + 1,
				lifecycle_revision = COALESCE(lifecycle_revision, 0) + 1,
				last_reinforced = MAX(
					COALESCE(retention_anchor_at, ?),
					COALESCE(inactive_since, ?),
					COALESCE(last_accessed, ?),
					COALESCE(last_reinforced, ?),
					COALESCE(last_access_reinforced_at, ?),
					?
				),
				is_inactive = 0,
				inactive_since = NULL,
				inactive_reason = NULL
			WHERE hash IN (%s)`, sqlPlaceholders(len(chunk)))
		args := make([]any, 0, 18+len(chunk))
		for i := 0; i < 18; i++ {
			args = append(args, observedAt)
		}
		args = append(args, stringArgs(chunk)...)
		if _, err := c.Exec(query, args...); err != nil {
			warnQueryError("更新关系证据失败", err)
			return
		}
	}
}
