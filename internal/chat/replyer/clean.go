package replyer

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"unicode"
)

// stripChineseParentheticalContent 对齐 utils.py 中
// re.sub(r"[([(（](?=.*[一-鿿]).*?[)\]）]", "") 的行为。
//
// Go 的 RE2 不支持前瞻断言，这里按 Python 语义手写扫描：
//   - 开括号为 ( [ （，闭括号为 ) ] ）；
//   - 前瞻 (?=.*[一-鿿]) 只看"开括号之后到行尾"是否存在中文，
//     闭括号之后出现中文同样算满足；
//   - ".*?" 为惰性匹配，最短匹配到该行内最近的一个闭括号；
//   - Python 的 "." 不匹配换行，因此匹配不会跨行，等价于逐行处理。
func stripChineseParentheticalContent(text string) string {
	if text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = stripChineseParentheticalLine(line)
	}
	return strings.Join(lines, "\n")
}

func stripChineseParentheticalLine(line string) string {
	runes := []rune(line)
	n := len(runes)
	if n == 0 {
		return line
	}
	// suffixCJK[i] 表示 runes[i:] 中是否存在中文，用于等价实现前瞻。
	suffixCJK := make([]bool, n+1)
	for i := n - 1; i >= 0; i-- {
		suffixCJK[i] = suffixCJK[i+1] || isCJKRangeRune(runes[i])
	}
	// nextClosing[i] 表示 >= i 的第一个闭括号下标，没有则为 n。
	nextClosing := make([]int, n+1)
	nextClosing[n] = n
	for i := n - 1; i >= 0; i-- {
		if isClosingBracketRune(runes[i]) {
			nextClosing[i] = i
		} else {
			nextClosing[i] = nextClosing[i+1]
		}
	}

	var b strings.Builder
	b.Grow(len(line))
	pos := 0
	for pos < n {
		start := -1
		for i := pos; i < n; i++ {
			if isOpeningBracketRune(runes[i]) {
				start = i
				break
			}
		}
		if start < 0 {
			b.WriteString(string(runes[pos:]))
			break
		}
		b.WriteString(string(runes[pos:start]))
		end := n
		if suffixCJK[start+1] {
			end = nextClosing[start+1]
		}
		if end >= n {
			// 前瞻不满足或缺少闭括号，该开括号不参与匹配。
			b.WriteRune(runes[start])
			pos = start + 1
			continue
		}
		pos = end + 1
	}
	return b.String()
}

func isOpeningBracketRune(r rune) bool {
	return r == '(' || r == '[' || r == '（'
}

func isClosingBracketRune(r rune) bool {
	return r == ')' || r == ']' || r == '）'
}

// isCJKRangeRune 对应 Python 正则中的 [一-鿿]。
func isCJKRangeRune(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

func westernRatio(paragraph string) float64 {
	var alnum []rune
	for _, r := range paragraph {
		if unicode.IsLetter(r) || unicode.IsNumber(r) { // Python str.isalnum()
			alnum = append(alnum, r)
		}
	}
	if len(alnum) == 0 {
		return 0
	}
	western := 0
	for _, r := range alnum {
		if isEnglishLetter(r) {
			western++
		}
	}
	return float64(western) / float64(len(alnum))
}

func randomDefaultReply(nickname string, rng *rand.Rand) string {
	replies := []string{
		nickname + "不知道哦",
		nickname + "不知道",
		"不知道哦",
		"不知道",
		"不晓得",
		"懒得说",
		"()",
	}
	return replies[rng.IntN(len(replies))]
}

// kaomojiEntry 占位符与原文的有序映射；Python dict 保持插入顺序，恢复时按序 replace
type kaomojiEntry struct {
	placeholder string
	text        string
}

// kaomojiMapping 对应 utils.protect_kaomoji 返回的 placeholder_to_kaomoji
type kaomojiMapping []kaomojiEntry

// kaomojiPattern 逐字移植 utils.py 中的正则：
//
//	([(\[（【][^()\[\]（）【】]*?[^一-龥a-zA-Z0-9\s][^()\[\]（）【】]*?[)\]）】])
//	|
//	([▼▽・ᴥω･﹏^><≧≦￣｀´∀ヮДд︿﹀へ｡ﾟ╥╯╰︶︹•⁄]{2,15})
//
// Python 的 \s 为 Unicode 空白，这里显式列出 str.isspace 覆盖的码位
const pyUnicodeWhitespace = `\x{0020}\t\n\v\f\r\x{001C}-\x{001F}\x{0085}\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}`

var kaomojiPattern = regexp.MustCompile(
	`([(\[（【][^()\[\]（）【】]*?[^一-龥a-zA-Z0-9` + pyUnicodeWhitespace + `][^()\[\]（）【】]*?[)\]）】])` +
		`|([▼▽・ᴥω･﹏^><≧≦￣｀´∀ヮДд︿﹀へ｡ﾟ╥╯╰︶︹•⁄]{2,15})`,
)

// protectKaomoji 移植 utils.protect_kaomoji
func protectKaomoji(text string, enabled bool) (protected string, mapping kaomojiMapping) {
	if !enabled {
		return text, nil
	}
	matches := kaomojiPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	for _, groups := range matches {
		kaomoji := ""
		if len(groups) > 1 && groups[1] != "" {
			kaomoji = groups[1]
		} else if len(groups) > 2 {
			kaomoji = groups[2]
		}
		if kaomoji == "" {
			continue
		}
		// 已标记的表情包引用不参与保护
		if strings.HasPrefix(kaomoji, "[表情包") && strings.HasSuffix(kaomoji, "]") {
			continue
		}
		placeholder := fmt.Sprintf("__KAOMOJI_%d__", len(mapping))
		text = strings.Replace(text, kaomoji, placeholder, 1)
		mapping = append(mapping, kaomojiEntry{placeholder: placeholder, text: kaomoji})
	}
	return text, mapping
}

// recoverKaomoji 移植 utils.recover_kaomoji；逐占位符全量替换
func recoverKaomoji(texts []string, mapping kaomojiMapping, enabled bool) []string {
	if !enabled || len(mapping) == 0 {
		return texts
	}
	out := make([]string, len(texts))
	for i, sentence := range texts {
		for _, entry := range mapping {
			sentence = strings.ReplaceAll(sentence, entry.placeholder, entry.text)
		}
		out[i] = sentence
	}
	return out
}
