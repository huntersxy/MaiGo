package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"maibot-go/internal/plugin_runtime"

	"gorm.io/gorm"
)

// pluginDatabaseModels 把 MaiBot SQLModel 类名映射到 Go 侧真实表名
// 只开放明确登记的表，未知模型直接失败，避免插件借通用接口探测任意表
var pluginDatabaseModels = map[string]string{
	"Messages":                    "mai_messages",
	"ModelTaskUsage":              "model_task_usage",
	"ModelUsage":                  "llm_usage",
	"Images":                      "images",
	"ToolRecord":                  "tool_records",
	"MaisakaMonitorEventRecord":   "maisaka_monitor_events",
	"OneTimeMaintenanceTask":      "one_time_maintenance_tasks",
	"StatisticsAggregationCursor": "statistics_aggregation_cursors",
	"StatisticsMessageHourly":     "statistics_message_hourly",
	"StatisticsToolHourly":        "statistics_tool_hourly",
	"StatisticsModelHourly":       "statistics_model_hourly",
	"HighFrequencyTerm":           "high_frequency_terms",
	"OnlineTime":                  "online_time",
	"Expression":                  "expressions",
	"Jargon":                      "jargons",
	"BinaryData":                  "binary_data",
	"PersonInfo":                  "person_info",
	"ChatSession":                 "chat_sessions",
	"BotPlatformAccount":          "bot_platform_accounts",
}

type pluginDatabasePort struct {
	db func() *gorm.DB

	mu      sync.Mutex
	columns map[string]map[string]bool
}

func newPluginDatabasePort(db func() *gorm.DB) *pluginDatabasePort {
	if db == nil {
		db = func() *gorm.DB { return nil }
	}
	return &pluginDatabasePort{db: db, columns: map[string]map[string]bool{}}
}

func (p *pluginDatabasePort) Get(ctx context.Context, _ string, modelName string, query pluginruntime.PluginDatabaseQuery) (any, error) {
	table, db, err := p.resolve(ctx, modelName)
	if err != nil {
		return nil, err
	}
	columns, err := p.tableColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	q, err := pluginDatabaseWhere(db.WithContext(ctx).Table(table), modelName, columns, query.Filters)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query.OrderBy) != "" {
		for _, item := range strings.Split(query.OrderBy, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			descending := strings.HasPrefix(item, "-")
			column := strings.TrimPrefix(item, "-")
			if !columns[column] {
				return nil, fmt.Errorf("%s 不存在字段 %s", modelName, column)
			}
			direction := " ASC"
			if descending {
				direction = " DESC"
			}
			q = q.Order(pluginDatabaseQuote(column) + direction)
		}
	}
	if query.Limit > 0 {
		q = q.Limit(int(query.Limit))
	}
	rows := make([]map[string]any, 0)
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("获取数据库记录失败: %w", err)
	}
	if query.SingleResult {
		if len(rows) == 0 {
			return nil, nil
		}
		return rows[0], nil
	}
	return rows, nil
}

func (p *pluginDatabasePort) Save(ctx context.Context, _ string, modelName string, data map[string]any, keyField, keyValue string) (any, error) {
	table, db, err := p.resolve(ctx, modelName)
	if err != nil {
		return nil, err
	}
	columns, err := p.tableColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("data 不能为空")
	}
	if err := pluginDatabaseValidateData(modelName, columns, data); err != nil {
		return nil, err
	}
	keyField = strings.TrimSpace(keyField)
	keyValue = strings.TrimSpace(keyValue)
	if keyField != "" && keyValue != "" {
		if !columns[keyField] {
			return nil, fmt.Errorf("%s 不存在字段 %s", modelName, keyField)
		}
		existing, err := p.findOne(ctx, db, table, keyField, keyValue)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if pluginDatabaseReturningSupported(db) {
				sql, args := pluginDatabaseUpdateReturningSQL(table, data, keyField, keyValue)
				record, err := p.scanReturning(ctx, db, sql, args)
				if err != nil {
					return nil, err
				}
				return record, nil
			}
			if err := db.WithContext(ctx).Table(table).
				Where(pluginDatabaseQuote(keyField)+" = ?", keyValue).
				Updates(data).Error; err != nil {
				return nil, fmt.Errorf("保存数据库记录出错: %w", err)
			}
			return p.findOne(ctx, db, table, keyField, keyValue)
		}
	}
	if pluginDatabaseReturningSupported(db) {
		sql, args := pluginDatabaseInsertReturningSQL(table, data)
		record, err := p.scanReturning(ctx, db, sql, args)
		if err != nil {
			return nil, err
		}
		return record, nil
	}
	if err := db.WithContext(ctx).Table(table).Create(data).Error; err != nil {
		return nil, fmt.Errorf("保存数据库记录出错: %w", err)
	}
	if keyField != "" && keyValue != "" {
		return p.findOne(ctx, db, table, keyField, keyValue)
	}
	return data, nil
}

