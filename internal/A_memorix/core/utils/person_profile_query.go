package utils

// A_memorix 人物画像查询、快照短路与注入块，对齐 Python person_profile_service.py 的查询段

import (
	"context"
	"fmt"
)

// IsSnapshotStale 对齐 _is_snapshot_stale，优先按 expires_at 判断
func (s *PersonProfileService) IsSnapshotStale(snapshot map[string]any, ttlSeconds float64) bool {
	if len(snapshot) == 0 {
		return true
	}
	now := s.nowFunc()
	if expiresAt, ok := snapshot["expires_at"]; ok && expiresAt != nil {
		parsed, err := pyFloat64(expiresAt)
		if err != nil {
			return true
		}
		return now >= parsed
	}
	updatedAt := 0.0
	if parsed, err := pyFloat64(snapshot["updated_at"]); err == nil {
		updatedAt = parsed
	}
	return now-updatedAt >= ttlSeconds
}

// ApplyManualOverride 对齐 _apply_manual_override，保留自动画像文本并叠加手工覆盖
func (s *PersonProfileService) ApplyManualOverride(personID string, profilePayload map[string]any) map[string]any {
	payload := make(map[string]any, len(profilePayload)+6)
	for key, value := range profilePayload {
		payload[key] = value
	}
	autoText := personProfileMapText(payload, "profile_text")
	payload["auto_profile_text"] = autoText
	payload["has_manual_override"] = false
	payload["manual_override_text"] = ""
	payload["override_updated_at"] = nil
	payload["override_updated_by"] = ""
	payload["profile_source"] = "auto_snapshot"

	if personID == "" || s.store == nil {
		return payload
	}

	override, err := s.store.GetPersonProfileOverride(personID)
	if err != nil {
		s.logger.Warning(fmt.Sprintf("读取人物画像手工覆盖失败: person_id=%s, err=%v", personID, err))
		return payload
	}
	if len(override) == 0 {
		return payload
	}

	manualText := pyStrip(personProfileMapText(override, "override_text"))
	if manualText == "" {
		return payload
	}

	payload["has_manual_override"] = true
	payload["manual_override_text"] = manualText
	payload["override_updated_at"] = override["updated_at"]
	payload["override_updated_by"] = personProfileMapText(override, "updated_by")
	payload["profile_text"] = manualText
	payload["profile_source"] = "manual_override"
	return payload
}

