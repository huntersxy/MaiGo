package scripts

// 迁移 MaiBot person_info.memory_points 到 A_Memorix，对齐 src/A_memorix/scripts/migrate_person_memory_points.py

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
)

// migratePersonMemoryLoggerName 对齐 get_logger("A_Memorix.MigratePersonMemory")
const migratePersonMemoryLoggerName = "A_Memorix.MigratePersonMemory"

// personMemorySelectSQL 对齐脚本中的 SELECT 语句
const personMemorySelectSQL = `
	SELECT person_id, person_name, user_nickname, memory_points
	FROM person_info
	WHERE memory_points IS NOT NULL AND memory_points != ''
	ORDER BY id ASC`

// personInfoRow person_info 待迁移行
type personInfoRow struct {
	personID     sql.NullString
	personName   sql.NullString
	userNickname sql.NullString
	memoryPoints sql.NullString
}

// personMemoryItem 单个记忆点，对齐 _parse_memory_points 的输出
type personMemoryItem struct {
	index    int
	category string
	content  string
	weight   string
}

// RunMigratePersonMemoryPoints 执行 migrate_person_memory_points.py 等价流程，返回进程退出码
func RunMigratePersonMemoryPoints(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "migrate_person_memory_points.py", "迁移 MaiBot person_info.memory_points 到 A_Memorix")
	parser.AddString([]string{"--db-path"}, DefaultDBPath(), "MaiBot SQLite 路径")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "A_Memorix 数据目录")
	parser.AddInt([]string{"--limit"}, 0, "限制迁移人数，0 表示全部")
	parser.AddBool([]string{"--dry-run"}, "仅预览，不写入")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}
	logger := newScriptLogger(ctx, migratePersonMemoryLoggerName)

	dbPath := ResolveRepoPath(args.String("db-path"), DefaultDBPath())
	if _, err := os.Stat(dbPath); err != nil {
		scriptPrintf(ctx, "数据库不存在: %s\n", dbPath)
		return 1
	}

	rows, err := loadPersonInfoRows(dbPath, args.Int("limit"))
	if err != nil {
		scriptErrorf(ctx, "读取 person_info 失败: %v\n", err)
		return 1
	}
	previewTotal := 0
	for _, row := range rows {
		previewTotal += len(parseMemoryPoints(row.memoryPoints))
	}
	scriptPrintf(ctx, "person_info 待迁移人物: %d 记忆点: %d\n", len(rows), previewTotal)
	if args.Bool("dry-run") {
		for index, row := range rows {
			if index >= previewLimit {
				break
			}
			scriptPrintf(ctx, "- person_id=%s person_name=%s\n", nullString(row.personID), previewPersonName(row.personName, row.userNickname))
		}
		return 0
	}

	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	runtime, err := newScriptKernel(dataDir, logger)
	if err != nil {
		scriptErrorf(ctx, "初始化 A_Memorix 内核失败: %v\n", err)
		return 1
	}
	defer shutdownScriptKernel(ctx, runtime, logger)
	if err := runtime.Initialize(ctx); err != nil {
		scriptErrorf(ctx, "初始化 A_Memorix 内核失败: %v\n", err)
		return 1
	}
	kernel := runtime.Kernel()

	migrated := 0
	skipped := 0
	for _, row := range rows {
		personID := strings.TrimSpace(nullString(row.personID))
		if personID == "" {
			continue
		}
		displayName := strings.TrimSpace(firstNonEmpty(row.personName, row.userNickname))
		entities := []string{personID}
		if displayName != "" {
			entities = append(entities, displayName)
		}
		for _, item := range parseMemoryPoints(row.memoryPoints) {
			result, err := kernel.IngestText(ctx, memruntime.IngestTextParams{
				ExternalID: fmt.Sprintf("person_memory:%s:%d", personID, item.index),
				SourceType: "person_fact",
				Text:       fmt.Sprintf("[%s] %s", item.category, item.content),
				PersonIDs:  []string{personID},
				Tags:       []string{item.category},
				Entities:   entities,
				Metadata: map[string]any{
					"category":     item.category,
					"weight":       item.weight,
					"display_name": displayName,
				},
			})
			if err != nil {
				scriptErrorf(ctx, "迁移 person_id=%s 的索引 %d 失败: %v\n", personID, item.index, err)
				return 1
			}
			if storedIDsNonEmpty(result) {
				migrated++
			} else {
				skipped++
			}
		}
	}

	scriptPrintf(ctx, "迁移完成: migrated=%d skipped=%d\n", migrated, skipped)
	stats, err := kernel.MemoryStats()
	if err != nil {
		scriptErrorf(ctx, "读取 memory_stats 失败: %v\n", err)
		return 1
	}
	if err := printJSONLine(ctx, stats); err != nil {
		scriptErrorf(ctx, "输出 memory_stats 失败: %v\n", err)
		return 1
	}
	return 0
}

// loadPersonInfoRows 读取 person_info，limit 大于 0 时附加 LIMIT
func loadPersonInfoRows(dbPath string, limit int) ([]personInfoRow, error) {
	db, err := openMaiBotSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := personMemorySelectSQL
	var queryArgs []any
	if limit > 0 {
		query += " LIMIT ?"
		queryArgs = append(queryArgs, limit)
	}
	cursor, err := db.Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	rows := make([]personInfoRow, 0)
	for cursor.Next() {
		var row personInfoRow
		if err := cursor.Scan(&row.personID, &row.personName, &row.userNickname, &row.memoryPoints); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// parseMemoryPoints 对齐 _parse_memory_points：解析失败按空列表处理
func parseMemoryPoints(raw sql.NullString) []personMemoryItem {
	items := make([]personMemoryItem, 0)
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return items
	}
	var values []any
	if err := UnmarshalJSON([]byte(raw.String), &values); err != nil {
		return items
	}
	for index, value := range values {
		text := strings.TrimSpace(pyTextOrEmpty(value))
		if text == "" {
			continue
		}
		parts := strings.Split(text, ":")
		var category, content, weight string
		if len(parts) >= 3 {
			category = strings.TrimSpace(parts[0])
			content = strings.TrimSpace(strings.Join(parts[1:len(parts)-1], ":"))
			weight = strings.TrimSpace(parts[len(parts)-1])
		} else {
			category = "其他"
			content = text
			weight = "1.0"
		}
		if content == "" {
			continue
		}
		if category == "" {
			category = "其他"
		}
		if weight == "" {
			weight = "1.0"
		}
		items = append(items, personMemoryItem{index: index, category: category, content: content, weight: weight})
	}
	return items
}

// pyTextOrEmpty 对齐 str(item or "")
func pyTextOrEmpty(value any) string {
	if !isTruthy(value) {
		return ""
	}
	return pyString(value)
}

// firstNonEmpty 对齐 person_name or user_nickname or ""
func firstNonEmpty(primary sql.NullString, secondary sql.NullString) string {
	if primary.Valid && primary.String != "" {
		return primary.String
	}
	if secondary.Valid {
		return secondary.String
	}
	return ""
}

// previewPersonName 对齐 dry-run 中 person_name or user_nickname 的打印值
func previewPersonName(primary sql.NullString, secondary sql.NullString) string {
	if primary.Valid && primary.String != "" {
		return primary.String
	}
	if secondary.Valid {
		return secondary.String
	}
	return "None"
}
