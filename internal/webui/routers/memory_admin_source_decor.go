package webuiapi

// WebUI 记忆来源与记录来源的展示装饰；对齐 memory.py 的
// _parse_memory_source / _decorate_memory_sources / _decorate_record_origins / _record_origin_label。

import (
	"context"
	"sort"
	"strings"
)

// memorySourceKindPrefixes 已知来源前缀，顺序即匹配优先级
var memorySourceKindPrefixes = []struct {
	prefix string
	kind   string
}{
	{"chat_summary:", "chat_summary"},
	{"chat_stream:", "chat_stream"},
	{"maibot.chat_history:", "chat_history"},
	{"chat_history:", "chat_history"},
	{"person_fact:", "person_fact"},
}

// parseMemorySource 把「前缀:标识」拆成 (来源类型, 标识)，无法识别返回空对
func parseMemorySource(source any) (string, string) {
	token := strings.TrimSpace(anyTrimmed(source))
	if token == "" {
		return "", ""
	}
	for _, entry := range memorySourceKindPrefixes {
		if strings.HasPrefix(token, entry.prefix) {
			return entry.kind, strings.TrimSpace(strings.TrimPrefix(token, entry.prefix))
		}
	}
	return "", ""
}

// memoryRecordOriginToken 记录用于解析来源展示名的标记
// 关系记录的 source 是证据段落哈希，取 origin_source 才有可读性
func memoryRecordOriginToken(row map[string]any) string {
	if token := strings.TrimSpace(anyTrimmed(row["origin_source"])); token != "" {
		return token
	}
	return strings.TrimSpace(anyTrimmed(row["source"]))
}

// memorySourcePersonNames 按 person_id 批量解析人物姓名
func memorySourcePersonNames(personIDs []string) map[string]string {
	return memoryPersonNames(personIDs)
}

// memorySourceChatNames 按 session_id 批量解析聊天流展示名
func (d Deps) memorySourceChatNames(ctx context.Context, chatIDs []string) map[string]string {
	names := map[string]string{}
	tokens := make([]string, 0, len(chatIDs))
	seen := map[string]struct{}{}
	for _, item := range chatIDs {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		return names
	}
	resolved, err := d.memoryChatDisplayNames(ctx, tokens)
	if err != nil {
		return names
	}
	for token, name := range resolved {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names[token] = trimmed
		}
	}
	return names
}

// decorateMemorySources 给来源列表补段落数、聊天流名与人物姓名
func (d Deps) decorateMemorySources(ctx context.Context, payload map[string]any) map[string]any {
	items, _ := payload["items"].([]any)
	if items == nil {
		return payload
	}
	type parsedSource struct {
		kind  string
		token string
	}
	parsed := make(map[int]parsedSource, len(items))
	var chatTokens, personTokens []string
	for index, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		kind, token := parseMemorySource(item["source"])
		parsed[index] = parsedSource{kind, token}
		if token == "" {
			continue
		}
		if kind == "person_fact" {
			personTokens = append(personTokens, token)
		} else {
			chatTokens = append(chatTokens, token)
		}
	}
	chatNames := d.memorySourceChatNames(ctx, chatTokens)
	personNames := memorySourcePersonNames(personTokens)

	for index, entry := range parsed {
		item, _ := items[index].(map[string]any)
		if item == nil {
			continue
		}
		item["paragraph_count"] = anyInt(item["count"])
		item["source_kind"] = entry.kind
		isPersonSource := entry.kind == "person_fact"
		if isPersonSource {
			item["chat_id"] = ""
			item["chat_name"] = ""
			item["person_id"] = entry.token
			item["person_name"] = personNames[entry.token]
		} else {
			item["chat_id"] = entry.token
			item["chat_name"] = chatNames[entry.token]
			item["person_id"] = ""
			item["person_name"] = ""
		}
	}
	return payload
}

