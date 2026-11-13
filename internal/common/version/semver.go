package version

import (
	"fmt"
	"strconv"
	"strings"
)

// SemVer 严格三段式语义版本
type SemVer struct {
	Major int
	Minor int
	Patch int
}

// ParseSemVer 解析 X.Y.Z 严格版本，不接受前缀、后缀或前导零
func ParseSemVer(value string) (SemVer, error) {
	text := strings.TrimSpace(value)
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return SemVer{}, fmt.Errorf("版本号必须为 X.Y.Z 格式: %q", value)
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return SemVer{}, fmt.Errorf("版本号必须为 X.Y.Z 格式: %q", value)
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return SemVer{}, fmt.Errorf("版本号必须为 X.Y.Z 格式: %q", value)
		}
		numbers[index] = number
	}
	return SemVer{Major: numbers[0], Minor: numbers[1], Patch: numbers[2]}, nil
}

// IsValidSemVer 判断是否严格三段式版本
func IsValidSemVer(value string) bool {
	_, err := ParseSemVer(value)
	return err == nil
}

// CompareSemVer 比较两个严格版本，返回 -1、0 或 1
func CompareSemVer(left, right string) (int, error) {
	a, err := ParseSemVer(left)
	if err != nil {
		return 0, err
	}
	b, err := ParseSemVer(right)
	if err != nil {
		return 0, err
	}
	if a.Major != b.Major {
		return sign(a.Major - b.Major), nil
	}
	if a.Minor != b.Minor {
		return sign(a.Minor - b.Minor), nil
	}
	if a.Patch != b.Patch {
		return sign(a.Patch - b.Patch), nil
	}
	return 0, nil
}

// CompareLoose 将非严格版本归一为三段数字后比较，非法部分按 0 处理
func CompareLoose(left, right string) int {
	a := looseNumbers(left)
	b := looseNumbers(right)
	for index := 0; index < 3; index++ {
		if a[index] != b[index] {
			return sign(a[index] - b[index])
		}
	}
	return 0
}

func looseNumbers(value string) [3]int {
	normalized := strings.TrimSpace(value)
	normalized = strings.TrimPrefix(strings.ToLower(normalized), "v")
	if index := strings.Index(normalized, "-"); index >= 0 {
		normalized = normalized[:index]
	}
	parts := strings.Split(normalized, ".")
	out := [3]int{}
	for index := 0; index < len(parts) && index < 3; index++ {
		digits := make([]byte, 0, len(parts[index]))
		for i := 0; i < len(parts[index]); i++ {
			if parts[index][i] >= '0' && parts[index][i] <= '9' {
				digits = append(digits, parts[index][i])
			}
		}
		number, err := strconv.Atoi(string(digits))
		if err == nil && number >= 0 {
			out[index] = number
		}
	}
	return out
}

func sign(value int) int {
	if value < 0 {
		return -1
	}
	if value > 0 {
		return 1
	}
	return 0
}
