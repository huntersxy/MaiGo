package chinesetypo

import (
	"context"
	_ "embed"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"MaiGo/internal/chat/replyer/jiebacut"

	"github.com/dlclark/regexp2"
)

//go:embed typo_words.tsv
var typoWords string

type wordReplacement struct {
	original string
	length   int
	variants []string
}

var dictionaryReplacements = sync.OnceValues(func() (map[rune][]wordReplacement, error) {
	type location struct {
		first rune
		index int
	}
	words := make(map[rune][]wordReplacement)
	locations := make(map[string]location)
	for line := range strings.Lines(typoWords) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		original, replacement, ok := strings.Cut(line, "\t")
		if !ok || original == "" || replacement == "" {
			return nil, fmt.Errorf("无效的错字词库词条")
		}
		loc, exists := locations[original]
		if !exists {
			first, _ := utf8.DecodeRuneInString(original)
			loc = location{first, len(words[first])}
			locations[original] = loc
			words[first] = append(words[first], wordReplacement{original: original, length: utf8.RuneCountInString(original)})
		}
		entry := &words[loc.first][loc.index]
		entry.variants = append(entry.variants, replacement)
	}
	return words, nil
})

var protectionPatterns = sync.OnceValue(func() []*regexp2.Regexp {
	patterns := []string{
		"(?<!`)(`+)(?!`)[\\s\\S]*?(?<!`)\\1(?!`)",
		`(?<!~)(~{3,})(?!~)[\s\S]*?(?<!~)\1(?!~)`,
		`“[^”]*”|‘[^’]*’|「[^」]*」|『[^』]*』|《[^》]*》|"(?:\\.|[^"\\])*"`,
		`(?<!\w)'[^'\n]+'(?!\w)`,
		"(?i)(?:https?://|www\\.)[^\\s<>\"'`，。！？；、）】]+",
		`[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}|@[\w.-]+`,
		"(?:[A-Za-z]:[\\\\/]|\\\\\\\\|\\.{1,2}[\\\\/]|/)[^\\s<>\"'`，。！？；、）】]+",
		`(?:[\w.-]+[\\/])+[\w.-]+`,
	}
	out := make([]*regexp2.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re := regexp2.MustCompile(pythonPattern(pattern), 0)
		re.MatchTimeout = 750 * time.Millisecond
		out = append(out, re)
	}
	return out
})

type textSpan struct{ start, end int }

func protectedSpans(ctx context.Context, text string) ([]textSpan, error) {
	var result []textSpan
	for _, pattern := range protectionPatterns() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		match, err := pattern.FindStringMatch(text)
		for err == nil && match != nil {
			result = append(result, textSpan{match.Index, match.Index + match.Length})
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			match, err = pattern.FindNextMatch(match)
		}
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

type dictionaryEdit struct {
	part           int
	text, original string
}

func dictionaryTypo(ctx context.Context, parts []replyPart, chance float64, random *rand.Rand) (*dictionaryEdit, error) {
	if len(parts) == 0 || chance <= 0 || random.Float64() >= chance {
		return nil, nil
	}
	var whole strings.Builder
	for _, part := range parts {
		whole.WriteString(part.text)
		whole.WriteString(part.separator)
	}
	spans, err := protectedSpans(ctx, whole.String())
	if err != nil {
		return nil, err
	}
	words, err := dictionaryReplacements()
	if err != nil {
		return nil, err
	}
	tokenizer, err := jiebacut.DefaultDictionary()
	if err != nil {
		return nil, err
	}
	fullText := []rune(whole.String())
	type candidate struct {
		part, start, end int
		original         string
		variants         []string
	}
	var candidates []candidate
	offset := 0
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text := []rune(part.text)
		tokens := tokenizer.Cut(part.text)
		ends := make(map[int]bool, len(tokens))
		end := 0
		for _, token := range tokens {
			end += utf8.RuneCountInString(token)
			ends[end] = true
		}
		start := 0
		for _, token := range tokens {
			if token == "" {
				continue
			}
			for _, entry := range words[text[start]] {
				end := start + entry.length
				if !ends[end] || string(text[start:end]) != entry.original || intersects(spans, offset+start, offset+end) {
					continue
				}
				var allowed []string
				for _, variant := range entry.variants {
					first, _ := utf8.DecodeRuneInString(variant)
					last, _ := utf8.DecodeLastRuneInString(variant)
					before, after := offset+start-1, offset+end
					if (before >= 0 && fullText[before] == first && first != text[start]) ||
						(after < len(fullText) && fullText[after] == last && last != text[end-1]) {
						continue
					}
					allowed = append(allowed, variant)
				}
				if len(allowed) != 0 {
					candidates = append(candidates, candidate{index, start, end, entry.original, allowed})
				}
			}
			start += utf8.RuneCountInString(token)
		}
		offset += len(text) + utf8.RuneCountInString(part.separator)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	edit := candidates[random.IntN(len(candidates))]
	text := []rune(parts[edit.part].text)
	return &dictionaryEdit{edit.part, string(text[:edit.start]) + edit.variants[random.IntN(len(edit.variants))] + string(text[edit.end:]), edit.original}, nil
}

func intersects(spans []textSpan, start, end int) bool {
	for _, span := range spans {
		if start < span.end && end > span.start {
			return true
		}
	}
	return false
}
