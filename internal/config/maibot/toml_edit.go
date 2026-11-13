package maibot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// tomlSection 表示 TOML 文本中的一个表头及其行范围
type tomlSection struct {
	header int
	end    int
	path   string
	array  bool
}

func splitTOMLText(text string) []string {
	return strings.Split(text, "\n")
}

func joinTOMLText(lines []string) string {
	return strings.Join(lines, "\n")
}

func readTOMLText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func parseTOMLHeader(line string) (string, bool, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '[' {
		return "", false, false
	}
	if strings.HasPrefix(trimmed, "[[") {
		if idx := strings.Index(trimmed[2:], "]]"); idx >= 0 {
			return strings.TrimSpace(trimmed[2 : 2+idx]), true, true
		}
		return "", false, false
	}
	if idx := strings.Index(trimmed[1:], "]"); idx >= 0 {
		return strings.TrimSpace(trimmed[1 : 1+idx]), false, true
	}
	return "", false, false
}

func scanTOMLSections(lines []string) []tomlSection {
	var sections []tomlSection
	for i, line := range lines {
		path, array, ok := parseTOMLHeader(line)
		if !ok {
			continue
		}
		sections = append(sections, tomlSection{header: i, path: path, array: array})
	}
	for i := range sections {
		if i+1 < len(sections) {
			sections[i].end = sections[i+1].header
		} else {
			sections[i].end = len(lines)
		}
	}
	return sections
}

func findTOMLSection(lines []string, path string, array bool) *tomlSection {
	sections := scanTOMLSections(lines)
	for i := range sections {
		if sections[i].path == path && sections[i].array == array {
			return &sections[i]
		}
	}
	return nil
}

func tomlKeyLine(line string) (string, int, bool) {
	trimmedLeft := strings.TrimLeft(line, " \t")
	if trimmedLeft == "" || strings.HasPrefix(trimmedLeft, "#") || strings.HasPrefix(trimmedLeft, "[") {
		return "", 0, false
	}
	offset := len(line) - len(trimmedLeft)
	eq := strings.Index(trimmedLeft, "=")
	if eq < 0 {
		return "", 0, false
	}
	key := strings.TrimSpace(trimmedLeft[:eq])
	if key == "" {
		return "", 0, false
	}
	return key, offset + eq, true
}

func tomlCommentIndex(s string) int {
	var inBasic, inLiteral, inMLBasic, inMLLiteral bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inMLBasic:
			if c == '\\' {
				i++
			} else if strings.HasPrefix(s[i:], `"""`) {
				inMLBasic = false
				i += 2
			}
		case inMLLiteral:
			if strings.HasPrefix(s[i:], "'''") {
				inMLLiteral = false
				i += 2
			}
		case inBasic:
			if c == '\\' {
				i++
			} else if c == '"' {
				inBasic = false
			}
		case inLiteral:
			if c == '\'' {
				inLiteral = false
			}
		default:
			if c == '#' {
				return i
			}
			if strings.HasPrefix(s[i:], `"""`) {
				inMLBasic = true
				i += 2
			} else if strings.HasPrefix(s[i:], "'''") {
				inMLLiteral = true
				i += 2
			} else if c == '"' {
				inBasic = true
			} else if c == '\'' {
				inLiteral = true
			}
		}
	}
	return -1
}

func replaceTOMLScalarLine(line, encoded string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return line
	}
	prefix := line[:eq+1]
	rest := line[eq+1:]
	idx := tomlCommentIndex(rest)
	if idx >= 0 {
		comment := rest[idx:]
		return prefix + " " + encoded + " " + strings.TrimLeft(comment, " \t")
	}
	return prefix + " " + encoded
}

func isTOMLTripleOpen(rest string) (string, bool) {
	if strings.Count(rest, `"""`) == 1 {
		return `"""`, true
	}
	if strings.Count(rest, "'''") == 1 {
		return "'''", true
	}
	return "", false
}

