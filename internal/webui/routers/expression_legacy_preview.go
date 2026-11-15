package webuiapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	_ "github.com/glebarez/go-sqlite"
	"github.com/google/uuid"

	"MaiGo/internal/learners"
)

const legacyExpressionUploadDir = "data/webui_legacy_expression_imports"

type legacyHTTPError struct {
	Status int
	Detail string
}

func (e *legacyHTTPError) Error() string { return e.Detail }

func legacyFail(status int, detail string) *legacyHTTPError {
	return &legacyHTTPError{Status: status, Detail: detail}
}

func (d Deps) legacyUploadDir() string {
	if strings.TrimSpace(d.InstanceDir) == "" {
		return ""
	}
	return filepath.Join(d.InstanceDir, filepath.FromSlash(legacyExpressionUploadDir))
}

func resolveLegacyDatabasePath(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(raw, "~"), string(os.PathSeparator)))
		}
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return filepath.Clean(raw)
	}
	return filepath.Clean(abs)
}

func legacySQLString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "1"
		}
		return "0"
	}
	return fmt.Sprint(value)
}

func legacySQLBool(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	}
	switch strings.ToLower(strings.TrimSpace(legacySQLString(value))) {
	case "1", "true", "t", "yes", "y":
		return true
	case "0", "false", "f", "no", "n", "", "none", "null":
		return false
	}
	return fallback
}

func legacySQLInt(value any, fallback int) int {
	if value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(legacySQLString(value)))
	if err != nil {
		return fallback
	}
	return parsed
}

func legacySQLTime(value any) time.Time {
	switch typed := value.(type) {
	case nil:
		return time.Now()
	case time.Time:
		return typed
	case int64:
		return time.Unix(typed, 0)
	case float64:
		return time.Unix(int64(typed), 0)
	}
	text := strings.TrimSpace(legacySQLString(value))
	if text == "" {
		return time.Now()
	}
	if seconds, err := strconv.ParseFloat(text, 64); err == nil {
		return time.Unix(int64(seconds), 0)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}
	return time.Now()
}

func legacyContentList(value any) string {
	normalize := func(items []any) string {
		normalized := make([]string, 0, len(items))
		for _, item := range items {
			if text := strings.TrimSpace(legacySQLString(item)); text != "" {
				normalized = append(normalized, text)
			}
		}
		encoded, _ := json.Marshal(normalized)
		return string(encoded)
	}
	if value == nil {
		return "[]"
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return "[]"
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			if list, ok := parsed.([]any); ok {
				return normalize(list)
			}
			return normalize([]any{parsed})
		}
		return normalize([]any{text})
	}
	if bytes, ok := value.([]byte); ok {
		return legacyContentList(string(bytes))
	}
	if list, ok := value.([]any); ok {
		return normalize(list)
	}
	return normalize([]any{value})
}

func legacyModifiedBy(value any) *learners.ModifiedBy {
	text := strings.TrimSpace(legacySQLString(value))
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "\"") && strings.HasSuffix(text, "\"") {
		var decoded string
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			text = strings.TrimSpace(decoded)
		}
	}
	switch strings.ToUpper(text) {
	case string(learners.ModifiedByAI):
		modified := learners.ModifiedByAI
		return &modified
	case string(learners.ModifiedByUser):
		modified := learners.ModifiedByUser
		return &modified
	}
	return nil
}

func openLegacySQLite(dbPath string) (*sql.DB, error) {
	path := resolveLegacyDatabasePath(dbPath)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, legacyFail(http.StatusBadRequest, "旧数据库文件不存在: "+path)
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func legacyTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var marker int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1", table).Scan(&marker)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func legacyTableColumns(ctx context.Context, db *sql.DB, table string) ([]string, map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info('"+strings.ReplaceAll(table, "'", "''")+"')")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns := make([]string, 0)
	set := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, nil, err
		}
		columns = append(columns, name)
		set[name] = struct{}{}
	}
	return columns, set, rows.Err()
}

func legacyQueryRows(ctx context.Context, db *sql.DB, query string) ([]map[string]any, []string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			row[column] = values[index]
		}
		out = append(out, row)
	}
	return out, columns, rows.Err()
}

