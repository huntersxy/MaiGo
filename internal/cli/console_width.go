package cli

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// displayWidth 返回终端显示宽度，ANSI 控制序列与组合字符不计宽
func displayWidth(text string) int {
	width := 0
	for index := 0; index < len(text); {
		if text[index] == 0x1b {
			next := skipANSISequence(text, index)
			if next > index {
				index = next
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		width += runeDisplayWidth(r)
		index += size
	}
	return width
}

// skipANSISequence 跳过 CSI 序列，返回结束位置；非 ANSI 时原样返回
func skipANSISequence(text string, start int) int {
	if start+1 >= len(text) || text[start] != 0x1b || text[start+1] != '[' {
		return start
	}
	for index := start + 2; index < len(text); index++ {
		if text[index] >= 0x40 && text[index] <= 0x7e {
			return index + 1
		}
	}
	return len(text)
}

// runeDisplayWidth 估算单个字符的终端宽度
func runeDisplayWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if r < 32 || (r >= 0x7f && r < 0xa0) {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

// isWideRune 判断东亚宽字符与常见 emoji
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f:
		return true
	case r == 0x2329 || r == 0x232a:
		return true
	case r >= 0x2e80 && r <= 0xa4cf && r != 0x303f:
		return true
	case r >= 0xac00 && r <= 0xd7a3:
		return true
	case r >= 0xf900 && r <= 0xfaff:
		return true
	case r >= 0xfe10 && r <= 0xfe19:
		return true
	case r >= 0xfe30 && r <= 0xfe6f:
		return true
	case r >= 0xff00 && r <= 0xff60:
		return true
	case r >= 0xffe0 && r <= 0xffe6:
		return true
	case r >= 0x1f300 && r <= 0x1faff:
		return true
	case r >= 0x20000 && r <= 0x3fffd:
		return true
	}
	return false
}

// wrapDisplay 按显示宽度换行，保留原有换行与 ANSI 序列
func wrapDisplay(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	lines := make([]string, 0)
	for _, rawLine := range strings.Split(text, "\n") {
		if displayWidth(rawLine) <= width {
			lines = append(lines, rawLine)
			continue
		}
		var builder strings.Builder
		current := 0
		for index := 0; index < len(rawLine); {
			if rawLine[index] == 0x1b {
				next := skipANSISequence(rawLine, index)
				if next > index {
					builder.WriteString(rawLine[index:next])
					index = next
					continue
				}
			}
			r, size := utf8.DecodeRuneInString(rawLine[index:])
			runeWidth := runeDisplayWidth(r)
			if runeWidth > 0 && current+runeWidth > width {
				lines = append(lines, builder.String())
				builder.Reset()
				current = 0
			}
			builder.WriteString(rawLine[index : index+size])
			current += runeWidth
			index += size
		}
		lines = append(lines, builder.String())
	}
	return lines
}

// truncateDisplay 按显示宽度截断文本并追加省略号
func truncateDisplay(text string, width int) string {
	if width <= 0 || displayWidth(text) <= width {
		return text
	}
	limit := width - 1
	if limit < 0 {
		limit = 0
	}
	var builder strings.Builder
	current := 0
	for index := 0; index < len(text); {
		if text[index] == 0x1b {
			next := skipANSISequence(text, index)
			if next > index {
				builder.WriteString(text[index:next])
				index = next
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		runeWidth := runeDisplayWidth(r)
		if current+runeWidth > limit {
			break
		}
		builder.WriteString(text[index : index+size])
		current += runeWidth
		index += size
	}
	builder.WriteString("…")
	return builder.String()
}

// padDisplayRight 用空格补齐到指定显示宽度
func padDisplayRight(text string, width int) string {
	current := displayWidth(text)
	if current >= width {
		return text
	}
	return text + strings.Repeat(" ", width-current)
}
