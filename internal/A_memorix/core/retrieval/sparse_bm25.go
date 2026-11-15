package retrieval

// A_memorix 稀疏检索（SQLite FTS5 + BM25），对齐 Python core/retrieval/sparse_bm25.py
// 说明：Python 直接持有独立 sqlite3 连接；Go 侧用 storage.Manager 新建独立连接，
// 关闭时回收，避免与 MetadataStore 主连接互相影响

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"maibot-go/internal/A_memorix/core/storage"
)

// SparseBM25Config 对齐 Python SparseBM25Config
type SparseBM25Config struct {
	Enabled                      bool
	Backend                      string
	LazyLoad                     bool
	Mode                         string
	TokenizerMode                string
	JiebaUserDict                string
	CharNgramN                   int
	CandidateK                   int
	MaxDocLen                    int
	EnableTokenizedShadowIndex   bool
	EnableNgramFallbackIndex     bool
	EnableLikeFallback           bool
	EnableRelationSparseFallback bool
	RelationCandidateK           int
	RelationMaxDocLen            int
	UnloadOnDisable              bool
	ShrinkMemoryOnUnload         bool
}

// NewSparseBM25Config 返回与 Python dataclass 默认值一致的配置
func NewSparseBM25Config() SparseBM25Config {
	return SparseBM25Config{
		Enabled:                      true,
		Backend:                      "fts5",
		LazyLoad:                     true,
		Mode:                         "auto",
		TokenizerMode:                "jieba",
		CharNgramN:                   2,
		CandidateK:                   80,
		MaxDocLen:                    2000,
		EnableTokenizedShadowIndex:   true,
		EnableNgramFallbackIndex:     true,
		EnableLikeFallback:           false,
		EnableRelationSparseFallback: true,
		RelationCandidateK:           60,
		RelationMaxDocLen:            512,
		UnloadOnDisable:              true,
		ShrinkMemoryOnUnload:         true,
	}
}

// Normalize 对齐 Python __post_init__ 的归一化与钳制
func (c *SparseBM25Config) Normalize() {
	c.Backend = strings.ToLower(strings.TrimSpace(c.Backend))
	if c.Backend == "" {
		c.Backend = "fts5"
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = "auto"
	}
	c.TokenizerMode = strings.ToLower(strings.TrimSpace(c.TokenizerMode))
	if c.TokenizerMode == "" {
		c.TokenizerMode = "jieba"
	}
	c.CharNgramN = max(c.CharNgramN, 1)
	c.CandidateK = max(c.CandidateK, 1)
	c.MaxDocLen = max(c.MaxDocLen, 0)
	c.RelationCandidateK = max(c.RelationCandidateK, 1)
	c.RelationMaxDocLen = max(c.RelationMaxDocLen, 0)
}

// Validate 校验配置取值，错误文案对齐 Python
func (c SparseBM25Config) Validate() error {
	switch c.Backend {
	case "fts5", "tantivy", "lucene":
	default:
		return fmt.Errorf("sparse.backend 非法: %s", c.Backend)
	}
	switch c.Mode {
	case "auto", "fallback_only", "hybrid":
	default:
		return fmt.Errorf("sparse.mode 非法: %s", c.Mode)
	}
	switch c.TokenizerMode {
	case "jieba", "mixed", "char_2gram":
	default:
		return fmt.Errorf("sparse.tokenizer_mode 非法: %s", c.TokenizerMode)
	}
	return nil
}

// SparseSearchBackend 对齐 Python SparseSearchBackend
type SparseSearchBackend interface {
	Name() string
	EnsureLoaded(conn *storage.Conn) bool
	SearchParagraphs(matchQuery string, limit int, maxDocLen int, allowedIDs []string, conn *storage.Conn) []storage.FTSSearchHit
	SearchRelations(matchQuery string, limit int, maxDocLen int, includeInactive bool, allowedIDs []string, conn *storage.Conn) []storage.RelationFTSSearchHit
	Stats(conn *storage.Conn) map[string]any
}

// SQLiteFTS5SparseBackend 基于 MetadataStore/SQLite FTS5 的默认实现
type SQLiteFTS5SparseBackend struct {
	store  *storage.Store
	config SparseBM25Config
}

// NewSQLiteFTS5SparseBackend 构造默认 backend
func NewSQLiteFTS5SparseBackend(store *storage.Store, config SparseBM25Config) *SQLiteFTS5SparseBackend {
	return &SQLiteFTS5SparseBackend{store: store, config: config}
}

