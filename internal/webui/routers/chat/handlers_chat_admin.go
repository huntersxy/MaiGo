package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"MaiGo/internal/webui/httputil"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/learners"
	"MaiGo/internal/person_info"
	"MaiGo/internal/platform_io"
)

func writeChatFailure(w http.ResponseWriter, status int, detail string) {
	httputil.WriteJSON(w, status, map[string]any{"detail": detail})
}

// ---------------------------------------------------------------------------
// 历史与基础信息
// ---------------------------------------------------------------------------

// handleChatHistory 对应 GET /api/chat/history
// 对齐 Python src/webui/routers/chat/routes.py:1306-1319：从数据库读取，不再只读内存快照
func (d Deps) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	limit, groupID, ok := d.chatHistoryQuery(w, r)
	if !ok {
		return
	}
	messages := d.loadChatHistory(r.Context(), limit, groupID)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "messages": messages, "total": len(messages)})
}

func (d Deps) handleChatClearHistory(w http.ResponseWriter, r *http.Request) {
	groupID := strings.TrimSpace(r.URL.Query().Get("group_id"))
	// 与 loadChatHistory 同口径：内聊主人落库身份是 OwnerNumericID
	platform, targetID, chatType := chatHistoryQueryTarget(groupID)
	if d.Memory == nil {
		writeChatFailure(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	sessions, err := chatsession.NewManager(d.Memory.GetDB()).ResolveSessionsByTarget(r.Context(), platform, targetID, chatType)
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "解析聊天流失败: "+err.Error())
		return
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, record := range sessions {
		if record.SessionID != "" {
			sessionIDs = append(sessionIDs, record.SessionID)
		}
	}
	var deleted int64
	if len(sessionIDs) > 0 {
		result := d.Memory.GetDB().WithContext(r.Context()).Where("session_id IN ?", sessionIDs).Delete(&memory.MessageLog{})
		if result.Error != nil {
			writeChatFailure(w, http.StatusInternalServerError, "清空聊天记录失败: "+result.Error.Error())
			return
		}
		deleted = result.RowsAffected
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "message": fmt.Sprintf("已清空 %d 条聊天记录", deleted)})
}

func (d Deps) handleChatPlatforms(w http.ResponseWriter, r *http.Request) {
	if d.Memory == nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "error": "记忆管理器未初始化", "platforms": []any{}})
		return
	}
	type platformRow struct {
		Platform string
		Count    int
	}
	var rows []platformRow
	err := d.Memory.GetDB().WithContext(r.Context()).Model(&personinfo.PersonInfo{}).
		Select("platform, count(*) AS count").Group("platform").Order("count DESC").Scan(&rows).Error
	if err != nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error(), "platforms": []any{}})
		return
	}
	platforms := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Platform) == "" {
			continue
		}
		platforms = append(platforms, map[string]any{"platform": row.Platform, "count": row.Count})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "platforms": platforms})
}

func (d Deps) handleChatPersons(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	if platform == "" {
		writeChatFailure(w, http.StatusUnprocessableEntity, "platform 为必填参数")
		return
	}
	if d.Memory == nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "error": "记忆管理器未初始化", "persons": []any{}})
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	limit := queryInt(r, "limit", 50, 1, 200)
	query := d.Memory.GetDB().WithContext(r.Context()).Model(&personinfo.PersonInfo{}).Where("platform = ?", platform)
	if search != "" {
		pattern := "%" + search + "%"
		query = query.Where("person_name LIKE ? OR user_nickname LIKE ? OR user_id LIKE ?", pattern, pattern, pattern)
	}
	var rows []personinfo.PersonInfo
	if err := query.Order("CASE WHEN last_known_time IS NULL THEN 1 ELSE 0 END").
		Order("last_known_time DESC").Limit(limit).Find(&rows).Error; err != nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error(), "persons": []any{}})
		return
	}
	persons := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		displayName := row.PersonName
		if displayName == "" {
			displayName = row.UserNickname
		}
		if displayName == "" {
			displayName = row.UserID
		}
		persons = append(persons, map[string]any{
			"person_id": row.PersonID, "user_id": row.UserID, "person_name": row.PersonName,
			"nickname": row.UserNickname, "is_known": row.IsKnown, "platform": row.Platform,
			"display_name": displayName,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "persons": persons, "total": len(persons)})
}

func (d Deps) handleChatInfo(w http.ResponseWriter, r *http.Request) {
	botName := ""
	if dir := strings.TrimSpace(d.InstanceDir); dir != "" {
		if cfg, err := maibotcfg.LoadFromInstanceDir(dir); err == nil {
			botName = strings.TrimSpace(cfg.Bot.Bot.Nickname)
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"bot_name": botName, "platform": internalchat.Platform, "active_sessions": 0,
	})
}

