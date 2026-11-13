package prompt

import (
	"fmt"
	"strings"
	"unicode"
)

// 与 MaiBot PromptManager 相同的转义字符，便于在模板中写 JSON 示例
const (
	leftBraceEscape  = "\uFDE9"
	rightBraceEscape = "\uFDEA"
)

const maxRenderDepth = 10

// escapeLiteralBraces 将 {{ / }} 转为占位符，避免与字段占位符冲突
func escapeLiteralBraces(template string) string {
	template = strings.ReplaceAll(template, "{{", leftBraceEscape)
	return strings.ReplaceAll(template, "}}", rightBraceEscape)
}

func restoreLiteralBraces(rendered string) string {
	rendered = strings.ReplaceAll(rendered, leftBraceEscape, "{")
	return strings.ReplaceAll(rendered, rightBraceEscape, "}")
}

// extractFieldNames 解析模板中的命名字段，忽略已转义的大括号
func extractFieldNames(template string) []string {
	escaped := escapeLiteralBraces(template)
	seen := make(map[string]struct{})
	var names []string
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '{' {
			continue
		}
		j := i + 1
		if j >= len(escaped) {
			break
		}
		if !isFieldStart(rune(escaped[j])) {
			continue
		}
		start := j
		for j < len(escaped) && isFieldPart(rune(escaped[j])) {
			j++
		}
		if j >= len(escaped) || escaped[j] != '}' {
			continue
		}
		name := escaped[start:j]
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			names = append(names, name)
		}
		i = j
	}
	return names
}

func isFieldStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isFieldPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

type fieldResolver func(name string) (string, bool, error)

func renderTemplate(template string, resolve fieldResolver, depth int) (string, error) {
	if depth > maxRenderDepth {
		return "", fmt.Errorf("prompt 渲染递归过深（>%d 层）", maxRenderDepth)
	}
	if strings.Contains(template, "{}") {
		escaped := escapeLiteralBraces(template)
		if strings.Contains(escaped, "{}") {
			return "", fmt.Errorf("模板中不允许未命名占位符 {}")
		}
	}

	escaped := escapeLiteralBraces(template)
	fields := extractFieldNames(template)
	values := make(map[string]string, len(fields))
	for _, name := range fields {
		value, ok, err := resolve(name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("缺少占位符 %q 的值", name)
		}
		values[name] = value
	}

	var b strings.Builder
	b.Grow(len(escaped))
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '{' {
			b.WriteByte(escaped[i])
			continue
		}
		j := i + 1
		if j >= len(escaped) || !isFieldStart(rune(escaped[j])) {
			b.WriteByte(escaped[i])
			continue
		}
		start := j
		for j < len(escaped) && isFieldPart(rune(escaped[j])) {
			j++
		}
		if j >= len(escaped) || escaped[j] != '}' {
			b.WriteByte(escaped[i])
			continue
		}
		name := escaped[start:j]
		b.WriteString(values[name])
		i = j
	}
	return restoreLiteralBraces(b.String()), nil
}
