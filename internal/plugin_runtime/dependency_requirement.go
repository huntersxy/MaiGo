package pluginruntime

import (
	"regexp"
	"strings"
)

var pep440PackageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var pep440PluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)+$`)

// CanonicalizePythonPackageName 对齐 packaging.utils.canonicalize_name
func CanonicalizePythonPackageName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	previousSeparator := false
	for _, char := range normalized {
		switch char {
		case '-', '_', '.':
			if !previousSeparator {
				builder.WriteByte('-')
			}
			previousSeparator = true
		default:
			builder.WriteRune(char)
			previousSeparator = false
		}
	}
	return strings.Trim(builder.String(), "-")
}

// pythonRequirement 是 pyproject.toml 中一条依赖的解析结果
type pythonRequirement struct {
	Name       string
	Specifier  string
	SpecSet    PEP440SpecifierSet
	SpecValid  bool
	Raw        string
	Normalized string
}

// parsePythonRequirement 截取 PEP 508 依赖中的包名与版本约束
// 只处理主程序依赖声明所需的子集，URL 与 marker 会被忽略
func parsePythonRequirement(raw string) (pythonRequirement, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return pythonRequirement{}, false
	}
	requirement := pythonRequirement{Raw: text}
	if markerIndex := strings.Index(text, ";"); markerIndex >= 0 {
		text = strings.TrimSpace(text[:markerIndex])
	}
	if atIndex := strings.Index(text, " @ "); atIndex >= 0 {
		text = strings.TrimSpace(text[:atIndex])
	}
	nameEnd := len(text)
	for index, char := range text {
		if char == '[' || char == '<' || char == '>' || char == '=' || char == '!' || char == '~' || char == ' ' || char == '	' {
			nameEnd = index
			break
		}
	}
	name := strings.TrimSpace(text[:nameEnd])
	if !pep440PackageNamePattern.MatchString(name) {
		return pythonRequirement{}, false
	}
	remaining := strings.TrimSpace(text[nameEnd:])
	if strings.HasPrefix(remaining, "[") {
		closeIndex := strings.Index(remaining, "]")
		if closeIndex < 0 {
			return pythonRequirement{}, false
		}
		remaining = strings.TrimSpace(remaining[closeIndex+1:])
	}
	requirement.Name = name
	requirement.Normalized = CanonicalizePythonPackageName(name)
	requirement.Specifier = remaining
	if remaining == "" {
		requirement.SpecValid = true
		return requirement, true
	}
	specSet, ok := ParsePEP440SpecifierSet(remaining)
	if !ok {
		return pythonRequirement{}, false
	}
	requirement.SpecSet = specSet
	requirement.SpecValid = true
	return requirement, true
}

func mergePythonSpecifierTexts(specifierTexts []string) string {
	merged := make([]string, 0, len(specifierTexts))
	seen := make(map[string]struct{})
	for _, specifierText := range specifierTexts {
		for _, part := range strings.Split(specifierText, ",") {
			normalized := strings.TrimSpace(part)
			if normalized == "" {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			merged = append(merged, normalized)
		}
	}
	return strings.Join(merged, ",")
}

func isSafePythonPluginID(pluginID string) bool {
	return pep440PluginIDPattern.MatchString(pluginID)
}
