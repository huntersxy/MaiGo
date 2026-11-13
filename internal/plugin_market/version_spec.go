package pluginmarket

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	versionSpecifierPattern = regexp.MustCompile(`^(===|==|!=|>=|<=|~=|>|<)\s*(\S.*)$`)
	versionValuePattern     = regexp.MustCompile(`^v?\d`)
)

// versionSpecifier 单条版本约束，覆盖 PEP 440 常用运算符
type versionSpecifier struct {
	Operator string
	Version  string
}

// parseVersionSpecifiers 解析逗号分隔的版本约束表达式
func parseVersionSpecifiers(spec string) ([]versionSpecifier, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, fmt.Errorf("版本约束不能为空")
	}
	parts := strings.Split(trimmed, ",")
	specifiers := make([]versionSpecifier, 0, len(parts))
	for _, part := range parts {
		clause := strings.TrimSpace(part)
		if clause == "" {
			return nil, fmt.Errorf("版本约束包含空项: %s", spec)
		}
		match := versionSpecifierPattern.FindStringSubmatch(clause)
		if match == nil {
			return nil, fmt.Errorf("无效的版本约束: %s", clause)
		}
		value := strings.TrimSpace(match[2])
		if !versionValuePattern.MatchString(value) {
			return nil, fmt.Errorf("无效的版本约束: %s", clause)
		}
		specifiers = append(specifiers, versionSpecifier{Operator: match[1], Version: value})
	}
	return specifiers, nil
}

// versionMatchesSpecifiers 判断版本是否满足全部约束
func versionMatchesSpecifiers(version string, specifiers []versionSpecifier) bool {
	for _, specifier := range specifiers {
		if !versionMatchesSpecifier(version, specifier) {
			return false
		}
	}
	return true
}

func versionMatchesSpecifier(version string, specifier versionSpecifier) bool {
	if specifier.Operator == "===" {
		return strings.TrimSpace(version) == specifier.Version
	}
	if strings.HasSuffix(specifier.Version, ".*") {
		matched := versionHasPrefix(version, strings.TrimSuffix(specifier.Version, "*"))
		if specifier.Operator == "!=" {
			return !matched
		}
		if specifier.Operator == "==" {
			return matched
		}
		return false
	}
	comparison := compareVersions(version, specifier.Version)
	switch specifier.Operator {
	case "==":
		return comparison == 0
	case "!=":
		return comparison != 0
	case ">=":
		return comparison >= 0
	case "<=":
		return comparison <= 0
	case ">":
		return comparison > 0
	case "<":
		return comparison < 0
	case "~=":
		if comparison < 0 {
			return false
		}
		return versionHasPrefix(version, compatibleReleasePrefix(specifier.Version))
	}
	return false
}

// compatibleReleasePrefix 对齐 ~= 的兼容前缀语义
func compatibleReleasePrefix(version string) string {
	segments := strings.Split(normalizeVersion(version), ".")
	if len(segments) <= 1 {
		return normalizeVersion(version) + "."
	}
	return strings.Join(segments[:len(segments)-1], ".") + "."
}

func versionHasPrefix(version, prefix string) bool {
	normalized := normalizeVersion(version)
	parts := strings.Split(strings.TrimSuffix(prefix, "."), ".")
	normalizedParts := strings.Split(normalized, ".")
	if len(parts) > len(normalizedParts) {
		return false
	}
	for i, part := range parts {
		if strings.TrimSpace(part) != normalizedParts[i] {
			return false
		}
	}
	return true
}