func replaceTOMLScalarBlock(lines []string, index int, encoded string) []string {
	line := lines[index]
	eq := strings.Index(line, "=")
	if eq < 0 {
		return lines
	}
	rest := line[eq+1:]
	delim, ok := isTOMLTripleOpen(rest)
	if !ok {
		lines[index] = replaceTOMLScalarLine(line, encoded)
		return lines
	}
	closeIndex := -1
	for i := index + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], delim) {
			closeIndex = i
			break
		}
	}
	if closeIndex < 0 {
		lines[index] = replaceTOMLScalarLine(line, encoded)
		return lines
	}
	newLine := line[:eq+1] + " " + encoded
	return append(append(append([]string{}, lines[:index]...), newLine), lines[closeIndex+1:]...)
}

func appendTOMLSection(lines []string, path string) []string {
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	return append(lines, "["+path+"]")
}

func setTOMLSectionScalar(lines []string, sectionPath, key, encoded string) []string {
	section := findTOMLSection(lines, sectionPath, false)
	if section == nil {
		lines = appendTOMLSection(lines, sectionPath)
		section = findTOMLSection(lines, sectionPath, false)
		if section == nil {
			return lines
		}
	}
	for i := section.header + 1; i < section.end; i++ {
		existing, _, ok := tomlKeyLine(lines[i])
		if !ok || existing != key {
			continue
		}
		return replaceTOMLScalarBlock(lines, i, encoded)
	}
	insert := section.end
	if insert > section.header+1 && strings.TrimSpace(lines[insert-1]) == "" {
		insert--
	}
	lines = append(lines, "")
	copy(lines[insert+1:], lines[insert:])
	lines[insert] = key + " = " + encoded
	return lines
}

func encodeTOMLValue(value any) string {
	raw, err := toml.Marshal(map[string]any{"v": value})
	if err != nil {
		return `""`
	}
	line := strings.TrimSpace(string(raw))
	if idx := strings.Index(line, "="); idx >= 0 {
		return strings.TrimSpace(line[idx+1:])
	}
	return line
}

func personalityProfileLines(profile PersonalityProfile) []string {
	return []string{
		"name = " + encodeTOMLValue(profile.Name),
		"personality = " + encodeTOMLValue(profile.Personality),
		"behavior_style = " + encodeTOMLValue(profile.BehaviorStyle),
		"reply_style = " + encodeTOMLValue(profile.ReplyStyle),
		"group_chat_prompt = " + encodeTOMLValue(profile.GroupChatPrompt),
		"private_chat_prompts = " + encodeTOMLValue(profile.PrivateChatPrompts),
		"multiple_reply_style = " + encodeTOMLValue(profile.MultipleReplyStyle),
		"multiple_probability = " + encodeTOMLValue(profile.MultipleProbability),
	}
}

func setTOMLPersonalityProfiles(lines []string, profiles []PersonalityProfile) []string {
	var profileComments [][]string
	sections := scanTOMLSections(lines)
	for i := range sections {
		if sections[i].path == "personality.profiles" && sections[i].array {
			profileComments = append(profileComments, sectionCommentBlock(lines, sections[i]))
		}
	}
	var block []string
	for i, profile := range profiles {
		if i > 0 {
			block = append(block, "")
		}
		if i < len(profileComments) {
			block = append(block, profileComments[i]...)
		}
		block = append(block, "[[personality.profiles]]")
		block = append(block, personalityProfileLines(profile)...)
	}
	first := -1
	last := -1
	for i := range sections {
		if sections[i].path == "personality.profiles" && sections[i].array {
			if first < 0 {
				first = sections[i].header
			}
			last = i
		}
	}
	if first >= 0 {
		end := sections[last].end
		out := append([]string{}, lines[:first]...)
		out = append(out, block...)
		if end < len(lines) && len(block) > 0 && strings.TrimSpace(block[len(block)-1]) != "" {
			out = append(out, "")
		}
		return append(out, lines[end:]...)
	}
	section := findTOMLSection(lines, "personality", false)
	if section == nil {
		lines = appendTOMLSection(lines, "personality")
		section = findTOMLSection(lines, "personality", false)
		if section == nil {
			return lines
		}
	}
	insert := section.end
	if insert > section.header+1 && strings.TrimSpace(lines[insert-1]) == "" {
		insert--
	}
	out := append([]string{}, lines[:insert]...)
	out = append(out, "")
	out = append(out, block...)
	out = append(out, lines[insert:]...)
	return out
}

func writeTOMLTextAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".maibot-toml-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}
