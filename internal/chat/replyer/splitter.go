package replyer

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"unicode"
)

var (
	splitterNewlineCollapse = regexp.MustCompile(`\n\s*\n+`)
	splitterNewlineBefore   = regexp.MustCompile(`\n\s*([，,。;\s])`)
	splitterNewlineAfter    = regexp.MustCompile(`([，,。;\s])\s*\n`)
	splitterInnerNewlines   = regexp.MustCompile(`[^\S\r\n]*[\r\n]+[^\S\r\n]*`)
)

// sentencePiece 句子内容与尾随分隔符，对齐 Python (content, sep) 元组
type sentencePiece struct {
	content   string
	separator string
}

// SplitIntoSentenceSegments 规则断句，移植自 utils._split_into_sentence_segments
func SplitIntoSentenceSegments(text string, rng *rand.Rand) []sentencePiece {
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 2))
	}

	text = splitterNewlineCollapse.ReplaceAllString(text, "\n")
	text = splitterNewlineBefore.ReplaceAllString(text, "\n$1")
	text = splitterNewlineAfter.ReplaceAllString(text, "$1\n")

	lenText := len([]rune(text))
	if lenText < 3 {
		if rng.Float64() < 0.01 {
			out := make([]sentencePiece, 0, lenText)
			for _, r := range text {
				out = append(out, sentencePiece{content: string(r), separator: ""})
			}
			return out
		}
		return []sentencePiece{{content: text, separator: ""}}
	}

	runes := []rune(text)
	insideQuote := buildInsideQuoteFlags(string(runes))

	separators := map[rune]struct{}{
		'，': {}, ',': {}, ' ': {}, '。': {}, ';': {}, '\n': {},
	}

	segments := make([]sentencePiece, 0, 8)
	current := strings.Builder{}

	flush := func(sep rune) {
		content := current.String()
		if content != "" {
			segments = append(segments, sentencePiece{content: content, separator: string(sep)})
			current.Reset()
			return
		}
		if sep == ' ' || sep == '\n' {
			segments = append(segments, sentencePiece{content: "", separator: string(sep)})
		}
	}

	for i, char := range runes {
		if _, ok := separators[char]; !ok {
			current.WriteRune(char)
			continue
		}

		canSplit := false
		if insideQuote[i] {
			canSplit = false
		} else if char == '\n' {
			canSplit = true
		} else {
			canSplit = true
			if i > 0 {
				prev := runes[i-1]
				if prev == ':' || prev == '：' {
					canSplit = false
				}
			}
			if i < len(runes)-1 {
				next := runes[i+1]
				if next == ':' || next == '：' {
					canSplit = false
				}
			}
			if canSplit && char == ' ' && i > 0 && i < len(runes)-1 {
				prev, next := runes[i-1], runes[i+1]
				if prev == '-' || prev == '—' || next == '-' || next == '—' {
					canSplit = false
				} else if isAlnumLatinOrDigit(prev) && isAlnumLatinOrDigit(next) {
					canSplit = false
				}
			}
		}

		if canSplit {
			flush(char)
		} else {
			current.WriteRune(char)
		}
	}

	if tail := current.String(); tail != "" {
		segments = append(segments, sentencePiece{content: tail, separator: ""})
	}

	segments = filterEmptyPieces(segments)
	if len(segments) == 0 {
		if text != "" {
			return []sentencePiece{{content: text, separator: ""}}
		}
		return nil
	}

	return mergeSentencePiecesProbabilistically(segments, lenText, rng)
}

func filterEmptyPieces(in []sentencePiece) []sentencePiece {
	out := make([]sentencePiece, 0, len(in))
	for _, p := range in {
		if p.content != "" || p.separator != "" {
			out = append(out, p)
		}
	}
	return out
}

func mergeSentencePiecesProbabilistically(segments []sentencePiece, lenText int, rng *rand.Rand) []sentencePiece {
	var splitStrength float64
	switch {
	case lenText < 12:
		splitStrength = 0.2
	case lenText < 32:
		splitStrength = 0.6
	default:
		splitStrength = 0.7
	}
	mergeProbability := 1.0 - splitStrength

	merged := make([]sentencePiece, 0, len(segments))
	idx := 0
	for idx < len(segments) {
		cur := segments[idx]
		if idx+1 < len(segments) && cur.content != "" && cur.separator != "\n" && rng.Float64() < mergeProbability {
			next := segments[idx+1]
			if next.content != "" {
				merged = append(merged, sentencePiece{
					content:   cur.content + cur.separator + next.content,
					separator: next.separator,
				})
			} else {
				merged = append(merged, sentencePiece{content: cur.content, separator: next.separator})
			}
			idx += 2
			continue
		}
		merged = append(merged, cur)
		idx++
	}

	out := make([]sentencePiece, 0, len(merged))
	for _, p := range merged {
		if strings.TrimSpace(p.content) == "" {
			continue
		}
		normalized := strings.TrimSpace(splitterInnerNewlines.ReplaceAllString(p.content, " "))
		if normalized == "" {
			continue
		}
		out = append(out, sentencePiece{content: normalized, separator: p.separator})
	}
	return out
}

func buildInsideQuoteFlags(text string) []bool {
	quoteChars := map[rune]struct{}{
		'"': {}, '\'': {}, '“': {}, '”': {}, '‘': {}, '’': {},
		'「': {}, '」': {}, '『': {}, '』': {},
	}
	runes := []rune(text)
	flags := make([]bool, len(runes))
	inQuote := false
	var currentQuote rune
	for i, ch := range runes {
		if _, ok := quoteChars[ch]; ok {
			if !inQuote {
				inQuote = true
				currentQuote = ch
				flags[i] = false
			} else {
				if ch == currentQuote || (ch == '"' || ch == '\'') && (currentQuote == '"' || currentQuote == '\'') {
					inQuote = false
					currentQuote = 0
				}
				flags[i] = false
			}
			continue
		}
		flags[i] = inQuote
	}
	return flags
}

func isEnglishLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isAlnumLatinOrDigit(r rune) bool {
	return unicode.IsDigit(r) || isEnglishLetter(r)
}
