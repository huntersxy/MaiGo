package services

// 复制聊天流的精确配置规则，保留 TOML 注释与其他聊天流配置。
// 对齐 services/chat_migration_config.py。

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ChatMigrationConfigIdentity 聊天流身份
type ChatMigrationConfigIdentity struct {
	Platform string `json:"platform"`
	ItemID   string `json:"item_id"`
	Type     string `json:"type"`
}

// ChatMigrationConfigSection 一组需要选择的配置项
type ChatMigrationConfigSection struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Label       string            `json:"label"`
	Path        []any             `json:"path"`
	Automatic   bool              `json:"automatic"`
	FieldLabels map[string]string `json:"field_labels"`
	Source      []map[string]any  `json:"source"`
	Target      []map[string]any  `json:"target"`
	Choices     []string          `json:"choices"`
	All         []map[string]any  `json:"-"`
}

// ChatMigrationConfigSnapshot 配置预览
type ChatMigrationConfigSnapshot struct {
	Fingerprint    string                       `json:"fingerprint"`
	Raw            string                       `json:"raw"`
	Sections       []ChatMigrationConfigSection `json:"sections"`
	TargetIdentity ChatMigrationConfigIdentity  `json:"target_identity"`
}

func chatMigrationIdentity(session map[string]any) ChatMigrationConfigIdentity {
	platform := sessionString(session["platform"])
	groupID := sessionString(session["group_id"])
	if groupID != "" {
		return ChatMigrationConfigIdentity{Platform: platform, ItemID: groupID, Type: "group"}
	}
	return ChatMigrationConfigIdentity{Platform: platform, ItemID: sessionString(session["user_id"]), Type: "private"}
}

func sessionString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func chatMigrationRuleMatches(rule any, target ChatMigrationConfigIdentity) bool {
	mapping, ok := rule.(map[string]any)
	if !ok {
		return false
	}
	ruleType := sessionString(mapping["rule_type"])
	if ruleType == "" {
		ruleType = sessionString(mapping["type"])
	}
	return sessionString(mapping["platform"]) == target.Platform &&
		sessionString(mapping["item_id"]) == target.ItemID && ruleType == target.Type
}

func chatMigrationSectionPath(path []any) string {
	parts := make([]string, len(path))
	for index, part := range path {
		parts[index] = fmt.Sprint(part)
	}
	return strings.Join(parts, ".")
}

// ChatMigrationConfigSnapshotFrom 读取并解析 bot_config
func ChatMigrationConfigSnapshotFrom(path string, source, target map[string]any) (ChatMigrationConfigSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ChatMigrationConfigSnapshot{}, err
	}
	document := map[string]any{}
	if err := toml.Unmarshal(raw, &document); err != nil {
		return ChatMigrationConfigSnapshot{}, err
	}
	sourceID := chatMigrationIdentity(source)
	targetID := chatMigrationIdentity(target)
	sections := make([]ChatMigrationConfigSection, 0)
	var walk func(value any, parts []any)
	var walkList func(list []map[string]any, parts []any)
	walk = func(value any, parts []any) {
		switch typed := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key], append(append([]any(nil), parts...), key))
			}
		case []map[string]any:
			walkList(typed, parts)
		case []any:
			list := make([]map[string]any, 0, len(typed))
			allMaps := true
			for _, item := range typed {
				mapping, ok := item.(map[string]any)
				if !ok {
					allMaps = false
					break
				}
				list = append(list, mapping)
			}
			if allMaps {
				walkList(list, parts)
				return
			}
			for index, item := range typed {
				walk(item, append(append([]any(nil), parts...), index))
			}
		}
	}
	walkList = func(list []map[string]any, parts []any) {
		incoming := make([]map[string]any, 0)
		existing := make([]map[string]any, 0)
		for _, item := range list {
			if chatMigrationRuleMatches(item, targetID) {
				existing = append(existing, item)
			}
			if chatMigrationRuleMatches(item, sourceID) {
				rule := cloneMigrationRow(item)
				rule["platform"] = targetID.Platform
				rule["item_id"] = targetID.ItemID
				if _, ok := rule["rule_type"]; ok {
					rule["rule_type"] = targetID.Type
				} else {
					rule["type"] = targetID.Type
				}
				incoming = append(incoming, rule)
			}
		}
		if len(incoming) > 0 && migrateDumps(incoming) != migrateDumps(existing) && sourceID != targetID {
			path := append([]any(nil), parts...)
			label := chatMigrationSectionPath(path)
			sections = append(sections, ChatMigrationConfigSection{
				ID: "config:" + label, Kind: "config", Label: label, Path: path,
				Automatic: len(existing) == 0,
				FieldLabels: map[string]string{
					"platform": "平台", "item_id": "聊天流 ID", "rule_type": "聊天类型",
				},
				Source: incoming, Target: existing, Choices: []string{"source", "target"},
				All: list,
			})
		}
		for index, item := range list {
			if chatMigrationRuleMatches(item, sourceID) || chatMigrationRuleMatches(item, targetID) {
				continue
			}
			walk(item, append(append([]any(nil), parts...), index))
		}
	}
	walk(document, nil)
	return ChatMigrationConfigSnapshot{
		Fingerprint: MigrationDigest(string(raw)), Raw: string(raw), Sections: sections, TargetIdentity: targetID,
	}, nil
}

