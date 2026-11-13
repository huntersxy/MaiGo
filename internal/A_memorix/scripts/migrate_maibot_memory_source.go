package scripts

// migrate_maibot_memory.py 的源库读取与 schema 兼容层

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// migrateSourceSchema 描述 chat_history 列兼容层
type migrateSourceSchema struct {
	versionLabel   string
	chatIDExpr     string
	startTimeExpr  string
	endTimeExpr    string
	participants   string
	theme          string
	keywords       string
	summary        string
	streamTable    string
	streamIDColumn string
	streamGroupCol string
	streamUserCol  string
}

// migrateSourceRow 是 chat_history 查询结果
type migrateSourceRow struct {
	ID           int64
	ChatID       string
	StartTime    any
	EndTime      any
	Participants any
	Theme        any
	Keywords     any
	Summary      any
}

// migratePreview 对齐 PreviewResult
type migratePreview struct {
	total        int
	distribution [][2]any
	samples      []migrateSourceRow
}

// migrateSourceDB 对齐 SourceDB，只读访问源库
type migrateSourceDB struct {
	db     *sql.DB
	path   string
	schema *migrateSourceSchema
}

// openMigrateSourceDB 打开源库并探测 schema
func openMigrateSourceDB(path string) (*migrateSourceDB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("源数据库不存在: %s", path)
	}
	db, err := openMaiBotSQLite(path)
	if err != nil {
		return nil, err
	}
	source := &migrateSourceDB{db: db, path: path}
	if err := source.detectSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return source, nil
}

// Close 关闭源库
func (s *migrateSourceDB) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// tableColumns 读取 PRAGMA table_info
func (s *migrateSourceDB) tableColumns(table string) map[string]bool {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return map[string]bool{}
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return map[string]bool{}
		}
		columns[name] = true
	}
	return columns
}

// detectSchema 对齐 _detect_schema
func (s *migrateSourceDB) detectSchema() error {
	columns := s.tableColumns("chat_history")
	if len(columns) == 0 {
		return fmt.Errorf("源库缺少 chat_history 表")
	}
	schema := &migrateSourceSchema{}
	streamColumns := s.tableColumns("chat_streams")
	if streamColumns["stream_id"] && streamColumns["group_id"] && streamColumns["user_id"] {
		schema.streamTable, schema.streamIDColumn, schema.streamGroupCol, schema.streamUserCol = "chat_streams", "stream_id", "group_id", "user_id"
	} else {
		sessionColumns := s.tableColumns("chat_sessions")
		if sessionColumns["session_id"] && sessionColumns["group_id"] && sessionColumns["user_id"] {
			schema.streamTable, schema.streamIDColumn, schema.streamGroupCol, schema.streamUserCol = "chat_sessions", "session_id", "group_id", "user_id"
		}
	}
	if columns["chat_id"] && columns["start_time"] && columns["end_time"] {
		schema.versionLabel, schema.chatIDExpr, schema.startTimeExpr, schema.endTimeExpr = "legacy", "chat_id", "start_time", "end_time"
	} else if columns["session_id"] && columns["start_timestamp"] && columns["end_timestamp"] {
		schema.versionLabel, schema.chatIDExpr, schema.startTimeExpr, schema.endTimeExpr = "current", "session_id", "start_timestamp", "end_timestamp"
	} else {
		return fmt.Errorf("无法识别 chat_history schema，需要旧版 chat_id/start_time/end_time 或新版 session_id/start_timestamp/end_timestamp 字段")
	}
	for key, column := range map[string]string{"participants": "participants", "theme": "theme", "keywords": "keywords", "summary": "summary"} {
		if columns[column] {
			switch key {
			case "participants":
				schema.participants = column
			case "theme":
				schema.theme = column
			case "keywords":
				schema.keywords = column
			case "summary":
				schema.summary = column
			}
		} else {
			switch key {
			case "participants":
				schema.participants = "''"
			case "theme":
				schema.theme = "''"
			case "keywords":
				schema.keywords = "''"
			case "summary":
				schema.summary = "''"
			}
		}
	}
	s.schema = schema
	return nil
}

