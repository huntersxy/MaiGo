package services

// 聊天迁移计划、幂等来源账本和主数据库复制，对齐 services/chat_migration_store.py。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/gorm"

	memory "MaiGo/internal/common/database"
)

// 表归属须显式声明，新增聊天流数据表时不得静默漏迁
var ChatMigrationScopes = map[string]string{
	"mai_messages":            "session_id",
	"tool_records":            "session_id",
	"llm_usage":               "session_id",
	"maisaka_monitor_events":  "session_id",
	"expressions":             "session_id",
	"file_bindings":           "session_id",
	"statistics_usage_hourly": "session_id",
	"high_frequency_terms":    "chat_id",
	"sharing_group_members":   "session_id",
	"maisaka_reply_effects":   "session_id",
	"learned_commands":        "session_id",
}

// ChatMigrationExcluded 消息小时统计等实例级表不属于聊天流副本
var ChatMigrationExcluded = map[string]struct{}{
	"chat_sessions": {}, "webui_help_turns": {}, "statistics_message_hourly": {},
}

// ChatMigrationLabels 迁移计数展示名
var ChatMigrationLabels = map[string]string{
	"mai_messages":              "聊天消息",
	"tool_records":              "工具调用",
	"llm_usage":                 "模型用量",
	"maisaka_monitor_events":    "观察记录",
	"expressions":               "表达",
	"file_bindings":             "附件绑定",
	"statistics_message_hourly": "消息统计",
	"statistics_usage_hourly":   "用量统计",
	"high_frequency_terms":      "词频",
	"sharing_group_members":     "相通组成员关系",
	"jargons":                   "黑话",
	"maisaka_reply_effects":     "回复评估记录",
	"learned_commands":          "学习的命令",
}

// MigrationStore 主数据库迁移存储
type MigrationStore struct {
	DB *gorm.DB
}

// NewMigrationStore 构造迁移存储
func NewMigrationStore(db *gorm.DB) *MigrationStore {
	return &MigrationStore{DB: db}
}

// Initialize 确保迁移账本表存在
func (s *MigrationStore) Initialize(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("数据库未接线")
	}
	return s.DB.WithContext(ctx).AutoMigrate(&memory.ChatMigrationJob{}, &memory.ChatMigrationCopy{})
}

// migrateEncode 对齐 Python encode：bytes 与 datetime 使用显式标记
func migrateEncode(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return map[string]any{"__bytes__": base64.StdEncoding.EncodeToString(typed)}
	case time.Time:
		return typed.Format("2006-01-02T15:04:05.999999")
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = migrateEncode(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = migrateEncode(item)
		}
		return out
	case int:
		return typed
	case int64:
		return typed
	case float64:
		return typed
	case string:
		return typed
	case bool:
		return typed
	default:
		return typed
	}
}

func migrateDumps(value any) string {
	encoded, err := sonic.ConfigStd.Marshal(migrateEncode(value))
	if err != nil {
		return "null"
	}
	return string(encoded)
}

// MigrationDigest 对齐 Python digest：SHA-256 over canonical JSON
func MigrationDigest(value any) string {
	sum := sha256.Sum256([]byte(migrateDumps(value)))
	return hex.EncodeToString(sum[:])
}

// MigrationPublicRow 去除敏感列并生成作用域说明
func MigrationPublicRow(row map[string]any) map[string]any {
	result := make(map[string]any, len(row))
	for key, value := range row {
		switch key {
		case "raw_content", "id", "session_id":
			continue
		}
		result[key] = value
	}
	if raw, ok := row["session_id_dict"].(string); ok {
		var mapping map[string]any
		_ = json.Unmarshal([]byte(raw), &mapping)
		isGlobal := migrationBool(row["is_global"])
		switch {
		case isGlobal:
			result["scope_label"] = "全局"
		case len(mapping) > 1:
			result["scope_label"] = fmt.Sprintf("共享于 %d 个聊天流", len(mapping))
		default:
			result["scope_label"] = "单个聊天流"
		}
	}
	return result
}