// decorateMemoryRecordOrigins 给记录行补 source_kind / chat_name / person_name / scope_label
func (d Deps) decorateMemoryRecordOrigins(ctx context.Context, rows []map[string]any) {
	if len(rows) == 0 {
		return
	}
	type parsedSource struct {
		kind  string
		token string
	}
	parsed := make([]parsedSource, len(rows))
	var chatTokens, personTokens []string
	personScope, chatScope := make([]string, 0), make([]string, 0)
	for index, row := range rows {
		kind, token := parseMemorySource(memoryRecordOriginToken(row))
		parsed[index] = parsedSource{kind, token}
		if token != "" {
			if kind == "person_fact" {
				personTokens = append(personTokens, token)
			} else {
				chatTokens = append(chatTokens, token)
			}
		}
		scopeType := strings.TrimSpace(anyTrimmed(row["scope_type"]))
		scopeToken := strings.TrimSpace(anyTrimmed(row["scope_id"]))
		if scopeToken == "" {
			continue
		}
		switch scopeType {
		case "person":
			personScope = append(personScope, scopeToken)
		case "chat":
			chatScope = append(chatScope, scopeToken)
		}
	}
	chatNames := d.memorySourceChatNames(ctx, chatTokens)
	personNames := memorySourcePersonNames(personTokens)
	personScopeNames := memorySourcePersonNames(personScope)
	chatScopeNames := d.memorySourceChatNames(ctx, chatScope)

	for index, entry := range parsed {
		row := rows[index]
		row["source_kind"] = entry.kind
		isPersonSource := entry.kind == "person_fact"
		if isPersonSource {
			row["chat_name"] = ""
			row["person_name"] = personNames[entry.token]
		} else {
			row["chat_name"] = chatNames[entry.token]
			row["person_name"] = ""
		}
		scopeType := strings.TrimSpace(anyTrimmed(row["scope_type"]))
		scopeToken := strings.TrimSpace(anyTrimmed(row["scope_id"]))
		switch scopeType {
		case "person":
			row["scope_label"] = personScopeNames[scopeToken]
		case "chat":
			row["scope_label"] = chatScopeNames[scopeToken]
		default:
			row["scope_label"] = ""
		}
	}
}

// memoryRecordOriginLabel 记录的来源可读标签，解析不出时返回空串而不是原始标记
func memoryRecordOriginLabel(row map[string]any, allowRawSource bool) string {
	if name := strings.TrimSpace(anyTrimmed(row["chat_name"])); name != "" {
		return name
	}
	if name := strings.TrimSpace(anyTrimmed(row["person_name"])); name != "" {
		return name
	}
	if !allowRawSource || strings.TrimSpace(anyTrimmed(row["source_kind"])) != "" {
		return ""
	}
	return strings.TrimSpace(anyTrimmed(row["source"]))
}

// memoryPersonIdentities 批量读取人物可读身份：姓名、平台昵称与群名片
func memoryPersonIdentities(personIDs []string) map[string]map[string]any {
	identities := map[string]map[string]any{}
	tokens := make([]string, 0, len(personIDs))
	seen := map[string]struct{}{}
	for _, item := range personIDs {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		return identities
	}
	sort.Strings(tokens)
	for _, chunk := range memoryChunkTokens(tokens, 300) {
		args := make([]any, 0, len(chunk))
		for _, token := range chunk {
			args = append(args, token)
		}
		rows := queryMemoryRowsOptional(`SELECT person_id, person_name, user_nickname, group_cardname
			FROM person_info WHERE person_id IN (`+memoryPlaceholders(len(chunk))+`)`, args...)
		for _, row := range rows {
			personID := strings.TrimSpace(anyTrimmed(row["person_id"]))
			if personID == "" {
				continue
			}
			cardnames := []string{}
			for _, item := range listValue(decodeMemoryJSONPayload(row["group_cardname"], []any{})) {
				if token := strings.TrimSpace(anyTrimmed(item)); token != "" {
					cardnames = append(cardnames, token)
				}
			}
			identities[personID] = map[string]any{
				"person_name":         strings.TrimSpace(anyTrimmed(row["person_name"])),
				"user_nickname":       strings.TrimSpace(anyTrimmed(row["user_nickname"])),
				"group_cardname_list": cardnames,
			}
		}
	}
	return identities
}

// decorateMemoryProfileIdentities 给画像列表补 person_name / user_nickname / group_cardname_list
func (d Deps) decorateMemoryProfileIdentities(_ context.Context, payload map[string]any) map[string]any {
	items, _ := payload["items"].([]any)
	if items == nil {
		return payload
	}
	personIDs := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		personIDs = append(personIDs, anyTrimmed(item["person_id"]))
	}
	identities := memoryPersonIdentities(personIDs)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		identity := identities[anyTrimmed(item["person_id"])]
		if identity == nil {
			item["person_name"] = ""
			item["user_nickname"] = ""
			item["group_cardname_list"] = []string{}
			continue
		}
		item["person_name"] = identity["person_name"]
		item["user_nickname"] = identity["user_nickname"]
		item["group_cardname_list"] = identity["group_cardname_list"]
	}
	return payload
}
