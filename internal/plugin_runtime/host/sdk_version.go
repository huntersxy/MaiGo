package host

import (
	"regexp"
	"strconv"
	"strings"
)

// sdkVersionSuffixPattern 剥离快照/预发布后缀，对齐 Python
// rpc_server._parse_version_tuple 的 re.split(r"[-.](snapshot|dev|alpha|beta|rc)")。
var sdkVersionSuffixPattern = regexp.MustCompile(`(?i)[-.](?:snapshot|dev|alpha|beta|rc)`)

// parseVersionTuple 把版本号解析成三段整数
//
// 对齐 Python rpc_server._parse_version_tuple：先剥离 -snapshot/-dev/-rc 等后缀，
// 再截断 build 元数据（+xxx），最后按 "." 分段并补齐到 3 段。
// 与 manifest 的 PEP440 比较是两套语义：SDK 版本握手只做简单元组比较。
func parseVersionTuple(version string) ([3]int, bool) {
	var out [3]int
	base := sdkVersionSuffixPattern.Split(version, 2)[0]
	if index := strings.Index(base, "+"); index >= 0 {
		base = base[:index]
	}
	parts := make([]string, 0, 3)
	for _, part := range strings.Split(base, ".") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	for index := 0; index < 3; index++ {
		value, err := strconv.Atoi(parts[index])
		if err != nil {
			return out, false
		}
		out[index] = value
	}
	return out, true
}

// compareVersionTuple 比较两个三段版本号
func compareVersionTuple(left, right [3]int) int {
	for index := 0; index < 3; index++ {
		switch {
		case left[index] < right[index]:
			return -1
		case left[index] > right[index]:
			return 1
		}
	}
	return 0
}
