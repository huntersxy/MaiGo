package storage

// A_memorix 事实 claim 写入与状态机，对齐 Python core/storage/metadata_fact.py 的 upsert_fact_claim

import "fmt"

// FactClaimUpsert 对齐 Python upsert_fact_claim 的关键字参数
// 字符串字段留空表示取 Python 侧默认值，指针字段为 nil 表示未指定
type FactClaimUpsert struct {
	ScopeType          string
	ScopeID            string
	FactKey            string
	ValueText          string
	Polarity           string
	Cardinality        string
	Stability          string
	ProfileSection     string
	Authority          string
	Confidence         *float64
	ValidFrom          *float64
	ValidTo            *float64
	EvidenceType       string
	EvidenceID         string
	EvidenceStance     string
	EvidenceWeight     *float64
	EvidenceMetadata   map[string]any
	SupersedesClaimIDs []string
	Reason             string
	ObservedAt         *float64
}

// factOptionalID 对齐 Python `str(value or "") or None`
func factOptionalID(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

// factFloatArg 把可选浮点转换为 SQL 参数
func factFloatArg(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// factAppendTransition 追加状态转换审计记录，对齐 Python _append_fact_transition
func factAppendTransition(c *Conn, oldClaimID, newClaimID *string, transitionType, reason, evidenceType, evidenceID string, createdAt float64) (int64, error) {
	result, err := c.Exec(`
		INSERT INTO fact_transitions (
			old_claim_id, new_claim_id, transition_type, reason,
			evidence_type, evidence_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
		factOptionalID(oldClaimID),
		factOptionalID(newClaimID),
		factStrip(transitionType),
		factStrip(reason),
		factStrip(evidenceType),
		factStrip(evidenceID),
		createdAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// factUpsertEvidenceInTx 写入或增强一条证据，对齐 Python _upsert_fact_evidence_in_transaction
func factUpsertEvidenceInTx(c *Conn, claimID, evidenceType, evidenceID, stance string, weight, observedAt float64, metadata map[string]any) error {
	if evidenceType == "" || evidenceID == "" {
		return nil
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := factSortedJSONDumps(metadata)
	if err != nil {
		return err
	}
	_, err = c.Exec(`
		INSERT INTO fact_evidence (
			claim_id, evidence_type, evidence_id, stance,
			weight, observed_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(claim_id, evidence_type, evidence_id, stance) DO UPDATE SET
			weight = MAX(fact_evidence.weight, excluded.weight),
			observed_at = MAX(fact_evidence.observed_at, excluded.observed_at),
			metadata_json = excluded.metadata_json
		`,
		claimID,
		evidenceType,
		evidenceID,
		stance,
		factClamp01(weight),
		observedAt,
		metadataJSON,
	)
	return err
}

// factLoadClaim 读取单条 claim，不存在返回 nil
func factLoadClaim(c *Conn, claimID string) (map[string]any, error) {
	rows, err := factQueryMaps(c, "SELECT * FROM fact_claims WHERE claim_id = ?", claimID)
	if err != nil {
		return nil, err
	}
	return factClaimRow(factFirstRow(rows)), nil
}

// factLoadEvidence 读取单条证据，不存在返回 nil
func factLoadEvidence(c *Conn, claimID, evidenceType, evidenceID, stance string) (map[string]any, error) {
	rows, err := factQueryMaps(c, `
		SELECT * FROM fact_evidence
		WHERE claim_id = ? AND evidence_type = ? AND evidence_id = ? AND stance = ?
		`, claimID, evidenceType, evidenceID, stance)
	if err != nil {
		return nil, err
	}
	return factEvidenceRow(factFirstRow(rows)), nil
}

// factLookupClaims 批量读取 claim 的身份字段，返回顺序与查询结果一致
func factLookupClaims(c *Conn, claimIDs []string) ([]map[string]any, error) {
	return factQueryMaps(c, fmt.Sprintf(`
		SELECT claim_id, scope_type, scope_id, conflict_group, status
		FROM fact_claims
		WHERE claim_id IN (%s)
		`, factPlaceholders(len(claimIDs))), factStringsToAny(claimIDs)...)
}

// factResolveSupersedes 解析待取代 claim，对齐 Python 对 supersedes_claim_ids 的去重与校验
// 返回按查询顺序排列的待取代 claim 与真正需要失效的 claim
func factResolveSupersedes(c *Conn, requested []string, claimID, scopeType, scopeID, conflictGroup string) ([]string, error) {
	rows, err := factLookupClaims(c, requested)
	if err != nil {
		return nil, err
	}
	found := make(map[string]map[string]any, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		item := factPyStrOrEmpty(row["claim_id"])
		if _, ok := found[item]; !ok {
			order = append(order, item)
		}
		found[item] = row
	}
	missing := make([]string, 0, len(requested))
	for _, item := range requested {
		if _, ok := found[item]; !ok {
			missing = append(missing, item)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("待取代 claim 不存在: %s", factPyReprList(missing))
	}
	invalid := make([]string, 0, len(order))
	for _, item := range order {
		row := found[item]
		if factPyStrOrEmpty(row["scope_type"]) != scopeType ||
			factPyStrOrEmpty(row["scope_id"]) != scopeID ||
			factPyStrOrEmpty(row["conflict_group"]) != conflictGroup {
			invalid = append(invalid, item)
		}
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("待取代 claim 不属于同一冲突组: %s", factPyReprList(invalid))
	}
	superseded := make([]string, 0, len(order))
	for _, item := range order {
		if factPyStrOrEmpty(found[item]["status"]) != "superseded" {
			superseded = append(superseded, item)
		}
	}
	return superseded, nil
}

// factActiveConflicts 读取同冲突组的其他 active claim，对齐 Python 的 active_conflicts 查询
func factActiveConflicts(c *Conn, scopeType, scopeID, conflictGroup, claimID string) ([]string, error) {
	rows, err := factQueryMaps(c, `
		SELECT claim_id
		FROM fact_claims
		WHERE scope_type = ? AND scope_id = ? AND conflict_group = ?
		  AND status = 'active' AND claim_id != ?
		ORDER BY claim_id ASC
		`, scopeType, scopeID, conflictGroup, claimID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, factPyStrOrEmpty(row["claim_id"]))
	}
	return out, nil
}

// UpsertFactClaim 写入或增强一条事实，并执行显式 supersession
// 对齐 Python upsert_fact_claim：返回字典包含 claim 全字段与 created/reinforced/restored/idempotent 标记
func (s *Store) UpsertFactClaim(in FactClaimUpsert) (map[string]any, error) {
	normalizedScopeType, err := factEnumToken("scope_type", in.ScopeType, factScopeTypes)
	if err != nil {
		return nil, err
	}
	normalizedScopeID, err := factRequiredToken("scope_id", in.ScopeID)
	if err != nil {
		return nil, err
	}
	requiredFactKey, err := factRequiredToken("fact_key", in.FactKey)
	if err != nil {
		return nil, err
	}
	normalizedFactKey := factNormalizedToken(requiredFactKey)
	normalizedValueText, err := factRequiredToken("value_text", in.ValueText)
	if err != nil {
		return nil, err
	}
	valueNormalized := factNormalizedToken(normalizedValueText)
	normalizedPolarity, err := factEnumToken("polarity", factStringOrDefault(in.Polarity, "positive"), factPolarities)
	if err != nil {
		return nil, err
	}
	normalizedCardinality, err := factEnumToken("cardinality", factStringOrDefault(in.Cardinality, "set"), factCardinalities)
	if err != nil {
		return nil, err
	}
	normalizedStability, err := factEnumToken("stability", factStringOrDefault(in.Stability, "stable"), factStabilities)
	if err != nil {
		return nil, err
	}
	normalizedProfileSection, err := factEnumToken("profile_section", factStringOrDefault(in.ProfileSection, "stable_facts"), factProfileSections)
	if err != nil {
		return nil, err
	}
	normalizedAuthority, err := factEnumToken("authority", factStringOrDefault(in.Authority, "direct_user"), factAuthorities)
	if err != nil {
		return nil, err
	}
	normalizedEvidenceStance, err := factEnumToken("evidence_stance", factStringOrDefault(in.EvidenceStance, "support"), factEvidenceStances)
	if err != nil {
		return nil, err
	}
	normalizedEvidenceType, normalizedEvidenceID, err := factEvidenceIdentity(in.EvidenceType, in.EvidenceID)
	if err != nil {
		return nil, err
	}
	if normalizedEvidenceType != "" && normalizedEvidenceStance != "support" {
		return nil, fmt.Errorf("upsert_fact_claim 只能用 support 证据断言事实；反向证据请使用 add_fact_evidence")
	}
	normalizedConfidence := factClamp01(factFloatOrDefault(in.Confidence, 1.0))
	now := nowTimestamp()
	if in.ObservedAt != nil {
		now = *in.ObservedAt
	}
	normalizedValidFrom := asOptionalFloat(in.ValidFrom)
	normalizedValidTo := asOptionalFloat(in.ValidTo)
	if normalizedValidFrom != nil && normalizedValidTo != nil && *normalizedValidTo < *normalizedValidFrom {
		return nil, fmt.Errorf("valid_to 不能早于 valid_from")
	}

	claimID, err := factClaimIdentity(normalizedScopeType, normalizedScopeID, normalizedFactKey, valueNormalized, normalizedPolarity)
	if err != nil {
		return nil, err
	}
	conflictGroup, err := factConflictGroup(normalizedScopeType, normalizedScopeID, normalizedFactKey, valueNormalized, normalizedCardinality)
	if err != nil {
		return nil, err
	}

	requestedSupersedes := make([]string, 0, len(in.SupersedesClaimIDs))
	seenSupersedes := make(map[string]struct{}, len(in.SupersedesClaimIDs))
	for _, item := range in.SupersedesClaimIDs {
		token := factStrip(item)
		if token == "" || token == claimID {
			continue
		}
		if _, exists := seenSupersedes[token]; exists {
			continue
		}
		seenSupersedes[token] = struct{}{}
		requestedSupersedes = append(requestedSupersedes, token)
	}

	created := false
	reinforced := false
	restored := false
	superseded := []string{}
	conflicting := []string{}
	var claim map[string]any
	var result map[string]any

	err = s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		existing, err := factLoadClaim(c, claimID)
		if err != nil {
			return err
		}
		if existing != nil && factPyStrOrEmpty(existing["cardinality"]) != normalizedCardinality {
			return fmt.Errorf("同一 claim 不能改变 cardinality；请创建新 claim 并显式取代旧 claim")
		}
		var existingEvidence map[string]any
		if existing != nil && normalizedEvidenceType != "" {
			existingEvidence, err = factLoadEvidence(c, claimID, normalizedEvidenceType, normalizedEvidenceID, normalizedEvidenceStance)
			if err != nil {
				return err
			}
		}
		if len(requestedSupersedes) > 0 {
			superseded, err = factResolveSupersedes(c, requestedSupersedes, claimID, normalizedScopeType, normalizedScopeID, conflictGroup)
			if err != nil {
				return err
			}
		}
		if existing != nil && existingEvidence != nil && len(requestedSupersedes) == 0 {
			claim = existing
			result = factClaimResult(existing, factClaimOutcome{idempotent: true})
			return nil
		}

		activeConflicts, err := factActiveConflicts(c, normalizedScopeType, normalizedScopeID, conflictGroup, claimID)
		if err != nil {
			return err
		}
		unsuperseded := make([]string, 0, len(activeConflicts))
		for _, item := range activeConflicts {
			if !factStringIn(superseded, item) {
				unsuperseded = append(unsuperseded, item)
			}
		}
		conflicting = unsuperseded
		targetStatus := "active"
		if len(unsuperseded) > 0 {
			targetStatus = "conflicted"
		}

		if existing == nil {
			if _, err := c.Exec(`
				INSERT INTO fact_claims (
					claim_id, scope_type, scope_id, fact_key,
					value_text, value_normalized, polarity, cardinality,
					conflict_group, stability, profile_section, authority, status,
					confidence, valid_from, valid_to, first_observed_at,
					last_confirmed_at, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				`,
				claimID,
				normalizedScopeType,
				normalizedScopeID,
				normalizedFactKey,
				normalizedValueText,
				valueNormalized,
				normalizedPolarity,
				normalizedCardinality,
				conflictGroup,
				normalizedStability,
				normalizedProfileSection,
				normalizedAuthority,
				targetStatus,
				normalizedConfidence,
				factFloatArg(normalizedValidFrom),
				factFloatArg(normalizedValidTo),
				now,
				now,
				now,
				now,
			); err != nil {
				return err
			}
			created = true
			if _, err := factAppendTransition(c, nil, &claimID, "assert", in.Reason, normalizedEvidenceType, normalizedEvidenceID, now); err != nil {
				return err
			}
		} else {
			previousStatus := factPyStrOrEmpty(existing["status"])
			restored = previousStatus == "superseded" || previousStatus == "retracted"
			if _, err := c.Exec(`
				UPDATE fact_claims
				SET value_text = ?, cardinality = ?, conflict_group = ?,
					stability = ?, profile_section = ?, authority = ?, status = ?,
					confidence = MAX(confidence, ?),
					valid_from = COALESCE(?, valid_from),
					valid_to = ?, last_confirmed_at = ?, updated_at = ?
				WHERE claim_id = ?
				`,
				normalizedValueText,
				normalizedCardinality,
				conflictGroup,
				normalizedStability,
				normalizedProfileSection,
				normalizedAuthority,
				targetStatus,
				normalizedConfidence,
				factFloatArg(normalizedValidFrom),
				factFloatArg(normalizedValidTo),
				now,
				now,
				claimID,
			); err != nil {
				return err
			}
			reinforced = previousStatus == "active" || previousStatus == "conflicted"
			transitionType := "reinforce"
			if restored {
				transitionType = "restore"
			}
			if _, err := factAppendTransition(c, &claimID, &claimID, transitionType, in.Reason, normalizedEvidenceType, normalizedEvidenceID, now); err != nil {
				return err
			}
		}

		for _, oldClaimID := range superseded {
			oldID := oldClaimID
			if _, err := c.Exec(`
				UPDATE fact_claims
				SET status = 'superseded', valid_to = COALESCE(valid_to, ?), updated_at = ?
				WHERE claim_id = ?
				`, now, now, oldID); err != nil {
				return err
			}
			if _, err := factAppendTransition(c, &oldID, &claimID, "supersede", in.Reason, normalizedEvidenceType, normalizedEvidenceID, now); err != nil {
				return err
			}
		}

		if err := factUpsertEvidenceInTx(c, claimID, normalizedEvidenceType, normalizedEvidenceID, normalizedEvidenceStance, factFloatOrDefault(in.EvidenceWeight, 1.0), now, in.EvidenceMetadata); err != nil {
			return err
		}

		claim, err = factLoadClaim(c, claimID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, fmt.Errorf("事实 claim 写入后丢失: %s", claimID)
	}
	if result != nil {
		return result, nil
	}
	return factClaimResult(claim, factClaimOutcome{
		created:     created,
		reinforced:  reinforced,
		restored:    restored,
		superseded:  superseded,
		conflicting: conflicting,
	}), nil
}

// factTrimmedDedupe 保序去重并去掉空白，对齐 Python 对 ID 列表的处理
func factTrimmedDedupe(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		token := factStrip(value)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}