// Name 返回 backend 名
func (b *SQLiteFTS5SparseBackend) Name() string { return "fts5" }

// EnsureLoaded 初始化 FTS/预分词/ngram/关系索引
func (b *SQLiteFTS5SparseBackend) EnsureLoaded(conn *storage.Conn) bool {
	if !b.store.EnsureFTSSchema(conn) {
		return false
	}
	b.store.EnsureFTSBackfilled(conn)
	if b.config.EnableTokenizedShadowIndex {
		b.store.EnsureParagraphTokenizedFTSSchema(conn)
		b.store.EnsureParagraphTokenizedFTSBackfilled(conn)
	}
	if b.config.EnableRelationSparseFallback {
		b.store.EnsureRelationsFTSSchema(conn)
		b.store.EnsureRelationsFTSBackfilled(conn)
	}
	if b.config.EnableNgramFallbackIndex {
		b.store.EnsureParagraphNgramSchema(conn)
		if !b.store.IsParagraphNgramReady(b.config.CharNgramN, conn) {
			zap.L().Warn("paragraph ngram 索引未就绪，检索路径将跳过 ngram fallback")
		}
	}
	return true
}

// SearchParagraphs 执行段落 BM25 检索
func (b *SQLiteFTS5SparseBackend) SearchParagraphs(matchQuery string, limit int, maxDocLen int, allowedIDs []string, conn *storage.Conn) []storage.FTSSearchHit {
	return b.store.FtsSearchBM25(matchQuery, limit, maxDocLen, conn, allowedIDs)
}

// SearchRelations 执行关系 BM25 检索
func (b *SQLiteFTS5SparseBackend) SearchRelations(matchQuery string, limit int, maxDocLen int, includeInactive bool, allowedIDs []string, conn *storage.Conn) []storage.RelationFTSSearchHit {
	return b.store.FtsSearchRelationsBM25(matchQuery, limit, maxDocLen, includeInactive, conn, allowedIDs)
}

// Stats 返回 backend 统计
func (b *SQLiteFTS5SparseBackend) Stats(conn *storage.Conn) map[string]any {
	docCount := 0
	if conn != nil {
		docCount = b.store.FtsDocCount(conn)
	}
	return map[string]any{"backend": b.Name(), "doc_count": docCount}
}

// ExperimentalExternalInvertedIndexBackend 对齐 Python 实验 backend 占位
type ExperimentalExternalInvertedIndexBackend struct {
	name string
}

// NewExperimentalExternalInvertedIndexBackend 构造实验 backend
func NewExperimentalExternalInvertedIndexBackend(backendName string) *ExperimentalExternalInvertedIndexBackend {
	return &ExperimentalExternalInvertedIndexBackend{name: strings.ToLower(strings.TrimSpace(backendName))}
}

// Name 返回 backend 名
func (b *ExperimentalExternalInvertedIndexBackend) Name() string { return b.name }

// EnsureLoaded 实验 backend 恒不可用
func (b *ExperimentalExternalInvertedIndexBackend) EnsureLoaded(*storage.Conn) bool { return false }

// SearchParagraphs 实验 backend 未接入
func (b *ExperimentalExternalInvertedIndexBackend) SearchParagraphs(string, int, int, []string, *storage.Conn) []storage.FTSSearchHit {
	return nil
}

// SearchRelations 实验 backend 未接入
func (b *ExperimentalExternalInvertedIndexBackend) SearchRelations(string, int, int, bool, []string, *storage.Conn) []storage.RelationFTSSearchHit {
	return nil
}

// Stats 返回实验 backend 名
func (b *ExperimentalExternalInvertedIndexBackend) Stats(*storage.Conn) map[string]any {
	return map[string]any{"backend": b.name}
}

// SparseBM25Index 对齐 Python SparseBM25Index
type SparseBM25Index struct {
	store         *storage.Store
	config        SparseBM25Config
	manager       *storage.Manager
	conn          *storage.Conn
	loaded        bool
	jiebaDictUsed bool
	lastLoadError string
	backend       SparseSearchBackend
	segmenter     storage.Segmenter
	hasJieba      bool
}

// NewSparseBM25Index 构造稀疏索引；segmenter 为空时按 Python HAS_JIEBA=False 退化
func NewSparseBM25Index(store *storage.Store, config SparseBM25Config, segmenter storage.Segmenter) *SparseBM25Index {
	config.Normalize()
	index := &SparseBM25Index{store: store, config: config, segmenter: segmenter, hasJieba: segmenter != nil}
	if config.Backend == "fts5" {
		index.backend = NewSQLiteFTS5SparseBackend(store, config)
		return index
	}
	index.backend = NewExperimentalExternalInvertedIndexBackend(config.Backend)
	return index
}