// RenderChatMigrationConfig 应用冲突选择并返回新配置文本
func RenderChatMigrationConfig(state ChatMigrationConfigSnapshot, decisions map[string]string) (string, error) {
	content := state.Raw
	for _, section := range state.Sections {
		choice, ok := decisions[section.ID]
		if !ok {
			if section.Automatic || len(section.Target) == 0 {
				choice = "source"
			}
		}
		if choice != "source" && choice != "target" {
			return "", fmt.Errorf("存在未处理的配置冲突")
		}
		if choice == "target" {
			continue
		}
		content = applyChatMigrationSection(content, section)
	}
	return content, nil
}

func applyChatMigrationSection(content string, section ChatMigrationConfigSection) string {
	path := chatMigrationSectionPath(section.Path)
	lines := strings.Split(content, "\n")
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		lines = strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	}
	headerPrefix := "[[" + path + "]]"
	hasBlocks := false
	kept := make([]string, 0, len(lines))
	for index := 0; index < len(lines); {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == headerPrefix {
			hasBlocks = true
			end := index + 1
			for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
				end++
			}
			block := strings.Join(lines[index:end], "\n")
			mapping := map[string]any{}
			_ = toml.Unmarshal([]byte(block), &mapping)
			// 表头 [[a.b]] 解析到嵌套 map，取出目标列表首项
			entries := chatMigrationBlockEntry(mapping, section.Path)
			if len(section.Target) > 0 && chatMigrationRuleMatches(entries, section.TargetIdentity()) {
				index = end
				continue
			}
			kept = append(kept, lines[index:end]...)
			index = end
			continue
		}
		kept = append(kept, lines[index])
		index++
	}
	if hasBlocks {
		for _, rule := range section.Source {
			kept = append(kept, "", headerPrefix)
			keys := make([]string, 0, len(rule))
			for key := range rule {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				kept = append(kept, key+" = "+chatMigrationTOMLValue(rule[key]))
			}
		}
		return strings.Join(kept, newline)
	}
	// 内联数组：替换父表中 key 赋值
	return applyInlineList(content, section)
}

func chatMigrationBlockEntry(mapping map[string]any, path []any) map[string]any {
	node := any(mapping)
	for _, part := range path {
		key, ok := part.(string)
		if !ok {
			return nil
		}
		current, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = current[key]
	}
	switch typed := node.(type) {
	case []map[string]any:
		if len(typed) > 0 {
			return typed[len(typed)-1]
		}
	case []any:
		if len(typed) > 0 {
			if item, ok := typed[len(typed)-1].(map[string]any); ok {
				return item
			}
		}
	}
	return nil
}

func chatMigrationRuleType(rule map[string]any) string {
	if value := sessionString(rule["rule_type"]); value != "" {
		return value
	}
	return sessionString(rule["type"])
}

