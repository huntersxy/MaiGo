package storage

// A_memorix FTS5/BM25 与字符 n-gram 索引，对齐 Python core/storage/metadata_fts.py

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Segmenter 提供 jieba 搜索模式分词，对齐 Python tokenizer_runtime 的 JIEBA_MODULE
type Segmenter interface {
	CutForSearch(sentence string) []string
}

// FTSSearchHit 段落 FTS 检索命中，对齐 Python 返回的 dict
type FTSSearchHit struct {
	Hash          string
	Content       string
	BM25Score     float64
	FallbackScore float64
}

// RelationFTSSearchHit 关系 FTS 检索命中
type RelationFTSSearchHit struct {
	Hash      string
	Subject   string
	Predicate string
	Object    string
	Content   string
	BM25Score float64
}

// phraseTokenPattern 对齐 Python r"[A-Za-z0-9_]+|[\u4e00-\u9fff]{2,}"
var phraseTokenPattern = regexp.MustCompile(`[A-Za-z0-9_]+|[\x{4e00}-\x{9fff}]{2,}`)

// SetSegmenter 注入分词器；未注入时按 Python HAS_JIEBA=False 的逐字符退化
func (s *Store) SetSegmenter(seg Segmenter) {
	s.mu.Lock()
	s.segmenter = seg
	s.mu.Unlock()
}

func (s *Store) currentSegmenter() Segmenter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.segmenter
}

// allowedHashFilter 生成允许哈希范围过滤，对齐 Python _allowed_hash_filter
func allowedHashFilter(column string, allowedHashes []string) (string, []any) {
	if allowedHashes == nil {
		return "", nil
	}
	normalized := make([]string, 0, len(allowedHashes))
	seen := make(map[string]struct{}, len(allowedHashes))
	for _, raw := range allowedHashes {
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		normalized = append(normalized, raw)
	}
	if len(normalized) == 0 {
		return " AND 0", nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return " AND 0", nil
	}
	return fmt.Sprintf(" AND %s IN (SELECT value FROM json_each(?))", column), []any{string(payload)}
}

