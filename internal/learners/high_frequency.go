package learners

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"maibot-go/internal/chat/replyer/jiebacut"
)

// 高频词学习第三路，对齐 MaiBot src/services/message_word_frequency_service.py
//
// jieba 词典从 replyer 的数据文件复制一份并内嵌，保证 learners 包可独立构建；
// 词典版本与 internal/replyer/data/jieba_dict.txt.gz 保持一致

//go:embed data/jieba_dict.txt.gz
var highFrequencyJiebaDictGz []byte

const (
	defaultHighFrequencyLimit    = 1000
	defaultHighFrequencyMinCount = 2
	defaultHighFrequencyMaxTerms = 1000
)

var (
	highFrequencyURLRe     = regexp.MustCompile("(?i)https?://\\S+|www\\.\\S+")
	highFrequencyCQRe      = regexp.MustCompile("\\[CQ:[^\\]]+\\]")
	highFrequencyMentionRe = regexp.MustCompile("@\\S+")
	highFrequencyTechRe    = regexp.MustCompile("[A-Za-z][A-Za-z0-9_+./#-]+")
	highFrequencyCJKRe     = regexp.MustCompile("[\\x{4e00}-\\x{9fff}]")
	highFrequencyLatinRe   = regexp.MustCompile("[a-zA-Z]")
	highFrequencyNumericRe = regexp.MustCompile("^[\\d._+-]+$")
	highFrequencyPunctRe   = regexp.MustCompile("^[\\p{P}\\p{S}_]+$")
	highFrequencySpaceRe   = regexp.MustCompile("\\s+")
)

const highFrequencyTokenTrimChars = " \t\r\n.,;:!?()[]{}<>\"'`~@#$%^&*=|\\/，。！？；：、（）【】《》“”‘’…"

var highFrequencyCNStopWords = map[string]struct{}{
	"一些": {}, "一样": {}, "一定": {}, "一直": {}, "一条": {}, "一种": {}, "一起": {}, "一下": {},
	"一个": {}, "一会": {}, "一是": {}, "不是": {}, "不过": {}, "不如": {}, "不能": {}, "不要": {},
	"不用": {}, "不会": {}, "但原": {}, "为了": {}, "为什么": {}, "也是": {}, "也许": {}, "于是": {},
	"什么": {}, "他们": {}, "以后": {}, "以前": {}, "以及": {}, "按照": {}, "但是": {}, "你们": {},
	"你的": {}, "其实": {}, "其它": {}, "其他": {}, "只是": {}, "只要": {}, "可以": {}, "可是": {},
	"没有": {}, "各位": {}, "回复": {}, "因为": {}, "因此": {}, "如果": {}, "它们": {}, "对于": {},
	"对方": {}, "就是": {}, "已经": {}, "并且": {}, "怎么": {}, "怎样": {}, "总之": {}, "我们": {},
	"我的": {}, "所以": {}, "所有": {}, "是否": {}, "是不是": {}, "未知": {}, "未知用户": {}, "有人": {},
	"有点": {}, "有没有": {}, "无法访问": {}, "然后": {}, "现在": {}, "关于": {}, "根据": {}, "由于": {},
	"的话": {}, "直接": {}, "真的": {}, "真是": {}, "自己": {}, "虽然": {}, "这是": {}, "通过": {},
	"这个": {}, "这些": {}, "这么": {}, "这里": {}, "这样": {}, "那个": {}, "那些": {}, "那么": {},
	"那里": {}, "那样": {}, "还是": {}, "还有": {}, "或者": {}, "而且": {}, "来说": {}, "知道": {},
	"一下子": {}, "一大": {}, "不了": {}, "之前": {}, "出来": {}, "只能": {}, "多少": {}, "今天": {},
	"图片": {}, "喜欢": {}, "好像": {}, "好看": {}, "哈哈": {}, "哈哈哈": {}, "开始": {}, "小时": {},
	"分钟": {}, "应该": {}, "我要": {}, "时候": {}, "时间": {}, "消息": {}, "确实": {}, "聊天": {},
	"表情": {}, "觉得": {}, "需要": {}, "感觉": {}, "看看": {}, "看到": {}, "群友": {}, "使用": {},
	"可能": {}, "问题": {}, "这种": {}, "里面": {}, "内容": {}, "用户": {}, "啊啊啊": {}, "东西": {},
}

