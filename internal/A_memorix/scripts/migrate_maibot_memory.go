package scripts

// 迁移 MaiBot chat_history 到 A_Memorix，对齐 src/A_memorix/scripts/migrate_maibot_memory.py
// 差异说明：写入路径复用 metadata/vector/graph 内核 API，不直接执行 Python 版的手写 INSERT

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// migrateMaiBotMemoryLoggerName 对齐 get_logger("A_Memorix.MigrateMaiBotMemory")
const migrateMaiBotMemoryLoggerName = "A_Memorix.MigrateMaiBotMemory"

const (
	migrationStateDirName     = "migration_state"
	migrationStateFileName    = "chat_history_resume.json"
	migrationBadRowsFileName  = "chat_history_bad_rows.jsonl"
	migrationReportFileName   = "chat_history_report.json"
	migrateSelectionTimeFmt   = "2006-01-02 15:04:05"
	migrateSelectionDateFmt   = "2006-01-02"
	migrateSelectionSlashDate = "2006/01/02"
)

// migrateOptions 保存 argparse 解析结果
type migrateOptions struct {
	sourceDB         string
	targetDataDir    string
	noResume         bool
	resetState       bool
	startID          *int
	endID            *int
	readBatchSize    int
	commitWindowRows int
	embedBatchSize   int
	entityEmbedBatch int
	embedWorkers     *int
	maxErrors        int
	logEvery         int
	dryRun           bool
	verifyOnly       bool
	timeFrom         string
	timeTo           string
	streamIDs        []string
	groupIDs         []string
	userIDs          []string
	yes              bool
	previewLimit     int
}

// migrateSelection 对齐 SelectionFilter
type migrateSelection struct {
	timeFromTS       *float64
	timeToTS         *float64
	streamIDs        []string
	streamFilterUsed bool
	startID          *int
	endID            *int
	timeFromRaw      string
	timeToRaw        string
}

// migrateMappedRow 是映射后的待写入行
type migrateMappedRow struct {
	rowID          int64
	chatID         string
	paragraphHash  string
	content        string
	source         string
	timeMeta       map[string]any
	entities       []string
	relations      [][3]string
	existingVector bool
}

// RunMigrateMaiBotMemory 执行 migrate_maibot_memory.py 等价流程
func RunMigrateMaiBotMemory(ctx context.Context, argv []string) int {
	return RunMigrateMaiBotMemoryWithIO(ctx, argv, ScriptStdout(ctx), ScriptStderr(ctx))
}

// RunMigrateMaiBotMemoryWithIO 使用指定输出执行迁移，供 WebUI 进程内调用
func RunMigrateMaiBotMemoryWithIO(ctx context.Context, argv []string, stdout io.Writer, stderr io.Writer) int {
	options, code, handled := parseMigrateMaiBotMemoryArgsWithIO(argv, stdout, stderr)
	if handled {
		return code
	}
	logger := NewScriptLoggerTo(migrateMaiBotMemoryLoggerName, stderr)
	runner := newMigrateRunner(options, logger, stdout)
	return runner.run(ctx)
}

// parseMigrateMaiBotMemoryArgs 对齐 build_parser 与 argparse 退出行为
func parseMigrateMaiBotMemoryArgs(argv []string) (*migrateOptions, int, bool) {
	return parseMigrateMaiBotMemoryArgsWithIO(argv, os.Stdout, os.Stderr)
}