func loadLegacyExpressions(ctx context.Context, db *sql.DB) ([]map[string]any, map[string]struct{}, error) {
	table := ""
	for _, candidate := range []string{"expression", "expressions"} {
		exists, err := legacyTableExists(ctx, db, candidate)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			table = candidate
			break
		}
	}
	if table == "" {
		return nil, nil, legacyFail(http.StatusBadRequest, "旧数据库缺少表: expression, expressions")
	}
	_, columns, err := legacyTableColumns(ctx, db, table)
	if err != nil {
		return nil, nil, err
	}
	if _, ok := columns["situation"]; !ok {
		return nil, nil, legacyFail(http.StatusBadRequest, fmt.Sprintf("旧表达方式表 %s 缺少 situation/style 字段", table))
	}
	if _, ok := columns["style"]; !ok {
		return nil, nil, legacyFail(http.StatusBadRequest, fmt.Sprintf("旧表达方式表 %s 缺少 situation/style 字段", table))
	}
	rows, _, err := legacyQueryRows(ctx, db, "SELECT * FROM "+table)
	if err != nil {
		return nil, nil, err
	}
	return rows, columns, nil
}

func loadLegacyChatStreams(ctx context.Context, db *sql.DB) (map[string]map[string]any, map[string]struct{}, error) {
	out := map[string]map[string]any{}
	exists, err := legacyTableExists(ctx, db, "chat_streams")
	if err != nil || !exists {
		return out, nil, err
	}
	_, columns, err := legacyTableColumns(ctx, db, "chat_streams")
	if err != nil {
		return nil, nil, err
	}
	if _, ok := columns["stream_id"]; !ok {
		return out, columns, nil
	}
	rows, _, err := legacyQueryRows(ctx, db, "SELECT * FROM chat_streams")
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		streamID := strings.TrimSpace(legacySQLString(row["stream_id"]))
		if streamID == "" {
			continue
		}
		out[streamID] = row
	}
	return out, columns, nil
}

func legacyRowChatID(row map[string]any, columns map[string]struct{}) string {
	if _, ok := columns["chat_id"]; ok {
		if id := strings.TrimSpace(legacySQLString(row["chat_id"])); id != "" {
			return id
		}
	}
	if _, ok := columns["session_id"]; ok {
		return strings.TrimSpace(legacySQLString(row["session_id"]))
	}
	return ""
}

// selectLegacyImportMatchedSessions 对齐 Python select_legacy_import_matched_sessions：
// 优先返回配置账号精确命中的候选；没有命中时只回退 account_id 为空的历史候选。
// 配置对来自 get_all_bot_account_pairs（平台已 normalize 小写）；候选平台同样小写化，
// 避免 chat_sessions.platform 大小写漂移导致错失配置命中。
func selectLegacyImportMatchedSessions(sessions []learners.ChatInfo, configuredAccounts [][2]string) []learners.ChatInfo {
	configured := make(map[[2]string]struct{}, len(configuredAccounts))
	for _, pair := range configuredAccounts {
		platform := strings.ToLower(strings.TrimSpace(pair[0]))
		accountID := strings.TrimSpace(pair[1])
		if platform == "" || accountID == "" {
			continue
		}
		configured[[2]string{platform, accountID}] = struct{}{}
	}
	matched := make([]learners.ChatInfo, 0, len(sessions))
	for _, session := range sessions {
		key := [2]string{strings.ToLower(strings.TrimSpace(session.Platform)), strings.TrimSpace(session.AccountID)}
		if _, ok := configured[key]; ok {
			matched = append(matched, session)
		}
	}
	if len(matched) > 0 {
		return matched
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.AccountID) == "" {
			matched = append(matched, session)
		}
	}
	return matched
}

// legacyConfiguredBotAccountPairs 读取当前实例有效平台账号；未注入时按空集合处理
func (d Deps) legacyConfiguredBotAccountPairs(ctx context.Context) ([][2]string, error) {
	if d.BotAccountPairs == nil {
		return nil, nil
	}
	return d.BotAccountPairs(ctx)
}

func (d Deps) legacyPreviewCandidates(ctx context.Context, store learners.WebUIStore) []learners.ChatInfo {
	if store == nil {
		return nil
	}
	chats, err := store.ExpressionChatTargets(ctx, true)
	if err != nil {
		return nil
	}
	return chats
}