// Config 返回配置快照
func (i *SparseBM25Index) Config() SparseBM25Config { return i.config }

// SetSegmenter 注入 jieba 分词器；对齐运行时替换 JIEBA_MODULE
func (i *SparseBM25Index) SetSegmenter(segmenter storage.Segmenter) {
	i.segmenter = segmenter
	i.hasJieba = segmenter != nil
}

// Loaded 返回索引是否已加载
func (i *SparseBM25Index) Loaded() bool { return i.loaded && i.conn != nil }

// LastLoadError 返回最近一次加载失败原因
func (i *SparseBM25Index) LastLoadError() string { return i.lastLoadError }

// EnsureLoaded 按需加载独立 FTS 连接与索引
func (i *SparseBM25Index) EnsureLoaded() bool {
	if !i.config.Enabled {
		return false
	}
	if i.Loaded() {
		return true
	}
	dbPath, err := i.store.DBPath()
	if err != nil {
		i.lastLoadError = err.Error()
		return false
	}
	manager := storage.NewManager(dbPath, 30*time.Second)
	conn, err := manager.NewConn()
	if err != nil {
		i.lastLoadError = err.Error()
		_ = manager.CloseAll()
		return false
	}
	if !i.backend.EnsureLoaded(conn) {
		i.lastLoadError = "backend_not_ready"
		_ = manager.CloseAll()
		return false
	}
	i.manager = manager
	i.conn = conn
	i.loaded = true
	i.lastLoadError = ""
	i.prepareTokenizer()
	zap.L().Debug("SparseBM25Index loaded",
		zap.String("backend", i.config.Backend),
		zap.String("tokenizer", i.config.TokenizerMode),
		zap.String("mode", i.config.Mode))
	return true
}

// Warmup 预热索引与首轮查询，返回结构化指标
func (i *SparseBM25Index) Warmup(sampleQueries []string, relationQuery string) map[string]any {
	started := time.Now()
	summary := map[string]any{
		"ok": false, "enabled": i.config.Enabled, "backend": i.config.Backend,
		"loaded": false, "doc_count": 0, "tokenized_query_count": 0,
		"paragraph_probe_count": 0, "relation_probe_count": 0,
		"duration_ms": 0.0, "error": "",
	}
	defer func() {
		summary["duration_ms"] = float64(time.Since(started).Microseconds()) / 1000.0
	}()
	if !i.config.Enabled {
		return summary
	}
	if !i.EnsureLoaded() {
		errorText := i.lastLoadError
		if errorText == "" {
			errorText = "ensure_loaded_failed"
		}
		summary["error"] = errorText
		return summary
	}

	if sampleQueries == nil {
		sampleQueries = []string{"记忆 检索", "关系 证据"}
	}
	probes := make([]string, 0, len(sampleQueries))
	for _, item := range sampleQueries {
		if text := strings.TrimSpace(item); text != "" {
			probes = append(probes, text)
		}
	}
	for _, probe := range probes {
		i.Tokenize(probe)
	}
	paragraphQuery := "记忆"
	if len(probes) > 0 {
		paragraphQuery = probes[0]
	}
	paragraphHits := i.Search(paragraphQuery, 1, nil)
	relationHits := []map[string]any{}
	if i.config.EnableRelationSparseFallback {
		relProbe := strings.TrimSpace(relationQuery)
		if relProbe == "" {
			relProbe = paragraphQuery
		}
		relationHits = i.SearchRelations(relProbe, 1, nil)
	}
	summary["ok"] = true
	summary["loaded"] = i.Loaded()
	summary["doc_count"] = i.store.FtsDocCount(i.conn)
	summary["tokenized_query_count"] = len(probes)
	summary["paragraph_probe_count"] = len(paragraphHits)
	summary["relation_probe_count"] = len(relationHits)
	return summary
}

// UserDictLoader 可选接口：分词器自行加载用户词典
type UserDictLoader interface {
	LoadUserDict(path string) error
}

