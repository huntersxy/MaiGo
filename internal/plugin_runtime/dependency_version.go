package pluginruntime

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PEP440Version 是插件依赖分析所需的 Python 版本子集
// 覆盖 epoch、release、pre/post/dev 与 local 标记，比较顺序对齐 packaging
type PEP440Version struct {
	Epoch   int
	Release []int
	PreKind string
	PreNum  int
	HasPre  bool
	PostNum int
	HasPost bool
	DevNum  int
	HasDev  bool
	Local   string
}

var pep440VersionPattern = regexp.MustCompile(`(?i)^ *v?(?:([0-9]+)!)?([0-9]+(?:[.][0-9]+)*)(?:[-_.]?(a|alpha|b|beta|c|rc|pre|preview)[-_.]?([0-9]+)?)?(?:-([0-9]+)|[-_.]?(post|rev|r)[-_.]?([0-9]+)?)?(?:[-_.]?(dev)[-_.]?([0-9]+)?)?(?:[+]([a-z0-9]+(?:[-_.][a-z0-9]+)*))? *$`)

// ParsePEP440Version 解析 Python 版本号，无法识别时返回 false
func ParsePEP440Version(raw string) (PEP440Version, bool) {
	matches := pep440VersionPattern.FindStringSubmatch(raw)
	if matches == nil {
		return PEP440Version{}, false
	}
	version := PEP440Version{}
	if matches[1] != "" {
		version.Epoch, _ = strconv.Atoi(matches[1])
	}
	for _, part := range strings.Split(matches[2], ".") {
		value, err := strconv.Atoi(part)
		if err != nil {
			return PEP440Version{}, false
		}
		version.Release = append(version.Release, value)
	}
	if matches[3] != "" {
		version.HasPre = true
		version.PreKind = normalizePEP440PreKind(matches[3])
		version.PreNum = 0
		if matches[4] != "" {
			version.PreNum, _ = strconv.Atoi(matches[4])
		}
	}
	if matches[5] != "" {
		version.HasPost = true
		version.PostNum, _ = strconv.Atoi(matches[5])
	} else if matches[6] != "" {
		version.HasPost = true
		version.PostNum = 0
		if matches[7] != "" {
			version.PostNum, _ = strconv.Atoi(matches[7])
		}
	}
	if matches[8] != "" {
		version.HasDev = true
		version.DevNum = 0
		if matches[9] != "" {
			version.DevNum, _ = strconv.Atoi(matches[9])
		}
	}
	if matches[10] != "" {
		version.Local = strings.ToLower(matches[10])
	}
	return version, true
}

func normalizePEP440PreKind(kind string) string {
	switch strings.ToLower(kind) {
	case "a", "alpha":
		return "a"
	case "b", "beta":
		return "b"
	default:
		return "rc"
	}
}

func pep440PreRank(kind string) int {
	switch kind {
	case "a":
		return 0
	case "b":
		return 1
	default:
		return 2
	}
}

// ComparePEP440 按 PEP 440 顺序比较两个版本
func ComparePEP440(left, right PEP440Version) int {
	if left.Epoch != right.Epoch {
		return compareInt(left.Epoch, right.Epoch)
	}
	if cmp := comparePEP440Release(left.Release, right.Release); cmp != 0 {
		return cmp
	}
	if cmp := comparePEP440Pre(left, right); cmp != 0 {
		return cmp
	}
	if cmp := comparePEP440Post(left, right); cmp != 0 {
		return cmp
	}
	if cmp := comparePEP440Dev(left, right); cmp != 0 {
		return cmp
	}
	return comparePEP440Local(left.Local, right.Local)
}

func comparePEP440Release(left, right []int) int {
	size := len(left)
	if len(right) > size {
		size = len(right)
	}
	for index := 0; index < size; index++ {
		leftValue, rightValue := 0, 0
		if index < len(left) {
			leftValue = left[index]
		}
		if index < len(right) {
			rightValue = right[index]
		}
		if leftValue != rightValue {
			return compareInt(leftValue, rightValue)
		}
	}
	return 0
}

func comparePEP440Pre(left, right PEP440Version) int {
	leftRank := pep440PreOrderRank(left)
	rightRank := pep440PreOrderRank(right)
	if leftRank != rightRank {
		return compareInt(leftRank, rightRank)
	}
	if left.HasPre && right.HasPre {
		if leftRank != pep440PreRank(left.PreKind) || rightRank != pep440PreRank(right.PreKind) {
			return 0
		}
		if left.PreNum != right.PreNum {
			return compareInt(left.PreNum, right.PreNum)
		}
	}
	return 0
}

