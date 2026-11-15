package scripts

// 迁移 MaiBot chat_history 到 A_Memorix，对齐 src/A_memorix/scripts/migrate_chat_history.py

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
)

// migrateChatHistoryLoggerName 对齐 get_logger("A_Memorix.MigrateChatHistory")
const migrateChatHistoryLoggerName = "A_Memorix.MigrateChatHistory"

// chatHistorySelectSQL 对齐脚本中的 SELECT 语句
const chatHistorySelectSQL = `
	SELECT id, session_id, start_timestamp, end_timestamp, participants, theme, keywords, summary
	FROM chat_history
	ORDER BY id ASC`

// chatHistoryRow chat_history 待迁移行
type chatHistoryRow struct {
	id           int64
	sessionID    sql.NullString
	startTS      any
	endTS        any
	participants sql.NullString
	theme        sql.NullString
	keywords     sql.NullString
	summary      sql.NullString
}

// RunMigrateChatHistory 执行 migrate_chat_history.py 等价流程，返回进程退出码
func RunMigrateChatHistory(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "migrate_chat_history.py", "迁移 MaiBot chat_history 到 A_Memorix")
	parser.AddString([]string{"--db-path"}, DefaultDBPath(), "MaiBot SQLite 路径")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "A_Memorix 数据目录")
	parser.AddInt([]string{"--limit"}, 0, "限制迁移条数，0 表示全部")
	parser.AddBool([]string{"--dry-run"}, "仅预览，不写入")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}
	logger := newScriptLogger(ctx, migrateChatHistoryLoggerName)

	dbPath := ResolveRepoPath(args.String("db-path"), DefaultDBPath())
	if _, err := os.Stat(dbPath); err != nil {
		scriptPrintf(ctx, "数据库不存在: %s\n", dbPath)
		return 1
	}

	rows, err := loadChatHistoryRows(dbPath, args.Int("limit"))
	if err != nil {
		scriptErrorf(ctx, "读取 chat_history 失败: %v\n", err)
		return 1
	}
	scriptPrintf(ctx, "chat_history 待处理: %d\n", len(rows))
	if args.Bool("dry-run") {
		for index, row := range rows {
			if index >= previewLimit {
				break
			}
			scriptPrintf(ctx, "- id=%d session=%s theme=%s\n", row.id, nullText(row.sessionID), nullText(row.theme))
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
		participants, err := parseJSONStringList(row.participants)
		if err != nil {
			scriptErrorf(ctx, "解析 chat_history 行 %d 的 participants 失败: %v\n", row.id, err)
			return 1
		}
		keywords, err := parseJSONStringList(row.keywords)
		if err != nil {
			scriptErrorf(ctx, "解析 chat_history 行 %d 的 keywords 失败: %v\n", row.id, err)
			return 1
		}
		theme := strings.TrimSpace(nullString(row.theme))
		summary := strings.TrimSpace(nullString(row.summary))
		text := strings.TrimSpace(fmt.Sprintf("主题：%s\n概括：%s", theme, summary))
		result, err := kernel.IngestSummary(ctx, memruntime.IngestSummaryParams{
			ExternalID:   fmt.Sprintf("chat_history:%d", row.id),
			ChatID:       nullString(row.sessionID),
			Text:         text,
			Participants: participants,
			TimeStart:    pythonTimestamp(row.startTS),
			TimeEnd:      pythonTimestamp(row.endTS),
			Tags:         keywords,
			Metadata:     map[string]any{"theme": theme, "source_row_id": row.id},
		})
		if err != nil {
			scriptErrorf(ctx, "迁移 chat_history 行 %d 失败: %v\n", row.id, err)
			return 1
		}
		if storedIDsNonEmpty(result) {
			migrated++
		} else {
			skipped++
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

// loadChatHistoryRows 读取 chat_history，limit 大于 0 时附加 LIMIT
func loadChatHistoryRows(dbPath string, limit int) ([]chatHistoryRow, error) {
	db, err := openMaiBotSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := chatHistorySelectSQL
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

	rows := make([]chatHistoryRow, 0)
	for cursor.Next() {
		var row chatHistoryRow
		if err := cursor.Scan(&row.id, &row.sessionID, &row.startTS, &row.endTS, &row.participants, &row.theme, &row.keywords, &row.summary); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// nullString 对齐 str(row[...] or "")：NULL 视为空串
func nullString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// nullText 对齐 f-string 中直接插入 SQLite 值，NULL 打印 None
func nullText(value sql.NullString) string {
	if !value.Valid {
		return "None"
	}
	return value.String
}
