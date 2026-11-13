package personinfo

// 人物画像管理：桥接 Memorix memory_profile_admin / memory_fact_admin
// 对齐 MaiBot person_profile_service 与 memory_flow_service 的 admin 调用

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix"
)

// ProfileQuery 人物画像查询参数
type ProfileQuery struct {
	PersonID      string
	PersonKeyword string
	ContextText   string
	Limit         int
	ForceRefresh  bool
	TimeoutMS     int
}

// ProfileEvidenceQuery 画像证据查询参数
type ProfileEvidenceQuery struct {
	PersonID      string
	PersonKeyword string
	Limit         int
	ForceRefresh  bool
	TimeoutMS     int
}

// memorixInvoker Memorix 宿主暴露的通用 admin 调用入口
type memorixInvoker interface {
	Invoke(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error)
}

func memorixHostValue() any {
	return memorix.DefaultRegistry().Host()
}

func (r *Registry) memorixHost() (memorixInvoker, error) {
	host := memorix.DefaultRegistry().Host()
	if host == nil {
		return nil, ErrMemorixUnavailable
	}
	invoker, ok := host.(memorixInvoker)
	if !ok {
		return nil, ErrProfileAdminUnavailable
	}
	return invoker, nil
}

func (r *Registry) invokeAdmin(ctx context.Context, component, action string, args map[string]any, timeoutMS int) (map[string]any, error) {
	invoker, err := r.memorixHost()
	if err != nil {
		return nil, err
	}
	payload := make(map[string]any, len(args)+1)
	for key, value := range args {
		payload[key] = value
	}
	payload["action"] = action
	var timeout *int
	if timeoutMS > 0 {
		timeout = &timeoutMS
	}
	raw, err := invoker.Invoke(ctx, component, payload, timeout)
	if err != nil {
		return nil, err
	}
	out, err := mapFromAny(raw)
	if err != nil {
		return nil, fmt.Errorf("personinfo: 解析 %s 响应失败: %w", component, err)
	}
	return out, nil
}

// RawProfileAdmin 原始画像管理调用，供 WebUI 与调试使用
func (r *Registry) RawProfileAdmin(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	return r.invokeAdmin(ctx, "memory_profile_admin", action, args, 0)
}

// RawFactAdmin 原始人物事实管理调用
func (r *Registry) RawFactAdmin(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	return r.invokeAdmin(ctx, "memory_fact_admin", action, args, 0)
}

// QueryProfile 查询人物画像，对齐 memory_profile_admin.query
func (r *Registry) QueryProfile(ctx context.Context, q ProfileQuery) (*ProfileSnapshot, error) {
	personID := strings.TrimSpace(q.PersonID)
	keyword := strings.TrimSpace(q.PersonKeyword)
	if personID == "" && keyword == "" {
		return nil, ErrProfileQueryTargetMissing
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 12
	}
	if limit > 50 {
		limit = 50
	}
	args := map[string]any{"limit": limit, "force_refresh": q.ForceRefresh}
	if personID != "" {
		args["person_id"] = personID
	}
	if keyword != "" {
		args["person_keyword"] = keyword
		args["keyword"] = keyword
	}
	if text := strings.TrimSpace(q.ContextText); text != "" {
		args["context_text"] = text
	}
	payload, err := r.invokeAdmin(ctx, "memory_profile_admin", "query", args, q.TimeoutMS)
	if err != nil {
		return nil, err
	}
	return ProfileSnapshotFromPayload(payload), nil
}

// ProfileEvidenceSnapshot 查询画像证据链，对齐 memory_profile_admin.evidence
func (r *Registry) ProfileEvidenceSnapshot(ctx context.Context, q ProfileEvidenceQuery) (*ProfileSnapshot, error) {
	personID := strings.TrimSpace(q.PersonID)
	keyword := strings.TrimSpace(q.PersonKeyword)
	if personID == "" && keyword == "" {
		return nil, ErrProfileQueryTargetMissing
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 12
	}
	args := map[string]any{"limit": limit, "force_refresh": q.ForceRefresh}
	if personID != "" {
		args["person_id"] = personID
	}
	if keyword != "" {
		args["person_keyword"] = keyword
		args["keyword"] = keyword
	}
	payload, err := r.invokeAdmin(ctx, "memory_profile_admin", "evidence", args, q.TimeoutMS)
	if err != nil {
		return nil, err
	}
	return ProfileSnapshotFromPayload(payload), nil
}