// ---------------------------------------------------------------------------
// 聊天流解析与详情
// ---------------------------------------------------------------------------

type chatResolveTarget struct {
	Platform string `json:"platform"`
	ItemID   string `json:"item_id"`
	RuleType string `json:"rule_type"`
}

func (d Deps) resolveChatTarget(ctx context.Context, platform, itemID, ruleType string) map[string]any {
	platform = strings.TrimSpace(platform)
	itemID = strings.TrimSpace(itemID)
	if ruleType != "private" {
		ruleType = "group"
	}
	result := map[string]any{"found": false, "session": nil}
	if platform == "" || itemID == "" || d.Memory == nil {
		return result
	}
	records, err := chatsession.NewManager(d.Memory.GetDB()).ResolveSessionsByTarget(ctx, platform, itemID, ruleType)
	if err != nil || len(records) == 0 {
		return result
	}
	row := d.buildChatSessionRow(ctx, &records[0], 0, 0, 0)
	return map[string]any{"found": true, "session": row.response()}
}

func (d Deps) handleChatResolveTarget(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	platform := strings.TrimSpace(query.Get("platform"))
	itemID := strings.TrimSpace(query.Get("item_id"))
	if platform == "" || itemID == "" {
		writeChatFailure(w, http.StatusUnprocessableEntity, "platform 与 item_id 为必填参数")
		return
	}
	ruleType := query.Get("rule_type")
	result := d.resolveChatTarget(r.Context(), platform, itemID, ruleType)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "found": result["found"], "session": result["session"]})
}

