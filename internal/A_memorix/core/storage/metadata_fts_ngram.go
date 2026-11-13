package storage

// A_memorix 字符 n-gram 倒排与 FTS 查询，对齐 Python core/storage/metadata_fts.py 后半部分

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// EnsureParagraphNgramSchema 确保段落 ngram 倒排表存在
func (s *Store) EnsureParagraphNgramSchema(conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("paragraph ngram schema 创建失败", zap.Error(err))
		return false
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS paragraph_ngrams (
				term TEXT NOT NULL,
				paragraph_hash TEXT NOT NULL,
				PRIMARY KEY (term, paragraph_hash),
				FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE
			)`,
		`CREATE INDEX IF NOT EXISTS idx_paragraph_ngrams_hash
			ON paragraph_ngrams(paragraph_hash)`,
		`CREATE TABLE IF NOT EXISTS paragraph_ngram_meta (
				key TEXT PRIMARY KEY,
				value TEXT
			)`,
	}
	for _, stmt := range statements {
		if _, err := c.Exec(stmt); err != nil {
			zap.L().Warn("paragraph ngram schema 创建失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("paragraph ngram schema 提交失败", zap.Error(err))
		return false
	}
	return true
}

// charNgrams 生成紧凑文本的字符 n-gram，对齐 Python _char_ngrams
func charNgrams(text string, n int) []string {
	compact := strings.Join(strings.Fields(strings.ToLower(text)), "")
	runes := []rune(compact)
	if len(runes) == 0 {
		return []string{}
	}
	if len(runes) < n {
		return []string{compact}
	}
	out := make([]string, 0, len(runes)-n+1)
	for i := 0; i+n <= len(runes); i++ {
		out = append(out, string(runes[i:i+n]))
	}
	return out
}

// getParagraphNgramNIfReady 读取已初始化的 ngram 配置；未初始化返回 nil
func (s *Store) getParagraphNgramNIfReady(c *Conn) *int {
	var value sql.NullString
	if err := c.QueryRow("SELECT value FROM paragraph_ngram_meta WHERE key='ngram_n'").Scan(&value); err != nil {
		return nil
	}
	if !value.Valid {
		return nil
	}
	parsed, err := strconv.Atoi(value.String)
	if err != nil {
		return nil
	}
	if parsed < 1 {
		parsed = 1
	}
	return &parsed
}

// IsParagraphNgramReady 检查 ngram 索引是否已初始化且与 active 段落数量一致
func (s *Store) IsParagraphNgramReady(n int, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	normalized := n
	if normalized < 1 {
		normalized = 1
	}
	current := s.getParagraphNgramNIfReady(c)
	if current == nil || *current != normalized {
		return false
	}
	paraCount, err := countRows(c, "SELECT COUNT(1) FROM paragraphs WHERE is_deleted IS NULL OR is_deleted = 0")
	if err != nil {
		return false
	}
	var value sql.NullString
	if err := c.QueryRow("SELECT value FROM paragraph_ngram_meta WHERE key='paragraph_count'").Scan(&value); err != nil {
		return false
	}
	if !value.Valid {
		return false
	}
	indexed, err := strconv.Atoi(value.String)
	if err != nil {
		return false
	}
	return paraCount == indexed
}

func (s *Store) setParagraphNgramMetaValue(c *Conn, key string, value string) error {
	_, err := c.Exec(`INSERT INTO paragraph_ngram_meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// adjustParagraphNgramCount 在索引已初始化时维护 active paragraph 计数
func (s *Store) adjustParagraphNgramCount(c *Conn, delta int) {
	if delta == 0 {
		return
	}
	var value sql.NullString
	if err := c.QueryRow("SELECT value FROM paragraph_ngram_meta WHERE key='paragraph_count'").Scan(&value); err != nil || !value.Valid {
		return
	}
	current, err := strconv.Atoi(value.String)
	if err != nil {
		return
	}
	if current < 0 {
		current = 0
	}
	next := current + delta
	if next < 0 {
		next = 0
	}
	_ = s.setParagraphNgramMetaValue(c, "paragraph_count", strconv.Itoa(next))
}

// UpsertParagraphNgramIfReady 若 ngram 索引已初始化，则只增量维护当前段落
func (s *Store) UpsertParagraphNgramIfReady(paragraphHash string, content string, countDelta int, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	n := s.getParagraphNgramNIfReady(c)
	if n == nil {
		return false
	}
	if _, err := c.Exec("DELETE FROM paragraph_ngrams WHERE paragraph_hash = ?", paragraphHash); err != nil {
		zap.L().Warn("paragraph ngram 增量更新失败", zap.Error(err))
		return false
	}
	terms := dedupeStrings(charNgrams(content, *n))
	if err := insertNgramBatch(c, paragraphHash, terms); err != nil {
		zap.L().Warn("paragraph ngram 增量更新失败", zap.Error(err))
		return false
	}
	s.adjustParagraphNgramCount(c, countDelta)
	return true
}

// DeleteParagraphNgramsIfReady 若 ngram 索引已初始化，则批量移除段落 ngram
func (s *Store) DeleteParagraphNgramsIfReady(paragraphHashes []string, countDelta int, conn *Conn) bool {
	hashes := make([]string, 0, len(paragraphHashes))
	for _, raw := range paragraphHashes {
		if strings.TrimSpace(raw) != "" {
			hashes = append(hashes, raw)
		}
	}
	if len(hashes) == 0 {
		return false
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	if s.getParagraphNgramNIfReady(c) == nil {
		return false
	}
	const batchSize = 900
	for start := 0; start < len(hashes); start += batchSize {
		end := start + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		batch := hashes[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for i, h := range batch {
			args[i] = h
		}
		query := fmt.Sprintf("DELETE FROM paragraph_ngrams WHERE paragraph_hash IN (%s)", placeholders)
		if _, err := c.Exec(query, args...); err != nil {
			zap.L().Warn("paragraph ngram 删除失败", zap.Error(err))
			return false
		}
	}
	s.adjustParagraphNgramCount(c, countDelta)
	return true
}

// EnsureParagraphNgramBackfilled 确保段落 ngram 倒排索引已回填
func (s *Store) EnsureParagraphNgramBackfilled(n int, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		return false
	}
	started := time.Now()
	if n < 1 {
		n = 1
	}
	var currentValue sql.NullString
	_ = c.QueryRow("SELECT value FROM paragraph_ngram_meta WHERE key='ngram_n'").Scan(&currentValue)
	currentN, hasCurrent := 0, false
	if currentValue.Valid {
		if parsed, convErr := strconv.Atoi(currentValue.String); convErr == nil {
			currentN, hasCurrent = parsed, true
		}
	}
	paraCount, err := countRows(c, "SELECT COUNT(1) FROM paragraphs WHERE is_deleted IS NULL OR is_deleted = 0")
	if err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	indexedDocs := 0
	var metaValue sql.NullString
	if err := c.QueryRow("SELECT value FROM paragraph_ngram_meta WHERE key='paragraph_count'").Scan(&metaValue); err == nil && metaValue.Valid {
		parsed, convErr := strconv.Atoi(metaValue.String)
		if convErr != nil {
			zap.L().Warn("paragraph ngram 回填失败", zap.Error(convErr))
			_ = c.Rollback()
			return false
		}
		indexedDocs = parsed
	} else {
		count, countErr := countRows(c, "SELECT COUNT(DISTINCT paragraph_hash) FROM paragraph_ngrams")
		if countErr != nil {
			zap.L().Warn("paragraph ngram 回填失败", zap.Error(countErr))
			_ = c.Rollback()
			return false
		}
		indexedDocs = count
	}
	if hasCurrent && currentN == n && paraCount == indexedDocs {
		return true
	}

	if _, err := c.Exec("DELETE FROM paragraph_ngrams"); err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	rows, err := c.Query(`SELECT hash, content
		FROM paragraphs
		WHERE is_deleted IS NULL OR is_deleted = 0`)
	if err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	termCount := 0
	pendingTerms := make([]string, 0, 2000)
	pendingHashes := make([]string, 0, 2000)
	flush := func() error {
		if len(pendingTerms) == 0 {
			return nil
		}
		termCount += len(pendingTerms)
		err := insertNgramPairs(c, pendingTerms, pendingHashes)
		pendingTerms = pendingTerms[:0]
		pendingHashes = pendingHashes[:0]
		return err
	}
	for rows.Next() {
		var hash string
		var content sql.NullString
		if err := rows.Scan(&hash, &content); err != nil {
			rows.Close()
			zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
			_ = c.Rollback()
			return false
		}
		for _, term := range dedupeStrings(charNgrams(content.String, n)) {
			pendingTerms = append(pendingTerms, term)
			pendingHashes = append(pendingHashes, hash)
		}
		if len(pendingTerms) >= 2000 {
			if err := flush(); err != nil {
				rows.Close()
				zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
				_ = c.Rollback()
				return false
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := flush(); err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := s.setParagraphNgramMetaValue(c, "ngram_n", strconv.Itoa(n)); err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := s.setParagraphNgramMetaValue(c, "paragraph_count", strconv.Itoa(paraCount)); err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := s.setParagraphNgramMetaValue(c, "updated_at", formatFloat(nowTimestamp())); err != nil {
		zap.L().Warn("paragraph ngram 回填失败", zap.Error(err))
		_ = c.Rollback()
		return false
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("paragraph ngram 回填提交失败", zap.Error(err))
		return false
	}
	zap.L().Info("paragraph ngram 回填完成",
		zap.Int("n", n),
		zap.Int("paragraphs", paraCount),
		zap.Int("terms", termCount),
		zap.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000.0),
	)
	return true
}