var highFrequencyENStopWords = map[string]struct{}{
	"a": {}, "about": {}, "after": {}, "all": {}, "also": {}, "an": {}, "and": {}, "any": {},
	"are": {}, "as": {}, "at": {}, "be": {}, "because": {}, "but": {}, "by": {}, "can": {},
	"could": {}, "for": {}, "from": {}, "had": {}, "has": {}, "have": {}, "how": {}, "if": {},
	"in": {}, "into": {}, "is": {}, "it": {}, "its": {}, "just": {}, "may": {}, "more": {},
	"not": {}, "of": {}, "on": {}, "or": {}, "our": {}, "so": {}, "that": {}, "the": {},
	"their": {}, "then": {}, "there": {}, "this": {}, "to": {}, "was": {}, "we": {}, "were": {},
	"what": {}, "when": {}, "where": {}, "which": {}, "who": {}, "why": {}, "will": {}, "with": {},
	"would": {}, "you": {}, "your": {},
}

// HighFrequencyTokenizer 高频词分词端口，默认使用与 MaiBot 相同的 jieba 精确模式
type HighFrequencyTokenizer interface {
	Cut(text string) []string
}

type jiebaHighFrequencyTokenizer struct {
	dict *jiebacut.Dictionary
}

func (t *jiebaHighFrequencyTokenizer) Cut(text string) []string {
	if t == nil || t.dict == nil {
		return nil
	}
	return t.dict.Cut(text)
}

var (
	highFrequencyTokenizerMu       sync.RWMutex
	highFrequencyTokenizerOverride HighFrequencyTokenizer
	highFrequencyDefaultOnce       sync.Once
	highFrequencyDefaultTokenizer  HighFrequencyTokenizer
	highFrequencyDefaultErr        error
)

// SetHighFrequencyTokenizer 注入自定义分词器，nil 表示恢复默认 jieba
func SetHighFrequencyTokenizer(tokenizer HighFrequencyTokenizer) {
	highFrequencyTokenizerMu.Lock()
	highFrequencyTokenizerOverride = tokenizer
	highFrequencyTokenizerMu.Unlock()
}

func resolveHighFrequencyTokenizer() (HighFrequencyTokenizer, error) {
	highFrequencyTokenizerMu.RLock()
	override := highFrequencyTokenizerOverride
	highFrequencyTokenizerMu.RUnlock()
	if override != nil {
		return override, nil
	}
	highFrequencyDefaultOnce.Do(func() {
		gz, err := gzip.NewReader(bytes.NewReader(highFrequencyJiebaDictGz))
		if err != nil {
			highFrequencyDefaultErr = fmt.Errorf("加载高频词 jieba 词典: %w", err)
			return
		}
		defer gz.Close()
		dict, err := jiebacut.NewDictionary(gz)
		if err != nil {
			highFrequencyDefaultErr = fmt.Errorf("加载高频词 jieba 词典: %w", err)
			return
		}
		highFrequencyDefaultTokenizer = &jiebaHighFrequencyTokenizer{dict: dict}
	})
	return highFrequencyDefaultTokenizer, highFrequencyDefaultErr
}

// HighFrequencyLearningOptions 对齐 update_high_frequency_terms_from_context_messages 参数
type HighFrequencyLearningOptions struct {
	Limit    int
	MinCount int
	MaxTerms int
}

func (o HighFrequencyLearningOptions) normalized() HighFrequencyLearningOptions {
	if o.Limit <= 0 {
		o.Limit = defaultHighFrequencyLimit
	}
	if o.MinCount <= 0 {
		o.MinCount = defaultHighFrequencyMinCount
	}
	if o.MaxTerms <= 0 {
		o.MaxTerms = defaultHighFrequencyMaxTerms
	}
	return o
}

// HighFrequencyLearningResult 一次高频词学习结果
type HighFrequencyLearningResult struct {
	UpdatedCount int
	Contents     []string
}

type highFrequencyChatTexts struct {
	chatID string
	texts  []string
}

