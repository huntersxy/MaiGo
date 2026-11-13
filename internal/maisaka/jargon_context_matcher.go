package maisaka

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// 黑话参考消息常量，对齐 Python jargon_context_matcher
const (
	// MaxJargonReferenceMatches 单条黑话参考消息最多注入的词条数
	MaxJargonReferenceMatches = 10
	// HighFrequencyTermTTLSeconds 高频词缓存有效期
	HighFrequencyTermTTLSeconds = 60.0
	// JargonReferenceHeader 黑话参考消息首行
	JargonReferenceHeader = "以下释义来自当前上下文中部分词汇的猜测含义，仅作理解聊天语境的参考，不一定为真，需要根据情景理解："
	// JargonReferenceDisplayPrefix 黑话参考消息展示前缀
	JargonReferenceDisplayPrefix = "<jargon>"
	// JargonReferenceType 参考消息类型标记
	JargonReferenceType = "jargon"
)

// JargonCandidate 一条候选黑话，对齐 Python _JargonCandidate
type JargonCandidate struct {
	Content       string
	Meaning       string
	Count         int
	IsGlobal      bool
	SessionIDDict string
}

// JargonMatch 一条机械命中结果，对齐 Python _JargonMatch
type JargonMatch struct {
	Content            string
	Meaning            string
	Score              float64
	FirstMessageIndex  int
	JargonCount        int
	HighFrequencyTerm  string
	HighFrequencyRank  int
	HighFrequencyCount int
}

// HighFrequencyTerm 高频词打分字段，对齐 Python _HighFrequencyTermSummary
type HighFrequencyTerm struct {
	Term            string
	Rank            int
	OccurrenceCount int
}

// JargonConfigLookup 黑话配置与分组作用域查询端口
type JargonConfigLookup interface {
	// JargonEnabledForChat 该会话是否启用黑话
	JargonEnabledForChat(sessionID string) bool
	// ResolveJargonGroupScope 该会话关联的黑话分组内会话集合
	ResolveJargonGroupScope(sessionID string) map[string]struct{}
}

// JargonRepository 黑话与高频词数据端口，读库由宿主实现
type JargonRepository interface {
	// ListJargons 返回已判定为黑话且释义非空的记录，按 count 降序
	ListJargons() []JargonCandidate
	// ListHighFrequencyTerms 返回某会话的高频词
	ListHighFrequencyTerms(sessionID string) []HighFrequencyTerm
}

// HighFrequencyTermCache 按会话缓存高频词，避免每个规划轮次重复读库
type HighFrequencyTermCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]termCacheEntry
}

type termCacheEntry struct {
	cachedAt time.Time
	terms    map[string]HighFrequencyTerm
}

// NewHighFrequencyTermCache 创建默认 TTL 的高频词缓存
func NewHighFrequencyTermCache() *HighFrequencyTermCache {
	return &HighFrequencyTermCache{
		ttl:     time.Duration(HighFrequencyTermTTLSeconds * float64(time.Second)),
		entries: map[string]termCacheEntry{},
	}
}

// Get 命中未过期缓存时返回词表
func (c *HighFrequencyTermCache) Get(sessionID string) (map[string]HighFrequencyTerm, bool) {
	if c == nil {
		return nil, false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok || now.Sub(entry.cachedAt) >= c.ttl {
		return nil, false
	}
	return entry.terms, true
}

// Put 写入缓存并清理过期条目
func (c *HighFrequencyTermCache) Put(sessionID string, terms map[string]HighFrequencyTerm) {
	if c == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if now.Sub(entry.cachedAt) >= c.ttl {
			delete(c.entries, key)
		}
	}
	c.entries[sessionID] = termCacheEntry{cachedAt: now, terms: terms}
}

// JargonMatcher 基于当前上下文做黑话机械匹配
type JargonMatcher struct {
	Config JargonConfigLookup
	Repo   JargonRepository
	Terms  *HighFrequencyTermCache
}

// NewJargonMatcher 创建黑话匹配器
func NewJargonMatcher(config JargonConfigLookup, repo JargonRepository) *JargonMatcher {
	return &JargonMatcher{Config: config, Repo: repo, Terms: NewHighFrequencyTermCache()}
}