// parseMigrateMaiBotMemoryArgsWithIO 解析参数并写入指定输出
func parseMigrateMaiBotMemoryArgsWithIO(argv []string, stdout io.Writer, stderr io.Writer) (*migrateOptions, int, bool) {
	parser := NewArgParser("migrate_maibot_memory.py", "迁移 MaiBot chat_history 到 A_memorix（高性能 + 可断点续传 + 可确认筛选）")
	parser.Stdout = stdout
	parser.Stderr = stderr
	parser.AddString([]string{"--source-db"}, DefaultDBPath(), "源数据库路径（默认 data/MaiBot.db）")
	parser.AddString([]string{"--target-data-dir"}, DefaultDataDir(), "A_memorix 数据目录（默认 data/a-memorix）")
	parser.AddBool([]string{"--resume"}, "启用断点续传（默认）")
	parser.AddBool([]string{"--no-resume"}, "禁用断点续传")
	parser.AddBool([]string{"--reset-state"}, "清空迁移状态文件后执行")
	parser.AddInt([]string{"--start-id"}, -1, "从指定 chat_history.id 开始迁移（覆盖断点）")
	parser.AddInt([]string{"--end-id"}, -1, "迁移到指定 chat_history.id")
	parser.AddInt([]string{"--read-batch-size"}, 2000, "源库分页读取大小（默认 2000）")
	parser.AddInt([]string{"--commit-window-rows"}, 20000, "每窗口提交行数（默认 20000）")
	parser.AddInt([]string{"--embed-batch-size"}, 256, "段落 embedding 批次大小（默认 256）")
	parser.AddInt([]string{"--entity-embed-batch-size"}, 512, "实体 embedding 批次大小（默认 512）")
	parser.AddInt([]string{"--embed-workers"}, -1, "embedding 并发数（默认读取配置）")
	parser.AddInt([]string{"--max-errors"}, 0, "坏行上限，0 表示不中止（默认 0）")
	parser.AddInt([]string{"--log-every"}, 5000, "日志输出步长（默认 5000）")
	parser.AddBool([]string{"--dry-run"}, "仅预览不写入")
	parser.AddBool([]string{"--verify-only"}, "仅执行严格校验")
	parser.AddString([]string{"--time-from"}, "", "开始时间：YYYY-MM-DD / YYYY/MM/DD / YYYY-MM-DD HH:mm[:ss]")
	parser.AddString([]string{"--time-to"}, "", "结束时间：YYYY-MM-DD / YYYY/MM/DD / YYYY-MM-DD HH:mm[:ss]")
	parser.AddStringList([]string{"--stream-id"}, "聊天流 stream_id（可重复）")
	parser.AddStringList([]string{"--group-id"}, "群号（可重复，自动映射 stream_id）")
	parser.AddStringList([]string{"--user-id"}, "用户号（可重复，自动映射 stream_id）")
	parser.AddBool([]string{"--yes"}, "跳过交互确认")
	parser.AddInt([]string{"--preview-limit"}, 20, "预览样本条数（默认 20）")
	args, code, handled := parser.Parse(argv)
	if handled {
		return nil, code, true
	}
	options := &migrateOptions{
		sourceDB:         args.String("source-db"),
		targetDataDir:    args.String("target-data-dir"),
		noResume:         args.Bool("no-resume"),
		resetState:       args.Bool("reset-state"),
		readBatchSize:    args.Int("read-batch-size"),
		commitWindowRows: args.Int("commit-window-rows"),
		embedBatchSize:   args.Int("embed-batch-size"),
		entityEmbedBatch: args.Int("entity-embed-batch-size"),
		maxErrors:        args.Int("max-errors"),
		logEvery:         args.Int("log-every"),
		dryRun:           args.Bool("dry-run"),
		verifyOnly:       args.Bool("verify-only"),
		timeFrom:         args.String("time-from"),
		timeTo:           args.String("time-to"),
		yes:              args.Bool("yes"),
		previewLimit:     args.Int("preview-limit"),
	}
	if args.Bool("resume") && args.Bool("no-resume") {
		fmt.Fprintln(stderr, "migrate_maibot_memory.py: error: argument --no-resume: not allowed with argument --resume")
		return nil, 2, true
	}
	if options.maxErrors < 0 {
		fmt.Fprintln(stderr, "migrate_maibot_memory.py: error: --max-errors 必须 >= 0")
		return nil, 2, true
	}
	if args.Provided("start-id") {
		value := args.Int("start-id")
		options.startID = &value
	}
	if args.Provided("end-id") {
		value := args.Int("end-id")
		options.endID = &value
	}
	if args.Provided("embed-workers") {
		value := args.Int("embed-workers")
		options.embedWorkers = &value
	}
	options.streamIDs = args.All("stream-id")
	options.groupIDs = args.All("group-id")
	options.userIDs = args.All("user-id")
	return options, 0, false
}

