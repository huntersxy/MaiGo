package qq_official

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type idMapping struct {
	Group map[string]string `json:"group"`
	User  map[string]string `json:"user"`
}

func loadIDMapping(directory string) (idMapping, error) {
	data, err := os.ReadFile(filepath.Join(directory, "id_map.json"))
	if errors.Is(err, os.ErrNotExist) {
		return idMapping{Group: map[string]string{}, User: map[string]string{}}, nil
	}
	if err != nil {
		return idMapping{}, err
	}
	var mapping idMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return idMapping{}, fmt.Errorf("QQ 统一 ID 映射文件格式无效: %w", err)
	}
	for _, entries := range []map[string]string{mapping.Group, mapping.User} {
		if entries == nil {
			return idMapping{}, fmt.Errorf("QQ 统一 ID 映射须包含 group 和 user 对象")
		}
		for key, value := range entries {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return idMapping{}, fmt.Errorf("QQ 统一 ID 映射须包含非空字符串键值")
			}
		}
	}
	return mapping, nil
}

func (m idMapping) save(directory string) error {
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return saveIdentityFile(filepath.Join(directory, "id_map.json"), encoded)
}

func saveIdentityFile(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".identity-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(encoded)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (a *Adapter) registerAPIs(binder LocalAPIBinder) {
	register := func(name, description string, handler func(context.Context, string, string, map[string]any) (any, error)) {
		binder.SetAPILocalHandler(name, PluginID, map[string]any{
			"version": "1", "public": true, "enabled": true, "description": description,
		}, handler)
	}
	register("adapter.avatar.get", "获取独立用户或群头像资源", a.avatar)
	register("webui_group_mappings", "查看已有的 QQ 群 OpenID 与群号映射", a.webuiGroupMappings)
	register("webui_mappings", "查看 QQ 群和用户的 OpenID 绑定", a.webuiMappings)
	register("webui_mapping_avatar", "获取已绑定群或用户的头像", a.webuiMappingAvatar)
	register("webui_identity_mode", "查询当前用户身份模式", a.webuiIdentityMode)
	register("webui_set_mapping_mode", "切换 QQ 号映射模式", a.webuiSetMappingMode)
	register("webui_mapping_migrations", "查询 QQ 号迁回 OpenID 的进度", a.webuiMappingMigrations)
	register("webui_finish_mapping", "完成迁移并关闭 QQ 号映射", a.webuiFinishMapping)
	register("webui_delete_bindings", "删除群或用户绑定及相关静默关联", a.webuiDeleteBindings)
}

type mappingPair struct{ openID, id string }

func sortedMapping(mapping map[string]string) []mappingPair {
	items := make([]mappingPair, 0, len(mapping))
	for openID, id := range mapping {
		items = append(items, mappingPair{openID, id})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].id < items[j].id || (items[i].id == items[j].id && items[i].openID < items[j].openID)
	})
	return items
}

func webUIList[Item any](items []Item) []any {
	result := make([]any, len(items))
	for index, item := range items {
		result[index] = item
	}
	return result
}

func (a *Adapter) webuiGroupMappings(context.Context, string, string, map[string]any) (any, error) {
	a.mu.Lock()
	mapping := a.mapping.Group
	a.mu.Unlock()
	groups := make([]any, 0, len(mapping))
	for _, row := range sortedMapping(mapping) {
		groups = append(groups, map[string]any{"group_openid": row.openID, "group_id": row.id})
	}
	return map[string]any{"total": len(groups), "groups": groups}, nil
}

