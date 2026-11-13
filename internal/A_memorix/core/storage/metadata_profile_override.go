package storage

// A_memorix 人物画像手工覆盖与人工别名覆盖，逐条对齐 Python core/storage/metadata_profile.py

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// GetPersonProfileOverride 获取人物画像手工覆盖内容，对齐 Python get_person_profile_override
func (s *Store) GetPersonProfileOverride(personID string) (map[string]any, error) {
	if personID == "" {
		return nil, nil
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	var (
		storedPersonID string
		overrideText   sql.NullString
		updatedAt      sql.NullFloat64
		updatedBy      sql.NullString
		source         sql.NullString
	)
	err = c.QueryRow(`
		SELECT person_id, override_text, updated_at, updated_by, source
		FROM person_profile_overrides
		WHERE person_id = ?
		LIMIT 1
	`, personID).Scan(&storedPersonID, &overrideText, &updatedAt, &updatedBy, &source)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"person_id":     storedPersonID,
		"override_text": overrideText.String,
		"updated_at":    personProfileFloatValue(updatedAt),
		"updated_by":    updatedBy.String,
		"source":        source.String,
	}, nil
}

// SetPersonProfileOverride 写入人物画像手工覆盖；空文本等价于清除覆盖，
// 对齐 Python set_person_profile_override（Python 侧 source 默认 "webui"，Go 由调用方显式传入）
func (s *Store) SetPersonProfileOverride(
	personID, overrideText, updatedBy, source string,
	updatedAt *float64,
) (map[string]any, error) {
	if personID == "" {
		return nil, errors.New("person_id 不能为空")
	}
	text := strings.TrimSpace(overrideText)
	if text == "" {
		if _, err := s.DeletePersonProfileOverride(personID); err != nil {
			return nil, err
		}
		return map[string]any{
			"person_id":     personID,
			"override_text": "",
			"updated_at":    nil,
			"updated_by":    updatedBy,
			"source":        source,
		}, nil
	}

	ts := nowTimestamp()
	if updatedAt != nil {
		ts = *updatedAt
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(`
		INSERT INTO person_profile_overrides (
			person_id, override_text, updated_at, updated_by, source
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(person_id) DO UPDATE SET
			override_text = excluded.override_text,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by,
			source = excluded.source
	`, personID, text, ts, updatedBy, source); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}

	stored, err := s.GetPersonProfileOverride(personID)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		return stored, nil
	}
	return map[string]any{
		"person_id":     personID,
		"override_text": text,
		"updated_at":    ts,
		"updated_by":    updatedBy,
		"source":        source,
	}, nil
}

// DeletePersonProfileOverride 删除人物画像手工覆盖，对齐 Python delete_person_profile_override
func (s *Store) DeletePersonProfileOverride(personID string) (bool, error) {
	if personID == "" {
		return false, nil
	}
	c, err := s.Conn()
	if err != nil {
		return false, err
	}
	result, err := c.Exec("DELETE FROM person_profile_overrides WHERE person_id = ?", personID)
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// normalizePersonProfileAliases 校验并按大小写无关规则去重人工别名，
// 对齐 Python _normalize_person_profile_aliases
func normalizePersonProfileAliases(aliases []string) ([]string, error) {
	// Python 侧 None 与 [] 的报错文案不同，Go 以 nil 切片对应 None
	if aliases == nil {
		return nil, errors.New("aliases 必须是字符串列表")
	}
	normalized := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, rawAlias := range aliases {
		alias := strings.TrimSpace(rawAlias)
		if alias == "" {
			continue
		}
		if utf8.RuneCountInString(alias) > 128 {
			return nil, errors.New("单个别名不能超过 128 个字符")
		}
		key := personProfileAliasCaser.String(alias)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, alias)
	}
	if len(normalized) == 0 {
		return nil, errors.New("至少需要保留一个人物别名")
	}
	if len(normalized) > 100 {
		return nil, errors.New("人物别名不能超过 100 个")
	}
	return normalized, nil
}

