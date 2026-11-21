package chinesetypo

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/dlclark/regexp2"
)

type reactionRule struct {
	keywords []string
	patterns []*regexp2.Regexp
	reaction string
}

func compileRules(rules []keywordRule) []reactionRule {
	compiled := make([]reactionRule, 0, len(rules))
	for _, rule := range rules {
		entry := reactionRule{reaction: trimSpace(rule.reaction)}
		if entry.reaction == "" {
			continue
		}
		for _, keyword := range rule.keywords {
			if keyword := trimSpace(keyword); keyword != "" {
				entry.keywords = append(entry.keywords, keyword)
			}
		}
		for _, pattern := range rule.patterns {
			if trimSpace(pattern) == "" {
				continue
			}
			if re, err := regexp2.Compile(pythonPattern(pattern), 0); err == nil {
				re.MatchTimeout = 750 * time.Millisecond
				entry.patterns = append(entry.patterns, re)
			}
		}
		compiled = append(compiled, entry)
	}
	return compiled
}

func (r reactionRule) match(ctx context.Context, text string) (string, bool) {
	for _, keyword := range r.keywords {
		if strings.Contains(text, keyword) {
			return r.reaction, true
		}
	}
	for _, re := range r.patterns {
		if ctx.Err() != nil {
			return "", false
		}
		match, err := re.FindStringMatch(text)
		if err != nil || match == nil {
			continue
		}
		reaction := r.reaction
		for _, name := range re.GetGroupNames() {
			if name[0] >= '0' && name[0] <= '9' {
				continue
			}
			value := ""
			if group := match.GroupByName(name); group != nil {
				value = group.String()
			}
			reaction = strings.ReplaceAll(reaction, "["+name+"]", value)
		}
		return reaction, true
	}
	return "", false
}

func pythonPattern(pattern string) string {
	var converted strings.Builder
	inClass := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			if i+1 < len(pattern) {
				i++
				class := ""
				switch pattern[i] {
				case 'w':
					class = `\p{L}\p{N}_`
				case 's':
					class = `\s\x1c-\x1f`
				case 'W', 'S':
					if !inClass {
						class = `^\p{L}\p{N}_`
						if pattern[i] == 'S' {
							class = `^\s\x1c-\x1f`
						}
					}
				case 'Z':
					converted.WriteString(`\z`)
					continue
				}
				if class != "" {
					if !inClass {
						converted.WriteByte('[')
					}
					converted.WriteString(class)
					if !inClass {
						converted.WriteByte(']')
					}
					continue
				}
				converted.WriteByte('\\')
			}
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if !inClass && strings.HasPrefix(pattern[i:], `(?P<`) {
				converted.WriteString(`(?<`)
				i += 3
				continue
			}
			if !inClass && strings.HasPrefix(pattern[i:], `(?P=`) {
				if name, _, ok := strings.Cut(pattern[i+4:], ")"); ok {
					converted.WriteString(`\k<` + name + `>`)
					i += 4 + len(name)
					continue
				}
			}
		}
		converted.WriteByte(pattern[i])
	}
	return converted.String()
}

func trimSpace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f })
}