// buildSelection 对齐 _build_selection_filter
func buildSelection(source *migrateSourceDB, options *migrateOptions, logger *ScriptLogger) (*migrateSelection, error) {
	if options.startID != nil && *options.startID <= 0 {
		return nil, fmt.Errorf("--start-id 必须 > 0")
	}
	if options.endID != nil && *options.endID <= 0 {
		return nil, fmt.Errorf("--end-id 必须 > 0")
	}
	if options.startID != nil && options.endID != nil && *options.startID > *options.endID {
		return nil, fmt.Errorf("--start-id 不能大于 --end-id")
	}
	selection := &migrateSelection{startID: options.startID, endID: options.endID, timeFromRaw: options.timeFrom, timeToRaw: options.timeTo}
	if options.timeFrom != "" {
		value, err := parseMigrateCLITime(options.timeFrom, false)
		if err != nil {
			return nil, err
		}
		selection.timeFromTS = &value
	}
	if options.timeTo != "" {
		value, err := parseMigrateCLITime(options.timeTo, true)
		if err != nil {
			return nil, err
		}
		selection.timeToTS = &value
	}
	if selection.timeFromTS != nil && selection.timeToTS != nil && *selection.timeFromTS > *selection.timeToTS {
		return nil, fmt.Errorf("--time-from 不能晚于 --time-to")
	}
	selection.streamFilterUsed = len(options.streamIDs) > 0 || len(options.groupIDs) > 0 || len(options.userIDs) > 0
	selection.streamIDs = source.resolveStreamIDs(options.streamIDs, options.groupIDs, options.userIDs)
	if selection.streamFilterUsed && len(selection.streamIDs) == 0 {
		logger.Warning("已指定 stream/group/user 筛选，但未解析到任何 stream_id，结果将为空。")
	}
	logger.Info(fmt.Sprintf("筛选条件: time_from=%s, time_to=%s, stream_ids=%d, stream_filter_requested=%t",
		emptyDash(options.timeFrom), emptyDash(options.timeTo), len(selection.streamIDs), selection.streamFilterUsed))
	return selection, nil
}

// parseMigrateCLITime 对齐 _parse_cli_datetime
func parseMigrateCLITime(text string, isEnd bool) (float64, error) {
	value := strings.TrimSpace(text)
	if value == "" {
		return 0, fmt.Errorf("时间不能为空")
	}
	layouts := []struct {
		layout   string
		dateOnly bool
	}{
		{"2006-01-02 15:04:05", false},
		{"2006/01/02 15:04:05", false},
		{"2006-01-02 15:04", false},
		{"2006/01/02 15:04", false},
		{"2006-01-02", true},
		{"2006/01/02", true},
	}
	for _, entry := range layouts {
		parsed, err := time.ParseInLocation(entry.layout, value, time.Local)
		if err != nil {
			continue
		}
		if entry.dateOnly && isEnd {
			parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 59, 0, time.Local)
		}
		return float64(parsed.UnixNano()) / 1e9, nil
	}
	return 0, fmt.Errorf("时间格式错误: %s，仅支持 YYYY-MM-DD、YYYY/MM/DD、YYYY-MM-DD HH:mm[:ss]、YYYY/MM/DD HH:mm[:ss]", value)
}

// sqliteTimestampExpr 对齐 _sqlite_timestamp_expr
func sqliteTimestampExpr(column string) string {
	return fmt.Sprintf("CASE WHEN typeof(%s) IN ('integer', 'real') THEN CAST(%s AS REAL) ELSE CAST(strftime('%%s', %s) AS REAL) END", column, column, column)
}

// buildSourceFingerprint 对齐 _build_source_db_fingerprint
func buildSourceFingerprint(path string) (map[string]any, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	hasher := sha1.New()
	buffer := make([]byte, 1<<20)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			hasher.Write(buffer[:n])
		}
		if readErr != nil {
			break
		}
	}
	payload := map[string]any{
		"path":  absolute,
		"size":  info.Size(),
		"mtime": float64(info.ModTime().UnixNano()) / 1e9,
		"sha1":  hex.EncodeToString(hasher.Sum(nil)),
	}
	return payload, pySha1(payload), nil
}