// pep440PreOrderRank 让 dev-only 版本排在任何 pre 之前，正式版排在任何 pre 之后
func pep440PreOrderRank(version PEP440Version) int {
	if !version.HasPre {
		if version.HasDev && !version.HasPost {
			return -1
		}
		return 3
	}
	return pep440PreRank(version.PreKind)
}

func comparePEP440Post(left, right PEP440Version) int {
	leftValue, rightValue := -1, -1
	if left.HasPost {
		leftValue = left.PostNum
	}
	if right.HasPost {
		rightValue = right.PostNum
	}
	return compareInt(leftValue, rightValue)
}

func comparePEP440Dev(left, right PEP440Version) int {
	leftValue, rightValue := 1<<30, 1<<30
	if left.HasDev {
		leftValue = left.DevNum
	}
	if right.HasDev {
		rightValue = right.DevNum
	}
	return compareInt(leftValue, rightValue)
}

func comparePEP440Local(left, right string) int {
	if left == right {
		return 0
	}
	if left == "" {
		return -1
	}
	if right == "" {
		return 1
	}
	leftParts := splitPEP440Local(left)
	rightParts := splitPEP440Local(right)
	size := len(leftParts)
	if len(rightParts) < size {
		size = len(rightParts)
	}
	for index := 0; index < size; index++ {
		leftNumber, leftErr := strconv.Atoi(leftParts[index])
		rightNumber, rightErr := strconv.Atoi(rightParts[index])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber != rightNumber {
				return compareInt(leftNumber, rightNumber)
			}
		case leftErr == nil:
			return 1
		case rightErr == nil:
			return -1
		default:
			if leftParts[index] != rightParts[index] {
				if leftParts[index] < rightParts[index] {
					return -1
				}
				return 1
			}
		}
	}
	return compareInt(len(leftParts), len(rightParts))
}

func splitPEP440Local(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
}

func compareInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func pep440VersionKey(version PEP440Version) string {
	return fmt.Sprintf("%d!%v|%s:%d:%t|%d:%t|%d:%t|%s", version.Epoch, version.Release,
		version.PreKind, version.PreNum, version.HasPre, version.PostNum, version.HasPost,
		version.DevNum, version.HasDev, version.Local)
}

// PEP440Specifier 是单个版本约束子句
type PEP440Specifier struct {
	Operator string
	Version  PEP440Version
	Raw      string
	HasWild  bool
	Wildcard []int
}

// PEP440SpecifierSet 是一组版本约束
type PEP440SpecifierSet struct {
	Specifiers []PEP440Specifier
	Raw        string
}

var pep440SpecifierPattern = regexp.MustCompile(`^[ ]*(===|==|!=|<=|>=|~=|<|>)[ ]*([^ ]+)$`)

// ParsePEP440SpecifierSet 解析逗号分隔的版本约束，非法表达式返回 false
func ParsePEP440SpecifierSet(raw string) (PEP440SpecifierSet, bool) {
	set := PEP440SpecifierSet{Raw: strings.TrimSpace(raw)}
	if set.Raw == "" {
		return set, true
	}
	for _, part := range strings.Split(set.Raw, ",") {
		clause := strings.TrimSpace(part)
		if clause == "" {
			return PEP440SpecifierSet{}, false
		}
		matches := pep440SpecifierPattern.FindStringSubmatch(clause)
		if matches == nil {
			return PEP440SpecifierSet{}, false
		}
		specifier := PEP440Specifier{Operator: matches[1], Raw: matches[2]}
		operator, versionText := matches[1], matches[2]
		if operator == "===" {
			specifier.Raw = versionText
			set.Specifiers = append(set.Specifiers, specifier)
			continue
		}
		if strings.Contains(versionText, "*") {
			if operator != "==" && operator != "!=" {
				return PEP440SpecifierSet{}, false
			}
			if !strings.HasSuffix(versionText, ".*") {
				return PEP440SpecifierSet{}, false
			}
			prefixText := strings.TrimSuffix(versionText, ".*")
			prefixVersion, ok := ParsePEP440Version(prefixText)
			if !ok || prefixVersion.HasPre || prefixVersion.HasPost || prefixVersion.HasDev || prefixVersion.Local != "" {
				return PEP440SpecifierSet{}, false
			}
			specifier.HasWild = true
			specifier.Wildcard = prefixVersion.Release
			set.Specifiers = append(set.Specifiers, specifier)
			continue
		}
		version, ok := ParsePEP440Version(versionText)
		if !ok {
			return PEP440SpecifierSet{}, false
		}
		if operator == "~=" && len(version.Release) < 2 {
			return PEP440SpecifierSet{}, false
		}
		specifier.Version = version
		set.Specifiers = append(set.Specifiers, specifier)
	}
	return set, true
}

