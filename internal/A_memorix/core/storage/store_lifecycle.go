package storage

// A_memorix 关系生命周期事件与到期评估，对齐 Python core/storage/metadata_store.py L3342-L3647
// 生命周期计算复用 internal/memorix/core/utils 的 RelationLifecycleState/EvaluateLifecycle/ApplyLifecycleEvent/RetentionAt

import (
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// RelationLifecycleEvaluation 对齐 Python evaluate_due_relation_lifecycles 的返回字典
type RelationLifecycleEvaluation struct {
	// Frozen 本次冻结的关系负载
	Frozen []map[string]any
	// Scheduled 重新排定下一到期时间的关系负载
	Scheduled []map[string]any
}

// RelationLifecycleStateFromRow 从关系行构造生命周期状态，对齐 Python _relation_lifecycle_state_from_row
func RelationLifecycleStateFromRow(row map[string]any) (utils.RelationLifecycleState, error) {
	hashValue := stringFromRow(row, "hash")
	anchorRaw, ok := row["retention_anchor_at"]
	if !ok || anchorRaw == nil {
		return utils.RelationLifecycleState{}, fmt.Errorf("关系缺少 retention_anchor_at: %s", hashValue)
	}
	strengthRaw, ok := row["retention_strength"]
	if !ok || strengthRaw == nil {
		return utils.RelationLifecycleState{}, fmt.Errorf("关系缺少 retention_strength: %s", hashValue)
	}
	reason := strings.TrimSpace(stringFromRow(row, "inactive_reason"))
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	return utils.RelationLifecycleState{
		Strength:               cleanupFloatValue(strengthRaw),
		AnchorAt:               cleanupFloatValue(anchorRaw),
		IsInactive:             miscBoolValue(row["is_inactive"]),
		InactiveSince:          floatPtrFromRow(row, "inactive_since"),
		InactiveReason:         reasonPtr,
		LastAccessReinforcedAt: floatPtrFromRow(row, "last_access_reinforced_at"),
	}, nil
}

// ApplyRelationLifecycleEvent 在单个元数据事务内应用关系生命周期事件
func (s *Store) ApplyRelationLifecycleEvent(hashes []string, event utils.RelationLifecycleEvent, policy utils.RelationLifecyclePolicy, now float64, strength float64) ([]map[string]any, error) {
	normalized := normalizeHashSequence(hashes)
	if len(normalized) == 0 {
		return []map[string]any{}, nil
	}
	transitions := make([]map[string]any, 0, len(normalized))
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		query := fmt.Sprintf(`
			SELECT hash, subject, object,
			       retention_strength, retention_anchor_at, next_lifecycle_at,
			       is_inactive, inactive_since, inactive_reason,
			       last_accessed, last_reinforced, last_access_reinforced_at
			FROM relations
			WHERE hash IN (%s)
			ORDER BY hash ASC
			`, sqlPlaceholders(len(normalized)))
		rows, err := c.Query(query, stringArgs(normalized)...)
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		for _, row := range items {
			previous, err := RelationLifecycleStateFromRow(row)
			if err != nil {
				return err
			}
			eventTime := now
			if previous.AnchorAt > eventTime {
				eventTime = previous.AnchorAt
			}
			for _, field := range []string{"inactive_since", "last_accessed", "last_reinforced", "last_access_reinforced_at"} {
				if value := floatPtrFromRow(row, field); value != nil && *value > eventTime {
					eventTime = *value
				}
			}
			decision, err := utils.ApplyLifecycleEvent(previous, event, eventTime, policy, strength)
			if err != nil {
				return err
			}
			accessIncrement := 0
			if event == utils.LifecycleEventAccess {
				accessIncrement = 1
			}
			reinforcedIncrement := 0
			if event == utils.LifecycleEventEvidence || event == utils.LifecycleEventReinforce {
				reinforcedIncrement = 1
			}
			inactiveFlag := 0
			if decision.IsInactive {
				inactiveFlag = 1
			}
			changedFlag := 0
			if decision.Changed {
				changedFlag = 1
			}
			if _, err := c.Exec(`
				UPDATE relations
				SET retention_strength = ?,
				    retention_anchor_at = ?,
				    next_lifecycle_at = ?,
				    is_inactive = ?,
				    inactive_since = ?,
				    inactive_reason = ?,
				    last_accessed = CASE
				        WHEN ? > 0 THEN MAX(COALESCE(last_accessed, ?), ?)
				        ELSE last_accessed
				    END,
				    access_count = COALESCE(access_count, 0) + ?,
				    last_access_reinforced_at = ?,
				    last_reinforced = CASE
				        WHEN ? > 0 THEN MAX(COALESCE(last_reinforced, ?), ?)
				        ELSE last_reinforced
				    END,
				    reinforcement_count = COALESCE(reinforcement_count, 0) + ?,
				    lifecycle_revision = COALESCE(lifecycle_revision, 0) + ?
				WHERE hash = ?
				`,
				decision.Strength,
				decision.AnchorAt,
				miscNullableFloat64(decision.NextLifecycleAt),
				inactiveFlag,
				miscNullableFloat64(decision.InactiveSince),
				miscNullableText(decision.InactiveReason),
				accessIncrement,
				eventTime,
				eventTime,
				accessIncrement,
				miscNullableFloat64(decision.LastAccessReinforcedAt),
				reinforcedIncrement,
				eventTime,
				eventTime,
				reinforcedIncrement,
				changedFlag,
				stringFromRow(row, "hash"),
			); err != nil {
				return err
			}
			decisionState := utils.RelationLifecycleState{
				Strength:               decision.Strength,
				AnchorAt:               decision.AnchorAt,
				IsInactive:             decision.IsInactive,
				InactiveSince:          decision.InactiveSince,
				InactiveReason:         decision.InactiveReason,
				LastAccessReinforcedAt: decision.LastAccessReinforcedAt,
			}
			score, err := utils.RetentionAt(decisionState, eventTime, policy)
			if err != nil {
				return err
			}
			transitions = append(transitions, map[string]any{
				"hash":                stringFromRow(row, "hash"),
				"subject":             stringFromRow(row, "subject"),
				"object":              stringFromRow(row, "object"),
				"was_inactive":        previous.IsInactive,
				"is_inactive":         decision.IsInactive,
				"inactive_reason":     miscNullableText(decision.InactiveReason),
				"retention_score":     score,
				"retention_anchor_at": decision.AnchorAt,
				"next_lifecycle_at":   miscNullableFloat64(decision.NextLifecycleAt),
				"lifecycle_changed":   decision.Changed,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return transitions, nil
}

// EvaluateDueRelationLifecycles 计算到期活跃关系并物化冻结状态或下一到期时间
func (s *Store) EvaluateDueRelationLifecycles(policy utils.RelationLifecyclePolicy, now float64, limit int) (RelationLifecycleEvaluation, error) {
	result := RelationLifecycleEvaluation{
		Frozen:    []map[string]any{},
		Scheduled: []map[string]any{},
	}
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		rows, err := c.Query(`
			SELECT hash, subject, object,
			       retention_strength, retention_anchor_at, next_lifecycle_at,
			       is_inactive, inactive_since, inactive_reason, last_access_reinforced_at
			FROM relations
			WHERE is_inactive = 0
			  AND is_permanent = 0
			  AND is_pinned = 0
			  AND COALESCE(protected_until, 0.0) <= ?
			  AND next_lifecycle_at <= ?
			ORDER BY next_lifecycle_at ASC, hash ASC
			LIMIT ?
			`, now, now, max(1, limit))
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		for _, row := range items {
			state, err := RelationLifecycleStateFromRow(row)
			if err != nil {
				return err
			}
			decision, err := utils.EvaluateLifecycle(state, now, policy)
			if err != nil {
				return err
			}
			inactiveFlag := 0
			if decision.IsInactive {
				inactiveFlag = 1
			}
			if _, err := c.Exec(`
				UPDATE relations
				SET next_lifecycle_at = ?,
				    is_inactive = ?,
				    inactive_since = ?,
				    inactive_reason = ?,
				    lifecycle_revision = COALESCE(lifecycle_revision, 0) + 1
				WHERE hash = ?
				`,
				miscNullableFloat64(decision.NextLifecycleAt),
				inactiveFlag,
				miscNullableFloat64(decision.InactiveSince),
				miscNullableText(decision.InactiveReason),
				stringFromRow(row, "hash"),
			); err != nil {
				return err
			}
			score, err := utils.RetentionAt(state, now, policy)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"hash":              stringFromRow(row, "hash"),
				"subject":           stringFromRow(row, "subject"),
				"object":            stringFromRow(row, "object"),
				"retention_score":   score,
				"next_lifecycle_at": miscNullableFloat64(decision.NextLifecycleAt),
			}
			if decision.IsInactive {
				result.Frozen = append(result.Frozen, payload)
			} else {
				result.Scheduled = append(result.Scheduled, payload)
			}
		}
		return nil
	})
	if err != nil {
		return RelationLifecycleEvaluation{}, err
	}
	return result, nil
}

// GetDecayPruneCandidateRows 返回冻结期已过且当前仍低于阈值的自动衰减关系
func (s *Store) GetDecayPruneCandidateRows(cutoffTime float64, now float64, policy utils.RelationLifecyclePolicy, limit int) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`
		SELECT hash, subject, object,
		       retention_strength, retention_anchor_at, next_lifecycle_at,
		       lifecycle_revision, is_inactive, inactive_since, inactive_reason,
		       is_permanent, is_pinned, protected_until, last_access_reinforced_at
		FROM relations
		WHERE is_inactive = 1
		  AND inactive_reason = 'decay'
		  AND inactive_since < ?
		  AND is_permanent = 0
		  AND is_pinned = 0
		  AND COALESCE(protected_until, 0.0) <= ?
		ORDER BY inactive_since ASC, hash ASC
		LIMIT ?
		`, cutoffTime, now, max(1, limit))
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	candidates := make([]map[string]any, 0, len(items))
	for _, row := range items {
		state, err := RelationLifecycleStateFromRow(row)
		if err != nil {
			return nil, err
		}
		score, err := utils.RetentionAt(state, now, policy)
		if err != nil {
			return nil, err
		}
		if score > policy.FreezeThreshold {
			continue
		}
		candidates = append(candidates, map[string]any{
			"hash":                         stringFromRow(row, "hash"),
			"subject":                      stringFromRow(row, "subject"),
			"object":                       stringFromRow(row, "object"),
			"retention_score":              score,
			"expected_lifecycle_revision":  intFromRow(row, "lifecycle_revision"),
			"expected_retention_strength":  cleanupFloatValue(row["retention_strength"]),
			"expected_retention_anchor_at": cleanupFloatValue(row["retention_anchor_at"]),
			"expected_inactive_since":      miscNullableFloat64(floatPtrFromRow(row, "inactive_since")),
			"expected_inactive_reason":     stringFromRow(row, "inactive_reason"),
			"expected_is_inactive":         miscBoolValue(row["is_inactive"]),
			"expected_is_permanent":        miscBoolValue(row["is_permanent"]),
			"expected_is_pinned":           miscBoolValue(row["is_pinned"]),
			"expected_protected_until":     miscNullableFloat64(floatPtrFromRow(row, "protected_until")),
		})
	}
	return candidates, nil
}
