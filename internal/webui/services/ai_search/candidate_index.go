package search

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"MaiGo/internal/chat/replyer/jiebacut"
)

// 词典与 Python jieba 0.42.1 一致，来源 internal/replyer/data/jieba_dict.txt.gz
//
//go:embed data/jieba_dict.txt.gz
var searchJiebaDictionaryGz []byte

// queryStopWords 对齐 Python _QUERY_STOP_WORDS
var queryStopWords = map[string]struct{}{
	"什么": {}, "为什么": {}, "为何": {}, "怎么": {}, "怎样": {}, "如何": {},
	"哪里": {}, "是否": {}, "有没有": {}, "可以": {}, "无法": {}, "不能": {},
	"一个": {}, "这个": {}, "那个": {},
}

var (
	searchJiebaDictionaryOnce sync.Once
	searchJiebaDictionary     *jiebacut.Dictionary
	searchJiebaDictionaryErr  error
)

func loadSearchJiebaDictionary() (*jiebacut.Dictionary, error) {
	searchJiebaDictionaryOnce.Do(func() {
		reader, err := gzip.NewReader(bytes.NewReader(searchJiebaDictionaryGz))
		if err != nil {
			searchJiebaDictionaryErr = fmt.Errorf("解压 jieba 词典失败: %w", err)
			return
		}
		defer reader.Close()
		searchJiebaDictionary, searchJiebaDictionaryErr = jiebacut.NewDictionary(reader)
	})
	return searchJiebaDictionary, searchJiebaDictionaryErr
}

// splitQueryTerms 对齐 Python _split_query_terms：
// 按 [\w.\-]+ 取词，中文再走 jieba 搜索模式补两个汉字以上的子词，最后去停用词并保序去重
func splitQueryTerms(query string) []string {
	terms := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	add := func(term string) {
		if term == "" {
			return
		}
		if _, stop := queryStopWords[term]; stop {
			return
		}
		if _, exists := seen[term]; exists {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	for _, token := range queryTokens(strings.ToLower(query)) {
		add(token)
		if !containsCJK(token) {
			continue
		}
		dictionary, err := loadSearchJiebaDictionary()
		if err != nil || dictionary == nil {
			continue
		}
		for _, word := range dictionary.CutForSearch(token) {
			if utf8.RuneCountInString(word) < 2 || !containsCJK(word) {
				continue
			}
			add(word)
		}
	}
	return terms
}

// queryTokens 等价 Python re.findall(r"[\w.\-]+", query)
func queryTokens(query string) []string {
	tokens := make([]string, 0, 4)
	start := -1
	runes := []rune(query)
	flush := func(end int) {
		if start < 0 {
			return
		}
		tokens = append(tokens, string(runes[start:end]))
		start = -1
	}
	for index, r := range runes {
		if isQueryTokenRune(r) {
			if start < 0 {
				start = index
			}
			continue
		}
		flush(index)
	}
	flush(len(runes))
	return tokens
}

func isQueryTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-'
}

func containsCJK(value string) bool {
	for _, r := range value {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// scoreCandidateTerms 对齐 Python _score_terms 的字段权重
func scoreCandidateTerms(terms []string, candidate Candidate) int {
	fields := []struct {
		weight int
		text   string
	}{
		{5, strings.ToLower(candidate.Title)},
		{3, strings.ToLower(candidate.Category)},
		{2, strings.ToLower(candidate.Description)},
		{1, strings.ToLower(candidate.Document)},
	}
	score := 0
	for _, term := range terms {
		for _, field := range fields {
			if strings.Contains(field.text, term) {
				score += field.weight
			}
		}
	}
	return score
}

// searchCandidates 对齐 Python AISearchDocumentStore.search_candidates：
// 空词返回空列表，按分数降序、原始 title 升序排序，并投影为 content
func searchCandidates(query string, candidates []Candidate, limit int) []searchDocument {
	terms := splitQueryTerms(query)
	if len(terms) == 0 || limit <= 0 {
		return []searchDocument{}
	}
	type scored struct {
		candidate Candidate
		score     int
	}
	ranked := make([]scored, 0, len(candidates))
	for _, candidate := range candidates {
		if score := scoreCandidateTerms(terms, candidate); score > 0 {
			ranked = append(ranked, scored{candidate: candidate, score: score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].candidate.Title < ranked[j].candidate.Title
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	documents := make([]searchDocument, 0, len(ranked))
	for _, item := range ranked {
		documents = append(documents, toSearchDocument(item.candidate))
	}
	return documents
}
