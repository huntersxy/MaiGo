package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
)

// 官方文档搜索与读取对齐 MaiBot src/webui/services/ai_search_documents.py
const (
	officialDocsBaseURL         = "https://docs.maibot.chat"
	officialDocsBundleURL       = officialDocsBaseURL + "/llms-full.txt"
	officialDocsCacheTTL        = 600 * time.Second
	officialDocsMaxBundleSize   = 2_000_000
	officialDocsMaxReadSize     = 8_000
	officialDocsMaxReadCount    = 4
	officialDocsSearchMaxLimit  = 8
	officialDocsSnippetLead     = 160
	officialDocsSnippetSize     = 500
	aiSearchDefaultToolLimit    = 6
	aiSearchMaxToolQueryLength  = 200
	officialDocsDownloadTimeout = 15 * time.Second
)

var officialDocHeaderPattern = regexp.MustCompile("(?m)^---[ \\t\\f\\v]*\\r?\\nurl:[ \\t\\f\\v]*([^\\r\\n]*?)[ \\t\\f\\v]*\\r?\\n---[ \\t\\f\\v]*\\r?\\n")

var officialDocTitlePattern = regexp.MustCompile("(?m)^#[ \\t]+(.+)$")

var officialDocsQueryStopWords = map[string]struct{}{
	"什么": {}, "为什么": {}, "为何": {}, "怎么": {}, "怎样": {}, "如何": {},
	"哪里": {}, "是否": {}, "有没有": {}, "可以": {}, "无法": {}, "不能": {},
	"一个": {}, "这个": {}, "那个": {},
}

// OfficialDocument 是官方 llms-full.txt 文档包中的单篇文档
type OfficialDocument struct {
	Path    string
	Title   string
	Content string

	pathLower    string
	titleLower   string
	contentLower string
}

// OfficialDocSearchResult 对应 search_official_docs 返回的单条文档片段
type OfficialDocSearchResult struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	URL           string `json:"url"`
	Length        int    `json:"length"`
	SnippetOffset int    `json:"snippet_offset"`
	Snippet       string `json:"snippet"`
}