// Create 新建预览任务
func (s *MigrationStore) Create(ctx context.Context, source, target string, payload map[string]any) (string, error) {
	jobID := strings.ReplaceAll(uuid.NewString(), "-", "")
	job := memory.ChatMigrationJob{
		ID: jobID, Source: source, Target: target, Status: "preview",
		CreatedAt: time.Now().Format("2006-01-02T15:04:05.999999"),
		Payload:   migrateDumps(payload), Error: "",
	}
	if err := s.DB.WithContext(ctx).Create(&job).Error; err != nil {
		return "", err
	}
	return jobID, nil
}

// Get 读取任务及解析后的 payload
func (s *MigrationStore) Get(ctx context.Context, jobID string) (map[string]any, error) {
	var job memory.ChatMigrationJob
	if err := s.DB.WithContext(ctx).Where("id = ?", jobID).Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("迁移任务不存在")
		}
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": job.ID, "status": job.Status, "source": job.Source, "target": job.Target,
		"created_at": job.CreatedAt, "payload": payload, "error": job.Error,
	}, nil
}

// List 列出最近任务；unfinished 时只看未结束任务
func (s *MigrationStore) List(ctx context.Context, unfinished bool) ([]map[string]any, error) {
	query := s.DB.WithContext(ctx).Model(&memory.ChatMigrationJob{})
	if unfinished {
		query = query.Where("status NOT IN ?", []string{"preview", "completed", "failed"})
	} else {
		query = query.Order("created_at DESC").Limit(100)
	}
	var jobs []memory.ChatMigrationJob
	if err := query.Find(&jobs).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, map[string]any{
			"id": job.ID, "source": job.Source, "target": job.Target,
			"status": job.Status, "created_at": job.CreatedAt, "error": job.Error,
		})
	}
	return result, nil
}

// Update 更新任务状态与 payload
func (s *MigrationStore) Update(ctx context.Context, jobID, status string, payload map[string]any, errorText string) error {
	return s.DB.WithContext(ctx).Model(&memory.ChatMigrationJob{}).Where("id = ?", jobID).
		Updates(map[string]any{"status": status, "payload": migrateDumps(payload), "error": errorText}).Error
}

// Claim 原子领取任务并检查同会话冲突
func (s *MigrationStore) Claim(ctx context.Context, jobID string, decisions map[string]string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job memory.ChatMigrationJob
		if err := tx.Where("id = ?", jobID).Take(&job).Error; err != nil {
			return err
		}
		if job.Status != "preview" {
			return errors.New("迁移任务已被执行，请勿重复提交")
		}
		var conflicts []memory.ChatMigrationJob
		if err := tx.Where("status NOT IN ?", []string{"preview", "completed", "failed"}).
			Where("source IN ? OR target IN ?", []string{job.Source, job.Target}, []string{job.Source, job.Target}).
			Limit(1).Find(&conflicts).Error; err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return errors.New("所选聊天流正在迁移或等待恢复")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return err
		}
		payload["decisions"] = decisions
		return tx.Model(&memory.ChatMigrationJob{}).Where("id = ?", jobID).
			Updates(map[string]any{"status": "queued", "payload": migrateDumps(payload)}).Error
	})
}