// UpdateHighFrequencyTermsFromContextMessages 从裁切上下文增量更新高频词词库
func UpdateHighFrequencyTermsFromContextMessages(ctx context.Context, reg *Registry, messages []ContextMessage, opts HighFrequencyLearningOptions) (HighFrequencyLearningResult, error) {
	result := HighFrequencyLearningResult{}
	if reg == nil {
		reg = DefaultRegistry()
	}
	opts = opts.normalized()
	groups := extractHighFrequencyTexts(messages)
	if len(groups) == 0 {
		return result, nil
	}
	tokenizer, err := resolveHighFrequencyTokenizer()
	if err != nil {
		return result, err
	}
	if tokenizer == nil {
		return result, fmt.Errorf("高频词分词器不可用")
	}
	generatedAt := time.Now()
	for _, group := range groups {
		terms := buildHighFrequencyTerms(group.texts, tokenizer, opts.Limit, opts.MinCount)
		if len(terms) == 0 {
			continue
		}
		inputs := make([]HighFrequencyTermInput, 0, len(terms))
		for _, item := range terms {
			inputs = append(inputs, HighFrequencyTermInput{
				Term:         item.Term,
				Count:        item.Count,
				MessageCount: item.MessageCount,
			})
			result.Contents = append(result.Contents, fmt.Sprintf("%s（%d 次）", item.Term, item.Count))
		}
		merged, err := reg.Store().MergeHighFrequencyTerms(ctx, group.chatID, inputs, generatedAt, opts.MaxTerms)
		if err != nil {
			return result, err
		}
		result.UpdatedCount += merged
	}
	return result, nil
}