// CorrectProfileEvidence 纠错画像证据
func (r *Registry) CorrectProfileEvidence(ctx context.Context, args map[string]any) (map[string]any, error) {
	return r.RawProfileAdmin(ctx, "correct_evidence", args)
}

// FactConflictResolution 人物事实冲突处置
type FactConflictResolution struct {
	ClaimID string `json:"claim_id"`
	Action  string `json:"action"`
	Reason  string `json:"reason,omitempty"`
}

// ResolveFactConflict 通过撤回或恢复处置冲突事实
func (r *Registry) ResolveFactConflict(ctx context.Context, resolution FactConflictResolution) (map[string]any, error) {
	claimID := strings.TrimSpace(resolution.ClaimID)
	if claimID == "" {
		return nil, fmt.Errorf("personinfo: 冲突处置需要 claim_id")
	}
	action := strings.ToLower(strings.TrimSpace(resolution.Action))
	if action != "retract" && action != "restore" {
		return nil, fmt.Errorf("personinfo: 不支持的冲突处置动作: %s", action)
	}
	reason := strings.TrimSpace(resolution.Reason)
	if reason == "" {
		reason = "person_fact_conflict_" + action
	}
	return r.RawFactAdmin(ctx, action, map[string]any{"claim_id": claimID, "reason": reason})
}

// ProfileStatus 查询画像刷新队列状态
func (r *Registry) ProfileStatus(ctx context.Context, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 20
	}
	return r.RawProfileAdmin(ctx, "status", map[string]any{"limit": limit})
}

// ProcessPendingProfileRefresh 处理一批画像刷新任务
func (r *Registry) ProcessPendingProfileRefresh(ctx context.Context, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 20
	}
	return r.RawProfileAdmin(ctx, "process_pending", map[string]any{"limit": limit})
}

// ListProfileSnapshots 列出人物画像快照
func (r *Registry) ListProfileSnapshots(ctx context.Context, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	return r.RawProfileAdmin(ctx, "list", map[string]any{"limit": limit})
}

// GetAliases 查询人物画像别名覆盖
func (r *Registry) GetAliases(ctx context.Context, personID string) (map[string]any, error) {
	return r.RawProfileAdmin(ctx, "get_aliases", map[string]any{"person_id": strings.TrimSpace(personID)})
}

// SetAliases 设置人物画像别名覆盖
func (r *Registry) SetAliases(ctx context.Context, personID string, aliases []string, updatedBy string) (map[string]any, error) {
	return r.RawProfileAdmin(ctx, "set_aliases", map[string]any{
		"person_id": strings.TrimSpace(personID), "aliases": aliases, "updated_by": updatedBy,
	})
}

// DeleteAliases 删除人物画像别名覆盖
func (r *Registry) DeleteAliases(ctx context.Context, personID string) (map[string]any, error) {
	return r.RawProfileAdmin(ctx, "delete_aliases", map[string]any{"person_id": strings.TrimSpace(personID)})
}

// SetProfileOverride 设置画像手工覆盖文本
func (r *Registry) SetProfileOverride(ctx context.Context, personID, text, updatedBy string) (map[string]any, error) {
	return r.RawProfileAdmin(ctx, "set_override", map[string]any{
		"person_id": strings.TrimSpace(personID), "override_text": text, "updated_by": updatedBy,
	})
}

// DeleteProfileOverride 删除画像手工覆盖
func (r *Registry) DeleteProfileOverride(ctx context.Context, personID string) (map[string]any, error) {
	return r.RawProfileAdmin(ctx, "delete_override", map[string]any{"person_id": strings.TrimSpace(personID)})
}

