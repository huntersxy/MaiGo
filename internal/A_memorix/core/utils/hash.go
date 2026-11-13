package utils

// A_memorix 哈希工具，对齐 Python core/utils/hash.py

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

// ComputeHash 计算文本哈希值，对齐 compute_hash
// hashType 省略时对齐 Python 默认值 "sha256"，仅支持 "sha256" 与 "md5"，其它取值返回错误
func ComputeHash(text string, hashType ...string) (string, error) {
	algorithm := "sha256"
	if len(hashType) > 0 {
		algorithm = hashType[0]
	}
	switch algorithm {
	case "sha256":
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:]), nil
	case "md5":
		sum := md5.Sum([]byte(text))
		return hex.EncodeToString(sum[:]), nil
	default:
		return "", fmt.Errorf("不支持的哈希算法: %s", algorithm)
	}
}

// NormalizeText 规范化文本用于哈希计算，对齐 normalize_text
// 依次执行：去除首尾空白、统一换行符为 \n、把非换行的连续空白压缩为一个空格
// 注意 Python 文档字符串声称会删除不可见字符，实际代码并未删除，制表符也会被压成空格
func NormalizeText(text string) string {
	text = strings.TrimFunc(text, pyIsSpace)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var b strings.Builder
	b.Grow(len(text))
	previousSpace := false
	for _, r := range text {
		if r != '\n' && pyIsSpace(r) {
			if !previousSpace {
				b.WriteByte(' ')
			}
			previousSpace = true
			continue
		}
		previousSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// ComputeParagraphHash 计算段落哈希，对齐 compute_paragraph_hash
func ComputeParagraphHash(paragraph string) string {
	hashValue, _ := ComputeHash(NormalizeText(paragraph))
	return hashValue
}

// ComputeEntityHash 计算实体哈希，对齐 compute_entity_hash（仅做 strip + lower）
func ComputeEntityHash(entity string) string {
	hashValue, _ := ComputeHash(pyLower(strings.TrimFunc(entity, pyIsSpace)))
	return hashValue
}

// ComputeRelationHash 计算关系哈希，对齐 compute_relation_hash
// Python 侧是 str(tuple(relation)) 后再哈希，这里用 pyTupleRepr 复刻 Python 的元组字面量
func ComputeRelationHash(relation []string) string {
	hashValue, _ := ComputeHash(pyTupleRepr(relation))
	return hashValue
}

// FormatHashKey 格式化哈希键，对齐 format_hash_key
func FormatHashKey(hashType string, hashValue string) string {
	return hashType + "-" + hashValue
}

// ParseHashKey 解析哈希键，对齐 parse_hash_key（只按第一个 - 切分）
func ParseHashKey(key string) (string, string, error) {
	parts := strings.SplitN(key, "-", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("无效的哈希键格式: %s", key)
	}
	return parts[0], parts[1], nil
}

// pyStrip 对齐 Python str.strip()，语义等同于 strings.TrimFunc(s, pyIsSpace)
func pyStrip(text string) string {
	return strings.TrimFunc(text, pyIsSpace)
}

// pyIsSpace 对齐 Python str.isspace()：Unicode White_Space 之外还包含 0x1c-0x1f
func pyIsSpace(r rune) bool {
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	return unicode.IsSpace(r)
}

// pyLower 对齐 Python str.lower()
// Unicode 中唯一一条无条件的多字符小写映射是 U+0130 -> "i" + U+0307，Go 的 strings.ToLower 会映射成单个 i
func pyLower(text string) string {
	if !strings.ContainsRune(text, 0x130) {
		return strings.ToLower(text)
	}
	var b strings.Builder
	b.Grow(len(text) + 1)
	for _, r := range text {
		if r == 0x130 {
			b.WriteString("i\u0307")
			continue
		}
		b.WriteString(strings.ToLower(string(r)))
	}
	return b.String()
}

// pyTupleRepr 对齐 Python str(tuple(str...)) 的输出，例如 ('a', 'b')、('a',)
func pyTupleRepr(items []string) string {
	if len(items) == 0 {
		return "()"
	}
	var b strings.Builder
	b.WriteByte('(')
	for i, item := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pyStrRepr(item))
	}
	if len(items) == 1 {
		b.WriteByte(',')
	}
	b.WriteByte(')')
	return b.String()
}

// pyStrRepr 对齐 Python repr(str)：优先单引号，含单引号且不含双引号时改用双引号
func pyStrRepr(text string) string {
	quote := '\''
	if strings.ContainsRune(text, '\'') && !strings.ContainsRune(text, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.Grow(len(text) + 2)
	b.WriteRune(quote)
	for _, r := range text {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("\\t")
		case r == '\n':
			b.WriteString("\\n")
		case r == '\r':
			b.WriteString("\\r")
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, "\\x%02x", r)
		case r <= 0xffff:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			fmt.Fprintf(&b, "\\U%08x", r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}
