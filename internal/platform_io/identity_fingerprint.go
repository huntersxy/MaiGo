package platformio

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// IdentityTextFingerprint 计算 QQ 官方与 SnowLuma 共享的纯文本身份指纹。
func IdentityTextFingerprint(segments []map[string]any) string {
	var parts strings.Builder
	for _, segment := range segments {
		switch segment["type"] {
		case "at", "reply":
		case "text":
			text, ok := segment["data"].(string)
			if !ok {
				return ""
			}
			parts.WriteString(text)
		default:
			return ""
		}
	}
	if utf8.RuneCountInString(parts.String()) > 2048 {
		return ""
	}
	text := strings.Join(strings.FieldsFunc(norm.NFKC.String(parts.String()), func(r rune) bool {
		return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
	}), " ")
	if utf8.RuneCountInString(text) < 8 || strings.HasPrefix(text, "/") {
		return ""
	}
	unique := make(map[rune]struct{})
	for _, r := range text {
		unique[r] = struct{}{}
		if len(unique) == 4 {
			sum := sha256.Sum256([]byte(text))
			return hex.EncodeToString(sum[:])
		}
	}
	return ""
}