func (a *Adapter) webuiMappings(context.Context, string, string, map[string]any) (any, error) {
	a.mu.Lock()
	mapping := a.mapping
	union := a.union
	a.mu.Unlock()
	rows := func(kind string, entries map[string]string) []map[string]any {
		out := make([]map[string]any, 0, len(entries))
		for _, row := range sortedMapping(entries) {
			group := ""
			if kind == "group" {
				group = row.id
			}
			out = append(out, map[string]any{
				"openid": row.openID, "id": row.id, "source": "manual", "nickname": "", "cardname": "",
				"group_id": group, "group_openid": "", "conflict": false, "status": "已绑定",
			})
		}
		return out
	}
	users := rows("user", mapping.User)
	for groupOpenID, group := range mapping.Group {
		for openID, profile := range a.profileGroup(mapping, groupOpenID) {
			status := "已确认"
			conflict := profile.Conflict
			manual := mapping.User[openID]
			switch {
			case manual != "" && manual != profile.UserID:
				conflict, status = true, "与手动绑定冲突，手动生效"
			case conflict:
				status = "冲突，自动绑定已暂停"
			case !a.settings.QQOfficial.SilentUserSync:
				status = "自动绑定已停用"
			case manual != "":
				status = "与手动绑定一致"
			}
			users = append(users, map[string]any{
				"openid": openID, "id": profile.UserID, "source": "auto", "nickname": profile.Nickname,
				"cardname": profile.Cardname, "group_id": group, "group_openid": groupOpenID, "conflict": conflict, "status": status,
			})
		}
	}
	sort.Slice(users, func(i, j int) bool {
		for _, field := range []string{"id", "source", "group_id", "openid"} {
			a, b := asString(users[i][field]), asString(users[j][field])
			if a != b {
				return a < b
			}
		}
		return false
	})
	app := strings.TrimSpace(a.settings.QQOfficial.AppID)
	represented := map[[2]string]bool{}
	for _, user := range users {
		targets := map[string]bool{}
		for _, row := range union.Bindings {
			if row.AppID == app && row.OpenID == user["openid"] && (user["group_openid"] == "" || row.GroupOpenID == user["group_openid"]) {
				targets[row.UnionOpenID] = true
			}
		}
		user["union_openid"] = ""
		if len(targets) == 1 {
			for target := range targets {
				user["union_openid"] = target
				represented[[2]string{asString(user["openid"]), target}] = true
			}
		}
	}
	for _, row := range union.Bindings {
		if row.AppID == app && !represented[[2]string{row.OpenID, row.UnionOpenID}] {
			users = append(users, map[string]any{
				"openid": row.OpenID, "id": firstNonEmpty(row.QQID, "—"), "source": "union", "union_openid": row.UnionOpenID,
				"nickname": "", "cardname": "", "group_id": mapping.Group[row.GroupOpenID], "group_openid": row.GroupOpenID,
				"conflict": false, "status": "官方身份关联",
			})
		}
	}
	return map[string]any{"group": webUIList(rows("group", mapping.Group)), "user": webUIList(users), "mapping_enabled": a.mappingEnabled.Load()}, nil
}

func (a *Adapter) webuiMappingAvatar(ctx context.Context, _, _ string, args map[string]any) (any, error) {
	kind, _ := args["kind"].(string)
	source := "manual"
	if value, ok := args["source"]; ok {
		source, _ = value.(string)
	}
	if (kind != "group" && kind != "user") || (source != "manual" && source != "auto" && source != "union") || (kind == "group" && source != "manual") {
		return nil, fmt.Errorf("映射类型或来源无效")
	}
	openID, _ := args["openid"].(string)
	a.mu.Lock()
	mapping := a.mapping
	union := a.union
	a.mu.Unlock()
	var target string
	switch {
	case source == "manual" && kind == "group":
		target = mapping.Group[openID]
	case source == "manual" && kind == "user":
		target = mapping.User[openID]
	case source == "auto" && kind == "user":
		group, _ := args["group_openid"].(string)
		target = a.profileGroup(mapping, group)[openID].UserID
	case source == "union" && kind == "user":
		group, _ := args["group_openid"].(string)
		app := strings.TrimSpace(a.settings.QQOfficial.AppID)
		for _, row := range union.Bindings {
			if row.AppID == app && row.GroupOpenID == group && row.OpenID == openID {
				target = row.UnionOpenID
				break
			}
		}
	}
	if target == "" {
		return nil, fmt.Errorf("群或用户映射不存在")
	}
	result, err := a.avatar(ctx, "", "", map[string]any{"platform": PlatformQQ, "target_id": target, "target_type": kind})
	if err != nil {
		return nil, err
	}
	resource := result.(map[string]any)
	if resource["status"] != "available" {
		return map[string]any{"image": "", "message": "该群或用户无法获取 QQ 头像"}, nil
	}
	address := strings.NewReplacer("/640", "/100", "s=640", "s=100").Replace(asString(resource["url"]))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("QQ 头像请求失败: HTTP %d", response.StatusCode)
	}
	mimeType, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";")
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return nil, fmt.Errorf("QQ 头像返回了不支持的图片格式")
	}
	const limit = 256 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("QQ 头像超过 256 KiB")
	}
	return map[string]any{"image": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)}, nil
}