// OfficialDocReadResult 对应 read_official_docs 返回的单篇正文
type OfficialDocReadResult struct {
	SourceID   string `json:"source_id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Offset     int    `json:"offset"`
	Length     int    `json:"length"`
	Content    string `json:"content"`
	NextOffset *int   `json:"next_offset,omitempty"`
}

// OfficialDocSource 是可点击的官方文档来源
type OfficialDocSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// AISearchDocumentOptions 允许替换文档包地址、HTTP 客户端和缓存有效期
type AISearchDocumentOptions struct {
	HTTPClient *http.Client
	BundleURL  string
	BaseURL    string
	CacheTTL   time.Duration
}

type officialDocsRefresh struct {
	done chan struct{}
	docs []OfficialDocument
	err  error
}

// AISearchDocumentStore 下载并缓存官方文档包，同时执行确定性关键词检索
type AISearchDocumentStore struct {
	mu sync.Mutex

	cache   []OfficialDocument
	expires time.Time
	refresh *officialDocsRefresh

	httpClient *http.Client
	bundleURL  string
	baseURL    string
	cacheTTL   time.Duration
	now        func() time.Time
}

var (
	defaultAISearchDocumentStoreOnce sync.Once
	defaultAISearchDocumentStore     *AISearchDocumentStore
)

// NewAISearchDocumentStore 构造官方文档仓库
func NewAISearchDocumentStore(opts AISearchDocumentOptions) *AISearchDocumentStore {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: officialDocsDownloadTimeout}
	}
	bundleURL := strings.TrimSpace(opts.BundleURL)
	if bundleURL == "" {
		bundleURL = officialDocsBundleURL
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = officialDocsBaseURL
	}
	ttl := opts.CacheTTL
	if ttl <= 0 {
		ttl = officialDocsCacheTTL
	}
	return &AISearchDocumentStore{
		httpClient: client,
		bundleURL:  bundleURL,
		baseURL:    baseURL,
		cacheTTL:   ttl,
		now:        time.Now,
	}
}

// DefaultAISearchDocumentStore 返回进程级共享文档仓库
func DefaultAISearchDocumentStore() *AISearchDocumentStore {
	defaultAISearchDocumentStoreOnce.Do(func() {
		defaultAISearchDocumentStore = NewAISearchDocumentStore(AISearchDocumentOptions{})
	})
	return defaultAISearchDocumentStore
}

// SearchOfficialDocs 搜索官方文档并返回相关片段
func (s *AISearchDocumentStore) SearchOfficialDocs(ctx context.Context, query string, limit int) ([]OfficialDocSearchResult, error) {
	if s == nil {
		return nil, fmt.Errorf("官方文档仓库未初始化")
	}
	if limit <= 0 {
		return []OfficialDocSearchResult{}, nil
	}
	documents, err := s.loadOfficialDocs(ctx)
	if err != nil {
		return nil, err
	}
	terms := splitOfficialDocsQueryTerms(query)
	if len(terms) == 0 {
		return []OfficialDocSearchResult{}, nil
	}

	type rankedDocument struct {
		score    int
		document OfficialDocument
	}
	ranked := make([]rankedDocument, 0, len(documents))
	for _, document := range documents {
		score := scoreOfficialDocsTerms(terms, []weightedDocumentText{
			{weight: 8, text: document.titleLower},
			{weight: 4, text: document.pathLower},
			{weight: 1, text: document.contentLower},
		})
		if score > 0 {
			ranked = append(ranked, rankedDocument{score: score, document: document})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].document.Title < ranked[j].document.Title
	})

	if limit > len(ranked) {
		limit = len(ranked)
	}
	results := make([]OfficialDocSearchResult, 0, limit)
	for _, item := range ranked[:limit] {
		document := item.document
		offset := findOfficialDocSnippetOffset(document, terms)
		content := []rune(document.Content)
		end := offset + officialDocsSnippetSize
		if end > len(content) {
			end = len(content)
		}
		snippet := ""
		if offset < len(content) {
			snippet = collapseOfficialDocWhitespace(string(content[offset:end]))
		}
		results = append(results, OfficialDocSearchResult{
			Path:          document.Path,
			Title:         document.Title,
			URL:           s.buildOfficialDocURL(document.Path),
			Length:        len(content),
			SnippetOffset: offset,
			Snippet:       snippet,
		})
	}
	return results, nil
}

// ReadOfficialDocs 按路径从指定偏移读取官方文档正文
func (s *AISearchDocumentStore) ReadOfficialDocs(ctx context.Context, paths []string, offset int) ([]OfficialDocReadResult, error) {
	if s == nil {
		return nil, fmt.Errorf("官方文档仓库未初始化")
	}
	if len(paths) == 0 {
		return []OfficialDocReadResult{}, nil
	}
	if offset < 0 {
		offset = 0
	}
	documents, err := s.loadOfficialDocs(ctx)
	if err != nil {
		return nil, err
	}
	documentMap := make(map[string]OfficialDocument, len(documents))
	for _, document := range documents {
		documentMap[document.Path] = document
	}
	results := make([]OfficialDocReadResult, 0, officialDocsMaxReadCount)
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		document, ok := documentMap[path]
		if !ok {
			continue
		}
		if _, duplicated := seen[path]; duplicated {
			continue
		}
		seen[path] = struct{}{}
		content := []rune(document.Content)
		start := offset
		if start > len(content) {
			start = len(content)
		}
		end := start + officialDocsMaxReadSize
		if end > len(content) {
			end = len(content)
		}
		result := OfficialDocReadResult{
			SourceID: document.Path,
			Title:    document.Title,
			URL:      s.buildOfficialDocURL(document.Path),
			Offset:   start,
			Length:   len(content),
			Content:  string(content[start:end]),
		}
		if end < len(content) {
			next := end
			result.NextOffset = &next
		}
		results = append(results, result)
		if len(results) >= officialDocsMaxReadCount {
			break
		}
	}
	return results, nil
}

// BuildSources 把已读取的官方文档 ID 转成可点击来源；缓存为空时返回空列表
func (s *AISearchDocumentStore) BuildSources(sourceIDs []string) []OfficialDocSource {
	if s == nil {
		return []OfficialDocSource{}
	}
	s.mu.Lock()
	cached := s.cache
	s.mu.Unlock()
	if cached == nil {
		return []OfficialDocSource{}
	}
	documentMap := make(map[string]OfficialDocument, len(cached))
	for _, document := range cached {
		documentMap[document.Path] = document
	}
	sources := make([]OfficialDocSource, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		document, ok := documentMap[sourceID]
		if !ok {
			continue
		}
		sources = append(sources, OfficialDocSource{Title: document.Title, URL: s.buildOfficialDocURL(sourceID)})
	}
	return sources
}

// CachedOfficialDocuments 返回当前缓存快照，主要用于来源构建与测试
func (s *AISearchDocumentStore) CachedOfficialDocuments() []OfficialDocument {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]OfficialDocument(nil), s.cache...)
}

// buildOfficialDocURL 把 LLM 文档路径转换为面向用户的文档站页面 URL
func (s *AISearchDocumentStore) buildOfficialDocURL(path string) string {
	base := officialDocsBaseURL
	if s != nil && s.baseURL != "" {
		base = s.baseURL
	}
	return base + strings.TrimSuffix(path, ".md")
}

func (s *AISearchDocumentStore) cacheFreshLocked() bool {
	return s.cache != nil && s.now().Before(s.expires)
}

// loadOfficialDocs 返回官方文档包；缓存过期时先用旧数据并在后台刷新，只有首次加载才等待下载
func (s *AISearchDocumentStore) loadOfficialDocs(ctx context.Context) ([]OfficialDocument, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.cacheFreshLocked() {
		documents := s.cache
		s.mu.Unlock()
		return documents, nil
	}
	refresh := s.ensureRefreshLocked()
	stale := s.cache
	s.mu.Unlock()
	if stale != nil {
		return stale, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-refresh.done:
		if refresh.err != nil {
			return nil, refresh.err
		}
		return refresh.docs, nil
	}
}

// ensureRefreshLocked 确保同一时间只有一个文档包下载任务在运行
func (s *AISearchDocumentStore) ensureRefreshLocked() *officialDocsRefresh {
	if s.refresh != nil {
		return s.refresh
	}
	refresh := &officialDocsRefresh{done: make(chan struct{})}
	s.refresh = refresh
	go func() {
		documents, err := s.downloadOfficialDocs()
		s.mu.Lock()
		refresh.docs = documents
		refresh.err = err
		if err == nil {
			s.cache = documents
			s.expires = s.now().Add(s.cacheTTL)
		}
		if s.refresh == refresh {
			s.refresh = nil
		}
		close(refresh.done)
		s.mu.Unlock()
		if err != nil {
			zap.L().Warn("官方文档包下载失败", zap.Error(err))
		}
	}()
	return refresh
}

// downloadOfficialDocs 下载并解析官方文档包
func (s *AISearchDocumentStore) downloadOfficialDocs() ([]OfficialDocument, error) {
	request, err := http.NewRequest(http.MethodGet, s.bundleURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建官方文档请求失败: %w", err)
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("下载官方文档包失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("官方文档包下载失败: HTTP %d", response.StatusCode)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return nil, fmt.Errorf("官方文档包端点返回了网页")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, officialDocsMaxBundleSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取官方文档包失败: %w", err)
	}
	if len(body) > officialDocsMaxBundleSize {
		return nil, fmt.Errorf("官方文档包大小超出限制")
	}
	documents := parseOfficialDocsBundle(string(body))
	if len(documents) == 0 {
		return nil, fmt.Errorf("官方文档包中没有可读取的文档")
	}
	return documents, nil
}

type weightedDocumentText struct {
	weight int
	text   string
}

func scoreOfficialDocsTerms(terms []string, texts []weightedDocumentText) int {
	score := 0
	for _, term := range terms {
		for _, weighted := range texts {
			if strings.Contains(weighted.text, term) {
				score += weighted.weight
			}
		}
	}
	return score
}

// splitOfficialDocsQueryTerms 把检索词拆成去重后的小写关键词；中文没有词表时以整段和两字窗口近似 jieba 搜索模式
func splitOfficialDocsQueryTerms(query string) []string {
	terms := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	add := func(term string) {
		if term == "" {
			return
		}
		if _, stop := officialDocsQueryStopWords[term]; stop {
			return
		}
		if _, exists := seen[term]; exists {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	runes := []rune(strings.ToLower(strings.TrimSpace(query)))
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		token := string(runes[start:end])
		start = -1
		if token == "" {
			return
		}
		if _, stop := officialDocsQueryStopWords[token]; stop {
			return
		}
		add(token)
		if !containsCJK(token) {
			return
		}
		tokenRunes := []rune(token)
		for index := 0; index+2 <= len(tokenRunes); index++ {
			add(string(tokenRunes[index : index+2]))
		}
	}
	for index, r := range runes {
		if isOfficialDocsQueryRune(r) {
			if start < 0 {
				start = index
			}
			continue
		}
		flush(index)
	}
	flush(len(runes))
	return terms
}

func isOfficialDocsQueryRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_' || r == '.' || r == '-'
}

func containsCJK(value string) bool {
	for _, r := range value {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func findOfficialDocSnippetOffset(document OfficialDocument, terms []string) int {
	minimum := -1
	for _, term := range terms {
		position := strings.Index(document.contentLower, term)
		if position < 0 {
			continue
		}
		if minimum < 0 || position < minimum {
			minimum = position
		}
	}
	if minimum < 0 {
		return 0
	}
	// Python 按字符切片定位；此处把字节偏移换算成字符偏移，避免截断多字节文本
	offset := utf8.RuneCountInString(document.contentLower[:minimum]) - officialDocsSnippetLead
	if offset < 0 {
		return 0
	}
	return offset
}

func collapseOfficialDocWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