func (i *SparseBM25Index) prepareTokenizer() {
	if i.jiebaDictUsed {
		return
	}
	if i.config.TokenizerMode != "jieba" && i.config.TokenizerMode != "mixed" {
		return
	}
	if i.segmenter == nil {
		zap.L().Warn("jieba 不可用，tokenizer 将退化为 char n-gram")
		return
	}
	if userDict := strings.TrimSpace(i.config.JiebaUserDict); userDict != "" {
		if loader, ok := i.segmenter.(UserDictLoader); ok {
			if err := loader.LoadUserDict(userDict); err != nil {
				zap.L().Warn("加载 jieba 用户词典失败", zap.String("path", userDict), zap.Error(err))
			} else {
				zap.L().Info("已加载 jieba 用户词典", zap.String("path", userDict))
			}
		}
	}
	i.jiebaDictUsed = true
}

// Tokenize 对齐 Python _tokenize
func (i *SparseBM25Index) Tokenize(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	switch i.config.TokenizerMode {
	case "jieba":
		tokens := i.tokenizeJieba(text)
		if len(tokens) > 0 {
			tokens = append(tokens, i.tokenizePhrases(text)...)
			return dedupeTokens(tokens)
		}
		fallback := i.tokenizeCharNgram(text, i.config.CharNgramN)
		fallback = append(fallback, i.tokenizePhrases(text)...)
		return dedupeTokens(fallback)
	case "mixed":
		tokens := i.tokenizeJieba(text)
		tokens = append(tokens, i.tokenizeCharNgram(text, i.config.CharNgramN)...)
		tokens = append(tokens, i.tokenizePhrases(text)...)
		return dedupeTokens(tokens)
	default:
		return dedupeTokens(i.tokenizeCharNgram(text, i.config.CharNgramN))
	}
}