func (a *Adapter) AssignIdentity(ctx context.Context, kind string, message map[string]any) (bool, string, error) {
	info, _ := asMap(message["message_info"])
	config, _ := asMap(info["additional_config"])
	sender, group := asString(config["qq_official_user_openid"]), asString(config["qq_official_group_openid"])
	if message["platform"] != PlatformQQ || sender == "" || message["account_id"] != a.runtime.RouteAccountID() {
		return false, "绑定命令仅支持当前 QQ 官方连接收到的消息", nil
	}
	segments, _ := message["raw_message"].([]map[string]any)
	var text []string
	mentioned := map[string]struct{}{}
	for _, segment := range segments {
		switch segment["type"] {
		case "text":
			text = append(text, asString(segment["data"]))
		case "at":
			data, _ := asMap(segment["data"])
			target := asString(data["target_user_id"])
			if target != "" && target != a.runtime.AccountID() && target != a.runtime.RouteAccountID() {
				mentioned[target] = struct{}{}
			}
		}
	}
	fields := strings.Fields(strings.Join(text, " "))
	value := ""
	if len(fields) == 2 {
		value = fields[1]
	}
	openID := sender
	var problem string
	if kind == "group" {
		openID = group
		if group == "" {
			problem = "/assign_group_id 仅支持群聊使用"
		} else if !isDecimal(value) {
			problem = "用法：/assign_group_id <群号>"
		}
	} else if !isDecimal(value) {
		problem = "用法：/assign_id <QQ号>（可 @ 一位用户为其绑定）"
	} else if len(mentioned) > 1 {
		problem = "一次只能绑定一位用户"
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	client, mapping := a.client, a.mapping
	a.mu.Unlock()
	if client == nil {
		return false, "", fmt.Errorf("QQ 官方消息网关未就绪")
	}
	if kind == "user" && problem == "" {
		effective, _ := a.groupUserMapping(mapping, group)
		for target := range mentioned {
			matched := ""
			for candidate, id := range effective.User {
				if id == target {
					if matched != "" {
						problem = "目标用户的身份映射不唯一，无法绑定"
						break
					}
					matched = candidate
				}
			}
			if matched == "" && isDecimal(target) {
				problem = "目标用户缺少 QQ 官方 OpenID，无法绑定"
			}
			openID = firstNonEmpty(matched, target)
		}
	}
	if problem == "" {
		if kind == "group" {
			mapping.Group = maps.Clone(mapping.Group)
			mapping.Group[openID] = value
		} else {
			mapping.User = maps.Clone(mapping.User)
			mapping.User[openID] = value
		}
		if err := mapping.save(resolveDataDir(a.hostHooks)); err != nil {
			return false, "", err
		}
		a.mu.Lock()
		a.mapping = mapping
		a.mu.Unlock()
	}
	result := problem
	if result == "" {
		if kind == "group" {
			result = "已绑定当前群的群号 " + value + "。启用映射时，后续消息归入该群号的会话。"
		} else {
			result = "已保留目标用户的 QQ 号关联 " + value + "；是否映射为 QQ 号由启用映射开关控制。" +
				"需要合并旧用户数据时，可在插件页面使用用户数据灌注功能。"
		}
	}
	scene, rawTarget := "private", sender
	if group != "" {
		scene, rawTarget = "group", group
	}
	key := scopedMessage{messageScope{account: a.runtime.AccountID(), scene: scene, target: rawTarget}, asString(message["message_id"])}
	passive := a.references.claimPassive(key)
	if passive == nil {
		return false, result, fmt.Errorf("绑定命令的回复凭证已过期或耗尽，无法发送回执")
	}
	if _, err := client.SendText(ctx, scene, rawTarget, result, passive); err != nil {
		return false, result, err
	}
	return problem == "", result, nil
}

func (m idMapping) resolveGroup(group, explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit, nil
	}
	var resolved string
	for openID, id := range m.Group {
		if id == group {
			if resolved != "" {
				return "", fmt.Errorf("群 %s 对应多个 OpenID", group)
			}
			resolved = openID
		}
	}
	if resolved != "" {
		return resolved, nil
	}
	if group == "" || isDecimal(group) {
		return "", fmt.Errorf("群 %s 缺少 OpenID", group)
	}
	return group, nil
}