// ProfileSnapshotFromPayload 将 Memorix 返回 map 归一化为 ProfileSnapshot
func ProfileSnapshotFromPayload(payload map[string]any) *ProfileSnapshot {
	if payload == nil {
		return &ProfileSnapshot{Success: false, Error: "empty profile payload", Raw: map[string]any{}}
	}
	snap := &ProfileSnapshot{Raw: payload}
	snap.Success = boolFromAny(payload["success"])
	snap.PersonID = stringFromAny(payload["person_id"])
	snap.PersonName = firstString(payload["person_name"], payload["name"])
	snap.ProfileText = firstString(payload["profile_text"], payload["summary"])
	snap.Summary = firstString(payload["summary"], payload["profile_text"])
	snap.AutoProfileText = stringFromAny(payload["auto_profile_text"])
	snap.ManualOverrideText = stringFromAny(payload["manual_override_text"])
	snap.ProfileSource = stringFromAny(payload["profile_source"])
	snap.HasManualOverride = boolFromAny(payload["has_manual_override"])
	snap.FromCache = boolFromAny(payload["from_cache"])
	snap.EvidenceUnchanged = boolFromAny(payload["evidence_unchanged"])
	snap.ProfileVersion = int64FromAny(payload["profile_version"])
	snap.UpdatedAt = payload["updated_at"]
	snap.ExpiresAt = payload["expires_at"]
	snap.UncertainFactCount = intFromAny(payload["uncertain_fact_count"])
	snap.Error = stringFromAny(payload["error"])
	snap.Aliases = stringListFromAny(payload["aliases"])
	snap.Sections = sectionsFromAny(payload["sections"])
	if len(snap.Sections) == 0 && snap.ProfileText != "" && IsStructuredProfileText(snap.ProfileText) {
		snap.Sections = ParseProfileSections(snap.ProfileText)
	}
	if traits, ok := payload["traits"]; ok {
		snap.Traits = stringListFromAny(traits)
	}
	if len(snap.Traits) == 0 && snap.ProfileText != "" {
		snap.Traits = ProfileTraits(snap.ProfileText, 8)
	}
	snap.Evidence = evidenceFromAny(payload["evidence"])
	snap.UncertainCandidates = uncertainCandidatesFromAny(payload["uncertain_candidates"])
	if snap.UncertainFactCount == 0 {
		snap.UncertainFactCount = len(snap.UncertainCandidates)
	}
	if !snap.Success && snap.Error == "" {
		snap.Error = "人物画像查询失败"
	}
	return snap
}

func evidenceFromAny(value any) []ProfileEvidence {
	items := mapListFromAny(value)
	out := make([]ProfileEvidence, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, ProfileEvidence{
			EvidenceKey:    stringFromAny(item["evidence_key"]),
			EvidenceType:   stringFromAny(item["evidence_type"]),
			Hash:           stringFromAny(item["hash"]),
			Content:        stringFromAny(item["content"]),
			Source:         stringFromAny(item["source"]),
			SourceType:     stringFromAny(item["source_type"]),
			Metadata:       mapFromAnyLoose(item["metadata"]),
			Score:          item["score"],
			Confidence:     item["confidence"],
			CorrectionMode: stringFromAny(item["correction_mode"]),
			Deletable:      boolFromAny(item["deletable"]),
			NotDeletable:   stringFromAny(item["not_deletable_reason"]),
			Raw:            item,
		})
	}
	return out
}

func uncertainCandidatesFromAny(value any) []ProfileUncertainCandidate {
	items := mapListFromAny(value)
	out := make([]ProfileUncertainCandidate, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		text := firstString(item["text"], item["value_text"], item["content"])
		claimID := stringFromAny(item["claim_id"])
		if text == "" && claimID == "" {
			continue
		}
		out = append(out, ProfileUncertainCandidate{ClaimID: claimID, Text: text})
	}
	return out
}

func sectionsFromAny(value any) map[string][]string {
	raw := mapFromAnyLoose(value)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string][]string, len(raw))
	for key, item := range raw {
		lines := stringListFromAny(item)
		if len(lines) == 0 {
			if text := stringFromAny(item); text != "" {
				lines = []string{text}
			}
		}
		out[key] = lines
	}
	return out
}

func mapFromAny(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	if out, ok := value.(map[string]any); ok {
		return out, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func mapFromAnyLoose(value any) map[string]any {
	out, _ := mapFromAny(value)
	return out
}

func mapListFromAny(value any) []map[string]any {
	if value == nil {
		return nil
	}
	if items, ok := value.([]map[string]any); ok {
		return items
	}
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
			continue
		}
		if m := mapFromAnyLoose(item); len(m) > 0 {
			out = append(out, m)
		}
	}
	return out
}

func stringListFromAny(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := stringFromAny(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{strings.TrimSpace(typed)}
	default:
		return nil
	}
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		if text := stringFromAny(value); text != "" {
			return text
		}
	}
	return ""
}

func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		}
		return false
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed != 0
	default:
		return false
	}
}

func intFromAny(value any) int {
	return int(int64FromAny(value))
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		var parsed int64
		_, _ = fmt.Sscan(strings.TrimSpace(typed), &parsed)
		return parsed
	default:
		return 0
	}
}
