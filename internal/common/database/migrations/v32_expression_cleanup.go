package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	memutils "MaiGo/internal/A_memorix/core/utils"
)

// promptExampleExpressionStyles 对齐 Python 学习 prompt 中泄漏的示例表达
var promptExampleExpressionStyles = map[string]struct{}{
	"我嘞个xxxx": {},
	"对对对":     {},
	"这么强！":    {},
}

// normalizeExpressionStyleForLearning 对齐 Python 的“使用”前缀清理
func normalizeExpressionStyleForLearning(style string) string {
	normalized := strings.TrimSpace(style)
	if strings.HasPrefix(normalized, "使用") {
		return strings.TrimSpace(strings.TrimPrefix(normalized, "使用"))
	}
	return normalized
}

func isPromptExampleExpressionStyle(style string) bool {
	_, ok := promptExampleExpressionStyles[normalizeExpressionStyleForLearning(style)]
	return ok
}

type expressionCleanupRow struct {
	id              int64
	sessionID       sql.NullString
	situation       string
	style           string
	normalizedStyle string
	contentList     string
	count           int
	checked         bool
	modifiedBy      sql.NullString
	createTime      any
	lastActiveTime  any
}

type expressionCleanupUpdate struct {
	id             int64
	situation      string
	style          string
	contentList    string
	count          int
	checked        bool
	modifiedBy     sql.NullString
	createTime     any
	lastActiveTime any
}

// cleanupExpressionStylePollution 对齐 v31_to_v32.py 的清理与合并计划
func cleanupExpressionStylePollution(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "expressions")
	if err != nil || !exists {
		return err
	}
	rows, err := q.Query(ctx,
		"SELECT id, session_id, situation, style, COALESCE(content_list,'[]'), count, checked, modified_by, create_time, last_active_time "+
			"FROM expressions ORDER BY id")
	if err != nil {
		return err
	}
	deleteIDs := make(map[int64]struct{})
	normalizedRows := make([]expressionCleanupRow, 0)
	for rows.Next() {
		var (
			row       expressionCleanupRow
			situation sql.NullString
			style     sql.NullString
			count     sql.NullInt64
			checked   sql.NullBool
		)
		if err := rows.Scan(&row.id, &row.sessionID, &situation, &style, &row.contentList,
			&count, &checked, &row.modifiedBy, &row.createTime, &row.lastActiveTime); err != nil {
			rows.Close()
			return err
		}
		row.situation = strings.TrimSpace(situation.String)
		row.style = strings.TrimSpace(style.String)
		row.normalizedStyle = normalizeExpressionStyleForLearning(row.style)
		row.count = int(count.Int64)
		row.checked = checked.Bool
		if isPromptExampleExpressionStyle(row.style) {
			deleteIDs[row.id] = struct{}{}
			continue
		}
		if row.situation == "" || row.normalizedStyle == "" {
			deleteIDs[row.id] = struct{}{}
			continue
		}
		if row.contentList == "" {
			row.contentList = "[]"
		}
		normalizedRows = append(normalizedRows, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	groups := make(map[string][]expressionCleanupRow)
	groupOrder := make([]string, 0)
	for _, row := range normalizedRows {
		key := expressionIdentityKey(row)
		if _, ok := groups[key]; !ok {
			groupOrder = append(groupOrder, key)
		}
		groups[key] = append(groups[key], row)
	}

	updates := make([]expressionCleanupUpdate, 0)
	for _, key := range groupOrder {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool {
			left, right := expressionTimeKey(group[i].createTime), expressionTimeKey(group[j].createTime)
			if left != right {
				return left < right
			}
			return group[i].id < group[j].id
		})
		keep := group[0]
		if len(group) > 1 {
			for _, row := range group[1:] {
				deleteIDs[row.id] = struct{}{}
			}
		}
		mergedContent, err := mergeExpressionContent(group)
		if err != nil {
			return err
		}
		serialized, err := memutils.PyJSONMarshal(mergedContent)
		if err != nil {
			return err
		}
		if keep.style == keep.normalizedStyle && len(group) == 1 && keep.contentList == serialized {
			continue
		}
		update := expressionCleanupUpdate{
			id:             keep.id,
			situation:      keep.situation,
			style:          keep.normalizedStyle,
			contentList:    serialized,
			count:          0,
			checked:        false,
			modifiedBy:     sql.NullString{},
			createTime:     nil,
			lastActiveTime: nil,
		}
		for _, row := range group {
			update.count += row.count
			update.checked = update.checked || row.checked
			if chooseExpressionModifiedBy(update.modifiedBy, row.modifiedBy) {
				update.modifiedBy = row.modifiedBy
			}
		}
		update.createTime, update.lastActiveTime = chooseExpressionTimes(group)
		updates = append(updates, update)
	}
	for id := range deleteIDs {
		for index := range updates {
			if updates[index].id == id {
				updates = append(updates[:index], updates[index+1:]...)
				break
			}
		}
	}
	for _, update := range updates {
		if err := q.Exec(ctx,
			"UPDATE expressions SET situation=?, style=?, content_list=?, count=?, checked=?, modified_by=?, create_time=?, last_active_time=? WHERE id=?",
			update.situation, update.style, update.contentList, update.count, update.checked,
			nullableString(update.modifiedBy), update.createTime, update.lastActiveTime, update.id); err != nil {
			return err
		}
	}
	return deleteExpressionRows(ctx, q, deleteIDs)
}