// Contains 判断版本是否满足所有约束
func (set PEP440SpecifierSet) Contains(version PEP440Version) bool {
	for _, specifier := range set.Specifiers {
		if !specifier.contains(version) {
			return false
		}
	}
	return true
}

func (specifier PEP440Specifier) contains(version PEP440Version) bool {
	switch specifier.Operator {
	case "===":
		return false // === 需要保留原始文本，由调用方使用 ContainsText
	case "==":
		if specifier.HasWild {
			return hasPEP440ReleasePrefix(version.Release, specifier.Wildcard)
		}
		return pep440Equal(version, specifier.Version)
	case "!=":
		if specifier.HasWild {
			return !hasPEP440ReleasePrefix(version.Release, specifier.Wildcard)
		}
		return !pep440Equal(version, specifier.Version)
	case ">=":
		return ComparePEP440(version, specifier.Version) >= 0
	case "<=":
		return ComparePEP440(version, specifier.Version) <= 0
	case ">":
		return ComparePEP440(version, specifier.Version) > 0
	case "<":
		return ComparePEP440(version, specifier.Version) < 0
	case "~=":
		if ComparePEP440(version, specifier.Version) < 0 {
			return false
		}
		prefix := specifier.Version.Release[:len(specifier.Version.Release)-1]
		return hasPEP440ReleasePrefix(version.Release, prefix)
	default:
		return false
	}
}

func pep440Equal(left, right PEP440Version) bool {
	if right.Local == "" {
		left.Local = ""
	}
	return ComparePEP440(left, right) == 0
}

func hasPEP440ReleasePrefix(release, prefix []int) bool {
	if len(release) < len(prefix) {
		return false
	}
	for index, value := range prefix {
		if release[index] != value {
			return false
		}
	}
	return true
}

// ContainsText 处理 === 任意字符串约束
func (set PEP440SpecifierSet) ContainsText(raw string, version PEP440Version) bool {
	for _, specifier := range set.Specifiers {
		if specifier.Operator == "===" {
			if strings.TrimSpace(raw) != specifier.Raw {
				return false
			}
			continue
		}
		if !specifier.contains(version) {
			return false
		}
	}
	return true
}

// RequirementsMayOverlap 粗略判断两个约束集合是否存在交集
// 对齐 Python ManifestValidator 的候选版本探测法
func RequirementsMayOverlap(left, right PEP440SpecifierSet) bool {
	candidates := buildPEP440CandidateVersions(left, right)
	for _, candidate := range candidates {
		if left.Contains(candidate) && right.Contains(candidate) {
			return true
		}
	}
	return false
}

func buildPEP440CandidateVersions(sets ...PEP440SpecifierSet) []PEP440Version {
	candidates := []PEP440Version{{Release: []int{0, 0, 0}}}
	seen := map[string]struct{}{pep440VersionKey(candidates[0]): {}}
	for _, set := range sets {
		for _, specifier := range set.Specifiers {
			if specifier.Operator == "===" {
				continue
			}
			release := specifier.Version.Release
			if specifier.HasWild {
				release = specifier.Wildcard
			}
			for _, candidate := range expandPEP440Boundary(release) {
				key := pep440VersionKey(candidate)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return ComparePEP440(candidates[i], candidates[j]) < 0
	})
	return candidates
}

func expandPEP440Boundary(release []int) []PEP440Version {
	parts := make([]int, 3)
	copy(parts, release)
	candidates := []PEP440Version{
		{Release: []int{parts[0], parts[1], parts[2]}},
		{Release: []int{parts[0], parts[1], parts[2] + 1}},
	}
	switch {
	case parts[2] > 0:
		candidates = append(candidates, PEP440Version{Release: []int{parts[0], parts[1], parts[2] - 1}})
	case parts[1] > 0:
		candidates = append(candidates, PEP440Version{Release: []int{parts[0], parts[1] - 1, 999}})
	case parts[0] > 0:
		candidates = append(candidates, PEP440Version{Release: []int{parts[0] - 1, 999, 999}})
	}
	return candidates
}
