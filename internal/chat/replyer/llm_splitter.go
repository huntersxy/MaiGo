package replyer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/bytedance/sonic"
)

// LLMSplitterFunc 调用 LLM 并返回原始文本；prompt 已包含待断句正文
// 对应 Python LLMServiceClient(task_name="fast_model", request_type="response.splitter")
type LLMSplitterFunc func(ctx context.Context, prompt string) (string, error)

// splitterPrompt 逐字对齐 src/chat/utils/llm_sentence_splitter.py 的 _SPLITTER_PROMPT
const splitterPrompt = `
请不要思考，快速地把下面的回复进行断句，按自然语义拆成若干条适合逐条发送的消息。
要求：
1. 只能返回 JSON 字符串数组，例如 ["第一句。", "第二句？"]，不要 Markdown、解释或代码块。
2. 只能在原文已有字符之间选择切分位置，不能改写文本、增删或调整顺序。
3. 不要把括号、引号、颜文字或代码从中间拆开。如果是单独的句子，不要在在结尾留逗号。
4. 如果是完整代码，公式，或者整理阅读更合适的文段，不需要拆分，返回"no_split"。
5. 如果是本身比较短，或者表意清晰的句子，不需要拆分，返回"no_split"。

原文：
`

const noSplitMarker = "no_split"

// SplitTextWithLLM 移植 llm_sentence_splitter.split_text_with_llm
// 返回 (内容, 尾随分隔符) 片段；错误语义与 Python 抛出的 ValueError 对齐
func SplitTextWithLLM(ctx context.Context, text string, generate LLMSplitterFunc) ([]sentencePiece, error) {
	if text == "" {
		return nil, nil
	}
	if generate == nil {
		return nil, errors.New("LLM 断句器未接入")
	}
	raw, err := generate(ctx, splitterPrompt+text)
	if err != nil {
		return nil, fmt.Errorf("LLM 断句请求失败: %w", err)
	}
	payload := normalizeJSONPayload(raw)

	if isNoSplitMarker(payload) {
		content, separator := NormalizeTypoSentence(text)
		return []sentencePiece{{content: content, separator: separator}}, nil
	}

	var decoded any
	if err := sonic.UnmarshalString(payload, &decoded); err != nil {
		return nil, errors.New("LLM 断句结果不是合法 JSON 数组")
	}
	if _, isString := decoded.(string); isString {
		return nil, errors.New("LLM 断句结果不是合法 JSON 数组")
	}
	items, ok := decoded.([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("LLM 断句结果必须是非空字符串数组")
	}
	sentences := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("LLM 断句结果必须是非空字符串数组")
		}
		sentences = append(sentences, s)
	}
	if len(sentences) == 1 && sentences[0] == noSplitMarker {
		content, separator := NormalizeTypoSentence(text)
		return []sentencePiece{{content: content, separator: separator}}, nil
	}

	comparisonText := stripWhitespaceAndNormalizePunctuation(text)
	reconstructed := stripWhitespaceAndNormalizePunctuation(strings.Join(sentences, ""))
	if reconstructed != comparisonText {
		return nil, fmt.Errorf(
			"LLM 断句结果未完整保留原文（忽略空白后）：首个差异位置=%d（从 0 开始），原文长度=%d，结果长度=%d，原文片段=%q，结果片段=%q",
			firstMismatchIndex(comparisonText, reconstructed),
			len([]rune(comparisonText)),
			len([]rune(reconstructed)),
			snippetAround(comparisonText, firstMismatchIndex(comparisonText, reconstructed)),
			snippetAround(reconstructed, firstMismatchIndex(comparisonText, reconstructed)),
		)
	}

	original, err := mapSentencesToOriginal(text, sentences)
	if err != nil {
		return nil, err
	}
	pieces := make([]sentencePiece, 0, len(original))
	for _, sentence := range original {
		content, separator := NormalizeTypoSentence(sentence)
		pieces = append(pieces, sentencePiece{content: content, separator: separator})
	}
	return pieces, nil
}

