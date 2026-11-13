package maibot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// LegacyConfigMigrationTaskName 对齐 MaiBot legacy_migration.LEGACY_CONFIG_MIGRATION_TASK_NAME
const LegacyConfigMigrationTaskName = "legacy_config_migration_v1"

// LegacyMigrationResult 旧配置结构迁移结果
type LegacyMigrationResult struct {
	Data     map[string]any
	Migrated bool
	Reason   string
}

// LegacyMigrationReport 一次加载中旧配置迁移的汇总
type LegacyMigrationReport struct {
	Applied    bool
	EnvApplied bool
	Reasons    []string
}

// MigrateLegacyBindEnvToBotConfig 把旧版 .env 的 HOST/PORT/WEBUI_HOST/WEBUI_PORT 绑定迁入主配置
// 对齐 MaiBot migrate_legacy_bind_env_to_bot_config_dict
func MigrateLegacyBindEnvToBotConfig(data map[string]any, getenv func(string) string) LegacyMigrationResult {
	if getenv == nil {
		getenv = os.Getenv
	}
	migrated := false
	var reasons []string

	mainHost := legacyParseHostEnv(getenv("HOST"))
	mainPort := legacyParsePortEnv(getenv("PORT"))
	maimMessage := legacyAsDict(data["maim_message"])
	if maimMessage == nil && (mainHost != nil || mainPort != nil) {
		maimMessage = map[string]any{}
		data["maim_message"] = maimMessage
	}
	if maimMessage != nil && mainHost != nil && legacyMigrateEnvValue(maimMessage, "ws_server_host", *mainHost, "127.0.0.1") {
		migrated = true
		reasons = append(reasons, "HOST->maim_message.ws_server_host")
	}
	if maimMessage != nil && mainPort != nil && legacyMigrateEnvValue(maimMessage, "ws_server_port", *mainPort, 8000) {
		migrated = true
		reasons = append(reasons, "PORT->maim_message.ws_server_port")
	}

	webuiHost := legacyParseHostEnv(getenv("WEBUI_HOST"))
	webuiPort := legacyParsePortEnv(getenv("WEBUI_PORT"))
	webui := legacyAsDict(data["webui"])
	if webui == nil && (webuiHost != nil || webuiPort != nil) {
		webui = map[string]any{}
		data["webui"] = webui
	}
	if webui != nil && webuiHost != nil && legacyMigrateEnvValue(webui, "host", *webuiHost, "127.0.0.1") {
		migrated = true
		reasons = append(reasons, "WEBUI_HOST->webui.host")
	}
	if webui != nil && webuiPort != nil && legacyMigrateEnvValue(webui, "port", *webuiPort, 8001) {
		migrated = true
		reasons = append(reasons, "WEBUI_PORT->webui.port")
	}

	return LegacyMigrationResult{Data: data, Migrated: migrated, Reason: strings.Join(reasons, ",")}
}

// TryMigrateLegacyBotConfig 修复 bot_config.toml 的旧结构
// 对齐 MaiBot try_migrate_legacy_bot_config_dict
func TryMigrateLegacyBotConfig(data map[string]any) LegacyMigrationResult {
	migrated := false
	var reasons []string

	bot := legacyAsDict(data["bot"])
	if bot != nil {
		if value, ok := bot["qq_account"].(int64); ok {
			if value > 0 {
				bot["qq_account"] = strconv.FormatInt(value, 10)
			} else {
				bot["qq_account"] = ""
			}
			migrated = true
			reasons = append(reasons, "bot.qq_account_int_to_string")
		} else if value, ok := bot["qq_account"].(int); ok {
			if value > 0 {
				bot["qq_account"] = strconv.Itoa(value)
			} else {
				bot["qq_account"] = ""
			}
			migrated = true
			reasons = append(reasons, "bot.qq_account_int_to_string")
		}
	}

	chat := legacyAsDict(data["chat"])
	if chat != nil && legacyMigrateChatTalkValueRules(chat) {
		migrated = true
		reasons = append(reasons, "chat.talk_value_rules_target")
	}

	expr := legacyAsDict(data["expression"])
	if expr != nil {
		if legacyMigrateExpressionLearningList(expr) {
			migrated = true
			reasons = append(reasons, "expression.learning_list")
		}
		if legacyMigrateExpressionGroups(expr) {
			migrated = true
			reasons = append(reasons, "expression.expression_groups")
		}
	}

	keywordReaction := legacyAsDict(data["keyword_reaction"])
	if keywordReaction != nil {
		if legacyDropEmptyKeywordRules(keywordReaction, "keyword_rules") {
			migrated = true
			reasons = append(reasons, "keyword_reaction.keyword_rules_empty")
		}
		if legacyDropEmptyKeywordRules(keywordReaction, "regex_rules") {
			migrated = true
			reasons = append(reasons, "keyword_reaction.regex_rules_empty")
		}
	}

	return LegacyMigrationResult{Data: data, Migrated: migrated, Reason: strings.Join(reasons, ",")}
}