func (d Deps) handleChatResolveTargets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Targets []chatResolveTarget `json:"targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	if len(body.Targets) > 200 {
		body.Targets = body.Targets[:200]
	}
	results := make([]map[string]any, 0, len(body.Targets))
	for _, target := range body.Targets {
		ruleType := target.RuleType
		if ruleType != "private" {
			ruleType = "group"
		}
		results = append(results, d.resolveChatTarget(r.Context(), target.Platform, target.ItemID, ruleType))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "results": results})
}

func (d Deps) getChatSettingsSession(w http.ResponseWriter, r *http.Request) (*chatsession.Record, bool) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "session_id"))
	if sessionID == "" {
		writeChatFailure(w, http.StatusBadRequest, "缺少聊天流 session_id")
		return nil, false
	}
	if d.Memory == nil {
		writeChatFailure(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return nil, false
	}
	record, err := d.findChatSession(r.Context(), sessionID)
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取聊天流失败: "+err.Error())
		return nil, false
	}
	if record == nil {
		writeChatFailure(w, http.StatusNotFound, "聊天流不存在: "+sessionID)
		return nil, false
	}
	return record, true
}

func (d Deps) handleChatSessionDetail(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	d.writeChatSessionDetail(w, record)
}

// writeChatSessionDetail 构建详情并写响应，适配器策略读取失败时保留真实错误
func (d Deps) writeChatSessionDetail(w http.ResponseWriter, record *chatsession.Record) {
	detail, err := d.chatSessionDetailPayload(record)
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取聊天流适配器状态失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "detail": detail})
}

// ---------------------------------------------------------------------------
// 会话删除
// ---------------------------------------------------------------------------

func (d Deps) handleChatDeleteSession(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	db := d.Memory.GetDB()
	ctx := r.Context()
	items := make([]map[string]any, 0)
	totalDeleted := int64(0)

	if manager, err := platformio.GetAdapterPolicyManager(); err == nil {
		if count, err := manager.RemoveChatOverrides(chatTypeOf(record), chatTargetID(record),
			strings.TrimSpace(record.Platform), pointerString(record.AccountID), pointerString(record.Scope)); err == nil && count > 0 {
			items = append(items, map[string]any{"key": "adapter_policy_overrides", "label": "适配器显式放行规则", "count": count})
			totalDeleted += int64(count)
		}
	}

	jargonDeleted, jargonUnlinked := d.deleteOrUnlinkChatJargons(ctx, db, record.SessionID)
	if jargonDeleted > 0 || jargonUnlinked > 0 {
		items = append(items, map[string]any{"key": "jargons", "label": "黑话", "count": jargonDeleted, "unlinked": jargonUnlinked})
		totalDeleted += int64(jargonDeleted)
	}

	type deleteTarget struct {
		key, label, table, field string
		model                    any
	}
	targets := []deleteTarget{
		{"messages", "消息", "mai_messages", "session_id", &memory.MessageLog{}},
		{"expressions", "表达", "expressions", "session_id", &learners.Expression{}},
		{"tool_records", "工具调用记录", "tool_records", "session_id", nil},
		{"statistics_message_hourly", "消息统计", "statistics_message_hourly", "chat_id", nil},
		{"high_frequency_terms", "高频词", "high_frequency_terms", "chat_id", &learners.HighFrequencyTerm{}},
		{"chat_sessions", "聊天流记录", "chat_sessions", "session_id", &chatsession.Record{}},
	}
	for _, target := range targets {
		deleted := int64(0)
		if target.model != nil {
			result := db.WithContext(ctx).Where(target.field+" = ?", record.SessionID).Delete(target.model)
			if result.Error != nil {
				writeChatFailure(w, http.StatusInternalServerError, "删除聊天流关联数据失败: "+result.Error.Error())
				return
			}
			deleted = result.RowsAffected
		} else if db.Migrator().HasTable(target.table) {
			result := db.WithContext(ctx).Exec("DELETE FROM "+target.table+" WHERE "+target.field+" = ?", record.SessionID)
			if result.Error != nil {
				writeChatFailure(w, http.StatusInternalServerError, "删除聊天流关联数据失败: "+result.Error.Error())
				return
			}
			deleted = result.RowsAffected
		}
		totalDeleted += deleted
		items = append(items, map[string]any{"key": target.key, "label": target.label, "count": deleted})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "session_id": record.SessionID, "deleted_total": totalDeleted,
		"jargons": map[string]any{"deleted": jargonDeleted, "unlinked": jargonUnlinked},
		"items":   items,
	})
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (d Deps) deleteOrUnlinkChatJargons(ctx context.Context, db *gorm.DB, sessionID string) (int, int) {
	var candidates []learners.Jargon
	like := "%\"" + sessionID + "\"%"
	if err := db.WithContext(ctx).Where("session_id_dict LIKE ?", like).Find(&candidates).Error; err != nil {
		return 0, 0
	}
	deleted, unlinked := 0, 0
	for index := range candidates {
		jargon := &candidates[index]
		var counts map[string]any
		if err := json.Unmarshal([]byte(jargon.SessionIDDict), &counts); err != nil || counts == nil {
			continue
		}
		removed, exists := counts[sessionID]
		if !exists {
			continue
		}
		delete(counts, sessionID)
		if len(counts) == 0 && !jargon.IsGlobal {
			if err := db.WithContext(ctx).Delete(jargon).Error; err == nil {
				deleted++
			}
			continue
		}
		encoded, err := json.Marshal(counts)
		if err != nil {
			continue
		}
		jargon.SessionIDDict = string(encoded)
		if refs := intValue(removed, 0); refs > 0 && jargon.Count > refs {
			jargon.Count -= refs
		} else if refs > 0 {
			jargon.Count = 0
		}
		if err := db.WithContext(ctx).Save(jargon).Error; err == nil {
			unlinked++
		}
	}
	return deleted, unlinked
}

// ---------------------------------------------------------------------------
// 适配器策略
// ---------------------------------------------------------------------------

func (d Deps) handleChatAdapterStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	if d.PlatformAdapters == nil {
		writeChatFailure(w, http.StatusServiceUnavailable, "平台适配器未接线")
		return
	}
	seen := map[string]bool{}
	normalized := make([]string, 0, len(body.SessionIDs))
	for _, value := range body.SessionIDs {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
		if len(normalized) == 500 {
			break
		}
	}
	statuses := map[string]chatAdapterAllowance{}
	if len(normalized) == 0 {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "statuses": statuses})
		return
	}
	if d.Memory == nil {
		writeChatFailure(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	var records []chatsession.Record
	if err := d.Memory.GetDB().WithContext(r.Context()).Where("session_id IN ?", normalized).Find(&records).Error; err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取聊天流失败: "+err.Error())
		return
	}
	for index := range records {
		record := &records[index]
		allowance, err := d.chatAdapterAllowance(record)
		if err != nil {
			writeChatFailure(w, http.StatusInternalServerError, "读取适配器放行状态失败: "+err.Error())
			return
		}
		statuses[record.SessionID] = allowance
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "statuses": statuses})
}

func adapterIdentityJSON(identity platformio.AdapterIdentity) map[string]any {
	return map[string]any{
		"adapter_id": identity.AdapterID, "plugin_id": identity.PluginID, "gateway_name": identity.GatewayName,
		"platform": identity.Platform, "account_id": identity.AccountID, "scope": identity.Scope,
	}
}

func (d Deps) handleChatAdapterPolicyDefaultsGet(w http.ResponseWriter, r *http.Request) {
	manager, err := platformio.GetAdapterPolicyManager()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	defaults, err := manager.GetDefaultActions()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	out := map[string]string{"group": "allow", "private": "allow"}
	for chatType, action := range defaults {
		out[string(chatType)] = action
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "defaults": out})
}

func (d Deps) handleChatAdapterPolicyDefaultsUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Group   string `json:"group"`
		Private string `json:"private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	manager, err := platformio.GetAdapterPolicyManager()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	if err := manager.SetDefaultAction(string(platformio.ChatTypeGroup), body.Group); err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := manager.SetDefaultAction(string(platformio.ChatTypePrivate), body.Private); err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	defaults, err := manager.GetDefaultActions()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	out := map[string]string{"group": "allow", "private": "allow"}
	for chatType, action := range defaults {
		out[string(chatType)] = action
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "defaults": out})
}