// normalizeJSONPayload 去除模型偶尔添加的 Markdown JSON 代码块包装
func normalizeJSONPayload(payload string) string {
	normalized := strings.TrimSpace(payload)
	if strings.HasPrefix(normalized, "```json") && strings.HasSuffix(normalized, "```") && len(normalized) >= len("```json")+3 {
		return strings.TrimSpace(normalized[7 : len(normalized)-3])
	}
	if strings.HasPrefix(normalized, "```") && strings.HasSuffix(normalized, "```") && len(normalized) >= 6 {
		return strings.TrimSpace(normalized[3 : len(normalized)-3])
	}
	return normalized
}

// isNoSplitMarker 兼容裸 no_split 与 JSON 字符串写法
func isNoSplitMarker(payload string) bool {
	trimmed := strings.TrimSpace(payload)
	trimmed = strings.Trim(trimmed, `"`)
	return strings.TrimSpace(trimmed) == noSplitMarker
}

// punctuationTranslation 对应 Python _PUNCTUATION_TABLE
func punctuationTranslation(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E:
		return r - 0xFEE0
	case r == 0x3000:
		return ' '
	case r == 0x3002:
		return '.'
	case r == 0x3001:
		return ','
	case r == 0x2018 || r == 0x2019:
		return '\''
	case r == 0x201C || r == 0x201D:
		return '"'
	case r == 0x3010:
		return '['
	case r == 0x3011:
		return ']'
	case r == 0x300A:
		return '<'
	case r == 0x300B:
		return '>'
	}
	return r
}

// isPythonWhitespace 对齐 Python str.isspace（含 \x1c-\x1f）
func isPythonWhitespace(r rune) bool {
	if r >= 0x1C && r <= 0x1F {
		return true
	}
	return isUnicodeSpace(r)
}

// stripWhitespaceAndNormalizePunctuation 对应 _strip_whitespace
func stripWhitespaceAndNormalizePunctuation(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if isPythonWhitespace(r) {
			continue
		}
		b.WriteRune(punctuationTranslation(r))
	}
	return b.String()
}

func firstMismatchIndex(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	for i := 0; i < n; i++ {
		if ar[i] != br[i] {
			return i
		}
	}
	return n
}

func snippetAround(text string, index int) string {
	runes := []rune(text)
	start := index - 20
	if start < 0 {
		start = 0
	}
	end := index + 21
	if end > len(runes) {
		end = len(runes)
	}
	if start > len(runes) {
		start = len(runes)
	}
	if end < start {
		end = start
	}
	return string(runes[start:end])
}

// mapSentencesToOriginal 按各句非空白字符数把切分位置映射回原文
func mapSentencesToOriginal(text string, sentences []string) ([]string, error) {
	counts := make([]int, 0, len(sentences))
	for _, sentence := range sentences {
		count := len([]rune(stripWhitespaceAndNormalizePunctuation(sentence)))
		if count > 0 {
			counts = append(counts, count)
		}
	}
	if len(counts) == 0 {
		return nil, nil
	}
	src := []rune(text)
	result := make([]string, 0, len(counts))
	start, consumed, target := 0, 0, 0
	for index, count := range counts {
		target += count
		if index == len(counts)-1 {
			result = append(result, string(src[start:]))
			break
		}
		end := start
		for consumed < target {
			if end >= len(src) {
				return nil, errors.New("LLM 断句结果未完整保留原文（忽略空白后）：原文长度不足")
			}
			if !isPythonWhitespace(src[end]) {
				consumed++
			}
			end++
		}
		sentenceEnd := end
		for end < len(src) && isPythonWhitespace(src[end]) {
			end++
		}
		cut := sentenceEnd
		if sentenceEnd > 0 {
			last := src[sentenceEnd-1]
			if last == '，' || last == ',' {
				cut = end
			}
		}
		result = append(result, string(src[start:cut]))
		start = end
	}
	return result, nil
}

func isUnicodeSpace(r rune) bool {
	return unicode.IsSpace(r)
}