// BuildReferenceMessage 根据当前上下文中其他用户消息构造黑话参考消息
func (m *JargonMatcher) BuildReferenceMessage(
	sessionID string,
	messages []ContextMessage,
	excludedContents []string,
	limit int,
) *ContextMessage {
	if m == nil || m.Config == nil || !m.Config.JargonEnabledForChat(sessionID) {
		return nil
	}
	matches := m.MatchForContext(sessionID, messages, limit)
	if len(matches) == 0 {
		return nil
	}
	excluded := map[string]struct{}{}
	for _, content := range excludedContents {
		excluded[NormalizeJargonMatchText(content)] = struct{}{}
	}
	filtered := make([]JargonMatch, 0, len(matches))
	for _, match := range matches {
		if _, skip := excluded[NormalizeJargonMatchText(match.Content)]; skip {
			continue
		}
		filtered = append(filtered, match)
	}
	if len(filtered) == 0 {
		return nil
	}
	lines := []string{JargonReferenceHeader}
	entries := make([][2]string, 0, len(filtered))
	for _, match := range filtered {
		lines = append(lines, match.Content+"："+match.Meaning)
		entries = append(entries, [2]string{match.Content, match.Meaning})
	}
	return &ContextMessage{
		Role:      RoleReference,
		Content:   strings.Join(lines, "\n"),
		Source:    SourceReference,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"reference_type": JargonReferenceType,
			"display_prefix": JargonReferenceDisplayPrefix,
			"jargon_entries": entries,
			"remaining_uses": nil,
		},
	}
}