// EnsureFTSSchema 确保 FTS5 schema 存在（幂等），对齐 Python ensure_fts_schema
func (s *Store) EnsureFTSSchema(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("FTS5 schema 创建失败", zap.Error(err))
		return false
	}
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS paragraphs_fts
			USING fts5(
				content,
				content='paragraphs',
				content_rowid='rowid',
				tokenize='unicode61'
			)`,
		`CREATE TRIGGER IF NOT EXISTS paragraphs_ai
			AFTER INSERT ON paragraphs
			BEGIN
				INSERT INTO paragraphs_fts(rowid, content)
				VALUES (new.rowid, new.content);
			END`,
		`CREATE TRIGGER IF NOT EXISTS paragraphs_ad
			AFTER DELETE ON paragraphs
			BEGIN
				INSERT INTO paragraphs_fts(paragraphs_fts, rowid, content)
				VALUES ('delete', old.rowid, old.content);
			END`,
		`CREATE TRIGGER IF NOT EXISTS paragraphs_au
			AFTER UPDATE OF content ON paragraphs
			BEGIN
				INSERT INTO paragraphs_fts(paragraphs_fts, rowid, content)
				VALUES ('delete', old.rowid, old.content);
				INSERT INTO paragraphs_fts(rowid, content)
				VALUES (new.rowid, new.content);
			END`,
	}
	for _, stmt := range statements {
		if _, err := c.Exec(stmt); err != nil {
			zap.L().Warn("FTS5 schema 创建失败（可能不支持 FTS5）", zap.Error(err))
			_ = c.Rollback()
			return false
		}
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("FTS5 schema 提交失败", zap.Error(err))
		return false
	}
	return true
}

// EnsureFTSBackfilled 确保 FTS 索引已回填，对齐 Python ensure_fts_backfilled
func (s *Store) EnsureFTSBackfilled(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("FTS 回填失败", zap.Error(err))
		return false
	}
	paraCount, err := countRows(c, "SELECT COUNT(1) FROM paragraphs")
	if err != nil {
		zap.L().Warn("FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	ftsCount, err := countRows(c, "SELECT COUNT(1) FROM paragraphs_fts")
	if err != nil {
		zap.L().Warn("FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if paraCount > 0 && ftsCount != paraCount {
		if _, err := c.Exec("INSERT INTO paragraphs_fts(paragraphs_fts) VALUES ('rebuild')"); err != nil {
			zap.L().Warn("FTS 回填失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
		if err := c.Commit(); err != nil {
			zap.L().Warn("FTS 回填提交失败", zap.Error(err))
			return false
		}
		zap.L().Info("FTS 回填完成", zap.Int("paragraphs", paraCount))
	}
	return true
}

// EnsureRelationsFTSSchema 确保关系 FTS5 schema 存在，对齐 Python ensure_relations_fts_schema
func (s *Store) EnsureRelationsFTSSchema(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("relations FTS5 schema 创建失败", zap.Error(err))
		return false
	}
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS relations_fts
			USING fts5(
				relation_hash UNINDEXED,
				content,
				tokenize='unicode61'
			)`,
		`CREATE TRIGGER IF NOT EXISTS relations_ai
			AFTER INSERT ON relations
			BEGIN
				INSERT INTO relations_fts(relation_hash, content)
				VALUES (
					new.hash,
					COALESCE(new.subject, '') || ' ' || COALESCE(new.predicate, '') || ' ' || COALESCE(new.object, '')
				);
			END`,
		`CREATE TRIGGER IF NOT EXISTS relations_ad
			AFTER DELETE ON relations
			BEGIN
				DELETE FROM relations_fts WHERE relation_hash = old.hash;
			END`,
		`CREATE TRIGGER IF NOT EXISTS relations_au
			AFTER UPDATE OF subject, predicate, object ON relations
			BEGIN
				DELETE FROM relations_fts WHERE relation_hash = new.hash;
				INSERT INTO relations_fts(relation_hash, content)
				VALUES (
					new.hash,
					COALESCE(new.subject, '') || ' ' || COALESCE(new.predicate, '') || ' ' || COALESCE(new.object, '')
				);
			END`,
	}
	for _, stmt := range statements {
		if _, err := c.Exec(stmt); err != nil {
			zap.L().Warn("relations FTS5 schema 创建失败（可能不支持 FTS5）", zap.Error(err))
			_ = c.Rollback()
			return false
		}
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("relations FTS5 schema 提交失败", zap.Error(err))
		return false
	}
	return true
}