func applyInlineList(content string, section ChatMigrationConfigSection) string {
	parent := chatMigrationSectionPath(section.Path[:len(section.Path)-1])
	key, _ := section.Path[len(section.Path)-1].(string)
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	lines := strings.Split(content, "\n")
	sectionStart, sectionEnd := -1, len(lines)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(strings.SplitN(trimmed, "#", 2)[0], "]") {
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			if name == parent && sectionStart < 0 {
				sectionStart = index
			} else if sectionStart >= 0 {
				sectionEnd = index
				break
			}
		}
	}
	target := append([]map[string]any(nil), section.All...)
	keptRules := make([]map[string]any, 0, len(target))
	for _, rule := range target {
		if chatMigrationRuleMatches(rule, ChatMigrationConfigIdentity{
			Platform: section.TargetIdentity().Platform,
			ItemID:   section.TargetIdentity().ItemID,
			Type:     section.TargetIdentity().Type,
		}) {
			continue
		}
		keptRules = append(keptRules, rule)
	}
	keptRules = append(keptRules, section.Source...)
	rendered := chatMigrationTOMLValue(keptRules)
	searchStart := 0
	if sectionStart >= 0 {
		searchStart = sectionStart
	}
	for index := searchStart; index < sectionEnd; index++ {
		trimmed := strings.TrimSpace(lines[index])
		assignment := strings.SplitN(trimmed, "=", 2)
		if len(assignment) != 2 || strings.TrimSpace(assignment[0]) != key {
			continue
		}
		prefixEnd := strings.Index(lines[index], "=") + 1
		valueStart := prefixEnd
		for valueStart < len(lines[index]) && (lines[index][valueStart] == ' ' || lines[index][valueStart] == '\t') {
			valueStart++
		}
		endIndex, endColumn := findTOMLValueEnd(lines, index, valueStart)
		suffix := ""
		if endIndex == index {
			suffix = lines[index][endColumn:]
		}
		lines[index] = lines[index][:valueStart] + rendered + suffix
		if endIndex > index {
			lines = append(lines[:index+1], lines[endIndex+1:]...)
		}
		return strings.Join(lines, newline)
	}
	insertLine := key + " = " + rendered
	if sectionStart >= 0 {
		lines = append(lines[:sectionStart+1], append([]string{insertLine}, lines[sectionStart+1:]...)...)
		return strings.Join(lines, newline)
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	lines = append(lines, "", "["+parent+"]", insertLine, "")
	return strings.Join(lines, newline)
}

// TargetIdentity 从 section.Target 推导目标身份
func (s ChatMigrationConfigSection) TargetIdentity() ChatMigrationConfigIdentity {
	if len(s.Target) == 0 {
		return ChatMigrationConfigIdentity{}
	}
	return ChatMigrationConfigIdentity{
		Platform: sessionString(s.Target[0]["platform"]),
		ItemID:   sessionString(s.Target[0]["item_id"]),
		Type:     chatMigrationRuleType(s.Target[0]),
	}
}

func findTOMLValueEnd(lines []string, startLine, startColumn int) (int, int) {
	depth := 0
	inString := byte(0)
	escaped := false
	for lineIndex := startLine; lineIndex < len(lines); lineIndex++ {
		line := lines[lineIndex]
		column := 0
		if lineIndex == startLine {
			column = startColumn
		}
		for ; column < len(line); column++ {
			char := line[column]
			if inString != 0 {
				if escaped {
					escaped = false
					continue
				}
				if char == '\\' && inString == '"' {
					escaped = true
					continue
				}
				if char == inString {
					inString = 0
				}
				continue
			}
			switch char {
			case '"', '\'':
				inString = char
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return lineIndex, column + 1
				}
			}
		}
	}
	return startLine, len(lines[startLine])
}

func chatMigrationTOMLValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return `""`
	case string:
		return strconv.Quote(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+" = "+chatMigrationTOMLValue(typed[key]))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case []map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, chatMigrationTOMLValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, chatMigrationTOMLValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return strconv.Quote(fmt.Sprint(typed))
	}
}

// ReplaceChatMigrationConfig 预览后配置未变化时原子替换
func ReplaceChatMigrationConfig(path, expected, content string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if MigrationDigest(string(raw)) != expected {
		return fmt.Errorf("配置在预览后发生变化，请重新预览")
	}
	temporary := path + ".chat-migration.tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
