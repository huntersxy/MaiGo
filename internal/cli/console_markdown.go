package cli

import (
	"strings"
)

// RenderMarkdown 将常用 Markdown 渲染为终端文本；rich 的完整语法不在迁移范围内
func (c *Console) RenderMarkdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	codeFence := strings.Repeat(string(rune(0x60)), 3)
	var builder strings.Builder
	inCode := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, codeFence) {
			inCode = !inCode
			if index != len(lines)-1 {
				builder.WriteString("\n")
			}
			continue
		}
		if inCode {
			builder.WriteString(c.decorate(StyleMuted, line))
			if index != len(lines)-1 {
				builder.WriteString("\n")
			}
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "### "):
			builder.WriteString(c.decorate(StyleAccent, c.renderInline(strings.TrimSpace(trimmed[4:]))))
		case strings.HasPrefix(trimmed, "## "):
			builder.WriteString(c.decorate(StyleAccent, c.renderInline(strings.TrimSpace(trimmed[3:]))))
		case strings.HasPrefix(trimmed, "# "):
			builder.WriteString(c.decorate(StyleAccent, c.renderInline(strings.TrimSpace(trimmed[2:]))))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			builder.WriteString("• ")
			builder.WriteString(c.renderInline(strings.TrimSpace(trimmed[2:])))
		case strings.HasPrefix(trimmed, "> "):
			builder.WriteString(c.decorate(StyleMuted, c.renderInline(strings.TrimSpace(trimmed[2:]))))
		default:
			builder.WriteString(c.renderInline(line))
		}
		if index != len(lines)-1 {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// Markdown 渲染并输出 Markdown 文本
func (c *Console) Markdown(text string) {
	c.Print(c.RenderMarkdown(text))
}

// renderInline 处理行内粗体、斜体、行内代码与链接
func (c *Console) renderInline(text string) string {
	var builder strings.Builder
	for index := 0; index < len(text); {
		switch {
		case strings.HasPrefix(text[index:], "**"):
			end := strings.Index(text[index+2:], "**")
			if end >= 0 {
				builder.WriteString(c.decorate(StyleBold, text[index+2:index+2+end]))
				index += 2 + end + 2
				continue
			}
		case text[index] == 0x60:
			end := strings.IndexByte(text[index+1:], 0x60)
			if end >= 0 {
				builder.WriteString(c.decorate(StyleCyan, text[index+1:index+1+end]))
				index += 1 + end + 1
				continue
			}
		case text[index] == '[':
			if label, target, next, ok := parseMarkdownLink(text, index); ok {
				builder.WriteString(c.renderInline(label))
				if target != "" && target != label {
					builder.WriteString(c.decorate(StyleMuted, " ("+target+")"))
				}
				index = next
				continue
			}
		case text[index] == '*':
			end := strings.IndexByte(text[index+1:], '*')
			if end > 0 {
				builder.WriteString(c.decorate(StyleItalic, text[index+1:index+1+end]))
				index += 1 + end + 1
				continue
			}
		}
		builder.WriteByte(text[index])
		index++
	}
	return builder.String()
}

// parseMarkdownLink 解析 [label](target)，返回下一位置
func parseMarkdownLink(text string, start int) (label, target string, next int, ok bool) {
	closeLabel := strings.IndexByte(text[start+1:], ']')
	if closeLabel < 0 {
		return "", "", start, false
	}
	closeLabel += start + 1
	if closeLabel+1 >= len(text) || text[closeLabel+1] != '(' {
		return "", "", start, false
	}
	closeTarget := strings.IndexByte(text[closeLabel+2:], ')')
	if closeTarget < 0 {
		return "", "", start, false
	}
	closeTarget += closeLabel + 2
	return text[start+1 : closeLabel], text[closeLabel+2 : closeTarget], closeTarget + 1, true
}
