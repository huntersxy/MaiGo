package personinfo

import (
	"strconv"
	"strings"

	"MaiGo/internal/person_info/identity"
)

const PlatformQQ = "qq"

// PersonID 生成与 MaiBot get_person_id 一致的 MD5(platform_userID)
func PersonID(platform string, userID any) string {
	return identity.PersonID(platform, userID)
}

// normalizePlatform 对齐 get_person_id 的平台归一：平台含 "-" 时取第二段，不做缺省替换
func normalizePlatform(platform string) string {
	p := strings.TrimSpace(platform)
	if !strings.Contains(p, "-") {
		return p
	}
	// 对齐 Python split("-")[1]：全切后取第二段（"a-b-c" → "b"）
	if parts := strings.SplitN(p, "-", 3); len(parts) > 1 {
		return parts[1]
	}
	return p
}

// ParseQQUserID 将 OneBot 数字 QQ 转为 int64，非数字返回 0
func ParseQQUserID(userID string) int64 {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0
	}
	n, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
