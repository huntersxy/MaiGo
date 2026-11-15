package webuiapi

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/learners"
)

// loadExpressionGroupsForWebUI 只读取 bot_config 的表达共享组，避免依赖完整配置校验
func loadExpressionGroupsForWebUI(instanceDir string) ([]maibotcfg.ChatStreamGroup, error) {
	instanceDir = strings.TrimSpace(instanceDir)
	if instanceDir == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(instanceDir, "config", "bot_config.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Expression struct {
			ExpressionGroups []maibotcfg.ChatStreamGroup `toml:"expression_groups"`
		} `toml:"expression"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 bot_config.toml: %w", err)
	}
	return doc.Expression.ExpressionGroups, nil
}

// expressionVectorSettings 读取表达向量配置；未注入学习配置时回落到实例 bot_config.toml
func (d Deps) expressionVectorSettings() learners.LearningSettings {
	return learners.NormalizeLearningSettings(learners.DefaultRegistry().LearningSettings())
}

// resolveExpressionVectorIndexPath 解析表达向量索引路径，相对路径以实例根目录为准
func (d Deps) resolveExpressionVectorIndexPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	if strings.TrimSpace(d.InstanceDir) == "" {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(d.InstanceDir, raw))
}

// loadExpressionVectorPayload 读取索引 JSON；文件不存在返回 exists=false
func (d Deps) loadExpressionVectorPayload(rawPath string) (string, map[string]any, bool, error) {
	path := d.resolveExpressionVectorIndexPath(rawPath)
	if path == "" {
		return "", nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil, false, nil
		}
		return path, nil, false, err
	}
	var payload map[string]any
	if err := sonic.Unmarshal(raw, &payload); err != nil {
		return path, nil, true, fmt.Errorf("表达向量索引格式异常: %s", path)
	}
	return path, payload, true, nil
}

func expressionAnyText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	}
	return ""
}

// expressionChatMap 拉取表达可见聊天流并建立 session_id 索引
func (d Deps) expressionChatMap(r *http.Request, store learners.WebUIStore, includeLegacy bool) map[string]learners.ChatInfo {
	out := map[string]learners.ChatInfo{}
	if store == nil {
		return out
	}
	chats, err := store.ExpressionChats(r.Context(), includeLegacy)
	if err != nil {
		return out
	}
	for _, chat := range chats {
		out[chat.SessionID] = chat
	}
	return out
}

func expressionChatInfoPayload(chat learners.ChatInfo) map[string]any {
	var expressionCount any
	if chat.ExpressionCount != nil {
		expressionCount = *chat.ExpressionCount
	}
	return map[string]any{
		"chat_id":          chat.SessionID,
		"chat_name":        chat.ChatName,
		"platform":         nullableString(chat.Platform),
		"account_id":       nullableString(chat.AccountID),
		"target_id":        nullableString(chat.TargetID),
		"is_group":         chat.IsGroup,
		"is_legacy":        chat.IsLegacy,
		"is_other_account": chat.IsOtherAccount,
		"use_expression":   chat.UseExpression,
		"enable_learning":  chat.EnableLearning,
		"expression_count": expressionCount,
	}
}

func expressionGroupTargetMatches(chat learners.ChatInfo, targetPlatform, targetItem, ruleType string) bool {
	platform := strings.TrimSpace(targetPlatform)
	item := strings.TrimSpace(targetItem)
	if platform == "" || item == "" {
		return false
	}
	if platform != "*" && platform != chat.Platform {
		return false
	}
	if item != "*" && item != chat.TargetID {
		return false
	}
	switch strings.TrimSpace(ruleType) {
	case "group":
		return chat.IsGroup
	case "private":
		return !chat.IsGroup
	}
	return platform != "*" && item != "*"
}

// expressionTargetSessionIDs 对齐 get_target_session_ids_with_wildcards：通配必须声明 group/private
func expressionTargetSessionIDs(chatByID map[string]learners.ChatInfo, target maibotcfg.TargetItem) []string {
	platform := strings.TrimSpace(target.Platform)
	item := strings.TrimSpace(target.ItemId)
	if platform == "" || item == "" {
		return nil
	}
	if platform == "*" || item == "*" {
		switch strings.TrimSpace(target.RuleType) {
		case "group", "private":
		default:
			return nil
		}
	}
	ids := make([]string, 0, len(chatByID))
	for id, chat := range chatByID {
		if expressionGroupTargetMatches(chat, platform, item, target.RuleType) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (d Deps) handleExpressionGroups(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/groups"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	includeLegacy, present, boolErr := optionalQueryBool(r, "include_legacy")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		includeLegacy = false
	}
	chats, err := store.ExpressionChats(r.Context(), includeLegacy)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达共享组失败: "+err.Error())
		return
	}
	chatByID := make(map[string]learners.ChatInfo, len(chats))
	for _, chat := range chats {
		chatByID[chat.SessionID] = chat
	}
	expressionGroups, err := loadExpressionGroupsForWebUI(d.InstanceDir)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达共享组失败: "+err.Error())
		return
	}
	groups := make([]map[string]any, 0)
	for index, group := range expressionGroups {
		ids := map[string]struct{}{}
		// Python 语义：空 targets 表示全量共享；显式 platform/item 通配才额外标记 global
		isGlobal := len(group.Targets) == 0
		for _, target := range group.Targets {
			platform := strings.TrimSpace(target.Platform)
			item := strings.TrimSpace(target.ItemId)
			if platform == "" || item == "" {
				continue
			}
			if platform == "*" && item == "*" {
				isGlobal = true
			}
			for _, id := range expressionTargetSessionIDs(chatByID, target) {
				ids[id] = struct{}{}
			}
		}
		if isGlobal {
			for id := range chatByID {
				ids[id] = struct{}{}
			}
		}
		resolved := make([]string, 0, len(ids))
		for id := range ids {
			resolved = append(resolved, id)
		}
		sort.Strings(resolved)
		members := make([]map[string]any, 0, len(resolved))
		for _, id := range resolved {
			members = append(members, expressionChatInfoPayload(chatByID[id]))
		}
		groups = append(groups, map[string]any{
			"index":     index,
			"name":      fmt.Sprintf("共享组 %d", index+1),
			"chat_ids":  resolved,
			"members":   members,
			"is_global": isGlobal,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": groups})
}

func (d Deps) handleExpressionVectorBuildProgress(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	settings := d.expressionVectorSettings()
	if !settings.UseVectorExpression {
		writeJSON(w, http.StatusOK, map[string]any{"status": "disabled", "completed": 0, "total": 0, "percent": 0.0})
		return
	}
	if strings.TrimSpace(settings.ExpressionVectorIndexPath) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unconfigured", "completed": 0, "total": 0, "percent": 0.0})
		return
	}
	// 未配置 embedding 模型时无法判定完成度，对齐 Python 返回 unconfigured
	currentProfile, hasProfile := learners.ExpressionVectorCurrentProfile(learners.DefaultRegistry())
	if !hasProfile || strings.TrimSpace(currentProfile.Marker) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unconfigured", "completed": 0, "total": 0, "percent": 0.0})
		return
	}
	_, payload, exists, err := d.loadExpressionVectorPayload(settings.ExpressionVectorIndexPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达向量补建进度失败: "+err.Error())
		return
	}

	// 索引里的 embedding_profile 必须与当前 profile 同源，否则向量不可算作已完成
	marker := ""
	dimension := 0
	if exists {
		rawProfile, _ := payload["embedding_profile"].(map[string]any)
		if rawProfile != nil {
			storedMarker := strings.TrimSpace(anyTrimmed(rawProfile["marker"]))
			if storedMarker == strings.TrimSpace(currentProfile.Marker) {
				marker = storedMarker
				dimension = intValue(rawProfile["dimension"], 0)
			}
		}
	}

	indexedByID := map[uint]map[string]any{}
	if exists {
		for _, key := range []string{"expressions", "items"} {
			for _, item := range listValue(payload[key]) {
				raw, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if id := uint(intValue(raw["id"], 0)); id > 0 {
					indexedByID[id] = raw
				}
			}
		}
	}

	// 统计口径对齐 Python：遍历全表，跳过空 situation/style
	total := 0
	completed := 0
	store := learnerStore()
	if store != nil {
		rows, err := store.ListExpressionsForVectorProgress(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "获取表达向量补建进度失败: "+err.Error())
			return
		}
		for _, row := range rows {
			situation := strings.TrimSpace(row.Situation)
			style := strings.TrimSpace(row.Style)
			if row.ID == 0 || situation == "" || style == "" {
				continue
			}
			total++
			indexed, ok := indexedByID[row.ID]
			if !ok || marker == "" {
				continue
			}
			if strings.TrimSpace(anyTrimmed(indexed["embedding_profile_marker"])) != marker {
				continue
			}
			if intValue(indexed["embedding_dimension"], 0) != dimension {
				continue
			}
			if strings.TrimSpace(anyTrimmed(indexed["fingerprint"])) !=
				learners.ExpressionFingerprint(row.ID, situation, style) {
				continue
			}
			completed++
		}
	}

	percent := 100.0
	if total > 0 {
		percent = math.Round(float64(completed)*1000/float64(total)) / 10
	}
	status := "pending"
	switch {
	case marker == "":
		status = "waiting_profile"
	case completed >= total:
		status = "completed"
	case learners.DefaultRegistry().ExpressionVectorSyncStatus().Running:
		status = "running"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"completed": completed,
		"total":     total,
		"percent":   percent,
	})
}

func (d Deps) handleExpressionVectorSpaces(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	settings := d.expressionVectorSettings()
	indexPath := d.resolveExpressionVectorIndexPath(settings.ExpressionVectorIndexPath)
	payload, err := learners.LoadExpressionVectorSpacePayload(indexPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	registry := learners.DefaultRegistry()
	options := learners.ExpressionVectorSpacesOptions{
		Enabled: settings.UseVectorExpression,
		Sync:    registry.ExpressionVectorSyncStatus(),
	}
	if options.Enabled {
		if current, ok := learners.ExpressionVectorCurrentProfile(registry); ok {
			options.Current = &current
		}
	}
	response, err := learners.BuildExpressionVectorSpaces(payload, options)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (d Deps) handleExpressionVectorSpaceDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	spaceID := strings.TrimSpace(chi.URLParam(r, "space_id"))
	if spaceID == "" {
		writeAPIError(w, http.StatusNotFound, "vector_space_not_found")
		return
	}
	settings := d.expressionVectorSettings()
	indexPath := d.resolveExpressionVectorIndexPath(settings.ExpressionVectorIndexPath)
	currentMarker := ""
	if settings.UseVectorExpression {
		if current, ok := learners.ExpressionVectorCurrentProfile(learners.DefaultRegistry()); ok {
			currentMarker = current.Marker
		}
	}
	status, err := learners.DeleteExpressionVectorSpaceFile(indexPath, spaceID, settings.UseVectorExpression, currentMarker)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch status {
	case learners.ExpressionVectorSpaceDeleted:
		reloadExpressionVectorStore()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": spaceID})
	case learners.ExpressionVectorSpaceInUse:
		writeAPIError(w, http.StatusConflict, "vector_space_in_use")
	default:
		writeAPIError(w, http.StatusNotFound, "vector_space_not_found")
	}
}

// reloadExpressionVectorStore 让内存索引在删除分组后重新载入清单，避免下一次保存写回已删除分组
func reloadExpressionVectorStore() {
	store, ok := learners.DefaultRegistry().ExpressionVectors().(*learners.PersistentExpressionVectorStore)
	if !ok {
		return
	}
	_ = store.Load()
}
