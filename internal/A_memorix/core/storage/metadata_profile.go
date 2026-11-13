package storage

// A_memorix 人物画像开关与活跃人物，逐条对齐 Python core/storage/metadata_profile.py

import (
	"database/sql"
	"errors"

	"golang.org/x/text/cases"
)

// personProfileAliasCaser 对齐 Python str.casefold 的完整大小写折叠
var personProfileAliasCaser = cases.Fold()

// personProfileScanner 覆盖 *sql.Row 与 *sql.Rows 的扫描能力
type personProfileScanner interface {
	Scan(dest ...any) error
}

// personProfileNullableFloat 把 None/float 语义转成 SQL 参数
func personProfileNullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// personProfileFloatValue 把可空 REAL 列转成 Python None/float 对应的 any
func personProfileFloatValue(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

// personProfileFloatAny 把可选浮点转成 Python None/float 对应的 any
func personProfileFloatAny(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// personProfileJSONList 对齐 Python 快照 _load_list：非列表或解析失败一律返回空列表
func personProfileJSONList(raw string) []any {
	if raw == "" {
		return []any{}
	}
	list, ok := jsonLoads(raw, []any{}).([]any)
	if !ok {
		return []any{}
	}
	return list
}

// personProfileAnyList 把 []string 转成 Python list 同构的 []any
func personProfileAnyList(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// personProfileAnyMapList 把 []map[string]any 转成 Python list 同构的 []any
func personProfileAnyMapList(values []map[string]any) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// SetPersonProfileSwitch 设置人物画像自动注入开关（按 stream_id + user_id），
// 对齐 Python set_person_profile_switch
func (s *Store) SetPersonProfileSwitch(streamID, userID string, enabled bool, updatedAt *float64) error {
	if streamID == "" || userID == "" {
		return errors.New("stream_id 和 user_id 不能为空")
	}
	ts := nowTimestamp()
	if updatedAt != nil {
		ts = *updatedAt
	}
	enabledValue := 0
	if enabled {
		enabledValue = 1
	}
	c, err := s.Conn()
	if err != nil {
		return err
	}
	if _, err := c.Exec(`
		INSERT INTO person_profile_switches (stream_id, user_id, enabled, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(stream_id, user_id) DO UPDATE SET
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, streamID, userID, enabledValue, ts); err != nil {
		return err
	}
	return c.Commit()
}

// GetPersonProfileSwitch 读取人物画像自动注入开关，对齐 Python get_person_profile_switch
func (s *Store) GetPersonProfileSwitch(streamID, userID string, defaultValue bool) (bool, error) {
	if streamID == "" || userID == "" {
		return defaultValue, nil
	}
	c, err := s.Conn()
	if err != nil {
		return false, err
	}
	var enabled int64
	err = c.QueryRow(
		"SELECT enabled FROM person_profile_switches WHERE stream_id = ? AND user_id = ?",
		streamID, userID,
	).Scan(&enabled)
	if isNoRows(err) {
		return defaultValue, nil
	}
	if err != nil {
		return false, err
	}
	return enabled != 0, nil
}

// GetEnabledPersonProfileSwitches 获取已开启画像注入开关的会话范围，
// 对齐 Python get_enabled_person_profile_switches
func (s *Store) GetEnabledPersonProfileSwitches(limit int) ([]map[string]any, error) {
	if limit < 1 {
		limit = 1
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`
		SELECT stream_id, user_id, enabled, updated_at
		FROM person_profile_switches
		WHERE enabled = 1
		ORDER BY updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var (
			streamID  string
			userID    string
			enabled   int64
			updatedAt sql.NullFloat64
		)
		if err := rows.Scan(&streamID, &userID, &enabled, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"stream_id":  streamID,
			"user_id":    userID,
			"enabled":    enabled != 0,
			"updated_at": personProfileFloatValue(updatedAt),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// MarkPersonProfileActive 记录活跃人物（供定时按需刷新），对齐 Python mark_person_profile_active
func (s *Store) MarkPersonProfileActive(streamID, userID, personID string, seenAt *float64) error {
	if streamID == "" || userID == "" || personID == "" {
		return nil
	}
	ts := nowTimestamp()
	if seenAt != nil {
		ts = *seenAt
	}
	c, err := s.Conn()
	if err != nil {
		return err
	}
	if _, err := c.Exec(`
		INSERT INTO person_profile_active_persons (stream_id, user_id, person_id, last_seen_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(stream_id, user_id, person_id) DO UPDATE SET
			last_seen_at = excluded.last_seen_at
	`, streamID, userID, personID, ts); err != nil {
		return err
	}
	return c.Commit()
}

// GetActivePersonIDsForEnabledSwitches 获取已开启开关范围内的活跃人物集合，
// 对齐 Python get_active_person_ids_for_enabled_switches
func (s *Store) GetActivePersonIDsForEnabledSwitches(activeAfter *float64, limit int) ([]string, error) {
	query := `
		SELECT a.person_id, MAX(a.last_seen_at) AS last_seen
		FROM person_profile_active_persons a
		JOIN person_profile_switches s
		  ON a.stream_id = s.stream_id AND a.user_id = s.user_id
		WHERE s.enabled = 1`
	args := make([]any, 0, 2)
	if activeAfter != nil {
		query += " AND a.last_seen_at >= ?"
		args = append(args, *activeAfter)
	}
	query += `
		GROUP BY a.person_id
		ORDER BY last_seen DESC
		LIMIT ?
	`
	if limit < 1 {
		limit = 1
	}
	args = append(args, limit)

	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	personIDs := make([]string, 0)
	for rows.Next() {
		var (
			personID string
			lastSeen sql.NullFloat64
		)
		if err := rows.Scan(&personID, &lastSeen); err != nil {
			return nil, err
		}
		if personID == "" {
			continue
		}
		personIDs = append(personIDs, personID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return personIDs, nil
}
