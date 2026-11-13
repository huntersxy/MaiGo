package pluginruntime

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	manifestSnapshotPattern      = regexp.MustCompile(`-snapshot\.\d+`)
	manifestSimpleVersionPattern = regexp.MustCompile(`^\d+(\.\d+){0,2}$`)
	manifestSemverPattern        = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// normalizeManifestVersion 对齐 VersionComparator.normalize_version
func normalizeManifestVersion(version string) string {
	raw := strings.TrimSpace(version)
	if raw == "" {
		return "0.0.0"
	}
	cleaned := manifestSnapshotPattern.ReplaceAllString(raw, "")
	if parsed, ok := ParsePEP440Version(cleaned); ok {
		parts := make([]string, 0, 3)
		for index := 0; index < 3; index++ {
			if index < len(parsed.Release) {
				parts = append(parts, strconv.Itoa(parsed.Release[index]))
			} else {
				parts = append(parts, "0")
			}
		}
		return strings.Join(parts, ".")
	}
	if !manifestSimpleVersionPattern.MatchString(cleaned) {
		return "0.0.0"
	}
	parts := strings.Split(cleaned, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:3], ".")
}

// parseManifestVersion 对齐 VersionComparator.parse_version
func parseManifestVersion(version string) [3]int {
	normalized := normalizeManifestVersion(version)
	parts := strings.Split(normalized, ".")
	var parsed [3]int
	for index := 0; index < 3 && index < len(parts); index++ {
		value, err := strconv.Atoi(parts[index])
		if err == nil {
			parsed[index] = value
		}
	}
	return parsed
}

// compareManifestVersions 返回 -1、0 或 1
func compareManifestVersions(left, right string) int {
	leftParts := parseManifestVersion(left)
	rightParts := parseManifestVersion(right)
	for index := 0; index < 3; index++ {
		if leftParts[index] < rightParts[index] {
			return -1
		}
		if leftParts[index] > rightParts[index] {
			return 1
		}
	}
	return 0
}

// isManifestVersionInRange 判断闭区间并返回失败原因
func isManifestVersionInRange(version, minVersion, maxVersion string) (bool, string) {
	if strings.TrimSpace(minVersion) == "" && strings.TrimSpace(maxVersion) == "" {
		return true, ""
	}
	normalizedVersion := normalizeManifestVersion(version)
	if strings.TrimSpace(minVersion) != "" {
		normalizedMin := normalizeManifestVersion(minVersion)
		if compareManifestVersions(normalizedVersion, normalizedMin) < 0 {
			return false, "版本 " + normalizedVersion + " 低于最小要求 " + normalizedMin
		}
	}
	if strings.TrimSpace(maxVersion) != "" {
		normalizedMax := normalizeManifestVersion(maxVersion)
		if compareManifestVersions(normalizedVersion, normalizedMax) > 0 {
			return false, "版本 " + normalizedVersion + " 高于最大支持 " + normalizedMax
		}
	}
	return true, ""
}

// isSameMajorMinorHigher 判断是否仅修订号高于声明上限
func isSameMajorMinorHigher(version, maxVersion string) bool {
	if strings.TrimSpace(version) == "" || strings.TrimSpace(maxVersion) == "" {
		return false
	}
	current := parseManifestVersion(version)
	max := parseManifestVersion(maxVersion)
	return current[0] == max[0] && current[1] == max[1] && compareManifestVersions(version, maxVersion) > 0
}

// isValidManifestSemver 对齐 VersionComparator.is_valid_semver
func isValidManifestSemver(version string) bool {
	return manifestSemverPattern.MatchString(strings.TrimSpace(version))
}

// isValidManifestProjectVersion 对齐 VersionComparator.is_valid_project_version
func isValidManifestProjectVersion(version string) bool {
	trimmed := strings.TrimSpace(version)
	return normalizeManifestVersion(trimmed) != "0.0.0" || trimmed == "0.0.0"
}

// manifestVersionMatchesSpecifier 对齐 _version_matches_specifier
func manifestVersionMatchesSpecifier(version, versionSpec string) bool {
	set, ok := ParsePEP440SpecifierSet(versionSpec)
	if !ok {
		return false
	}
	parsed, ok := ParsePEP440Version(strings.TrimSpace(version))
	if !ok {
		return false
	}
	return set.ContainsText(version, parsed)
}
