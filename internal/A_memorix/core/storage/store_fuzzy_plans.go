package storage

// A_memorix 模糊修改计划，对齐 Python metadata_store.py 的 memory_fuzzy_modify_plans 方法

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FuzzyModifyPlanInput 创建计划的入参
type FuzzyModifyPlanInput struct {
	RequestText    string
	Scope          string
	Plan           map[string]any
	Preview        map[string]any
	TargetPersonID string
	TargetChatID   string
	Status         string
	Confidence     float64
	RequestedBy    string
	Reason         string
	PlanID         *string
}

// FuzzyModifyPlanUpdate 更新计划的入参，nil 表示不修改
type FuzzyModifyPlanUpdate struct {
	Status     *string
	Plan       map[string]any
	Preview    map[string]any
	Execution  map[string]any
	Confidence *float64
	ExecutedAt *float64
	Reason     *string
}

// CreateFuzzyModifyPlan 创建模糊修改计划
func (s *Store) CreateFuzzyModifyPlan(input FuzzyModifyPlanInput) (map[string]any, error) {
	opID := ""
	if input.PlanID != nil {
		opID = strings.TrimSpace(*input.PlanID)
	}
	if opID == "" {
		opID = "fuzzy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	now := float64(time.Now().UnixNano()) / 1e9
	plan := input.Plan
	if plan == nil {
		plan = map[string]any{}
	}
	preview := input.Preview
	if preview == nil {
		preview = map[string]any{}
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "awaiting_confirmation"
	}
	payload := map[string]any{
		"plan_id":          opID,
		"request_text":     strings.TrimSpace(input.RequestText),
		"scope":            strings.TrimSpace(input.Scope),
		"target_person_id": strings.TrimSpace(input.TargetPersonID),
		"target_chat_id":   strings.TrimSpace(input.TargetChatID),
		"status":           status,
		"confidence":       input.Confidence,
		"plan":             plan,
		"preview":          preview,
		"execution":        map[string]any{},
		"created_at":       now,
		"updated_at":       now,
		"executed_at":      nil,
		"requested_by":     strings.TrimSpace(input.RequestedBy),
		"reason":           strings.TrimSpace(input.Reason),
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	const statement = "INSERT INTO memory_fuzzy_modify_plans (" +
		"plan_id, request_text, scope, target_person_id, target_chat_id, " +
		"status, confidence, plan_json, preview_json, execution_json, " +
		"created_at, updated_at, executed_at, requested_by, reason" +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)"
	if _, err := c.Exec(statement,
		opID, payload["request_text"], payload["scope"],
		optionalString(strings.TrimSpace(input.TargetPersonID)), optionalString(strings.TrimSpace(input.TargetChatID)),
		status, input.Confidence, jsonDumps(plan), jsonDumps(preview), jsonDumps(map[string]any{}),
		now, now, optionalString(strings.TrimSpace(input.RequestedBy)), optionalString(strings.TrimSpace(input.Reason))); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	created, err := s.GetFuzzyModifyPlan(opID)
	if err != nil {
		return nil, err
	}
	if created != nil {
		return created, nil
	}
	return payload, nil
}

// UpdateFuzzyModifyPlan 更新模糊修改计划
func (s *Store) UpdateFuzzyModifyPlan(planID string, update FuzzyModifyPlanUpdate) (map[string]any, error) {
	token := strings.TrimSpace(planID)
	if token == "" {
		return nil, nil
	}
	assignments := []string{"updated_at = ?"}
	params := []any{float64(time.Now().UnixNano()) / 1e9}
	if update.Status != nil {
		assignments = append(assignments, "status = ?")
		params = append(params, strings.TrimSpace(*update.Status))
	}
	if update.Plan != nil {
		assignments = append(assignments, "plan_json = ?")
		params = append(params, jsonDumps(update.Plan))
	}
	if update.Preview != nil {
		assignments = append(assignments, "preview_json = ?")
		params = append(params, jsonDumps(update.Preview))
	}
	if update.Execution != nil {
		assignments = append(assignments, "execution_json = ?")
		params = append(params, jsonDumps(update.Execution))
	}
	if update.Confidence != nil {
		assignments = append(assignments, "confidence = ?")
		params = append(params, *update.Confidence)
	}
	if update.ExecutedAt != nil {
		assignments = append(assignments, "executed_at = ?")
		params = append(params, *update.ExecutedAt)
	}
	if update.Reason != nil {
		assignments = append(assignments, "reason = ?")
		params = append(params, optionalString(strings.TrimSpace(*update.Reason)))
	}
	params = append(params, token)
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf("UPDATE memory_fuzzy_modify_plans SET %s WHERE plan_id = ?", strings.Join(assignments, ", "))
	result, err := c.Exec(statement, params...)
	if err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	affected, err := rowsAffected(result)
	if err != nil {
		return nil, err
	}
	if affected <= 0 {
		return nil, nil
	}
	return s.GetFuzzyModifyPlan(token)
}

// ClaimFuzzyModifyPlan 原子领取待执行计划
func (s *Store) ClaimFuzzyModifyPlan(planID string, staleAfterSeconds float64) (map[string]any, error) {
	token := strings.TrimSpace(planID)
	if token == "" {
		return nil, nil
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if staleAfterSeconds < 1 {
		staleAfterSeconds = 1
	}
	staleBefore := now - staleAfterSeconds
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	const statement = "UPDATE memory_fuzzy_modify_plans " +
		"SET status = 'executing', updated_at = ? " +
		"WHERE plan_id = ? AND (" +
		"status IN ('awaiting_confirmation', 'failed') " +
		"OR (status = 'executing' AND updated_at <= ?))"
	result, err := c.Exec(statement, now, token, staleBefore)
	if err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	affected, err := rowsAffected(result)
	if err != nil {
		return nil, err
	}
	if affected <= 0 {
		return nil, nil
	}
	return s.GetFuzzyModifyPlan(token)
}

// ListFuzzyModifyPlans 列出模糊修改计划
func (s *Store) ListFuzzyModifyPlans(limit int, statuses []string, scope string) ([]map[string]any, error) {
	normalizedStatuses := make([]string, 0, len(statuses))
	for _, item := range statuses {
		token := strings.TrimSpace(item)
		if token != "" {
			normalizedStatuses = append(normalizedStatuses, token)
		}
	}
	where := make([]string, 0, 2)
	params := make([]any, 0, len(normalizedStatuses)+2)
	if len(normalizedStatuses) > 0 {
		where = append(where, fmt.Sprintf("status IN (%s)", sqlPlaceholders(len(normalizedStatuses))))
		params = append(params, stringArgs(normalizedStatuses)...)
	}
	if scopeToken := strings.TrimSpace(scope); scopeToken != "" {
		where = append(where, "scope = ?")
		params = append(params, scopeToken)
	}
	if limit < 1 {
		limit = 50
	}
	params = append(params, limit)
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	query := fmt.Sprintf("SELECT * FROM memory_fuzzy_modify_plans %s ORDER BY created_at DESC LIMIT ?", whereSQL)
	rows, err := s.Query(query, params...)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, fuzzyModifyPlanRowToDict(row))
	}
	return out, nil
}

