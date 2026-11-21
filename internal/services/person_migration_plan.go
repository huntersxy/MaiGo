package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

type PersonMigrationConflict struct {
	ID      string   `json:"id"`
	Field   string   `json:"field"`
	Source  any      `json:"source"`
	Target  any      `json:"target"`
	Choices []string `json:"choices"`
}

type PersonMigrationChange struct {
	Table  string         `json:"table"`
	Keys   []string       `json:"keys"`
	Before map[string]any `json:"before"`
	After  map[string]any `json:"after"`
}

type PersonMigrationDatabase struct {
	Name        string                  `json:"name"`
	Path        string                  `json:"path"`
	Fingerprint *string                 `json:"fingerprint"`
	Changes     []PersonMigrationChange `json:"changes"`
	Counts      map[string]int          `json:"counts"`
}

type PersonDatabasePlan struct {
	PersonMigrationDatabase
	conn      *sql.Conn
	ctx       context.Context
	schema    string
	tables    map[string]bool
	decisions map[string]string
	conflicts []PersonMigrationConflict
}

func newPersonDatabasePlan(ctx context.Context, conn *sql.Conn, schema, name, path string, decisions map[string]string) (*PersonDatabasePlan, error) {
	plan := &PersonDatabasePlan{
		PersonMigrationDatabase: PersonMigrationDatabase{Name: name, Path: path, Changes: []PersonMigrationChange{}, Counts: map[string]int{}},
		conn:                    conn, ctx: ctx, schema: schema, decisions: decisions, tables: map[string]bool{},
	}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM `+personQuote(schema)+`.sqlite_schema WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		plan.tables[name] = true
	}
	return plan, rows.Err()
}

func personQuote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (plan *PersonDatabasePlan) HasTable(name string) bool {
	return plan.tables[name]
}

func (plan *PersonDatabasePlan) RowCount(table string, where string, args ...any) (int, error) {
	var count int
	err := plan.conn.QueryRowContext(plan.ctx, `SELECT count(*) FROM `+plan.qualified(table)+` WHERE `+where, args...).Scan(&count)
	return count, err
}

func (plan *PersonDatabasePlan) qualified(table string) string {
	return personQuote(plan.schema) + "." + personQuote(table)
}

func (plan *PersonDatabasePlan) QualifiedTable(table string) string {
	return plan.qualified(table)
}

