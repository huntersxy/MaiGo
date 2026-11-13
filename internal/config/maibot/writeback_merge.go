package maibot

import (
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// mergeTOMLPreservingComments 把新生成的 TOML 内容合并回原文件
// 未变更的节直接复用原文；变更/新增的节保留节上方与键上方注释，并保留键尾注释
// 对齐 tomlkit 的保注释写回，同时避免整文件重写丢失未变更节里的注释
func mergeTOMLPreservingComments(original, generated []byte) []byte {
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

	originalLines := splitTOMLText(string(original))
	generatedLines := splitTOMLText(string(generated))
	originalSections := scanTOMLSections(originalLines)
	generatedSections := scanTOMLSections(generatedLines)
	if len(generatedSections) == 0 {
		return generated
	}

	var out []string
	generatedPreamble := trimTrailingBlankLines(generatedLines[:generatedSections[0].header])
	if len(originalSections) > 0 {
		originalPreamble := trimTrailingBlankLines(originalLines[:originalSections[0].header])
		if len(originalPreamble) > 0 {
			out = append(out, originalPreamble...)
		} else {
			out = append(out, generatedPreamble...)
		}
	} else {
		out = append(out, generatedPreamble...)
	}

	for index, generatedSection := range generatedSections {
		// go-toml 把嵌套表展开成独立表头，原文可能把它写成父节里的内联表
		// （services = {} / default_headers = {}）。父节未变更时会原样复用原文，
		// 内联定义已经生效，此时不再输出生成节，否则同一键被定义两次。
		if originalInlineCoversSection(originalData, generatedData, originalLines, originalSections, generatedSections, index, generatedSection) {
			continue
		}
		occurrence := 0
		for prior := 0; prior < index; prior++ {
			if generatedSections[prior].path == generatedSection.path && generatedSections[prior].array == generatedSection.array {
				occurrence++
			}
		}
		originalSection := findMatchingTOMLSection(originalSections, generatedSection, occurrence)
		unchanged := originalSection != nil && tomlSectionEqual(originalData, generatedData, generatedSection)
		if index > 0 && originalSection != nil {
			out = append(out, sectionCommentBlock(originalLines, *originalSection)...)
		}
		if unchanged {
			out = append(out, trimTrailingBlankLines(originalLines[originalSection.header:originalSection.end])...)
			out = append(out, "")
			continue
		}
		out = append(out, changedSectionLines(originalLines, originalSection, generatedLines, generatedSection)...)
		out = append(out, "")
	}

	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	merged := []byte(strings.Join(out, "\n") + "\n")
	// 合并结果必须仍是合法 TOML；保注释合并若产出坏文本，退回纯生成内容
	var check map[string]any
	if err := toml.Unmarshal(merged, &check); err != nil {
		return generated
	}
	return merged
}

// originalInlineCoversSection 判断生成节是否已被原文某祖先节的内联表覆盖。
//
// go-toml 把嵌套表展开成独立表头，原文常写成父节里的内联表：
// [web] 里 services = {}，[[models]] 里 extra_params = {…}。
// 祖先节未变更时会原样复用原文行，内联定义已经生效，生成节必须跳过，
// 否则同一键被定义两次（含 [models.extra_params] 与 [models.extra_params.thinking] 这样的多级生成节）。
func originalInlineCoversSection(
	originalData, generatedData map[string]any,
	originalLines []string, originalSections []tomlSection,
	generatedSections []tomlSection,
	index int, section tomlSection,
) bool {
	path := section.path
	if path == "" || section.array {
		return false
	}
	// 原文已有同名表头时按正常节处理
	for _, origin := range originalSections {
		if origin.path == path && !origin.array {
			return false
		}
	}
	for i := range originalSections {
		ancestor := originalSections[i]
		if !strings.HasPrefix(path, ancestor.path+".") {
			continue
		}
		childPath := strings.TrimPrefix(path, ancestor.path+".")
		if !sectionHasInlineKey(originalLines, ancestor, childPath) {
			continue
		}
		// 该祖先节在生成文件中也必须存在
		generatedIndex := -1
		for j := index - 1; j >= 0; j-- {
			if generatedSections[j].path == ancestor.path && generatedSections[j].array == ancestor.array {
				generatedIndex = j
				break
			}
		}
		if generatedIndex < 0 {
			continue
		}
		occurrence := 0
		for prior := 0; prior < generatedIndex; prior++ {
			if generatedSections[prior].path == ancestor.path && generatedSections[prior].array == ancestor.array {
				occurrence++
			}
		}
		if findMatchingTOMLSection(originalSections, generatedSections[generatedIndex], occurrence) == nil {
			continue
		}
		// 祖先节数据未变更才复用原文；变更时必须输出生成的子表
		if tomlSectionEqual(originalData, generatedData, generatedSections[generatedIndex]) {
			return true
		}
	}
	return false
}

// sectionHasInlineKey 判断某一节里是否用 key = {…} 内联定义了子表
func sectionHasInlineKey(lines []string, section tomlSection, childPath string) bool {
	target, _, _ := strings.Cut(childPath, ".")
	for line := section.header + 1; line < section.end; line++ {
		key, eq, ok := tomlKeyLine(lines[line])
		if !ok {
			continue
		}
		if strings.Trim(strings.TrimSpace(key), `"'`) != strings.Trim(target, `"'`) {
			continue
		}
		value := strings.TrimSpace(lines[line][eq+1:])
		if idx := tomlCommentIndex(value); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		if strings.HasPrefix(value, "{") {
			return true
		}
	}
	return false
}

func findMatchingTOMLSection(sections []tomlSection, target tomlSection, occurrence int) *tomlSection {
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

func sectionCommentBlock(lines []string, target tomlSection) []string {
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

func changedSectionLines(originalLines []string, originalSection *tomlSection, generatedLines []string, generatedSection tomlSection) []string {
	generated := trimTrailingBlankLines(generatedLines[generatedSection.header:generatedSection.end])
	if originalSection == nil || len(generated) == 0 {
		return generated
	}
	originalKeys := make(map[string]int)
	for i := originalSection.header + 1; i < originalSection.end; i++ {
		key, _, ok := tomlKeyLine(originalLines[i])
		if ok {
			originalKeys[key] = i
		}
	}
	out := []string{generated[0]}
	for _, line := range generated[1:] {
		key, _, ok := tomlKeyLine(line)
		if !ok {
			out = append(out, line)
			continue
		}
		originalIndex, exists := originalKeys[key]
		if !exists {
			out = append(out, line)
			continue
		}
		out = append(out, keyCommentBlock(originalLines, originalSection.header, originalIndex)...)
		if comment := tomlTrailingComment(originalLines[originalIndex]); comment != "" && tomlCommentIndex(line) < 0 {
			line = strings.TrimRight(line, " \t") + " " + comment
		}
		out = append(out, line)
	}
	return out
}

func keyCommentBlock(lines []string, lowerBound, keyIndex int) []string {
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

func tomlTrailingComment(line string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return ""
	}
	rest := line[eq+1:]
	index := tomlCommentIndex(rest)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(rest[index:])
}

func trimTrailingBlankLines(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return append([]string(nil), lines[:end]...)
}

func tomlSectionEqual(originalData, generatedData map[string]any, section tomlSection) bool {
	originalValue, ok := tomlValueAtPath(originalData, section.path)
	if !ok {
		return false
	}
	generatedValue, ok := tomlValueAtPath(generatedData, section.path)
	if !ok {
		return false
	}
	return reflect.DeepEqual(originalValue, generatedValue)
}

func tomlValueAtPath(data map[string]any, path string) (any, bool) {
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

// MergeTOMLPreservingComments 供 WebUI 等外部包复用保注释合并
func MergeTOMLPreservingComments(original, generated []byte) []byte {
	return mergeTOMLPreservingComments(original, generated)
}