// GetPersonProfileAliasOverride 读取人工维护的人物别名集合，
// 对齐 Python get_person_profile_alias_override
func (s *Store) GetPersonProfileAliasOverride(personID string) (map[string]any, error) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return nil, nil
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	var (
		storedPersonID string
		aliasesJSON    sql.NullString
		updatedAt      sql.NullFloat64
		updatedBy      sql.NullString
		source         sql.NullString
	)
	err = c.QueryRow(`
		SELECT person_id, aliases_json, updated_at, updated_by, source
		FROM person_profile_alias_overrides
		WHERE person_id = ?
		LIMIT 1
	`, token).Scan(&storedPersonID, &aliasesJSON, &updatedAt, &updatedBy, &source)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	raw := aliasesJSON.String
	if raw == "" {
		raw = "[]"
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("人物别名覆盖数据解析失败: person_id=%s: %w", token, err)
	}
	list, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("人物别名覆盖数据格式错误: person_id=%s", token)
	}
	aliases := make([]any, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("人物别名覆盖数据格式错误: person_id=%s", token)
		}
		aliases = append(aliases, text)
	}
	return map[string]any{
		"person_id":  storedPersonID,
		"aliases":    aliases,
		"updated_at": personProfileFloatValue(updatedAt),
		"updated_by": updatedBy.String,
		"source":     source.String,
	}, nil
}

// PersonProfileAliasOverrideInput 对齐 Python set_person_profile_alias_override 的关键字参数
type PersonProfileAliasOverrideInput struct {
	// PersonID 人物 ID，会去除首尾空白
	PersonID string
	// Aliases 完整人工别名集合
	Aliases []string
	// UpdatedBy 操作者
	UpdatedBy string
	// Source 来源，Python 默认 "webui"
	Source string
	// UpdatedAt 指定更新时间，nil 表示取系统时间
	UpdatedAt *float64
	// Conn 指定连接，nil 表示使用存储主连接；传入时不自行提交
	Conn *Conn
}

// SetPersonProfileAliasOverride 写入完整的人工别名集合，保存后该集合优先于自动推导结果，
// 对齐 Python set_person_profile_alias_override
func (s *Store) SetPersonProfileAliasOverride(input PersonProfileAliasOverrideInput) (map[string]any, error) {
	token := strings.TrimSpace(input.PersonID)
	if token == "" {
		return nil, errors.New("person_id 不能为空")
	}
	normalized, err := normalizePersonProfileAliases(input.Aliases)
	if err != nil {
		return nil, err
	}
	ts := nowTimestamp()
	if input.UpdatedAt != nil {
		ts = *input.UpdatedAt
	}
	c, err := s.ResolveConn(input.Conn)
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(`
		INSERT INTO person_profile_alias_overrides (
			person_id, aliases_json, updated_at, updated_by, source
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(person_id) DO UPDATE SET
			aliases_json = excluded.aliases_json,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by,
			source = excluded.source
	`, token, jsonDumps(normalized), ts, input.UpdatedBy, input.Source); err != nil {
		return nil, err
	}
	if input.Conn == nil {
		if err := c.Commit(); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"person_id":  token,
		"aliases":    personProfileAnyList(normalized),
		"updated_at": ts,
		"updated_by": input.UpdatedBy,
		"source":     input.Source,
	}, nil
}

// DeletePersonProfileAliasOverride 清除人工别名集合，使画像重新使用自动推导别名，
// 对齐 Python delete_person_profile_alias_override
func (s *Store) DeletePersonProfileAliasOverride(personID string, conn *Conn) (bool, error) {
	token := strings.TrimSpace(personID)
	if token == "" {
		return false, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false, err
	}
	result, err := c.Exec("DELETE FROM person_profile_alias_overrides WHERE person_id = ?", token)
	if err != nil {
		return false, err
	}
	if conn == nil {
		if err := c.Commit(); err != nil {
			return false, err
		}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}