func legacyAsDict(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func legacyAsList(value any) []any {
	list, _ := value.([]any)
	return list
}

func legacyParseHostEnv(value string) *string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func legacyParsePortEnv(value string) *int {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	port, err := strconv.Atoi(normalized)
	if err != nil || port <= 0 || port > 65535 {
		return nil
	}
	return &port
}

func legacyMigrateEnvValue(section map[string]any, key string, parsed any, defaultValue any) bool {
	if legacyIsNil(parsed) {
		return false
	}
	current, exists := section[key]
	if legacyValueEqual(current, parsed) {
		return false
	}
	if exists && !legacyValueEqual(current, defaultValue) {
		return false
	}
	section[key] = parsed
	return true
}

func legacyValueEqual(a, b any) bool {
	if legacyIsNil(a) || legacyIsNil(b) {
		return legacyIsNil(a) && legacyIsNil(b)
	}
	if af, ok := legacyNumericValue(a); ok {
		if bf, ok := legacyNumericValue(b); ok {
			return af == bf
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

func legacyNumericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func legacyIsNil(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func legacyParseTripletTarget(value string) map[string]any {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return nil
	}
	ruleType := parts[2]
	if ruleType != "group" && ruleType != "private" {
		return nil
	}
	return map[string]any{"platform": parts[0], "item_id": parts[1], "rule_type": ruleType}
}

func legacyParseExpressionGroupTarget(value string) map[string]any {
	normalized := strings.TrimSpace(value)
	if normalized == "*" {
		return map[string]any{"platform": "*", "item_id": "*", "rule_type": "group"}
	}
	return legacyParseTripletTarget(normalized)
}

func legacyParseEnableDisable(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "enable", "true":
			return true, true
		case "disable", "false":
			return false, true
		}
	}
	return false, false
}

func legacyMigrateExpressionLearningList(expr map[string]any) bool {
	learningList := legacyAsList(expr["learning_list"])
	if learningList == nil {
		return false
	}
	if len(learningList) > 0 {
		allDict := true
		for _, item := range learningList {
			if legacyAsDict(item) == nil {
				allDict = false
				break
			}
		}
		if allDict {
			return false
		}
	}

	migratedItems := make([]any, 0, len(learningList))
	for _, row := range learningList {
		rowItems := legacyAsList(row)
		if len(rowItems) < 4 {
			return false
		}
		targetRaw := rowItems[0]
		useExpression, okUse := legacyParseEnableDisable(rowItems[1])
		enableLearning, okLearn := legacyParseEnableDisable(rowItems[2])
		enableJargon, okJargon := legacyParseEnableDisable(rowItems[3])
		if !okJargon {
			if _, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(rowItems[3])), 64); err == nil {
				enableJargon = false
				okJargon = true
			}
		}
		if !okUse || !okLearn || !okJargon {
			return false
		}

		var target map[string]any
		if targetRaw == nil || fmt.Sprint(targetRaw) == "" {
			target = map[string]any{"platform": "", "item_id": "", "rule_type": "group"}
		} else {
			target = legacyParseTripletTarget(fmt.Sprint(targetRaw))
			if target == nil {
				return false
			}
		}
		migratedItems = append(migratedItems, map[string]any{
			"platform":               target["platform"],
			"item_id":                target["item_id"],
			"rule_type":              target["rule_type"],
			"use_expression":         useExpression,
			"enable_learning":        enableLearning,
			"enable_jargon_learning": enableJargon,
		})
	}
	expr["learning_list"] = migratedItems
	return true
}