// MatchForContext 返回当前上下文机械命中的黑话，按权重取前若干条
func (m *JargonMatcher) MatchForContext(sessionID string, messages []ContextMessage, limit int) []JargonMatch {
	if m == nil || m.Repo == nil {
		return nil
	}
	if limit < 1 {
		limit = 1
	}
	userTexts := ExtractOtherUserTexts(messages, nil)
	if len(userTexts) == 0 {
		return nil
	}
	candidates := m.scopedCandidates(sessionID)
	if len(candidates) == 0 {
		return nil
	}
	terms := m.highFrequencyTerms(sessionID)
	matchesByContent := map[string]JargonMatch{}
	for messageIndex, text := range userTexts {
		normalizedText := NormalizeJargonMatchText(text)
		if normalizedText == "" {
			continue
		}
		for _, candidate := range candidates {
			contentKey := NormalizeJargonMatchText(candidate.Content)
			if contentKey == "" {
				continue
			}
			if _, seen := matchesByContent[contentKey]; seen {
				continue
			}
			if !ContainsMechanicalJargonMatch(normalizedText, contentKey) {
				continue
			}
			term, hit := terms[contentKey]
			rank := 0
			count := 0
			termText := ""
			if hit {
				rank = term.Rank
				count = term.OccurrenceCount
				termText = term.Term
			}
			matchesByContent[contentKey] = JargonMatch{
				Content:            candidate.Content,
				Meaning:            candidate.Meaning,
				Score:              CalculateJargonMatchScore(candidate.Count, messageIndex, rank, count, hit),
				FirstMessageIndex:  messageIndex,
				JargonCount:        candidate.Count,
				HighFrequencyTerm:  termText,
				HighFrequencyRank:  rank,
				HighFrequencyCount: count,
			}
		}
	}
	matches := make([]JargonMatch, 0, len(matchesByContent))
	for _, match := range matchesByContent {
		matches = append(matches, match)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		left, right := matches[i], matches[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.FirstMessageIndex != right.FirstMessageIndex {
			return left.FirstMessageIndex < right.FirstMessageIndex
		}
		if len(left.Content) != len(right.Content) {
			return len([]rune(left.Content)) > len([]rune(right.Content))
		}
		return left.Content < right.Content
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func (m *JargonMatcher) scopedCandidates(sessionID string) []JargonCandidate {
	relatedSessionIDs := map[string]struct{}{}
	if m.Config != nil {
		relatedSessionIDs = m.Config.ResolveJargonGroupScope(sessionID)
	}
	records := m.Repo.ListJargons()
	candidates := make([]JargonCandidate, 0, len(records))
	for _, record := range records {
		content := strings.TrimSpace(record.Content)
		meaning := strings.TrimSpace(record.Meaning)
		if content == "" || meaning == "" {
			continue
		}
		if !record.IsGlobal && !JargonInScope(record.SessionIDDict, relatedSessionIDs) {
			continue
		}
		record.Content = content
		record.Meaning = meaning
		candidates = append(candidates, record)
	}
	return candidates
}

func (m *JargonMatcher) highFrequencyTerms(sessionID string) map[string]HighFrequencyTerm {
	normalizedSessionID := strings.TrimSpace(sessionID)
	if normalizedSessionID == "" {
		return map[string]HighFrequencyTerm{}
	}
	if m.Terms == nil {
		m.Terms = NewHighFrequencyTermCache()
	}
	if cached, ok := m.Terms.Get(normalizedSessionID); ok {
		return cached
	}
	rows := m.Repo.ListHighFrequencyTerms(normalizedSessionID)
	termsByKey := map[string]HighFrequencyTerm{}
	for _, row := range rows {
		key := NormalizeJargonMatchText(row.Term)
		if key != "" {
			termsByKey[key] = row
		}
	}
	m.Terms.Put(normalizedSessionID, termsByKey)
	return termsByKey
}

// ExtractOtherUserTexts 提取上下文中其他用户的消息文本并去重
func ExtractOtherUserTexts(messages []ContextMessage, selfUserIDs map[string]struct{}) []string {
	texts := make([]string, 0, len(messages))
	seen := map[string]struct{}{}
	for _, message := range messages {
		if message.Role != RoleUser || message.Source != SourceUser {
			continue
		}
		if _, isSelf := selfUserIDs[message.UserID]; isSelf {
			continue
		}
		messageID := strings.TrimSpace(message.MessageID)
		if messageID != "" {
			if _, dup := seen[messageID]; dup {
				continue
			}
			seen[messageID] = struct{}{}
		}
		if text := ExtractJargonMessageText(message.Content); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

// ExtractJargonMessageText 去掉规划器消息前缀并压缩空白
func ExtractJargonMessageText(content string) string {
	return strings.Join(strings.Fields(StripPlannerMessagePrefix(content)), " ")
}

// StripPlannerMessagePrefix 去掉 <message ...> 前缀
func StripPlannerMessagePrefix(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(strings.ToLower(trimmed), "<message") {
		return trimmed
	}
	end := strings.Index(trimmed, ">")
	if end < 0 {
		return trimmed
	}
	return trimmed[end+1:]
}

// NormalizeJargonMatchText 小写并压缩空白
func NormalizeJargonMatchText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
}

// ContainsMechanicalJargonMatch 子串命中
func ContainsMechanicalJargonMatch(normalizedText, normalizedTerm string) bool {
	return normalizedTerm != "" && strings.Contains(normalizedText, normalizedTerm)
}

// CalculateJargonMatchScore 计算黑话匹配权重
func CalculateJargonMatchScore(
	candidateCount int,
	firstMessageIndex int,
	highFrequencyRank int,
	highFrequencyCount int,
	hitHighFrequency bool,
) float64 {
	highFrequencyScore := 0.0
	if hitHighFrequency {
		highFrequencyScore = 1000.0 + float64(highFrequencyCount)*2.0
		if highFrequencyRank > 0 {
			bonus := 100.0 - float64(highFrequencyRank)
			if bonus > 0 {
				highFrequencyScore += bonus
			}
		}
	}
	return float64(candidateCount) + highFrequencyScore - float64(firstMessageIndex)*0.01
}

// JargonInScope 判断黑话的会话字典与相关会话是否相交
func JargonInScope(sessionIDDict string, relatedSessionIDs map[string]struct{}) bool {
	if strings.TrimSpace(sessionIDDict) == "" {
		return false
	}
	parsed := map[string]any{}
	if err := json.Unmarshal([]byte(sessionIDDict), &parsed); err != nil {
		return false
	}
	for sessionID := range parsed {
		if _, ok := relatedSessionIDs[sessionID]; ok {
			return true
		}
	}
	return false
}

// ExtractJargonReferenceContents 从已有黑话参考消息中提取已注入过的词条内容
func ExtractJargonReferenceContents(messages []ContextMessage) map[string]struct{} {
	contents := map[string]struct{}{}
	for _, message := range messages {
		if message.Role != RoleReference {
			continue
		}
		referenceType, _ := message.Metadata["reference_type"].(string)
		if referenceType != JargonReferenceType {
			continue
		}
		for content := range extractJargonReferenceContentsFromText(message.Content) {
			contents[content] = struct{}{}
		}
	}
	return contents
}

func extractJargonReferenceContentsFromText(content string) map[string]struct{} {
	contents := map[string]struct{}{}
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == JargonReferenceHeader {
			continue
		}
		if item := extractJargonReferenceContentFromLine(line); item != "" {
			contents[item] = struct{}{}
		}
	}
	return contents
}

func extractJargonReferenceContentFromLine(line string) string {
	normalized := strings.TrimSpace(line)
	normalized = stripLeadingOrderNumber(normalized)
	index := strings.Index(normalized, "：")
	if index <= 0 {
		return ""
	}
	return strings.TrimSpace(normalized[:index])
}

func stripLeadingOrderNumber(line string) string {
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits >= len(line) {
		return line
	}
	rest := line[digits:]
	rest = strings.TrimLeft(rest, " ")
	if strings.HasPrefix(rest, ".") {
		return strings.TrimLeft(rest[1:], " ")
	}
	return line
}

// IsJargonReferenceText 判断一段文本是否是黑话参考消息
func IsJargonReferenceText(content string) bool {
	text := strings.TrimLeft(StripPlannerMessagePrefix(content), " ")
	return strings.HasPrefix(text, JargonReferenceDisplayPrefix) || strings.HasPrefix(text, JargonReferenceHeader)
}