func pluginDatabaseReturningSupported(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil {
		return false
	}
	switch strings.ToLower(db.Dialector.Name()) {
	case "postgres", "sqlite":
		return true
	default:
		return false
	}
}

func pluginDatabaseInsertReturningSQL(table string, data map[string]any) (string, []any) {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	columns := make([]string, 0, len(keys))
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		columns = append(columns, pluginDatabaseQuote(key))
		placeholders = append(placeholders, "?")
		args = append(args, data[key])
	}
	return "INSERT INTO " + pluginDatabaseQuote(table) + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ") RETURNING *", args
}

func pluginDatabaseUpdateReturningSQL(table string, data map[string]any, keyField, keyValue string) (string, []any) {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assignments := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)+1)
	for _, key := range keys {
		assignments = append(assignments, pluginDatabaseQuote(key)+" = ?")
		args = append(args, data[key])
	}
	args = append(args, keyValue)
	return "UPDATE " + pluginDatabaseQuote(table) + " SET " + strings.Join(assignments, ", ") + " WHERE " + pluginDatabaseQuote(keyField) + " = ? RETURNING *", args
}

func (p *pluginDatabasePort) scanReturning(ctx context.Context, db *gorm.DB, sql string, args []any) (map[string]any, error) {
	record := map[string]any{}
	if err := db.WithContext(ctx).Raw(sql, args...).Scan(&record).Error; err != nil {
		return nil, fmt.Errorf("保存数据库记录出错: %w", err)
	}
	if len(record) == 0 {
		return nil, fmt.Errorf("保存数据库记录后未返回记录")
	}
	return record, nil
}