func (d Deps) handleChatAdapterPluginPolicyGet(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	if pluginID == "" {
		writeChatFailure(w, http.StatusBadRequest, "缺少适配器插件 ID")
		return
	}
	manager, err := d.adapterPolicyManager()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	// 优先用运行中驱动的完整身份；未运行时退回 plugin_id 单键做只读展示
	running, ok := d.pluginAdapterIdentity(pluginID)
	identity := platformio.AdapterIdentity{PluginID: pluginID}
	var active *platformio.AdapterIdentity
	if ok {
		identity = running
		active = &running
	}
	policy, err := manager.GetAdapterPolicy(identity)
	if err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	defaults, _ := manager.GetDefaultActions()
	defaultMap := map[string]string{"group": "allow", "private": "allow"}
	for chatType, action := range defaults {
		defaultMap[string(chatType)] = action
	}
	payload := map[string]any{
		"success": true, "plugin_id": pluginID, "global_defaults": defaultMap, "policy": policy,
	}
	for key, value := range adapterAccountStatus(manager, pluginID, identity, active) {
		payload[key] = value
	}
	httputil.WriteJSON(w, http.StatusOK, payload)
}

func (d Deps) handleChatAdapterPluginPolicyUpdate(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	if pluginID == "" {
		writeChatFailure(w, http.StatusBadRequest, "缺少适配器插件 ID")
		return
	}
	var body struct {
		Group        platformio.ChatPolicyEditor     `json:"group"`
		Private      platformio.ChatPolicyEditor     `json:"private"`
		PolicyGroups *[]platformio.PolicyGroupEditor `json:"policy_groups"`
		ActiveGroup  *string                         `json:"active_group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	identity, ok := d.pluginAdapterIdentity(pluginID)
	if !ok {
		writeChatFailure(w, http.StatusNotFound, "适配器当前未运行，无法确定规则归属，请先启动适配器")
		return
	}
	manager, err := d.adapterPolicyManager()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	var policyGroups []platformio.PolicyGroupEditor
	if body.PolicyGroups != nil {
		policyGroups = *body.PolicyGroups
	} else {
		policyGroups = nil
	}
	activeGroup := ""
	if body.ActiveGroup != nil {
		activeGroup = *body.ActiveGroup
	} else if body.PolicyGroups == nil {
		// 未提供分组时保持空串，避免触发“切换分组必须带 policy_groups”
		activeGroup = ""
	}
	if err := manager.SetAdapterPolicy(
		identity,
		map[platformio.ChatType]platformio.ChatPolicyEditor{
			platformio.ChatTypeGroup:   body.Group,
			platformio.ChatTypePrivate: body.Private,
		},
		policyGroups,
		activeGroup,
	); err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	policy, err := manager.GetAdapterPolicy(identity)
	if err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	defaults, _ := manager.GetDefaultActions()
	defaultMap := map[string]string{"group": "allow", "private": "allow"}
	for chatType, action := range defaults {
		defaultMap[string(chatType)] = action
	}
	payload := map[string]any{
		"success": true, "plugin_id": pluginID, "global_defaults": defaultMap, "policy": policy,
	}
	for key, value := range adapterAccountStatus(manager, pluginID, identity, &identity) {
		payload[key] = value
	}
	httputil.WriteJSON(w, http.StatusOK, payload)
}

func (d Deps) handleChatSessionAdapterPolicy(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	if chatTargetID(record) == "" {
		writeChatFailure(w, http.StatusBadRequest, "聊天流缺少目标 ID，无法保存适配器策略")
		return
	}
	var body struct {
		AdapterID string `json:"adapter_id"`
		Action    string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	identity, detail, status := d.runningAdapterIdentity(body.AdapterID)
	if status != 0 {
		writeChatFailure(w, status, detail)
		return
	}
	if strings.ToLower(strings.TrimSpace(identity.Platform)) != strings.ToLower(strings.TrimSpace(record.Platform)) {
		writeChatFailure(w, http.StatusBadRequest, "适配器与聊天流的平台不匹配，无法保存策略")
		return
	}
	manager, err := d.adapterPolicyManager()
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "读取适配器策略失败: "+err.Error())
		return
	}
	if err := manager.SetChatOverride(identity, chatTypeOf(record), chatTargetID(record), body.Action); err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	d.writeChatSessionDetail(w, record)
}