// pySha1 对齐 _json_hash：sort_keys + ensure_ascii=False 后 sha1
func pySha1(payload map[string]any) string {
	text, err := utils.PyJSONMarshal(payload)
	if err != nil {
		return ""
	}
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

// mapSourceRow 对齐 _map_row
func mapSourceRow(row migrateSourceRow, vectorStore *storage.VectorStore, stats map[string]any, logger *ScriptLogger) (*migrateMappedRow, error) {
	participants := parseMigrateListField(row.Participants, "participants", row.ID, stats, logger)
	keywords := parseMigrateListField(row.Keywords, "keywords", row.ID, stats, logger)
	keywordsTop := keywords
	if len(keywordsTop) > 8 {
		keywordsTop = keywordsTop[:8]
	}
	theme := normalizeMigrateName(row.Theme)
	summary := normalizeMigrateName(row.Summary)
	content := strings.TrimSpace(fmt.Sprintf("话题：%s\n概括：%s\n参与者：%s\n关键词：%s", theme, summary, strings.Join(participants, "、"), strings.Join(keywordsTop, "、")))
	paragraphHash, err := utils.ComputeHash(utils.NormalizeText(content))
	if err != nil {
		return nil, err
	}
	startTS := pyFloatOrZero(row.StartTime)
	endTS := pyFloatOr(row.EndTime, startTS)
	entities := dedupKeepOrder(append(append(append([]string{}, participants...), theme), keywordsTop...))
	var relations [][3]string
	if theme != "" {
		for _, participant := range participants {
			relations = append(relations, [3]string{participant, "参与话题", theme})
		}
		for _, keyword := range keywordsTop {
			relations = append(relations, [3]string{theme, "关键词", keyword})
		}
	}
	return &migrateMappedRow{
		rowID:          row.ID,
		chatID:         strings.TrimSpace(row.ChatID),
		paragraphHash:  paragraphHash,
		content:        content,
		source:         "maibot.chat_history:" + strings.TrimSpace(row.ChatID),
		timeMeta:       map[string]any{"event_time_start": startTS, "event_time_end": endTS, "time_granularity": "minute", "time_confidence": 0.95},
		entities:       entities,
		relations:      relations,
		existingVector: vectorStore != nil && vectorStore.Contains(paragraphHash),
	}, nil
}

// parseMigrateListField 对齐 _parse_json_list_field 的 JSON/分隔符降级
func parseMigrateListField(raw any, field string, rowID int64, stats map[string]any, logger *ScriptLogger) []string {
	if raw == nil {
		return nil
	}
	text := strings.TrimSpace(pyString(raw))
	if text == "" {
		return nil
	}
	var parsed any
	if err := UnmarshalJSON([]byte(text), &parsed); err == nil {
		return normalizeMigrateListItems(parsed, field, rowID, stats, logger, false)
	}
	for _, separator := range []string{",", "，", "、", ";", "；", "\n"} {
		if strings.Contains(text, separator) {
			warnCoercedField(stats, logger, rowID, field, fmt.Sprintf("按分隔符 %q 拆分", separator))
			parts := strings.Split(strings.ReplaceAll(text, "\r", "\n"), separator)
			return dedupKeepOrder(parts)
		}
	}
	warnCoercedField(stats, logger, rowID, field, "按单个文本项处理")
	return dedupKeepOrder([]string{text})
}

// normalizeMigrateListItems 对齐 _normalize_list_field_items
func normalizeMigrateListItems(data any, field string, rowID int64, stats map[string]any, logger *ScriptLogger, coerced bool) []string {
	var candidates []any
	switch typed := data.(type) {
	case []any:
		candidates = typed
	case map[string]any:
		for _, key := range []string{"items", "values", "names", "keywords", "participants"} {
			if list, ok := typed[key].([]any); ok {
				candidates = list
				break
			}
		}
		if candidates == nil {
			for _, value := range typed {
				candidates = append(candidates, value)
			}
		}
	default:
		candidates = []any{data}
	}
	items := make([]string, 0, len(candidates))
	for _, item := range candidates {
		text := ""
		if mapping, ok := item.(map[string]any); ok {
			for _, key := range []string{"name", "label", "value", "text", "keyword"} {
				if value, ok := mapping[key]; ok && strings.TrimSpace(pyString(value)) != "" {
					text = strings.TrimSpace(pyString(value))
					break
				}
			}
		} else {
			text = strings.TrimSpace(pyString(item))
		}
		if text != "" {
			items = append(items, text)
		}
	}
	if coerced {
		warnCoercedField(stats, logger, rowID, field, fmt.Sprintf("字段类型为 %T", data))
	}
	return dedupKeepOrder(items)
}

// warnCoercedField 对齐 _warn_list_field_coerced
func warnCoercedField(stats map[string]any, logger *ScriptLogger, rowID int64, field string, detail string) {
	stats["coerced_list_fields"] = intOrZero(stats["coerced_list_fields"]) + 1
	if intOrZero(stats["coerced_list_fields"]) <= 20 {
		logger.Warning(fmt.Sprintf("chat_history.id=%d 的 %s 已降级解析: %s", rowID, field, detail))
	}
}

// normalizeMigrateName 对齐 _normalize_name
func normalizeMigrateName(value any) string {
	return strings.TrimSpace(pyString(value))
}

// dedupKeepOrder 对齐 _dedup_keep_order，按小写去重
func dedupKeepOrder(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

// sortedKeys 返回排序后的 map 键
func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// stringSliceArgs 把字符串切片转成 []any
func stringSliceArgs(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

// emptyDash 对齐预览里空值显示 -
func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// pyFloatOrZero 对齐 _safe_float(value, 0.0)
func pyFloatOrZero(value any) float64 { return pyFloatOr(value, 0) }

// pyFloatOr 对齐 _safe_float 的默认值语义
func pyFloatOr(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case nil:
		return fallback
	case int64:
		return float64(typed)
	case float64:
		return typed
	case []byte:
		return pyFloatOr(string(typed), fallback)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return fallback
		}
		return parsed
	default:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(pyString(typed)), 64)
		if err != nil {
			return fallback
		}
		return parsed
	}
}
