package pluginmarket

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// snapshotSuffixPattern 对齐 VersionComparator.normalize_version 的前置清理
	snapshotSuffixPattern = regexp.MustCompile(`-snapshot\.\d+`)
	leadingReleasePattern = regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\.(\d+))?`)
	strictSemverPattern   = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// normalizeVersion 对齐 VersionComparator.normalize_version：截取 release 段并补齐三段式
func normalizeVersion(version string) string {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return "0.0.0"
	}
	trimmed = snapshotSuffixPattern.ReplaceAllString(trimmed, "")
	match := leadingReleasePattern.FindStringSubmatch(trimmed)
	if match == nil {
		return "0.0.0"
	}
	parts := make([]string, 3)
	for i := 1; i <= 3; i++ {
		raw := ""
		if i < len(match) {
			raw = match[i]
		}
		if raw == "" {
			parts[i-1] = "0"
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return "0.0.0"
		}
		parts[i-1] = strconv.Itoa(value)
	}
	return strings.Join(parts, ".")
}

// parseVersionTriple 对齐 VersionComparator.parse_version：解析失败回落 (0,0,0)
func parseVersionTriple(version string) [3]int {
	normalized := normalizeVersion(version)
	segments := strings.Split(normalized, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(segments); i++ {
		value, err := strconv.Atoi(segments[i])
		if err != nil {
			return [3]int{}
		}
		out[i] = value
	}
	return out
}

// compareVersions 对齐 VersionComparator.compare：-1 小于，1 大于，0 相等
func compareVersions(left, right string) int {
	a := parseVersionTriple(left)
	b := parseVersionTriple(right)
	for i := 0; i < 3; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// isInVersionRange 对齐 VersionComparator.is_in_range：闭区间判断并返回失败原因
func isInVersionRange(version, minVersion, maxVersion string) (bool, string) {
	if minVersion == "" && maxVersion == "" {
		return true, ""
	}
	normalizedVersion := normalizeVersion(version)
	if minVersion != "" {
		normalizedMin := normalizeVersion(minVersion)
		if compareVersions(normalizedVersion, normalizedMin) < 0 {
			return false, "版本 " + normalizedVersion + " 低于最小要求 " + normalizedMin
		}
	}
	if maxVersion != "" {
		normalizedMax := normalizeVersion(maxVersion)
		if compareVersions(normalizedVersion, normalizedMax) > 0 {
			return false, "版本 " + normalizedVersion + " 高于最大支持 " + normalizedMax
		}
	}
	return true, ""
}

// isSameMajorMinorHigher 对齐 is_same_major_minor_higher_version：仅补丁号更高时放行
func isSameMajorMinorHigher(version, maxVersion string) bool {
	if version == "" || maxVersion == "" {
		return false
	}
	current := parseVersionTriple(version)
	maximum := parseVersionTriple(maxVersion)
	return current[0] == maximum[0] && current[1] == maximum[1] && compareVersions(version, maxVersion) > 0
}

// isValidSemver 对齐 VersionComparator.is_valid_semver：严格 X.Y.Z
func isValidSemver(version string) bool {
	return strictSemverPattern.MatchString(strings.TrimSpace(version))
}

// compareReleaseVersions 按严格三段式比较发布版本，发布版本已通过 semver 校验
func compareReleaseVersions(left, right string) int {
	a := parseVersionTriple(left)
	b := parseVersionTriple(right)
	for i := 0; i < 3; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
