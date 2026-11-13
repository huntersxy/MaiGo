package messagereceive

import (
	"fmt"
	"regexp"
	"strings"
)

// BanMessageFilter 按配置顺序执行屏蔽词与屏蔽正则匹配
// 顺序固定为配置中的先后顺序，命中即停止，与 Python check_ban_words/check_ban_regex 一致
type BanMessageFilter struct {
	words    []string
	patterns []*regexp.Regexp
	sources  []string
}

// NewBanMessageFilter 编译屏蔽词与屏蔽正则
// 非法正则在装载期返回真实错误，对齐 Python MessageReceiveConfig 的配置期校验
func NewBanMessageFilter(words, patterns []string) (*BanMessageFilter, error) {
	filter := &BanMessageFilter{
		words:    append([]string(nil), words...),
		patterns: make([]*regexp.Regexp, 0, len(patterns)),
		sources:  append([]string(nil), patterns...),
	}
	for _, pattern := range patterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("Invalid regex pattern in ban_msgs_regex: '%s'", pattern)
		}
		filter.patterns = append(filter.patterns, compiled)
	}
	return filter, nil
}

// FirstBanWord 返回第一个命中的屏蔽词；空文本不匹配
func (f *BanMessageFilter) FirstBanWord(text string) string {
	if f == nil || text == "" {
		return ""
	}
	for _, word := range f.words {
		if word == "" {
			continue
		}
		if strings.Contains(text, word) {
			return word
		}
	}
	return ""
}

// FirstBanRegex 返回第一个命中的屏蔽正则原文；空文本不匹配
func (f *BanMessageFilter) FirstBanRegex(text string) string {
	if f == nil || text == "" {
		return ""
	}
	for index, pattern := range f.patterns {
		if pattern == nil {
			continue
		}
		if pattern.MatchString(text) {
			return f.sources[index]
		}
	}
	return ""
}