func legacyMigrateChatTalkValueRules(chat map[string]any) bool {
	rules := legacyAsList(chat["talk_value_rules"])
	if rules == nil {
		return false
	}
	migrated := false
	for _, rule := range rules {
		ruleItem := legacyAsDict(rule)
		if ruleItem == nil {
			continue
		}
		targetRaw, ok := ruleItem["target"]
		if !ok {
			continue
		}
		target := strings.TrimSpace(fmt.Sprint(targetRaw))
		if targetRaw == nil {
			target = ""
		}
		var parsed map[string]any
		if target == "" {
			parsed = map[string]any{"platform": "", "item_id": "", "rule_type": "group"}
		} else {
			parsed = legacyParseTripletTarget(target)
			if parsed == nil {
				continue
			}
		}
		ruleItem["platform"] = parsed["platform"]
		ruleItem["item_id"] = parsed["item_id"]
		ruleItem["rule_type"] = parsed["rule_type"]
		delete(ruleItem, "target")
		migrated = true
	}
	return migrated
}

func legacyMigrateExpressionGroups(expr map[string]any) bool {
	rawGroups, ok := expr["expression_groups"]
	if !ok {
		return false
	}
	if value, isString := rawGroups.(string); isString {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			expr["expression_groups"] = []any{}
			return true
		}
		parsed := legacyParseExpressionGroupTarget(normalized)
		if parsed == nil {
			return false
		}
		expr["expression_groups"] = []any{map[string]any{"expression_groups": []any{parsed}}}
		return true
	}
	groups := legacyAsList(rawGroups)
	if groups == nil {
		return false
	}
	if len(groups) > 0 {
		allDict := true
		for _, item := range groups {
			if legacyAsDict(item) == nil {
				allDict = false
				break
			}
		}
		if allDict {
			return false
		}
	}
	migratedGroups := make([]any, 0, len(groups))
	for _, group := range groups {
		groupItems := legacyAsList(group)
		if groupItems == nil {
			return false
		}
		targets := make([]any, 0, len(groupItems))
		for _, item := range groupItems {
			parsed := legacyParseExpressionGroupTarget(fmt.Sprint(item))
			if parsed == nil {
				return false
			}
			targets = append(targets, parsed)
		}
		migratedGroups = append(migratedGroups, map[string]any{"expression_groups": targets})
	}
	expr["expression_groups"] = migratedGroups
	return true
}

func legacyDropEmptyKeywordRules(keywordReaction map[string]any, key string) bool {
	raw := legacyAsList(keywordReaction[key])
	if raw == nil {
		return false
	}
	cleaned := make([]any, 0, len(raw))
	dropped := false
	for _, item := range raw {
		itemDict := legacyAsDict(item)
		if itemDict == nil {
			cleaned = append(cleaned, item)
			continue
		}
		keywords := legacyAsList(itemDict["keywords"])
		regex := legacyAsList(itemDict["regex"])
		reaction := itemDict["reaction"]
		if len(keywords) == 0 && len(regex) == 0 && (reaction == nil || strings.TrimSpace(fmt.Sprint(reaction)) == "") {
			dropped = true
			continue
		}
		cleaned = append(cleaned, item)
	}
	if !dropped {
		return false
	}
	keywordReaction[key] = cleaned
	return true
}

// LegacyMigrationStatePath 返回一次性迁移状态文件路径
func LegacyMigrationStatePath(instanceRoot string) string {
	return filepath.Join(instanceRoot, "data", LegacyConfigMigrationTaskName+".json")
}

type legacyMigrationState struct {
	Status    string `json:"status"`
	Migrated  bool   `json:"migrated"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
}

// LegacyMigrationCompleted 判断一次性旧配置迁移是否已完成
func LegacyMigrationCompleted(instanceRoot string) bool {
	raw, err := os.ReadFile(LegacyMigrationStatePath(instanceRoot))
	if err != nil {
		return false
	}
	var state legacyMigrationState
	if err := json.Unmarshal(raw, &state); err != nil {
		return false
	}
	return strings.TrimSpace(state.Status) == "done"
}

// MarkLegacyMigrationCompleted 写入一次性迁移完成状态
func MarkLegacyMigrationCompleted(instanceRoot string, migrated bool, reason string) error {
	path := LegacyMigrationStatePath(instanceRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	state := legacyMigrationState{
		Status:    "done",
		Migrated:  migrated,
		Reason:    reason,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