// resolveStreamIDs 对齐 resolve_stream_ids
func (s *migrateSourceDB) resolveStreamIDs(streamIDs, groupIDs, userIDs []string) []string {
	resolved := map[string]bool{}
	for _, value := range streamIDs {
		if text := strings.TrimSpace(value); text != "" {
			resolved[text] = true
		}
	}
	hasGroupOrUser := false
	for _, value := range append(append([]string{}, groupIDs...), userIDs...) {
		if strings.TrimSpace(value) != "" {
			hasGroupOrUser = true
		}
	}
	if !hasGroupOrUser {
		return sortedKeys(resolved)
	}
	if s.schema == nil || s.schema.streamTable == "" {
		return sortedKeys(resolved)
	}
	selectBy := func(field string, values []string) {
		var normalized []string
		for _, value := range values {
			if text := strings.TrimSpace(value); text != "" {
				normalized = append(normalized, text)
			}
		}
		if len(normalized) == 0 {
			return
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ",")
		query := fmt.Sprintf("SELECT DISTINCT %s AS stream_id FROM %s WHERE %s IN (%s)", s.schema.streamIDColumn, s.schema.streamTable, field, placeholders)
		rows, err := s.db.Query(query, stringSliceArgs(normalized)...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var streamID sql.NullString
			if err := rows.Scan(&streamID); err == nil {
				if text := strings.TrimSpace(streamID.String); text != "" {
					resolved[text] = true
				}
			}
		}
	}
	selectBy(s.schema.streamGroupCol, groupIDs)
	selectBy(s.schema.streamUserCol, userIDs)
	return sortedKeys(resolved)
}

// buildWhere 对齐 _build_where
func (s *migrateSourceDB) buildWhere(selection *migrateSelection, startAfter *int64) (string, []any) {
	schema := s.schema
	var conditions []string
	var params []any
	if selection.startID != nil {
		conditions = append(conditions, "id >= ?")
		params = append(params, *selection.startID)
	}
	if selection.endID != nil {
		conditions = append(conditions, "id <= ?")
		params = append(params, *selection.endID)
	}
	if startAfter != nil {
		conditions = append(conditions, "id > ?")
		params = append(params, *startAfter)
	}
	if len(selection.streamIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(selection.streamIDs)), ",")
		conditions = append(conditions, fmt.Sprintf("%s IN (%s)", schema.chatIDExpr, placeholders))
		for _, value := range selection.streamIDs {
			params = append(params, value)
		}
	} else if selection.streamFilterUsed {
		conditions = append(conditions, "1=0")
	}
	if selection.timeFromTS != nil && selection.timeToTS != nil {
		conditions = append(conditions, fmt.Sprintf("(%s >= ? AND %s <= ?)", sqliteTimestampExpr(schema.endTimeExpr), sqliteTimestampExpr(schema.startTimeExpr)))
		params = append(params, *selection.timeFromTS, *selection.timeToTS)
	} else if selection.timeFromTS != nil {
		conditions = append(conditions, fmt.Sprintf("(%s >= ?)", sqliteTimestampExpr(schema.endTimeExpr)))
		params = append(params, *selection.timeFromTS)
	} else if selection.timeToTS != nil {
		conditions = append(conditions, fmt.Sprintf("(%s <= ?)", sqliteTimestampExpr(schema.startTimeExpr)))
		params = append(params, *selection.timeToTS)
	}
	if len(conditions) == 0 {
		return "", params
	}
	return "WHERE " + strings.Join(conditions, " AND "), params
}

// selectClause 对齐 _select_clause
func (s *migrateSourceDB) selectClause(includeContent bool) string {
	schema := s.schema
	fields := []string{"id", fmt.Sprintf("%s AS chat_id", schema.chatIDExpr), fmt.Sprintf("%s AS start_time", schema.startTimeExpr), fmt.Sprintf("%s AS end_time", schema.endTimeExpr)}
	if includeContent {
		fields = append(fields, fmt.Sprintf("%s AS participants", schema.participants), fmt.Sprintf("%s AS theme", schema.theme), fmt.Sprintf("%s AS keywords", schema.keywords), fmt.Sprintf("%s AS summary", schema.summary))
	} else {
		fields = append(fields, fmt.Sprintf("%s AS theme", schema.theme), fmt.Sprintf("%s AS summary", schema.summary))
	}
	return strings.Join(fields, ", ")
}