func expressionIdentityKey(row expressionCleanupRow) string {
	session := "\x00null"
	if row.sessionID.Valid {
		session = "\x00s" + row.sessionID.String
	}
	return session + "\x00" + row.situation + "\x00" + row.normalizedStyle
}

func mergeExpressionContent(group []expressionCleanupRow) ([]string, error) {
	merged := make([]string, 0)
	for _, row := range group {
		contents, err := loadExpressionContentList(row.contentList, row.id)
		if err != nil {
			return nil, err
		}
		for _, content := range contents {
			if !slices.Contains(merged, content) {
				merged = append(merged, content)
			}
		}
	}
	situation := strings.TrimSpace(group[0].situation)
	if situation != "" && !slices.Contains(merged, situation) {
		merged = append(merged, situation)
	}
	return merged, nil
}

func loadExpressionContentList(raw string, rowID int64) ([]string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("表达方式 #%d 的 content_list 包含多余内容", rowID)
		}
		return nil, err
	}
	items, ok := parsed.([]any)
	if !ok {
		return nil, fmt.Errorf("表达方式 #%d 的 content_list 不是 JSON 数组", rowID)
	}
	contents := make([]string, 0, len(items))
	for _, item := range items {
		content := strings.TrimSpace(pyString(item))
		if content != "" && !slices.Contains(contents, content) {
			contents = append(contents, content)
		}
	}
	return contents, nil
}

func chooseExpressionModifiedBy(current, candidate sql.NullString) bool {
	if !candidate.Valid || candidate.String == "" {
		return false
	}
	if candidate.String == "USER" {
		return current.String != "USER" || !current.Valid
	}
	if candidate.String == "AI" {
		return !current.Valid || current.String == ""
	}
	return false
}

func chooseExpressionTimes(group []expressionCleanupRow) (any, any) {
	var createTime, lastActive any
	createKey, lastKey := "", ""
	for _, row := range group {
		if key := expressionTimeKey(row.createTime); key != "" && (createTime == nil || key < createKey) {
			createTime, createKey = row.createTime, key
		}
		if key := expressionTimeKey(row.lastActiveTime); key != "" && (lastActive == nil || key > lastKey) {
			lastActive, lastKey = row.lastActiveTime, key
		}
	}
	return createTime, lastActive
}

func expressionTimeKey(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case interface{ Format(string) string }:
		return typed.Format("2006-01-02 15:04:05.999999999")
	default:
		return ""
	}
}

func deleteExpressionRows(ctx context.Context, q querier, ids map[int64]struct{}) error {
	if len(ids) == 0 {
		return nil
	}
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for start := 0; start < len(ordered); start += 500 {
		end := start + 500
		if end > len(ordered) {
			end = len(ordered)
		}
		batch := ordered[start:end]
		placeholders := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch))
		for _, id := range batch {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if err := q.Exec(ctx, "DELETE FROM expressions WHERE id IN ("+strings.Join(placeholders, ",")+")", args...); err != nil {
			return err
		}
	}
	return nil
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func pyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case json.Number:
		return string(typed)
	case float64:
		text := strconv.FormatFloat(typed, 'f', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return text
	default:
		return fmt.Sprint(typed)
	}
}