func extractHighFrequencyTexts(messages []ContextMessage) []highFrequencyChatTexts {
	order := make([]string, 0)
	textsByChat := make(map[string][]string)
	seen := make(map[string]struct{})
	for _, message := range messages {
		if message.SourceKind != "user" || message.IsBotSelf || message.IsNotify {
			continue
		}
		chatID := strings.TrimSpace(message.SessionID)
		if chatID == "" {
			continue
		}
		messageID := strings.TrimSpace(message.MessageID)
		if messageID != "" {
			key := chatID + "\x00" + messageID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		text := strings.Join(strings.Fields(message.Text), " ")
		if text == "" {
			continue
		}
		if _, ok := textsByChat[chatID]; !ok {
			order = append(order, chatID)
		}
		textsByChat[chatID] = append(textsByChat[chatID], text)
	}
	groups := make([]highFrequencyChatTexts, 0, len(order))
	for _, chatID := range order {
		groups = append(groups, highFrequencyChatTexts{chatID: chatID, texts: textsByChat[chatID]})
	}
	return groups
}

type highFrequencyItem struct {
	Term         string
	Count        int
	MessageCount int
	Frequency    float64
	MessageFreq  float64
}

func buildHighFrequencyTerms(texts []string, tokenizer HighFrequencyTokenizer, limit, minCount int) []highFrequencyItem {
	wordCounter := map[string]int{}
	phraseCounter := map[string]int{}
	wordMessageCounter := map[string]int{}
	phraseMessageCounter := map[string]int{}
	messageCount := 0
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		messageCount++
		tokens := tokenizeMeaningfulTerms(text, tokenizer)
		if len(tokens) == 0 {
			continue
		}
		phrases := iterPhrases(tokens)
		for _, token := range tokens {
			wordCounter[token]++
		}
		for _, phrase := range phrases {
			phraseCounter[phrase]++
		}
		tokenSet := stringSet(tokens)
		phraseSet := stringSet(phrases)
		for token := range tokenSet {
			wordMessageCounter[token]++
		}
		for phrase := range phraseSet {
			if _, ok := tokenSet[phrase]; ok {
				continue
			}
			phraseMessageCounter[phrase]++
		}
	}
	totalTermCount := 0
	for _, count := range wordCounter {
		totalTermCount += count
	}
	for _, count := range phraseCounter {
		totalTermCount += count
	}
	// 对齐 Python _build_frequency_items：词与词组先按 term 合并，再统一筛选与排序
	for term, count := range phraseCounter {
		wordCounter[term] += count
	}
	for term, count := range phraseMessageCounter {
		wordMessageCounter[term] += count
	}
	items := make([]highFrequencyItem, 0, len(wordCounter))
	for term, count := range wordCounter {
		if count < minCount {
			continue
		}
		items = append(items, highFrequencyItem{
			Term:         term,
			Count:        count,
			MessageCount: wordMessageCounter[term],
			Frequency:    ratio(count, totalTermCount),
			MessageFreq:  ratio(wordMessageCounter[term], messageCount),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		if items[i].MessageCount != items[j].MessageCount {
			return items[i].MessageCount > items[j].MessageCount
		}
		leftLen := utf8.RuneCountInString(items[i].Term)
		rightLen := utf8.RuneCountInString(items[j].Term)
		if leftLen != rightLen {
			return leftLen > rightLen
		}
		return items[i].Term < items[j].Term
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func tokenizeMeaningfulTerms(text string, tokenizer HighFrequencyTokenizer) []string {
	cleaned := highFrequencyMentionRe.ReplaceAllString(
		highFrequencyCQRe.ReplaceAllString(highFrequencyURLRe.ReplaceAllString(text, " "), " "), " ")
	terms := make([]string, 0)
	for _, raw := range tokenizer.Cut(cleaned) {
		token := normalizeHighFrequencyToken(raw)
		if isMeaningfulHighFrequencyToken(token) {
			terms = append(terms, token)
		}
	}
	existing := stringSet(terms)
	for _, raw := range findHighFrequencyTechTerms(cleaned) {
		token := normalizeHighFrequencyToken(raw)
		if _, ok := existing[token]; ok {
			continue
		}
		if isMeaningfulHighFrequencyToken(token) {
			terms = append(terms, token)
			existing[token] = struct{}{}
		}
	}
	return terms
}

func findHighFrequencyTechTerms(text string) []string {
	indexes := highFrequencyTechRe.FindAllStringIndex(text, -1)
	terms := make([]string, 0, len(indexes))
	for _, index := range indexes {
		start, end := index[0], index[1]
		if start > 0 && isASCIIWordByte(text[start-1]) {
			continue
		}
		if end < len(text) && isASCIIWordByte(text[end]) {
			continue
		}
		terms = append(terms, text[start:end])
	}
	return terms
}

func isASCIIWordByte(value byte) bool {
	return value == '_' || value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func normalizeHighFrequencyToken(raw string) string {
	token := strings.TrimSpace(strings.ToLower(raw))
	token = strings.Trim(token, highFrequencyTokenTrimChars)
	return highFrequencySpaceRe.ReplaceAllString(token, " ")
}

func isMeaningfulHighFrequencyToken(token string) bool {
	if token == "" {
		return false
	}
	if _, ok := highFrequencyCNStopWords[token]; ok {
		return false
	}
	if _, ok := highFrequencyENStopWords[token]; ok {
		return false
	}
	if highFrequencyNumericRe.MatchString(token) || highFrequencyPunctRe.MatchString(token) {
		return false
	}
	if highFrequencyCJKRe.MatchString(token) {
		return countHighFrequencyCJK(token) >= 2
	}
	if highFrequencyLatinRe.MatchString(token) {
		return utf8.RuneCountInString(token) >= 2
	}
	return utf8.RuneCountInString(token) >= 2
}

func iterPhrases(tokens []string) []string {
	phrases := make([]string, 0)
	for size := 2; size <= 3; size++ {
		if len(tokens) < size {
			break
		}
		for index := 0; index+size <= len(tokens); index++ {
			parts := tokens[index : index+size]
			phrase := joinHighFrequencyPhrase(parts)
			if isMeaningfulHighFrequencyPhrase(phrase, parts) {
				phrases = append(phrases, phrase)
			}
		}
	}
	return phrases
}

func joinHighFrequencyPhrase(tokens []string) string {
	allCJK := true
	for _, token := range tokens {
		if !highFrequencyCJKRe.MatchString(token) || highFrequencyLatinRe.MatchString(token) {
			allCJK = false
			break
		}
	}
	if allCJK {
		return strings.Join(tokens, "")
	}
	return strings.Join(tokens, " ")
}

func isMeaningfulHighFrequencyPhrase(phrase string, tokens []string) bool {
	if phrase == "" {
		return false
	}
	if _, ok := highFrequencyCNStopWords[phrase]; ok {
		return false
	}
	if _, ok := highFrequencyENStopWords[phrase]; ok {
		return false
	}
	if len(stringSet(tokens)) == 1 {
		return false
	}
	if highFrequencyCJKRe.MatchString(phrase) {
		return countHighFrequencyCJK(phrase) >= 4
	}
	for _, token := range tokens {
		if highFrequencyLatinRe.MatchString(token) {
			return true
		}
	}
	return false
}

func countHighFrequencyCJK(text string) int {
	return len(highFrequencyCJKRe.FindAllString(text, -1))
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func ratio(value, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) / float64(total)
}