// migrationRow 查询一组行
func migrationRows(ctx context.Context, db *gorm.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		row := map[string]any{}
		if err := db.ScanRows(rows, &row); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func migrationTableNames(ctx context.Context, db *gorm.DB) ([]string, error) {
	rows, err := migrationRows(ctx, db, "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if name, ok := row["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

func migrationColumns(ctx context.Context, db *gorm.DB, table string) ([]string, error) {
	rows, err := migrationRows(ctx, db, "PRAGMA table_info("+quoteMigrationIdent(table)+")")
	if err != nil {
		return nil, err
	}
	columns := make([]string, 0, len(rows))
	for _, row := range rows {
		if name, ok := row["name"].(string); ok {
			columns = append(columns, name)
		}
	}
	return columns, nil
}

func quoteMigrationIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func migrationIdentity(row map[string]any) map[string]string {
	platform, _ := row["platform"].(string)
	groupID, _ := row["group_id"].(string)
	userID, _ := row["user_id"].(string)
	if groupID != "" {
		return map[string]string{"platform": platform, "item_id": groupID, "type": "group"}
	}
	return map[string]string{"platform": platform, "item_id": userID, "type": "private"}
}

// Snapshot 生成迁移预览数据
func (s *MigrationStore) Snapshot(ctx context.Context, source, target string) (map[string]any, error) {
	return s.snapshotWithDB(ctx, s.DB, source, target)
}

func (s *MigrationStore) snapshotWithDB(ctx context.Context, db *gorm.DB, source, target string) (map[string]any, error) {
	if source == target {
		return nil, errors.New("来源和目标不能是同一个聊天流")
	}
	db = db.WithContext(ctx)
	names, err := migrationTableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	nameSet := make(map[string]struct{}, len(names))
	unknown := make([]string, 0)
	for _, name := range names {
		nameSet[name] = struct{}{}
		if _, ok := ChatMigrationScopes[name]; ok {
			continue
		}
		if _, ok := ChatMigrationExcluded[name]; ok {
			continue
		}
		columns, colErr := migrationColumns(ctx, db, name)
		if colErr != nil {
			return nil, colErr
		}
		for _, column := range columns {
			if column == "session_id" || column == "chat_id" {
				unknown = append(unknown, name)
				break
			}
		}
	}
	if len(unknown) > 0 {
		return nil, errors.New("以下聊天数据表尚未声明迁移规则: " + strings.Join(unknown, ", "))
	}
	sessions, err := migrationRows(ctx, db,
		"SELECT * FROM chat_sessions WHERE session_id IN (?, ?)", source, target)
	if err != nil {
		return nil, err
	}
	if len(sessions) != 2 {
		return nil, errors.New("来源或目标聊天流已不存在，请重新选择真实聊天流")
	}
	identities := make(map[string]map[string]any, 2)
	for _, row := range sessions {
		sessionID, _ := row["session_id"].(string)
		identities[sessionID] = row
	}
	tables := make(map[string][]map[string]any)
	for name, scope := range ChatMigrationScopes {
		if _, ok := nameSet[name]; !ok {
			continue
		}
		rows, queryErr := migrationRows(ctx, db,
			fmt.Sprintf("SELECT * FROM %s WHERE %s IN (?, ?)", quoteMigrationIdent(name), quoteMigrationIdent(scope)),
			source, target)
		if queryErr != nil {
			return nil, queryErr
		}
		sortMigrationRows(rows)
		tables[name] = rows
	}
	jargonRows, err := migrationRows(ctx, db, "SELECT * FROM jargons ORDER BY id")
	if err != nil {
		return nil, err
	}
	jargons := make([]map[string]any, 0)
	for _, row := range jargonRows {
		mapping, ok := migrationSessionDict(row)
		if !ok {
			continue
		}
		_, fromSource := mapping[source]
		_, fromTarget := mapping[target]
		if fromSource || fromTarget {
			jargons = append(jargons, row)
		}
	}
	tables["jargons"] = jargons
	ledger, err := migrationRows(ctx, db, "SELECT * FROM chat_migration_copies")
	if err != nil {
		return nil, err
	}
	data := map[string]any{
		"identities": identities, "tables": tables, "ledger": ledger, "conflicts": []map[string]any{},
	}
	conflicts := make([]map[string]any, 0)
	for _, name := range []string{"expressions", "jargons", "learned_commands"} {
		for _, row := range s.SourceRows(data, name, source) {
			if s.AlreadyCopied(data, name, row, source, target) {
				continue
			}
			matches := s.Matches(data, name, row, target)
			if len(matches) == 0 {
				continue
			}
			label := migrationRowLabel(row)
			targetRows := make([]map[string]any, 0, len(matches))
			for _, item := range matches {
				targetRows = append(targetRows, MigrationPublicRow(item))
			}
			conflicts = append(conflicts, map[string]any{
				"id":      fmt.Sprintf("%s:%v", name, row["id"]),
				"kind":    name,
				"label":   label,
				"source":  MigrationPublicRow(row),
				"target":  targetRows,
				"choices": []string{"source", "target", "both"},
			})
		}
	}
	data["conflicts"] = conflicts
	identityFields := []string{"session_id", "platform", "user_id", "group_id", "account_id", "scope"}
	identitySummary := make(map[string]map[string]any, len(identities))
	for key, row := range identities {
		summary := make(map[string]any, len(identityFields))
		for _, field := range identityFields {
			if value, ok := row[field]; ok {
				summary[field] = value
			}
		}
		identitySummary[key] = summary
	}
	data["fingerprint"] = MigrationDigest(map[string]any{"tables": tables, "identities": identitySummary})
	return data, nil
}

func migrationRowLabel(row map[string]any) string {
	for _, key := range []string{"content", "command"} {
		if text, ok := row[key].(string); ok && text != "" {
			return text
		}
	}
	situation, _ := row["situation"].(string)
	style, _ := row["style"].(string)
	return situation + " / " + style
}

func migrationSessionDict(row map[string]any) (map[string]any, bool) {
	raw, ok := row["session_id_dict"].(string)
	if !ok {
		if bytes, isBytes := row["session_id_dict"].([]byte); isBytes {
			raw = string(bytes)
		} else {
			return nil, false
		}
	}
	var mapping map[string]any
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return nil, false
	}
	return mapping, true
}

func sortMigrationRows(rows []map[string]any) {
	sort.Slice(rows, func(i, j int) bool { return migrateDumps(rows[i]) < migrateDumps(rows[j]) })
}

// SourceRows 按来源拆分聚合行
func (s *MigrationStore) SourceRows(data map[string]any, name, source string) []map[string]any {
	tables, _ := data["tables"].(map[string][]map[string]any)
	ledger, _ := data["ledger"].([]map[string]any)
	rows := make([]map[string]any, 0)
	for _, row := range tables[name] {
		if name == "jargons" {
			mapping, ok := migrationSessionDict(row)
			isGlobal := migrationBool(row["is_global"])
			if !ok || isGlobal {
				continue
			}
			if _, ok := mapping[source]; !ok {
				continue
			}
			rows = append(rows, row)
			continue
		}
		scope := ChatMigrationScopes[name]
		if value, ok := row[scope]; ok && fmt.Sprint(value) == source {
			rows = append(rows, row)
		}
	}
	if !strings.HasPrefix(name, "statistics_") && name != "high_frequency_terms" {
		return rows
	}
	// 聚合行可以同时包含多个来源的贡献；按来源拆开，避免重复累加
	transfers := make([]map[string]any, 0)
	for _, row := range rows {
		native := MigrationAmounts(row)
		for _, item := range ledger {
			if fmt.Sprint(item["table_name"]) != name || fmt.Sprint(item["target"]) != source ||
				fmt.Sprint(item["target_key"]) != fmt.Sprint(row["id"]) {
				continue
			}
			var values map[string]any
			if raw, ok := item["amounts"].(string); ok {
				_ = json.Unmarshal([]byte(raw), &values)
			}
			for key, value := range values {
				native[key] = migrationNumber(native[key]) - migrationNumber(value)
			}
			transfer := cloneMigrationRow(row)
			for key, value := range values {
				transfer[key] = value
			}
			transfer["__migration_origin"] = item["origin"]
			transfers = append(transfers, transfer)
		}
		hasNative := false
		for _, value := range native {
			if migrationNumber(value) != 0 {
				hasNative = true
				break
			}
		}
		if hasNative {
			transfer := cloneMigrationRow(row)
			for key, value := range native {
				transfer[key] = value
			}
			transfer["__migration_origin"] = fmt.Sprintf("%s:%v", source, row["id"])
			transfers = append(transfers, transfer)
		}
	}
	return transfers
}

func cloneMigrationRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}

// MigrationAmounts 提取统计维度数值
func MigrationAmounts(row map[string]any) map[string]any {
	result := make(map[string]any)
	for key, value := range row {
		switch key {
		case "id", "rank", "frequency", "message_frequency":
			continue
		}
		if _, ok := migrationNumberOK(value); ok {
			result[key] = value
		}
	}
	return result
}

func migrationNumber(value any) float64 {
	number, _ := migrationNumberOK(value)
	return number
}

func migrationNumberOK(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

// Origin 返回来源记录标识
func (s *MigrationStore) Origin(data map[string]any, name string, row map[string]any, source string) string {
	if origin, ok := row["__migration_origin"]; ok {
		return fmt.Sprint(origin)
	}
	key := ""
	for _, field := range []string{"id", "event_id", "effect_id", "group_id"} {
		if value, ok := row[field]; ok {
			key = fmt.Sprint(value)
			break
		}
	}
	ledger, _ := data["ledger"].([]map[string]any)
	for _, item := range ledger {
		if fmt.Sprint(item["table_name"]) == name && fmt.Sprint(item["target"]) == source &&
			fmt.Sprint(item["target_key"]) == key {
			return fmt.Sprint(item["origin"])
		}
	}
	return source + ":" + key
}

// AlreadyCopied 判断来源记录是否已有目标副本或用户保留决定
func (s *MigrationStore) AlreadyCopied(data map[string]any, name string, row map[string]any, source, target string) bool {
	if name == "jargons" {
		mapping, ok := migrationSessionDict(row)
		isGlobal := migrationBool(row["is_global"])
		if ok {
			_, inTarget := mapping[target]
			if isGlobal || inTarget {
				return true
			}
		}
	}
	origin := s.Origin(data, name, row, source)
	if strings.HasPrefix(origin, target+":") {
		return true
	}
	ledger, _ := data["ledger"].([]map[string]any)
	var existing map[string]any
	for _, item := range ledger {
		if fmt.Sprint(item["table_name"]) == name && fmt.Sprint(item["origin"]) == origin &&
			fmt.Sprint(item["target"]) == target {
			existing = item
			break
		}
	}
	if existing == nil {
		return false
	}
	if strings.HasPrefix(name, "statistics_") || name == "high_frequency_terms" {
		var amounts map[string]any
		if raw, ok := existing["amounts"].(string); ok {
			_ = json.Unmarshal([]byte(raw), &amounts)
		}
		return migrateDumps(amounts) == migrateDumps(MigrationAmounts(row))
	}
	if fmt.Sprint(existing["target_key"]) == "" {
		return true
	}
	key := fmt.Sprint(existing["target_key"])
	for _, item := range s.SourceRows(data, name, target) {
		for _, field := range []string{"id", "event_id", "effect_id", "group_id"} {
			if value, ok := item[field]; ok && fmt.Sprint(value) == key {
				return true
			}
		}
	}
	return false
}

// Matches 查找目标作用域内同内容记录
func (s *MigrationStore) Matches(data map[string]any, name string, row map[string]any, target string) []map[string]any {
	candidates := s.SourceRows(data, name, target)
	keys := []string{"content"}
	switch name {
	case "expressions":
		keys = []string{"situation", "style"}
	case "learned_commands":
		keys = []string{"command", "target"}
	}
	matches := make([]map[string]any, 0)
	for _, item := range candidates {
		if fmt.Sprint(item["id"]) == fmt.Sprint(row["id"]) {
			continue
		}
		allEqual := true
		for _, key := range keys {
			if fmt.Sprint(item[key]) != fmt.Sprint(row[key]) {
				allEqual = false
				break
			}
		}
		if allEqual {
			matches = append(matches, item)
		}
	}
	return matches
}

// Apply 在单个事务内复制数据并写入账本
func (s *MigrationStore) Apply(ctx context.Context, jobID, expected string, decisions map[string]string) (map[string]int, error) {
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	source, _ := job["source"].(string)
	target, _ := job["target"].(string)
	counts := map[string]int{}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		data, err := s.snapshotWithDB(ctx, tx, source, target)
		if err != nil {
			return err
		}
		if fmt.Sprint(data["fingerprint"]) != expected {
			return errors.New("聊天数据已变化，请重新预览并处理冲突")
		}
		conflicts, _ := data["conflicts"].([]map[string]any)
		for _, conflict := range conflicts {
			choice := decisions[fmt.Sprint(conflict["id"])]
			if choice == "" {
				return errors.New("存在未处理的冲突")
			}
		}
		tables, _ := data["tables"].(map[string][]map[string]any)
		names := make([]string, 0, len(tables))
		for name := range tables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			counts[name] = 0
			if name == "sharing_group_members" {
				choice := decisions["sharing_groups"]
				if choice == "" {
					choice = "both"
				}
				if choice == "target" {
					continue
				}
				if choice == "source" {
					if err := tx.Exec("DELETE FROM sharing_group_members WHERE session_id = ?", target).Error; err != nil {
						return err
					}
				}
			}
			for _, original := range s.SourceRows(data, name, source) {
				if s.AlreadyCopied(data, name, original, source, target) {
					continue
				}
				row := cloneMigrationRow(original)
				delete(row, "__migration_origin")
				origin := s.Origin(data, name, original, source)
				ledger, _ := data["ledger"].([]map[string]any)
				var previous map[string]any
				for _, item := range ledger {
					if fmt.Sprint(item["table_name"]) == name && fmt.Sprint(item["origin"]) == origin &&
						fmt.Sprint(item["target"]) == target {
						previous = item
						break
					}
				}
				choice := decisions[fmt.Sprintf("%s:%v", name, row["id"])]
				if choice == "" {
					choice = "both"
				}
				matches := []map[string]any{}
				if name == "expressions" || name == "jargons" || name == "learned_commands" {
					matches = s.Matches(data, name, row, target)
				}
				targetKey := ""
				if choice == "source" {
					for _, match := range matches {
						if name == "jargons" {
							mapping, ok := migrationSessionDict(match)
							if !ok {
								continue
							}
							delete(mapping, target)
							if err := tx.Exec("UPDATE jargons SET session_id_dict = ? WHERE id = ?",
								migrateDumps(mapping), match["id"]).Error; err != nil {
								return err
							}
							continue
						}
						if err := tx.Exec("DELETE FROM "+quoteMigrationIdent(name)+" WHERE id = ?", match["id"]).Error; err != nil {
							return err
						}
					}
				}
				if choice != "target" {
					delete(row, "id")
					if name == "maisaka_monitor_events" {
						delete(row, "event_id")
					}
					if name == "maisaka_reply_effects" {
						row["effect_id"] = strings.ReplaceAll(uuid.NewString(), "-", "")
					}
					if name == "jargons" {
						mapping, ok := migrationSessionDict(row)
						if !ok {
							continue
						}
						row["session_id_dict"] = migrateDumps(map[string]any{target: mapping[source]})
						row["is_global"] = false
					} else {
						row[ChatMigrationScopes[name]] = target
					}
					existing, err := migrationUniqueMatch(ctx, tx, name, row)
					if err != nil {
						return err
					}
					if existing != nil {
						if strings.HasPrefix(name, "statistics_") || name == "high_frequency_terms" {
							oldAmounts := map[string]any{}
							if previous != nil {
								if raw, ok := previous["amounts"].(string); ok {
									_ = json.Unmarshal([]byte(raw), &oldAmounts)
								}
							}
							additive := map[string]any{}
							for key, value := range MigrationAmounts(row) {
								additive[key] = migrationNumber(existing[key]) + migrationNumber(value) - migrationNumber(oldAmounts[key])
							}
							if err := migrationUpdate(tx, name, "id", existing["id"], additive); err != nil {
								return err
							}
						}
						targetKey = fmt.Sprint(existing["id"])
						if targetKey == "<nil>" || targetKey == "" {
							targetKey = fmt.Sprint(existing["group_id"])
						}
					} else {
						insertedID, err := migrationInsert(ctx, tx, name, row)
						if err != nil {
							return err
						}
						if insertedID != 0 {
							targetKey = strconv.FormatInt(insertedID, 10)
						} else {
							targetKey = fmt.Sprint(row["group_id"])
						}
						counts[name]++
					}
				}
				values := map[string]any{
					"table_name": name, "origin": origin, "target": target,
					"target_key": targetKey, "amounts": migrateDumps(MigrationAmounts(original)),
				}
				if previous != nil {
					if err := tx.Model(&memory.ChatMigrationCopy{}).Where("id = ?", previous["id"]).Updates(values).Error; err != nil {
						return err
					}
				} else if err := tx.Create(&memory.ChatMigrationCopy{
					Table: name, Origin: origin, Target: target, TargetKey: targetKey,
					Amounts: migrateDumps(MigrationAmounts(original)),
				}).Error; err != nil {
					return err
				}
			}
		}
		var payload map[string]any
		if raw, ok := job["payload"].(map[string]any); ok {
			payload = raw
		}
		if payload == nil {
			payload = map[string]any{}
		}
		result := make(map[string]any, len(counts))
		for key, value := range counts {
			result[key] = value
		}
		payload["result"] = result
		payload["database_committed"] = true
		return tx.Model(&memory.ChatMigrationJob{}).Where("id = ?", jobID).Updates(map[string]any{
			"status": "finalizing", "payload": migrateDumps(payload), "error": "",
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

func migrationInsert(ctx context.Context, tx *gorm.DB, table string, row map[string]any) (int64, error) {
	columns := make([]string, 0, len(row))
	for column := range row {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	quoted := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	values := make([]any, len(columns))
	for index, column := range columns {
		quoted[index] = quoteMigrationIdent(column)
		placeholders[index] = "?"
		values[index] = row[column]
	}
	statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteMigrationIdent(table), strings.Join(quoted, ","), strings.Join(placeholders, ","))
	if err := tx.WithContext(ctx).Exec(statement, values...).Error; err != nil {
		return 0, err
	}
	var insertedID int64
	if err := tx.WithContext(ctx).Raw("SELECT last_insert_rowid()").Scan(&insertedID).Error; err != nil {
		return 0, nil
	}
	return insertedID, nil
}

func migrationUpdate(tx *gorm.DB, table, key string, id any, patch map[string]any) error {
	columns := make([]string, 0, len(patch))
	for column := range patch {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	assignments := make([]string, len(columns))
	values := make([]any, 0, len(columns)+1)
	for index, column := range columns {
		assignments[index] = quoteMigrationIdent(column) + "=?"
		values = append(values, patch[column])
	}
	values = append(values, id)
	return tx.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?",
		quoteMigrationIdent(table), strings.Join(assignments, ","), quoteMigrationIdent(key)), values...).Error
}

func migrationUniqueMatch(ctx context.Context, tx *gorm.DB, table string, row map[string]any) (map[string]any, error) {
	keysets := make([][]string, 0)
	indexes, err := migrationRows(ctx, tx, "PRAGMA index_list("+quoteMigrationIdent(table)+")")
	if err != nil {
		return nil, err
	}
	for _, index := range indexes {
		unique := migrationNumber(index["unique"])
		if unique == 0 {
			continue
		}
		name := fmt.Sprint(index["name"])
		columns, colErr := migrationRows(ctx, tx, "PRAGMA index_info("+quoteMigrationIdent(name)+")")
		if colErr != nil {
			return nil, colErr
		}
		keys := make([]string, 0, len(columns))
		for _, column := range columns {
			keys = append(keys, fmt.Sprint(column["name"]))
		}
		keysets = append(keysets, keys)
	}
	if table == "sharing_group_members" {
		keysets = append(keysets, []string{"group_id", "session_id"})
	}
	for _, keys := range keysets {
		where := make([]string, 0, len(keys))
		values := make([]any, 0, len(keys))
		valid := true
		for _, key := range keys {
			if _, ok := row[key]; !ok {
				valid = false
				break
			}
			where = append(where, quoteMigrationIdent(key)+" = ?")
			values = append(values, row[key])
		}
		if !valid {
			continue
		}
		found, queryErr := migrationRows(ctx, tx,
			fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT 1", quoteMigrationIdent(table), strings.Join(where, " AND ")),
			values...)
		if queryErr != nil {
			return nil, queryErr
		}
		if len(found) > 0 {
			return found[0], nil
		}
	}
	return nil, nil
}

func migrationBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "1" || normalized == "true"
	default:
		return false
	}
}
