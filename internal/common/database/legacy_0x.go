package memory

import "gorm.io/gorm"

// legacyExclusiveTables 只存在于 0.x 结构的表
var legacyExclusiveTables = []string{
	"chat_streams", "emoji", "emoji_description_cache", "expression",
	"group_info", "image_descriptions", "jargon", "messages", "thinking_back",
}

// legacySharedMarkers 0.x 与 1.x 共用表名、靠独有列区分的标记
var legacySharedMarkers = map[string][]string{
	"action_records": {"chat_id", "time"},
	"chat_history":   {"chat_id", "original_text"},
	"images":         {"emoji_hash", "path", "type"},
	"llm_usage":      {"model_api_provider", "status"},
	"online_time":    {"duration"},
	"person_info":    {"nickname", "group_nick_name"},
}

// DetectLegacy0XDatabase 判定已连接的 SQLite 库是否为 0.x 结构
//
// 对齐 MaiBot config/legacy_upgrade_confirmation.py 的 _detect_legacy_0x_database：
// user_version=1 判定为旧库；更高的版本号或空库直接排除；否则按专属表与共享表标记识别。
func DetectLegacy0XDatabase(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	var userVersion int
	row := db.Raw("PRAGMA user_version").Row()
	if row == nil {
		return false
	}
	if err := row.Scan(&userVersion); err != nil {
		return false
	}
	if userVersion == 1 {
		return true
	}
	if userVersion > 1 {
		return false
	}

	var tableNames []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").
		Pluck("name", &tableNames).Error; err != nil || len(tableNames) == 0 {
		return false
	}
	present := make(map[string]struct{}, len(tableNames))
	for _, name := range tableNames {
		present[name] = struct{}{}
	}
	for _, name := range legacyExclusiveTables {
		if _, exists := present[name]; exists {
			return true
		}
	}

	for name, required := range legacySharedMarkers {
		if _, exists := present[name]; !exists {
			continue
		}
		var columns []string
		if err := db.Raw("SELECT name FROM pragma_table_info(?)", name).Pluck("name", &columns).Error; err != nil {
			continue
		}
		columnSet := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			columnSet[column] = struct{}{}
		}
		allPresent := true
		for _, column := range required {
			if _, exists := columnSet[column]; !exists {
				allPresent = false
				break
			}
		}
		if allPresent {
			return true
		}
	}
	return false
}