// QueryPersonProfile 对齐 query_person_profile，查询或刷新人物画像
// topK 为 0 时取 Python 默认 12，ttlSeconds 为 0 表示不写过期时间
func (s *PersonProfileService) QueryPersonProfile(ctx context.Context, personID string, personKeyword string, topK int, ttlSeconds float64, forceRefresh bool, sourceNote string) (map[string]any, error) {
	if topK <= 0 {
		topK = 12
	}
	pid := pyStrip(personID)
	keyword := pyStrip(personKeyword)
	if pid == "" && keyword != "" {
		resolved, err := s.ResolvePersonID(ctx, keyword)
		if err != nil {
			return nil, err
		}
		pid = resolved
	}
	if pid == "" {
		return map[string]any{
			"success": false,
			"error":   "person_id 无效，且未能通过别名解析",
		}, nil
	}
	if s.store == nil {
		return nil, fmt.Errorf("人物画像存储未初始化")
	}

	latest, err := s.store.GetLatestPersonProfileSnapshot(pid)
	if err != nil {
		return nil, err
	}
	if !forceRefresh && !s.IsSnapshotStale(latest, ttlSeconds) {
		aliases, primaryName := s.GetPersonAliases(ctx, pid)
		payload := map[string]any{
			"success":     true,
			"person_id":   pid,
			"person_name": primaryName,
			"from_cache":  true,
		}
		payload = mergePersonProfilePayload(payload, latest)
		if len(aliases) > 0 && personProfileValueMissing(payload["aliases"]) {
			payload["aliases"] = aliases
		}
		return s.ApplyManualOverride(pid, payload), nil
	}

	aliases, primaryName := s.GetPersonAliases(ctx, pid)
	if len(aliases) == 0 && keyword != "" {
		aliases = []string{keyword}
		primaryName = keyword
	}
	relationLimit := topK * 2
	if relationLimit < 10 {
		relationLimit = 10
	}
	relationEdges, err := s.CollectRelationEvidence(aliases, relationLimit, pid)
	if err != nil {
		return nil, err
	}
	vectorTopK := topK
	if vectorTopK < 4 {
		vectorTopK = 4
	}
	vectorEvidence, err := s.CollectVectorEvidence(ctx, aliases, vectorTopK, pid)
	if err != nil {
		return nil, err
	}
	factClaimLimit := topK * 8
	if factClaimLimit < 32 {
		factClaimLimit = 32
	}
	factClaims, err := s.collectPersonFactClaims(pid, factClaimLimit)
	if err != nil {
		return nil, err
	}
	fingerprint := s.ProfileEvidenceFingerprint(pid, primaryName, aliases, relationEdges, vectorEvidence, factClaims)

	var expiresAt *float64
	if ttlSeconds > 0 {
		value := s.nowFunc() + ttlSeconds
		expiresAt = &value
	}

	if len(latest) > 0 && personProfileMapStr(latest, "evidence_fingerprint") == fingerprint {
		snapshotID, ok := latest["snapshot_id"]
		if !ok || snapshotID == nil {
			return nil, fmt.Errorf("人物画像快照缺少 snapshot_id，无法执行证据版本短路")
		}
		numericID, err := pyFloat64(snapshotID)
		if err != nil {
			return nil, fmt.Errorf("人物画像快照缺少 snapshot_id，无法执行证据版本短路")
		}
		refreshNote := sourceNote
		if refreshNote == "" {
			refreshNote = personProfileMapStr(latest, "source_note")
		}
		refreshed, err := s.store.RefreshPersonProfileSnapshotCache(int64(numericID), expiresAt, refreshNote)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{
			"success":            true,
			"person_id":          pid,
			"person_name":        primaryName,
			"from_cache":         true,
			"evidence_unchanged": true,
		}
		payload = mergePersonProfilePayload(payload, refreshed)
		if len(aliases) > 0 && personProfileValueMissing(payload["aliases"]) {
			payload["aliases"] = aliases
		}
		return s.ApplyManualOverride(pid, payload), nil
	}

	classifiedBuckets, classifyErr := s.classifyProfileEvidence(ctx, pid, primaryName, aliases, relationEdges, vectorEvidence)
	if classifyErr != nil {
		return nil, classifyErr
	}
	mergedBuckets, mergeErr := s.mergeFactClaimBuckets(classifiedBuckets, factClaims)
	if mergeErr != nil {
		s.logger.Warning(fmt.Sprintf("人物画像事实声明合并失败: person_id=%s, err=%v", pid, mergeErr))
	} else {
		classifiedBuckets = mergedBuckets
	}

	evidenceIDs := make([]string, 0, len(relationEdges)+len(vectorEvidence))
	seen := make(map[string]struct{}, len(relationEdges)+len(vectorEvidence))
	for _, item := range append(append([]map[string]any{}, relationEdges...), vectorEvidence...) {
		hashValue := pyStrip(pyStr(item["hash"]))
		if hashValue == "" {
			continue
		}
		if _, exists := seen[hashValue]; exists {
			continue
		}
		seen[hashValue] = struct{}{}
		evidenceIDs = append(evidenceIDs, hashValue)
	}

	profileText := s.buildProfileText(pid, primaryName, aliases, relationEdges, vectorEvidence, classifiedBuckets)
	claimIDs := make([]string, 0, len(factClaims))
	for _, claim := range factClaims {
		claimIDs = append(claimIDs, pyStr(claim["claim_id"]))
	}

	snapshot, err := s.store.UpsertPersonProfileSnapshot(PersonProfileSnapshotWrite{
		PersonID:            pid,
		ProfileText:         profileText,
		Aliases:             aliases,
		RelationEdges:       relationEdges,
		VectorEvidence:      vectorEvidence,
		EvidenceIDs:         evidenceIDs,
		FactClaimIDs:        claimIDs,
		EvidenceFingerprint: fingerprint,
		ExpiresAt:           expiresAt,
		SourceNote:          sourceNote,
	})
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"success":     true,
		"person_id":   pid,
		"person_name": primaryName,
		"from_cache":  false,
	}
	payload = mergePersonProfilePayload(payload, snapshot)
	return s.ApplyManualOverride(pid, payload), nil
}

// FormatPersonaProfileBlock 对齐 format_persona_profile_block，生成 replyer 注入块
func FormatPersonaProfileBlock(profile map[string]any) string {
	if len(profile) == 0 || !isTruthy(profile["success"]) {
		return ""
	}
	text := pyStrip(personProfileMapText(profile, "profile_text"))
	if text == "" {
		return ""
	}
	text = BuildProfileInjectionText(text, 2, 1, nil, true)
	if text == "" {
		return ""
	}
	return "【人物画像-内部参考】\n" + text + "\n仅供内部推理，不要向用户逐字复述。"
}

// mergePersonProfilePayload 按 Python 字典展开语义合并，overlay 覆盖 base
func mergePersonProfilePayload(base map[string]any, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

// personProfileValueMissing 对齐 Python 真值判断，空串、空列表与 nil 视为缺失
func personProfileValueMissing(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return pyStrip(typed) == ""
	case []string:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
}

// personProfileMapText 对齐 str(value or "")：缺失、None 与假值都返回空串
func personProfileMapText(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || !isTruthy(value) {
		return ""
	}
	return pyStr(value)
}

// personProfileMapStr 对齐 str(values.get(key, ""))：仅缺键返回空串
func personProfileMapStr(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	return pyStr(value)
}