func (d Deps) buildLegacyPreview(ctx context.Context, dbPath string, store learners.WebUIStore) (map[string]any, error) {
	db, err := openLegacySQLite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, columns, err := loadLegacyExpressions(ctx, db)
	if err != nil {
		return nil, err
	}
	streams, _, err := loadLegacyChatStreams(ctx, db)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, row := range rows {
		oldChatID := legacyRowChatID(row, columns)
		if oldChatID == "" {
			continue
		}
		counts[oldChatID]++
	}
	oldChatIDs := make([]string, 0, len(counts))
	for oldChatID := range counts {
		oldChatIDs = append(oldChatIDs, oldChatID)
	}
	sort.Strings(oldChatIDs)
	candidates := d.legacyPreviewCandidates(ctx, store)
	configuredAccounts, err := d.legacyConfiguredBotAccountPairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("加载 Bot 账号配置: %w", err)
	}
	groups := make([]map[string]any, 0, len(oldChatIDs))
	totalCount := 0
	matchedCount := 0
	for _, oldChatID := range oldChatIDs {
		stream := streams[oldChatID]
		platform := strings.TrimSpace(legacySQLString(stream["platform"]))
		groupID := strings.TrimSpace(legacySQLString(stream["group_id"]))
		userID := strings.TrimSpace(legacySQLString(stream["user_id"]))
		chatType := ""
		targetID := ""
		if platform != "" && groupID != "" {
			chatType = "group"
			targetID = groupID
		} else if platform != "" && userID != "" {
			chatType = "private"
			targetID = userID
		}
		var matchedSessionID any
		var matchedChatName any
		matchedSessions := make([]map[string]any, 0)
		if platform != "" && targetID != "" && chatType != "" {
			matches := make([]learners.ChatInfo, 0)
			for _, candidate := range candidates {
				if candidate.Platform != platform || candidate.TargetID != targetID {
					continue
				}
				if (chatType == "group") != candidate.IsGroup {
					continue
				}
				matches = append(matches, candidate)
			}
			sort.SliceStable(matches, func(i, j int) bool { return matches[i].SessionID < matches[j].SessionID })
			matches = selectLegacyImportMatchedSessions(matches, configuredAccounts)
			if len(matches) == 1 {
				matchedSessionID = matches[0].SessionID
				name := matches[0].ChatName
				if name == "" {
					name = matches[0].SessionID
				}
				matchedChatName = name
			}
			for _, match := range matches {
				name := match.ChatName
				if name == "" {
					name = match.SessionID
				}
				matchedSessions = append(matchedSessions, map[string]any{
					"session_id": match.SessionID,
					"chat_name":  name,
					"account_id": nullableString(match.AccountID),
				})
			}
		}
		count := counts[oldChatID]
		totalCount += count
		if len(matchedSessions) > 0 {
			matchedCount++
		}
		groups = append(groups, map[string]any{
			"old_chat_id":        oldChatID,
			"expression_count":   count,
			"platform":           nullableString(platform),
			"target_id":          nullableString(targetID),
			"chat_type":          nullableString(chatType),
			"matched_session_id": matchedSessionID,
			"matched_chat_name":  matchedChatName,
			"matched":            len(matchedSessions) > 0,
			"matched_sessions":   matchedSessions,
		})
	}
	return map[string]any{
		"success":         true,
		"db_path":         resolveLegacyDatabasePath(dbPath),
		"total_count":     totalCount,
		"matched_count":   matchedCount,
		"unmatched_count": len(groups) - matchedCount,
		"groups":          groups,
	}, nil
}

func writeLegacyError(w http.ResponseWriter, err error, fallback string) {
	var httpErr *legacyHTTPError
	if errors.As(err, &httpErr) {
		writeAPIError(w, httpErr.Status, httpErr.Detail)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, fallback+err.Error())
}

func (d Deps) handleLegacyExpressionPreview(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/legacy-import/preview"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var request struct {
		DBPath string `json:"db_path"`
	}
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	result, err := d.buildLegacyPreview(r.Context(), request.DBPath, store)
	if err != nil {
		writeLegacyError(w, err, "预览旧版表达方式导入失败: ")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d Deps) handleLegacyExpressionPreviewFile(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/legacy-import/preview-file"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	uploadDir := d.legacyUploadDir()
	if uploadDir == "" {
		writeInstanceDirMissing(w, path)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeValidationError(w, "请上传旧版数据库文件")
		return
	}
	defer file.Close()
	filename := "legacy.db"
	if header != nil && strings.TrimSpace(header.Filename) != "" {
		filename = filepath.Base(header.Filename)
	}
	suffix := filepath.Ext(filename)
	if suffix == "" {
		suffix = ".db"
	}
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "预览上传旧版表达方式导入失败: "+err.Error())
		return
	}
	targetPath := filepath.Join(uploadDir, uuid.NewString()+suffix)
	target, err := os.Create(targetPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "预览上传旧版表达方式导入失败: "+err.Error())
		return
	}
	if _, err := io.Copy(target, file); err != nil {
		_ = target.Close()
		_ = os.Remove(targetPath)
		writeAPIError(w, http.StatusInternalServerError, "预览上传旧版表达方式导入失败: "+err.Error())
		return
	}
	if err := target.Close(); err != nil {
		_ = os.Remove(targetPath)
		writeAPIError(w, http.StatusInternalServerError, "预览上传旧版表达方式导入失败: "+err.Error())
		return
	}
	result, err := d.buildLegacyPreview(r.Context(), targetPath, store)
	if err != nil {
		writeLegacyError(w, err, "预览上传旧版表达方式导入失败: ")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
