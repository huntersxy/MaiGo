package learners

import "unicode"

// itoa 无依赖整数转字符串，避免为日志与预览引入 fmt 开销
func jargonItoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// IsSingleCharJargon 对齐 expression_utils.is_single_char_jargon
func IsSingleCharJargon(content string) bool {
	runes := []rune(content)
	if len(runes) != 1 {
		return false
	}
	ch := runes[0]
	return unicode.Is(unicode.Han, ch) ||
		(ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9')
}