func (i *SparseBM25Index) tokenizeJieba(text string) []string {
	if i.segmenter == nil {
		return nil
	}
	tokens := i.segmenter.CutForSearch(text)
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if trimmed := strings.ToLower(strings.TrimSpace(token)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (i *SparseBM25Index) tokenizeCharNgram(text string, n int) []string {
	compact := strings.ToLower(whitespacePattern.ReplaceAllString(text, ""))
	if compact == "" {
		return nil
	}
	runes := []rune(compact)
	if len(runes) < n {
		return []string{compact}
	}
	out := make([]string, 0, len(runes)-n+1)
	for start := 0; start+n <= len(runes); start++ {
		out = append(out, string(runes[start:start+n]))
	}
	return out
}

func (i *SparseBM25Index) tokenizePhrases(text string) []string {
	matches := phraseTokenRegexp.FindAllString(text, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, strings.ToLower(match))
	}
	return out
}

var (
	whitespacePattern = regexp.MustCompile(`\s+`)
	phraseTokenRegexp = regexp.MustCompile(`[A-Za-z0-9_]+|[\x{4e00}-\x{9fff}]{2,}`)
	singleHanPattern  = regexp.MustCompile(`^[\x{4e00}-\x{9fff}]$`)
)

// MatchTokens 对齐 Python _match_tokens：过滤低信息中文单字
func MatchTokens(tokens []string) []string {
	normalized := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	informative := make([]string, 0, len(normalized))
	for _, token := range normalized {
		if !isLowSignalQueryToken(token) {
			informative = append(informative, token)
		}
	}
	if len(informative) == 0 {
		informative = normalized
	}
	return dedupeTokens(informative)
}

func isLowSignalQueryToken(token string) bool {
	text := strings.TrimSpace(token)
	return len([]rune(text)) == 1 && singleHanPattern.MatchString(text)
}

// BuildMatchQuery 对齐 Python _build_match_query：双引号转义后 OR 连接，最多 64 个
func BuildMatchQuery(tokens []string) string {
	safe := make([]string, 0, len(tokens))
	for _, token := range tokens {
		escaped := strings.TrimSpace(strings.ReplaceAll(token, `"`, `""`))
		if escaped == "" {
			continue
		}
		safe = append(safe, `"`+escaped+`"`)
	}
	if len(safe) == 0 {
		return ""
	}
	if len(safe) > 64 {
		safe = safe[:64]
	}
	return strings.Join(safe, " OR ")
}

// Search 执行段落 BM25 检索，对齐 Python SparseBM25Index.search
func (i *SparseBM25Index) Search(query string, k int, allowedIDs []string) []map[string]any {
	if !i.config.Enabled {
		return nil
	}
	if i.config.LazyLoad && !i.Loaded() {
		if !i.EnsureLoaded() {
			return nil
		}
	}
	if !i.Loaded() {
		return nil
	}
	tokens := i.Tokenize(query)
	matchTokens := MatchTokens(tokens)
	matchQuery := BuildMatchQuery(matchTokens)
	if matchQuery == "" {
		return nil
	}
	limit := max(k, 1)
	allowed, emptyAllowed := normalizeAllowedIDs(allowedIDs)
	if emptyAllowed {
		return nil
	}
	var rows []storage.FTSSearchHit
	if i.config.EnableTokenizedShadowIndex {
		rows = i.store.FtsSearchTokenizedParagraphsBM25(matchQuery, limit, i.config.MaxDocLen, i.conn, allowed)
	}
	if len(rows) == 0 {
		rows = i.backend.SearchParagraphs(matchQuery, limit, i.config.MaxDocLen, allowed, i.conn)
	}
	if len(rows) == 0 {
		rows = i.fallbackSubstringSearch(matchTokens, limit, allowed)
	}

	results := make([]map[string]any, 0, len(rows))
	tokenCount := max(len(matchTokens), 1)
	for rank, row := range rows {
		contentLow := strings.ToLower(row.Content)
		matched := make(map[string]struct{}, len(matchTokens))
		for _, token := range matchTokens {
			if strings.Contains(contentLow, token) {
				matched[token] = struct{}{}
			}
		}
		results = append(results, map[string]any{
			"hash":                row.Hash,
			"content":             row.Content,
			"rank":                rank + 1,
			"bm25_score":          row.BM25Score,
			"score":               -row.BM25Score,
			"matched_token_count": len(matched),
			"matched_token_ratio": float64(len(matched)) / float64(tokenCount),
		})
	}
	return results
}

// SearchRelations 执行关系稀疏检索
func (i *SparseBM25Index) SearchRelations(query string, k int, allowedIDs []string) []map[string]any {
	if !i.config.Enabled || !i.config.EnableRelationSparseFallback {
		return nil
	}
	if i.config.LazyLoad && !i.Loaded() && !i.EnsureLoaded() {
		return nil
	}
	if !i.Loaded() {
		return nil
	}
	matchQuery := BuildMatchQuery(MatchTokens(i.Tokenize(query)))
	if matchQuery == "" {
		return nil
	}
	limit := max(k, 1)
	allowed, emptyAllowed := normalizeAllowedIDs(allowedIDs)
	if emptyAllowed {
		return nil
	}
	rows := i.backend.SearchRelations(matchQuery, limit, i.config.RelationMaxDocLen, false, allowed, i.conn)
	results := make([]map[string]any, 0, len(rows))
	for rank, row := range rows {
		results = append(results, map[string]any{
			"hash":       row.Hash,
			"subject":    row.Subject,
			"predicate":  row.Predicate,
			"object":     row.Object,
			"content":    row.Content,
			"rank":       rank + 1,
			"bm25_score": row.BM25Score,
			"score":      -row.BM25Score,
		})
	}
	return results
}

func (i *SparseBM25Index) fallbackSubstringSearch(tokens []string, limit int, allowedIDs []string) []storage.FTSSearchHit {
	if len(tokens) == 0 {
		return nil
	}
	uniq := dedupeTokens(tokens)
	if len(uniq) > 32 {
		uniq = uniq[:32]
	}
	if len(uniq) == 0 {
		return nil
	}
	if i.config.EnableNgramFallbackIndex {
		i.store.EnsureParagraphNgramSchema(i.conn)
		if i.store.IsParagraphNgramReady(i.config.CharNgramN, i.conn) {
			if rows := i.store.NgramSearchParagraphs(uniq, limit, i.config.MaxDocLen, i.conn, allowedIDs); len(rows) > 0 {
				return rows
			}
		} else {
			zap.L().Debug("paragraph ngram 索引未就绪，跳过 ngram fallback")
		}
	}
	if !i.config.EnableLikeFallback {
		return nil
	}
	conditions := strings.Repeat("p.content LIKE ? OR ", len(uniq)-1) + "p.content LIKE ?"
	params := make([]any, 0, len(uniq)+2)
	for _, token := range uniq {
		params = append(params, "%"+token+"%")
	}
	scopeClause := ""
	if allowedIDs != nil {
		allowed := dedupeStrings(allowedIDs)
		if len(allowed) == 0 {
			return nil
		}
		scopeClause = "AND p.hash IN (SELECT value FROM json_each(?))"
		encoded, err := json.Marshal(allowed)
		if err != nil {
			return nil
		}
		params = append(params, string(encoded))
	}
	scanLimit := max(limit*8, 200)
	params = append(params, scanLimit)
	sqlText := `
		SELECT p.hash, p.content
		FROM paragraphs p
		WHERE (p.is_deleted IS NULL OR p.is_deleted = 0)
		  AND (` + conditions + `)
		  ` + scopeClause + `
		LIMIT ?
	`
	rows, err := i.store.Query(sqlText, params...)
	if err != nil || len(rows) == 0 {
		return nil
	}
	tokenCount := max(len(uniq), 1)
	type scored struct {
		hash    string
		content string
		score   float64
	}
	graded := make([]scored, 0, len(rows))
	for _, row := range rows {
		content := stringFromMap(row, "content")
		contentLow := strings.ToLower(content)
		matched := 0
		matchedLen := 0
		for _, token := range uniq {
			if strings.Contains(contentLow, token) {
				matched++
				matchedLen += len([]rune(token))
			}
		}
		if matched == 0 {
			continue
		}
		coverage := float64(matched) / float64(tokenCount)
		lengthBonus := float64(matchedLen) / float64(max(len([]rune(contentLow)), 1))
		if i.config.MaxDocLen > 0 {
			content = truncateRunes(content, i.config.MaxDocLen)
		}
		graded = append(graded, scored{hash: stringFromMap(row, "hash"), content: content, score: coverage*0.8 + lengthBonus*0.2})
	}
	sort.SliceStable(graded, func(a, b int) bool { return graded[a].score > graded[b].score })
	if len(graded) > limit {
		graded = graded[:limit]
	}
	out := make([]storage.FTSSearchHit, 0, len(graded))
	for _, item := range graded {
		out = append(out, storage.FTSSearchHit{Hash: item.hash, Content: item.content, BM25Score: -item.score, FallbackScore: item.score})
	}
	return out
}

// UpsertParagraph 同步单段落 FTS 索引
func (i *SparseBM25Index) UpsertParagraph(paragraphHash string) bool {
	if !i.Loaded() {
		return false
	}
	ok := i.store.FtsUpsertParagraph(paragraphHash, i.conn)
	if i.config.EnableTokenizedShadowIndex {
		ok = i.store.FtsUpsertTokenizedParagraph(paragraphHash, i.conn) && ok
	}
	return ok
}

// DeleteParagraph 删除单段落 FTS 索引
func (i *SparseBM25Index) DeleteParagraph(paragraphHash string) bool {
	if !i.Loaded() {
		return false
	}
	ok := i.store.FtsDeleteParagraph(paragraphHash, i.conn)
	if i.config.EnableTokenizedShadowIndex {
		ok = i.store.FtsDeleteTokenizedParagraph(paragraphHash, i.conn) && ok
	}
	return ok
}

// Unload 关闭独立连接并尽量释放 SQLite 内存
func (i *SparseBM25Index) Unload() {
	manager := i.manager
	conn := i.conn
	if conn != nil && i.config.ShrinkMemoryOnUnload {
		i.store.ShrinkMemory(conn)
	}
	if manager != nil {
		_ = manager.CloseAll()
	}
	i.manager = nil
	i.conn = nil
	i.loaded = false
	zap.L().Info("SparseBM25Index unloaded")
}

// Stats 返回稀疏检索统计
func (i *SparseBM25Index) Stats() map[string]any {
	var backendStats map[string]any
	if i.Loaded() {
		backendStats = i.backend.Stats(i.conn)
	} else {
		backendStats = i.backend.Stats(nil)
	}
	docCount := 0
	if value, ok := backendStats["doc_count"].(int); ok {
		docCount = value
	}
	return map[string]any{
		"enabled":                         i.config.Enabled,
		"backend":                         i.config.Backend,
		"mode":                            i.config.Mode,
		"tokenizer_mode":                  i.config.TokenizerMode,
		"enable_tokenized_shadow_index":   i.config.EnableTokenizedShadowIndex,
		"enable_ngram_fallback_index":     i.config.EnableNgramFallbackIndex,
		"enable_like_fallback":            i.config.EnableLikeFallback,
		"enable_relation_sparse_fallback": i.config.EnableRelationSparseFallback,
		"loaded":                          i.Loaded(),
		"has_jieba":                       i.hasJieba,
		"doc_count":                       docCount,
	}
}

func dedupeTokens(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func normalizeAllowedIDs(allowedIDs []string) ([]string, bool) {
	if allowedIDs == nil {
		return nil, false
	}
	out := dedupeStrings(allowedIDs)
	if len(out) == 0 {
		return nil, true
	}
	return out, false
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func stringFromMap(row map[string]any, key string) string {
	value, ok := row[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