// preview 对齐 SourceDB.preview
func (s *migrateSourceDB) preview(selection *migrateSelection, limit int) (*migratePreview, error) {
	where, params := s.buildWhere(selection, nil)
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) AS c FROM chat_history "+where, params...).Scan(&total); err != nil {
		return nil, err
	}
	preview := &migratePreview{total: total}
	distQuery := fmt.Sprintf("SELECT %s AS chat_id, COUNT(*) AS c FROM chat_history %s GROUP BY %s ORDER BY c DESC LIMIT 30", s.schema.chatIDExpr, where, s.schema.chatIDExpr)
	rows, err := s.db.Query(distQuery, params...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var chatID sql.NullString
		var count int
		if err := rows.Scan(&chatID, &count); err != nil {
			rows.Close()
			return nil, err
		}
		preview.distribution = append(preview.distribution, [2]any{strings.TrimSpace(chatID.String), count})
	}
	rows.Close()
	sampleQuery := fmt.Sprintf("SELECT %s FROM chat_history %s ORDER BY id ASC LIMIT ?", s.selectClause(false), where)
	sampleParams := append(append([]any{}, params...), scriptMaxInt(1, limit))
	rows, err = s.db.Query(sampleQuery, sampleParams...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanMigrateSourceRow(rows, false)
		if err != nil {
			return nil, err
		}
		preview.samples = append(preview.samples, row)
	}
	return preview, rows.Err()
}

// iterRows 对齐 SourceDB.iter_rows
func (s *migrateSourceDB) iterRows(selection *migrateSelection, batchSize int, startAfter int64) ([][]migrateSourceRow, error) {
	var batches [][]migrateSourceRow
	cursor := startAfter
	for {
		where, params := s.buildWhere(selection, &cursor)
		query := fmt.Sprintf("SELECT %s FROM chat_history %s ORDER BY id ASC LIMIT ?", s.selectClause(true), where)
		bind := append(append([]any{}, params...), scriptMaxInt(1, batchSize))
		rows, err := s.db.Query(query, bind...)
		if err != nil {
			return nil, err
		}
		batch := make([]migrateSourceRow, 0)
		for rows.Next() {
			row, err := scanMigrateSourceRow(rows, true)
			if err != nil {
				rows.Close()
				return nil, err
			}
			batch = append(batch, row)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		batches = append(batches, batch)
		cursor = batch[len(batch)-1].ID
	}
	return batches, nil
}

// sampleRowsForVerify 对齐 sample_rows_for_verify
func (s *migrateSourceDB) sampleRowsForVerify(selection *migrateSelection, size int) ([]migrateSourceRow, error) {
	where, params := s.buildWhere(selection, nil)
	query := fmt.Sprintf("SELECT %s FROM chat_history %s ORDER BY RANDOM() LIMIT ?", s.selectClause(true), where)
	bind := append(append([]any{}, params...), scriptMaxInt(1, size))
	rows, err := s.db.Query(query, bind...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]migrateSourceRow, 0)
	for rows.Next() {
		row, err := scanMigrateSourceRow(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// scanMigrateSourceRow 读取一行 chat_history
func scanMigrateSourceRow(rows *sql.Rows, includeContent bool) (migrateSourceRow, error) {
	var row migrateSourceRow
	if includeContent {
		err := rows.Scan(&row.ID, &row.ChatID, &row.StartTime, &row.EndTime, &row.Participants, &row.Theme, &row.Keywords, &row.Summary)
		return row, err
	}
	err := rows.Scan(&row.ID, &row.ChatID, &row.StartTime, &row.EndTime, &row.Theme, &row.Summary)
	return row, err
}