func (plan *PersonDatabasePlan) Rows(table, where string, args ...any) ([]map[string]any, error) {
	if !plan.HasTable(table) {
		return nil, nil
	}
	if where == "" {
		where = "1"
	}
	rows, err := plan.conn.QueryContext(plan.ctx, `SELECT * FROM `+plan.qualified(table)+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return personMigrationRows(rows)
}

func personMigrationRows(rows *sql.Rows) ([]map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	for rows.Next() {
		values, destinations := make([]any, len(columns)), make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			row[column] = values[index]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (plan *PersonDatabasePlan) pragma(name, object string) ([]map[string]any, error) {
	rows, err := plan.conn.QueryContext(plan.ctx, `PRAGMA `+personQuote(plan.schema)+`.`+name+`(`+personQuote(object)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return personMigrationRows(rows)
}

func (plan *PersonDatabasePlan) Choose(key, field string, source, target any) any {
	if reflect.DeepEqual(source, target) || source == nil || source == "" {
		return target
	}
	if target == nil || target == "" {
		return source
	}
	id := plan.Name + ":" + key + ":" + field
	plan.conflicts = append(plan.conflicts, PersonMigrationConflict{ID: id, Field: field, Source: source, Target: target, Choices: []string{"source", "target"}})
	if plan.decisions[id] == "source" {
		return source
	}
	return target
}

func (plan *PersonDatabasePlan) Combine(key string, source, target map[string]any) map[string]any {
	result := maps.Clone(target)
	for field, value := range source {
		current := target[field]
		switch field {
		case "count", "know_counts", "message_count", "occurrence_count", "request_count", "prompt_tokens",
			"completion_tokens", "total_tokens", "cache_hit_tokens", "cache_miss_tokens", "cost", "duration_count",
			"duration_sum", "duration_sum_sq", "response_time_count", "response_time_sum":
			result[field] = personMigrationAdd(value, current)
		case "created_at", "first_seen", "first_observed_at", "first_known_time", "created_timestamp":
			result[field] = personMigrationBoundary(value, current, false)
		case "updated_at", "last_seen", "last_seen_at", "last_confirmed_at", "last_known_time", "last_active_timestamp":
			result[field] = personMigrationBoundary(value, current, true)
		case "is_known":
			result[field] = value == true || current == true || personMigrationNumber(value) != 0 || personMigrationNumber(current) != 0
		default:
			result[field] = plan.Choose(key, field, value, current)
		}
	}
	return result
}

func personMigrationNumber(value any) float64 {
	switch typed := value.(type) {
	case int64:
		return float64(typed)
	case float64:
		return typed
	case json.Number:
		result, _ := typed.Float64()
		return result
	case bool:
		if typed {
			return 1
		}
	}
	return 0
}

func personMigrationAdd(left, right any) any {
	leftInt, leftOK := left.(int64)
	rightInt, rightOK := right.(int64)
	if (leftOK || left == nil) && (rightOK || right == nil) {
		return leftInt + rightInt
	}
	return personMigrationNumber(left) + personMigrationNumber(right)
}

func personMigrationBoundary(left, right any, latest bool) any {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	var earlier bool
	switch typed := left.(type) {
	case time.Time:
		rightTime, ok := right.(time.Time)
		earlier = ok && typed.Before(rightTime)
	case string:
		earlier = typed < fmt.Sprint(right)
	default:
		earlier = personMigrationNumber(left) < personMigrationNumber(right)
	}
	if earlier != latest {
		return left
	}
	return right
}

func personMigrationKey(fields []string, row map[string]any) string {
	values := make([]any, len(fields))
	for index, key := range fields {
		values[index] = row[key]
	}
	payload, _ := json.Marshal(values)
	return string(payload)
}

func (plan *PersonDatabasePlan) tableKeys(table string) ([]string, [][]string, error) {
	columns, err := plan.pragma("table_info", table)
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(columns, func(left, right map[string]any) int {
		return int(personMigrationNumber(left["pk"]) - personMigrationNumber(right["pk"]))
	})
	var keys []string
	for _, column := range columns {
		if personMigrationNumber(column["pk"]) != 0 {
			keys = append(keys, column["name"].(string))
		}
	}
	if len(keys) == 0 {
		return nil, nil, fmt.Errorf("%s.%s 缺少主键，不能安全迁移", plan.Name, table)
	}
	indexes, err := plan.pragma("index_list", table)
	if err != nil {
		return nil, nil, err
	}
	var unique [][]string
	for _, index := range indexes {
		if personMigrationNumber(index["unique"]) == 0 || personMigrationNumber(index["partial"]) != 0 {
			continue
		}
		columns, err := plan.pragma("index_info", index["name"].(string))
		if err != nil {
			return nil, nil, err
		}
		fields := make([]string, 0, len(columns))
		for _, column := range columns {
			if name, ok := column["name"].(string); ok {
				fields = append(fields, name)
			}
		}
		if len(fields) > 0 && len(fields) == len(columns) && !slices.Equal(keys, fields) {
			unique = append(unique, fields)
		}
	}
	return keys, unique, nil
}

func (plan *PersonDatabasePlan) Transform(table, where string, args []any, transform func(map[string]any) (map[string]any, error), merge func(string, map[string]any, map[string]any) (map[string]any, error), keepSource bool) error {
	if !plan.HasTable(table) {
		return nil
	}
	keys, unique, err := plan.tableKeys(table)
	if err != nil {
		return err
	}
	rows, err := plan.Rows(table, where, args...)
	if err != nil {
		return err
	}
	current := map[string]map[string]any{}
	original := map[string]map[string]any{}
	type move struct {
		oldKey string
		row    map[string]any
	}
	var moves []move
	for _, row := range rows {
		key := personMigrationKey(keys, row)
		original[key] = row
		after, err := transform(maps.Clone(row))
		if err != nil {
			return err
		}
		switch {
		case after == nil:
		case personMigrationKey(keys, after) == key && !slices.ContainsFunc(unique, func(fields []string) bool {
			return personMigrationKey(fields, after) != personMigrationKey(fields, row)
		}):
			current[key] = after
		default:
			moves = append(moves, move{oldKey: key, row: after})
			if keepSource {
				current[key] = row
			}
		}
	}
	lookups := make([]map[string]string, len(unique))
	indexRow := func(key string, row map[string]any, add bool) {
		for index, fields := range unique {
			if slices.ContainsFunc(fields, func(field string) bool { return row[field] == nil }) {
				continue
			}
			value := personMigrationKey(fields, row)
			if add {
				lookups[index][value] = key
			} else {
				delete(lookups[index], value)
			}
		}
	}
	for index := range lookups {
		lookups[index] = map[string]string{}
	}
	for key, row := range current {
		indexRow(key, row, true)
	}
	slices.SortFunc(moves, func(left, right move) int { return strings.Compare(left.oldKey, right.oldKey) })
	for _, moved := range moves {
		row := moved.row
		key := personMigrationKey(keys, row)
		matchedKey := ""
		if current[key] != nil {
			matchedKey = key
		}
		for index, fields := range unique {
			if match, exists := lookups[index][personMigrationKey(fields, row)]; exists {
				if matchedKey != "" && matchedKey != match {
					return fmt.Errorf("%s.%s：多个唯一键指向不同目标", plan.Name, table)
				}
				matchedKey = match
			}
		}
		if matchedKey != "" {
			key = matchedKey
			target := current[key]
			for _, field := range keys {
				row[field] = target[field]
			}
			label := table + ":" + key
			if merge == nil {
				row = plan.Combine(label, row, target)
			} else {
				row, err = merge(label, row, target)
				if err != nil {
					return err
				}
			}
			indexRow(key, target, false)
		}
		current[key] = row
		indexRow(key, row, true)
	}
	changed := make(map[string]bool, len(original)+len(current))
	for key, after := range current {
		if !reflect.DeepEqual(after, original[key]) {
			changed[key] = true
		}
	}
	for key := range original {
		if current[key] == nil {
			changed[key] = true
		}
	}
	for _, key := range slices.Sorted(maps.Keys(changed)) {
		plan.Changes = append(plan.Changes, PersonMigrationChange{Table: table, Keys: keys, Before: original[key], After: current[key]})
		plan.Counts[table]++
	}
	return nil
}

func (plan *PersonDatabasePlan) apply() error {
	for _, change := range plan.Changes {
		if change.Before == nil {
			continue
		}
		conditions := make([]string, 0, len(change.Keys))
		values := make([]any, 0, len(change.Keys))
		for _, field := range change.Keys {
			conditions = append(conditions, personQuote(field)+" IS ?")
			values = append(values, change.Before[field])
		}
		if _, err := plan.conn.ExecContext(plan.ctx, `DELETE FROM `+plan.qualified(change.Table)+` WHERE `+strings.Join(conditions, " AND "), values...); err != nil {
			return err
		}
	}
	for _, change := range plan.Changes {
		if change.After == nil {
			continue
		}
		fields := slices.Sorted(maps.Keys(change.After))
		values := make([]any, len(fields))
		markers := make([]string, len(fields))
		for index, field := range fields {
			values[index] = change.After[field]
			fields[index] = personQuote(field)
			markers[index] = "?"
		}
		if _, err := plan.conn.ExecContext(plan.ctx, `INSERT INTO `+plan.qualified(change.Table)+` (`+strings.Join(fields, ",")+`) VALUES (`+strings.Join(markers, ",")+`)`, values...); err != nil {
			return err
		}
	}
	rows, err := plan.conn.QueryContext(plan.ctx, `PRAGMA `+personQuote(plan.schema)+`.foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New(plan.Name + " 迁移后外键校验失败")
	}
	return rows.Err()
}