func (p *pluginDatabasePort) Update(ctx context.Context, _ string, modelName string, data, filters map[string]any) (any, error) {
	table, db, err := p.resolve(ctx, modelName)
	if err != nil {
		return 0, err
	}
	columns, err := p.tableColumns(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("data 不能为空")
	}
	if err := pluginDatabaseValidateData(modelName, columns, data); err != nil {
		return 0, err
	}
	q, err := pluginDatabaseWhere(db.WithContext(ctx).Table(table), modelName, columns, filters)
	if err != nil {
		return 0, err
	}
	result := q.Updates(data)
	if result.Error != nil {
		return 0, fmt.Errorf("更新数据库记录出错: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (p *pluginDatabasePort) Delete(ctx context.Context, _ string, modelName string, filters map[string]any) (any, error) {
	table, db, err := p.resolve(ctx, modelName)
	if err != nil {
		return 0, err
	}
	columns, err := p.tableColumns(ctx, db, table)
	if err != nil {
		return 0, err
	}
	where, args, err := pluginDatabaseWhereSQL(modelName, columns, filters)
	if err != nil {
		return 0, err
	}
	sql := "DELETE FROM " + pluginDatabaseQuote(table)
	if where != "" {
		sql += " WHERE " + where
	}
	result := db.WithContext(ctx).Exec(sql, args...)
	if result.Error != nil {
		return 0, fmt.Errorf("删除数据库记录出错: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (p *pluginDatabasePort) Count(ctx context.Context, _ string, modelName string, filters map[string]any) (any, error) {
	table, db, err := p.resolve(ctx, modelName)
	if err != nil {
		return 0, err
	}
	columns, err := p.tableColumns(ctx, db, table)
	if err != nil {
		return 0, err
	}
	q, err := pluginDatabaseWhere(db.WithContext(ctx).Table(table), modelName, columns, filters)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("统计数据库记录出错: %w", err)
	}
	return count, nil
}

func (p *pluginDatabasePort) resolve(ctx context.Context, modelName string) (string, *gorm.DB, error) {
	if p == nil || p.db == nil {
		return "", nil, fmt.Errorf("数据库服务未初始化")
	}
	db := p.db()
	if db == nil {
		return "", nil, fmt.Errorf("数据库服务未初始化")
	}
	modelName = strings.TrimSpace(modelName)
	table, ok := pluginDatabaseModels[modelName]
	if !ok {
		return "", nil, fmt.Errorf("未找到数据模型: %s", modelName)
	}
	if !db.WithContext(ctx).Migrator().HasTable(table) {
		return "", nil, fmt.Errorf("数据表不存在: %s", table)
	}
	return table, db, nil
}

func (p *pluginDatabasePort) findOne(ctx context.Context, db *gorm.DB, table, keyField, keyValue string) (map[string]any, error) {
	rows := make([]map[string]any, 0, 1)
	if err := db.WithContext(ctx).Table(table).
		Where(pluginDatabaseQuote(keyField)+" = ?", keyValue).Limit(1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("获取数据库记录失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (p *pluginDatabasePort) tableColumns(ctx context.Context, db *gorm.DB, table string) (map[string]bool, error) {
	p.mu.Lock()
	cached := p.columns[table]
	p.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var names []string
	var err error
	if db.Dialector.Name() == "sqlite" {
		var rows []struct {
			Name string `gorm:"column:name"`
		}
		err = db.WithContext(ctx).Raw("PRAGMA table_info(" + pluginDatabaseQuote(table) + ")").Scan(&rows).Error
		for _, row := range rows {
			names = append(names, row.Name)
		}
	} else {
		var rows []struct {
			ColumnName string `gorm:"column:column_name"`
		}
		err = db.WithContext(ctx).Raw(
			"SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=?",
			table).Scan(&rows).Error
		for _, row := range rows {
			names = append(names, row.ColumnName)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("读取数据表结构失败: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("数据表不存在: %s", table)
	}
	columns := make(map[string]bool, len(names))
	for _, name := range names {
		columns[name] = true
	}
	p.mu.Lock()
	p.columns[table] = columns
	p.mu.Unlock()
	return columns, nil
}

func pluginDatabaseWhere(q *gorm.DB, modelName string, columns map[string]bool, filters map[string]any) (*gorm.DB, error) {
	where, args, err := pluginDatabaseWhereSQL(modelName, columns, filters)
	if err != nil {
		return nil, err
	}
	if where != "" {
		q = q.Where(where, args...)
	}
	return q, nil
}

func pluginDatabaseWhereSQL(modelName string, columns map[string]bool, filters map[string]any) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}
	keys := make([]string, 0, len(filters))
	for key := range filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		if !columns[key] {
			return "", nil, fmt.Errorf("%s 不存在字段 %s", modelName, key)
		}
		if filters[key] == nil {
			parts = append(parts, pluginDatabaseQuote(key)+" IS NULL")
			continue
		}
		parts = append(parts, pluginDatabaseQuote(key)+" = ?")
		args = append(args, filters[key])
	}
	return strings.Join(parts, " AND "), args, nil
}

func pluginDatabaseValidateData(modelName string, columns map[string]bool, data map[string]any) error {
	for key := range data {
		if !columns[key] {
			return fmt.Errorf("%s 不存在字段 %s", modelName, key)
		}
	}
	return nil
}

func pluginDatabaseQuote(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}