// EnsureRelationsFTSBackfilled 确保关系 FTS 索引已回填
func (s *Store) EnsureRelationsFTSBackfilled(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("relations FTS 回填失败", zap.Error(err))
		return false
	}
	relCount, err := countRows(c, "SELECT COUNT(1) FROM relations")
	if err != nil {
		zap.L().Warn("relations FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	ftsCount, err := countRows(c, "SELECT COUNT(1) FROM relations_fts")
	if err != nil {
		zap.L().Warn("relations FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if relCount != ftsCount {
		if _, err := c.Exec("DELETE FROM relations_fts"); err != nil {
			zap.L().Warn("relations FTS 回填失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
		rebuildSQL := `INSERT INTO relations_fts(relation_hash, content)
			SELECT
				r.hash,
				COALESCE(r.subject, '') || ' ' || COALESCE(r.predicate, '') || ' ' || COALESCE(r.object, '')
			FROM relations r`
		if _, err := c.Exec(rebuildSQL); err != nil {
			zap.L().Warn("relations FTS 回填失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
		if err := c.Commit(); err != nil {
			zap.L().Warn("relations FTS 回填提交失败", zap.Error(err))
			return false
		}
		zap.L().Info("relations FTS 回填完成", zap.Int("relations", relCount))
	}
	return true
}

// EnsureParagraphTokenizedFTSSchema 确保预分词段落 FTS5 shadow index 存在
func (s *Store) EnsureParagraphTokenizedFTSSchema(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("paragraph tokenized FTS5 schema 创建失败", zap.Error(err))
		return false
	}
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS paragraphs_tokenized_fts
			USING fts5(
				paragraph_hash UNINDEXED,
				tokenized,
				tokenize='unicode61'
			)`,
		`CREATE TABLE IF NOT EXISTS paragraph_tokenized_fts_meta (
				key TEXT PRIMARY KEY,
				value TEXT
			)`,
	}
	for _, stmt := range statements {
		if _, err := c.Exec(stmt); err != nil {
			zap.L().Warn("paragraph tokenized FTS5 schema 创建失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("paragraph tokenized FTS5 schema 提交失败", zap.Error(err))
		return false
	}
	return true
}

// paragraphPhraseTokens 提取英文/数字词与连续汉字短语，对齐 Python _paragraph_phrase_tokens
func paragraphPhraseTokens(text string) []string {
	matches := phraseTokenPattern.FindAllString(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.ToLower(m))
	}
	return out
}

// tokenizeParagraphForFTS 生成预分词文本，对齐 Python _tokenize_paragraph_for_fts
func (s *Store) tokenizeParagraphForFTS(text string) string {
	tokens := make([]string, 0, len(text))
	if seg := s.currentSegmenter(); seg != nil {
		for _, raw := range seg.CutForSearch(text) {
			token := strings.ToLower(strings.TrimSpace(raw))
			if token != "" {
				tokens = append(tokens, token)
			}
		}
	} else {
		for _, r := range strings.ToLower(text) {
			tokens = append(tokens, string(r))
		}
	}
	tokens = append(tokens, paragraphPhraseTokens(text)...)
	seen := make(map[string]struct{}, len(tokens))
	unique := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		unique = append(unique, token)
	}
	return strings.Join(unique, " ")
}

// refreshParagraphTokenizedFTSMeta 刷新预分词索引元数据
func (s *Store) refreshParagraphTokenizedFTSMeta(c *Conn) error {
	exists, err := s.tableExists(c, "paragraph_tokenized_fts_meta")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	paraCount, err := countRows(c, "SELECT COUNT(1) FROM paragraphs WHERE is_deleted IS NULL OR is_deleted = 0")
	if err != nil {
		return err
	}
	if _, err := c.Exec(`INSERT INTO paragraph_tokenized_fts_meta(key, value) VALUES('paragraph_count', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(paraCount)); err != nil {
		return err
	}
	if _, err := c.Exec(`INSERT INTO paragraph_tokenized_fts_meta(key, value) VALUES('updated_at', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, formatFloat(nowTimestamp())); err != nil {
		return err
	}
	return nil
}

// EnsureParagraphTokenizedFTSBackfilled 确保预分词段落 FTS5 shadow index 已回填
func (s *Store) EnsureParagraphTokenizedFTSBackfilled(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		return false
	}
	started := time.Now()
	if !s.EnsureParagraphTokenizedFTSSchema(c) {
		return false
	}
	paraCount, err := countRows(c, "SELECT COUNT(1) FROM paragraphs WHERE is_deleted IS NULL OR is_deleted = 0")
	if err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	indexed := -1
	var metaValue sql.NullString
	if err := c.QueryRow("SELECT value FROM paragraph_tokenized_fts_meta WHERE key='paragraph_count'").Scan(&metaValue); err == nil && metaValue.Valid {
		parsed, convErr := strconv.Atoi(metaValue.String)
		if convErr != nil {
			zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(convErr))
			_ = c.Rollback()
			return false
		}
		indexed = parsed
	}
	if indexed == paraCount {
		return true
	}

	if _, err := c.Exec("DELETE FROM paragraphs_tokenized_fts"); err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	rows, err := c.Query(`SELECT hash, content
		FROM paragraphs
		WHERE is_deleted IS NULL OR is_deleted = 0`)
	if err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	pairs := make([][2]string, 0, paraCount)
	for rows.Next() {
		var hash string
		var content sql.NullString
		if err := rows.Scan(&hash, &content); err != nil {
			rows.Close()
			zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
		pairs = append(pairs, [2]string{hash, s.tokenizeParagraphForFTS(content.String)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := insertTokenizedBatch(c, pairs); err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := s.refreshParagraphTokenizedFTSMeta(c); err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("paragraph tokenized FTS 回填提交失败", zap.Error(err))
		return false
	}
	zap.L().Info("paragraph tokenized FTS 回填完成",
		zap.Int("paragraphs", paraCount),
		zap.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000.0),
	)
	return true
}
