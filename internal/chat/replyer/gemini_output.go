package replyer

// 兼容 Gemini 混入可见正文的显式思考格式，对齐 chat/replyer/gemini_output.py

import (
	"regexp"
	"strings"
)

// GeminiReplyFormatError 已识别分析输出，但无法可靠提取正式回复
type GeminiReplyFormatError struct {
	Message string
}

func (e *GeminiReplyFormatError) Error() string { return e.Message }

const geminiHeaderPattern = `(?:thinking\s+process|thought\s+process|思考过程|思维链)`

var (
	geminiAngleStart = regexp.MustCompile(`(?i)\A⌊\s*` + geminiHeaderPattern + `\s*(?:⌉|[:：])`)
	geminiPlainStart = regexp.MustCompile(`(?i)\A(?:#{1,6}\s+)?(?:\*\*)?` + geminiHeaderPattern + `(?:\*\*)?\s*[:：]`)
	geminiXMLStart   = regexp.MustCompile(`(?i)\A<(think|thinking|analysis)>`)
	geminiXMLTag     = regexp.MustCompile(`(?i)</?(think|thinking|analysis)>`)
	geminiAngleEnd   = regexp.MustCompile(`(?m)^\s*⌈`)
)

// NormalizeGeminiReply 只剥离开头有明确边界的思考块；不猜测草稿中的哪一句是正文
func NormalizeGeminiReply(text string) (string, error) {
	remaining := strings.TrimSpace(text)
	changed := false
	for remaining != "" {
		if match := geminiAngleStart.FindStringIndex(remaining); match != nil {
			boundary := geminiAngleEnd.FindStringIndex(remaining[match[1]:])
			if boundary == nil {
				return "", &GeminiReplyFormatError{Message: "Gemini 思考块缺少明确正文分界，已阻止发送。"}
			}
			remaining = strings.TrimSpace(remaining[match[1]+boundary[1]:])
		} else if match := geminiXMLStart.FindStringIndex(remaining); match != nil {
			// 配对扫描处理连续、嵌套块，未闭合时绝不把分析当正文
			stack := make([]string, 0, 2)
			end := -1
			for _, tag := range geminiXMLTag.FindAllStringSubmatchIndex(remaining, -1) {
				full := remaining[tag[0]:tag[1]]
				name := strings.ToLower(remaining[tag[2]:tag[3]])
				if strings.HasPrefix(full, "</") {
					if len(stack) == 0 || stack[len(stack)-1] != name {
						return "", &GeminiReplyFormatError{Message: "Gemini 思考标签配对错误，已阻止发送。"}
					}
					stack = stack[:len(stack)-1]
					if len(stack) == 0 {
						end = tag[1]
						break
					}
				} else {
					stack = append(stack, name)
				}
			}
			if end < 0 {
				return "", &GeminiReplyFormatError{Message: "Gemini 思考标签未闭合，已阻止发送。"}
			}
			remaining = strings.TrimSpace(remaining[end:])
		} else if geminiPlainStart.MatchString(remaining) {
			return "", &GeminiReplyFormatError{Message: "Gemini 分析标题后缺少可靠正文边界，已阻止发送。"}
		} else {
			break
		}
		changed = true
	}
	if changed && remaining == "" {
		return "", &GeminiReplyFormatError{Message: "Gemini 输出仅包含思考内容，已阻止发送。"}
	}
	if !changed {
		return text, nil
	}
	return remaining, nil
}
