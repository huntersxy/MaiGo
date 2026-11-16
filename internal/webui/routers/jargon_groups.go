package webuiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/learners"
	botservices "MaiGo/internal/services"
)

// loadJargonGroupsForWebUI 只读取 bot_config 的黑话共享组，避免依赖完整配置校验
func loadJargonGroupsForWebUI(instanceDir string) ([]maibotcfg.ChatStreamGroup, error) {
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
		Jargon struct {
			JargonGroups []maibotcfg.ChatStreamGroup `toml:"jargon_groups"`
		} `toml:"jargon"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 bot_config.toml: %w", err)
	}
	return doc.Jargon.JargonGroups, nil
}

// jargonTargetMatches 对齐 ChatConfigUtils.target_matches_session 的规则类型判定
func jargonTargetMatches(chat learners.JargonChat, target maibotcfg.TargetItem) bool {
	platform := strings.TrimSpace(target.Platform)
	item := strings.TrimSpace(target.ItemId)
	if platform == "" || item == "" {
		return false
	}
	if platform != "*" && platform != chat.Platform {
		return false
	}
	if item != "*" && item != chat.TargetID {
		return false
	}
	switch strings.TrimSpace(target.RuleType) {
	case "group":
		return chat.IsGroup
	case "private":
		return !chat.IsGroup
	}
	// 非通配目标未声明 rule_type 时按聊天类型两侧都接受
	return platform != "*" && item != "*"
}

// jargonTargetSessionIDs 对齐 get_target_session_ids_with_wildcards：通配必须声明 group/private
func jargonTargetSessionIDs(chats map[string]learners.JargonChat, target maibotcfg.TargetItem) []string {
	platform := strings.TrimSpace(target.Platform)
	item := strings.TrimSpace(target.ItemId)
	if platform == "" || item == "" {
		return nil
	}
	if platform != "*" && item != "*" {
		ids := make([]string, 0, len(chats))
		for id, chat := range chats {
			if jargonTargetMatches(chat, target) {
				ids = append(ids, id)
			}
		}
		return ids
	}
	switch strings.TrimSpace(target.RuleType) {
	case "group", "private":
	default:
		return nil
	}
	ids := make([]string, 0, len(chats))
	for id, chat := range chats {
		if jargonTargetMatches(chat, target) {
			ids = append(ids, id)
		}
	}
	return ids
}

// handleJargonGroups 对齐 GET /api/webui/jargon/groups
func (d Deps) handleJargonGroups(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/groups"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	chats, err := store.JargonChats(r.Context(), true)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取黑话共享组失败: "+err.Error())
		return
	}
	chatByID := make(map[string]learners.JargonChat, len(chats))
	for _, chat := range chats {
		chatByID[chat.SessionID] = chat
	}
	groups := botservices.ListSharingGroups(r.Context())
	data := make([]map[string]any, 0, len(groups))
	for index, group := range groups {
		if !group.Enabled || !group.Capabilities["jargon"] {
			continue
		}
		isGlobal := botservices.SharingGroupIsGlobal(group)
		ids := map[string]struct{}{}
		for id := range botservices.SharingResolvedMembers(r.Context(), group) {
			ids[id] = struct{}{}
		}
		resolved := make([]string, 0, len(ids))
		for id := range ids {
			resolved = append(resolved, id)
		}
		sort.Strings(resolved)
		members := make([]map[string]any, 0, len(resolved))
		for _, id := range resolved {
			if chat, exists := chatByID[id]; exists {
				members = append(members, jargonChatToWebUI(chat))
			}
		}
		data = append(data, map[string]any{
			"id":           group.ID,
			"color":        group.Color,
			"index":        index,
			"name":         group.Name,
			"session_ids":  resolved,
			"members":      members,
			"jargon_count": jargonGroupCount(r.Context(), d, resolved, isGlobal),
			"is_global":    isGlobal,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data})
}

// jargonGroupCount 统计组范围内命中的黑话条数，全局组统计全部记录
func jargonGroupCount(ctx context.Context, d Deps, sessionIDs []string, isGlobal bool) int {
	if d.Memory == nil || d.Memory.GetDB() == nil {
		return 0
	}
	var dicts []string
	if err := d.Memory.GetDB().WithContext(ctx).Model(&learners.Jargon{}).Pluck("session_id_dict", &dicts).Error; err != nil {
		return 0
	}
	if isGlobal {
		return len(dicts)
	}
	scope := make(map[string]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		scope[id] = struct{}{}
	}
	count := 0
	for _, dict := range dicts {
		var mapping map[string]any
		if err := json.Unmarshal([]byte(dict), &mapping); err != nil {
			continue
		}
		for id := range mapping {
			if _, ok := scope[id]; ok {
				count++
				break
			}
		}
	}
	return count
}
