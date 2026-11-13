package host

import (
	"reflect"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// pluginTOMLSection 表示插件配置 TOML 文本中的一个表头及其行范围
type pluginTOMLSection struct {
	header int
	end    int
	path   string
	array  bool
}

// mergePluginTOMLPreservingComments 把新生成的插件配置合并回原文件
// 未变更的节直接复用原文；变更/新增的节保留节上方、键上方与键尾注释
func mergePluginTOMLPreservingComments(original, generated []byte) []byte {
	if strings.TrimSpace(string(original)) == "" {
		return generated
	}
	var originalData map[string]any
	if err := toml.Unmarshal(original, &originalData); err != nil {
		return generated
	}
	var generatedData map[string]any
	if err := toml.Unmarshal(generated, &generatedData); err != nil {
		return generated
	}

	originalLines := splitPluginTOMLText(string(original))
	generatedLines := splitPluginTOMLText(string(generated))
	originalSections := scanPluginTOMLSections(originalLines)
	generatedSections := scanPluginTOMLSections(generatedLines)
	if len(generatedSections) == 0 {
		return generated
	}

	var out []string
	generatedPreamble := trimPluginTOMLBlankLines(generatedLines[:generatedSections[0].header])
	if len(originalSections) > 0 {
		originalPreamble := trimPluginTOMLBlankLines(originalLines[:originalSections[0].header])
		if len(originalPreamble) > 0 {
			out = append(out, originalPreamble...)
		} else {
			out = append(out, generatedPreamble...)
		}
	} else {
		out = append(out, generatedPreamble...)
	}

	for index, generatedSection := range generatedSections {
		occurrence := 0
		for prior := 0; prior < index; prior++ {
			if generatedSections[prior].path == generatedSection.path && generatedSections[prior].array == generatedSection.array {
				occurrence++
			}
		}
		originalSection := findPluginTOMLSection(originalSections, generatedSection, occurrence)
		unchanged := originalSection != nil && pluginTOMLSectionEqual(originalData, generatedData, generatedSection)
		if index > 0 && originalSection != nil {
			out = append(out, pluginTOMLSectionCommentBlock(originalLines, *originalSection)...)
		}
		if unchanged {
			out = append(out, trimPluginTOMLBlankLines(originalLines[originalSection.header:originalSection.end])...)
			out = append(out, "")
			continue
		}
		out = append(out, changedPluginTOMLSectionLines(originalLines, originalSection, generatedLines, generatedSection)...)
		out = append(out, "")
	}

	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func findPluginTOMLSection(sections []pluginTOMLSection, target pluginTOMLSection, occurrence int) *pluginTOMLSection {
	seen := 0
	for i := range sections {
		if sections[i].path != target.path || sections[i].array != target.array {
			continue
		}
		if seen == occurrence {
			return &sections[i]
		}
		seen++
	}
	return nil
}

func pluginTOMLSectionCommentBlock(lines []string, target pluginTOMLSection) []string {
	start := target.header
	for start > 0 {
		trimmed := strings.TrimSpace(lines[start-1])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
		start--
	}
	if start == target.header {
		return nil
	}
	return append([]string(nil), lines[start:target.header]...)
}

func changedPluginTOMLSectionLines(originalLines []string, originalSection *pluginTOMLSection, generatedLines []string, generatedSection pluginTOMLSection) []string {
	generated := trimPluginTOMLBlankLines(generatedLines[generatedSection.header:generatedSection.end])
	if originalSection == nil || len(generated) == 0 {
		return generated
	}
	originalKeys := make(map[string]int)
	for i := originalSection.header + 1; i < originalSection.end; i++ {
		key, _, ok := pluginTOMLKeyLine(originalLines[i])
		if ok {
			originalKeys[key] = i
		}
	}
	out := []string{generated[0]}
	for _, line := range generated[1:] {
		key, _, ok := pluginTOMLKeyLine(line)
		if !ok {
			out = append(out, line)
			continue
		}
		originalIndex, exists := originalKeys[key]
		if !exists {
			out = append(out, line)
			continue
		}
		out = append(out, pluginTOMLKeyCommentBlock(originalLines, originalSection.header, originalIndex)...)
		if comment := pluginTOMLTrailingComment(originalLines[originalIndex]); comment != "" && pluginTOMLCommentIndex(line) < 0 {
			line = strings.TrimRight(line, " \t") + " " + comment
		}
		out = append(out, line)
	}
	return out
}

func pluginTOMLKeyCommentBlock(lines []string, lowerBound, keyIndex int) []string {
	start := keyIndex
	for start > lowerBound+1 {
		trimmed := strings.TrimSpace(lines[start-1])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
		start--
	}
	if start == keyIndex {
		return nil
	}
	return append([]string(nil), lines[start:keyIndex]...)
}

func pluginTOMLTrailingComment(line string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return ""
	}
	rest := line[eq+1:]
	index := pluginTOMLCommentIndex(rest)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(rest[index:])
}

func trimPluginTOMLBlankLines(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return append([]string(nil), lines[:end]...)
}

func pluginTOMLSectionEqual(originalData, generatedData map[string]any, section pluginTOMLSection) bool {
	originalValue, ok := pluginTOMLValueAtPath(originalData, section.path)
	if !ok {
		return false
	}
	generatedValue, ok := pluginTOMLValueAtPath(generatedData, section.path)
	if !ok {
		return false
	}
	return reflect.DeepEqual(originalValue, generatedValue)
}

func pluginTOMLValueAtPath(data map[string]any, path string) (any, bool) {
	if path == "" {
		return data, true
	}
	var current any = data
	for _, part := range strings.Split(path, ".") {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func splitPluginTOMLText(text string) []string {
	return strings.Split(text, "\n")
}

func parsePluginTOMLHeader(line string) (string, bool, bool) {
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

func scanPluginTOMLSections(lines []string) []pluginTOMLSection {
	var sections []pluginTOMLSection
	for i, line := range lines {
		path, array, ok := parsePluginTOMLHeader(line)
		if !ok {
			continue
		}
		sections = append(sections, pluginTOMLSection{header: i, path: path, array: array})
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

func pluginTOMLKeyLine(line string) (string, int, bool) {
	trimmedLeft := strings.TrimLeft(line, " \t")
	if trimmedLeft == "" || strings.HasPrefix(trimmedLeft, "#") || strings.HasPrefix(trimmedLeft, "[") {
		return "", 0, false
	}
	eq := strings.Index(trimmedLeft, "=")
	if eq < 0 {
		return "", 0, false
	}
	key := strings.TrimSpace(trimmedLeft[:eq])
	if key == "" {
		return "", 0, false
	}
	return key, len(line) - len(trimmedLeft) + eq, true
}

func pluginTOMLCommentIndex(s string) int {
	var inBasic, inLiteral, inMLBasic, inMLLiteral bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inMLBasic:
			if c == '\\' {
				i++
			} else if strings.HasPrefix(s[i:], "\"\"\"") {
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
			if strings.HasPrefix(s[i:], "\"\"\"") {
				inMLBasic = true
				i += 2
			} else if strings.HasPrefix(s[i:], "'''") {
				inMLLiteral = true
				i += 2
			} else if c == '"' {
				inBasic = true
			} else if c == '\'' {
				inLiteral = true
			} else if c == '#' {
				return i
			}
		}
	}
	return -1
}