// GetFuzzyModifyPlan 读取模糊修改计划
func (s *Store) GetFuzzyModifyPlan(planID string) (map[string]any, error) {
	token := strings.TrimSpace(planID)
	if token == "" {
		return nil, nil
	}
	rows, err := s.Query("SELECT * FROM memory_fuzzy_modify_plans WHERE plan_id = ? LIMIT 1", token)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return fuzzyModifyPlanRowToDict(rows[0]), nil
}

// fuzzyModifyPlanRowToDict 解码计划行的 JSON 字段
func fuzzyModifyPlanRowToDict(row map[string]any) map[string]any {
	payload := make(map[string]any, len(row)+3)
	for key, value := range row {
		payload[key] = value
	}
	payload["plan"] = jsonLoads(payload["plan_json"], map[string]any{})
	payload["preview"] = jsonLoads(payload["preview_json"], map[string]any{})
	payload["execution"] = jsonLoads(payload["execution_json"], map[string]any{})
	delete(payload, "plan_json")
	delete(payload, "preview_json")
	delete(payload, "execution_json")
	payload["target_person_id"] = stringFromRow(payload, "target_person_id")
	payload["target_chat_id"] = stringFromRow(payload, "target_chat_id")
	payload["requested_by"] = stringFromRow(payload, "requested_by")
	payload["reason"] = stringFromRow(payload, "reason")
	return payload
}
