package storage

// A_memorix FTS/BM25 检索与预分词索引增量维护，对齐 Python core/storage/metadata_fts.py

import (
	"database/sql"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// FtsUpsertTokenizedParagraph 增量维护预分词段落 FTS shadow index
func (s *Store) FtsUpsertTokenizedParagraph(paragraphHash string, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	ownsTransaction := !c.InTransaction()
	exists, err := s.tableExists(c, "paragraphs_tokenized_fts")
	if err != nil || !exists {
		return false
	}
	var content sql.NullString
	err = c.QueryRow(`SELECT hash, content
		FROM paragraphs
		WHERE hash = ?
		  AND (is_deleted IS NULL OR is_deleted = 0)`, paragraphHash).Scan(new(string), &content)
	if err != nil && err != sql.ErrNoRows {
		zap.L().Warn("paragraph tokenized FTS upsert 失败", zap.Error(err))
		if ownsTransaction && c.InTransaction() {
			_ = c.Rollback()
		}
		return false
	}
	if _, err := c.Exec("DELETE FROM paragraphs_tokenized_fts WHERE paragraph_hash = ?", paragraphHash); err != nil {
		zap.L().Warn("paragraph tokenized FTS upsert 失败", zap.Error(err))
		if ownsTransaction && c.InTransaction() {
			_ = c.Rollback()
		}
		return false
	}
	if err == nil {
		if _, execErr := c.Exec(
			"INSERT INTO paragraphs_tokenized_fts(paragraph_hash, tokenized) VALUES (?, ?)",
			paragraphHash, s.tokenizeParagraphForFTS(content.String),
		); execErr != nil {
			zap.L().Warn("paragraph tokenized FTS upsert 失败", zap.Error(execErr))
			if ownsTransaction && c.InTransaction() {
				_ = c.Rollback()
			}
			return false
		}
	}
	if err := s.refreshParagraphTokenizedFTSMeta(c); err != nil {
		zap.L().Warn("paragraph tokenized FTS upsert 失败", zap.Error(err))
		if ownsTransaction && c.InTransaction() {
			_ = c.Rollback()
		}
		return false
	}
	if ownsTransaction {
		if err := c.Commit(); err != nil {
			zap.L().Warn("paragraph tokenized FTS upsert 提交失败", zap.Error(err))
			return false
		}
	}
	return true
}

// FtsDeleteTokenizedParagraph 从预分词段落 FTS shadow index 删除段落
func (s *Store) FtsDeleteTokenizedParagraph(paragraphHash string, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	ownsTransaction := !c.InTransaction()
	exists, err := s.tableExists(c, "paragraphs_tokenized_fts")
	if err != nil || !exists {
		return false
	}
	if _, err := c.Exec("DELETE FROM paragraphs_tokenized_fts WHERE paragraph_hash = ?", paragraphHash); err != nil {
		zap.L().Warn("paragraph tokenized FTS delete 失败", zap.Error(err))
		if ownsTransaction && c.InTransaction() {
			_ = c.Rollback()
		}
		return false
	}
	if err := s.refreshParagraphTokenizedFTSMeta(c); err != nil {
		zap.L().Warn("paragraph tokenized FTS delete 失败", zap.Error(err))
		if ownsTransaction && c.InTransaction() {
			_ = c.Rollback()
		}
		return false
	}
	if ownsTransaction {
		if err := c.Commit(); err != nil {
			zap.L().Warn("paragraph tokenized FTS delete 提交失败", zap.Error(err))
			return false
		}
	}
	return true
}

// FtsUpsertParagraph 将段落写入（或覆盖）到 FTS 索引
func (s *Store) FtsUpsertParagraph(paragraphHash string, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	ownsTransaction := !c.InTransaction()
	var rowid int64
	var content sql.NullString
	err = c.QueryRow("SELECT rowid, content FROM paragraphs WHERE hash = ?", paragraphHash).Scan(&rowid, &content)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		zap.L().Warn("FTS upsert 失败", zap.Error(err))
		if ownsTransaction {
			_ = c.Rollback()
		}
		return false
	}
	if _, err := c.Exec("INSERT OR REPLACE INTO paragraphs_fts(rowid, content) VALUES (?, ?)", rowid, content.String); err != nil {
		zap.L().Warn("FTS upsert 失败", zap.Error(err))
		if ownsTransaction {
			_ = c.Rollback()
		}
		return false
	}
	if ownsTransaction {
		if err := c.Commit(); err != nil {
			zap.L().Warn("FTS upsert 提交失败", zap.Error(err))
			return false
		}
	}
	return true
}

// FtsDeleteParagraph 从 FTS 索引删除段落
func (s *Store) FtsDeleteParagraph(paragraphHash string, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	ownsTransaction := !c.InTransaction()
	var rowid int64
	var content sql.NullString
	err = c.QueryRow("SELECT rowid, content FROM paragraphs WHERE hash = ?", paragraphHash).Scan(&rowid, &content)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		zap.L().Warn("FTS delete 失败", zap.Error(err))
		if ownsTransaction {
			_ = c.Rollback()
		}
		return false
	}
	if _, err := c.Exec(
		"INSERT INTO paragraphs_fts(paragraphs_fts, rowid, content) VALUES ('delete', ?, ?)",
		rowid, content.String,
	); err != nil {
		zap.L().Warn("FTS delete 失败", zap.Error(err))
		if ownsTransaction {
			_ = c.Rollback()
		}
		return false
	}
	if ownsTransaction {
		if err := c.Commit(); err != nil {
			zap.L().Warn("FTS delete 提交失败", zap.Error(err))
			return false
		}
	}
	return true
}

// FtsSearchBM25 使用 FTS5 + bm25 执行全文检索
func (s *Store) FtsSearchBM25(matchQuery string, limit int, maxDocLen int, conn *Conn, allowedHashes []string) []FTSSearchHit {
	if strings.TrimSpace(matchQuery) == "" {
		return []FTSSearchHit{}
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("FTS 查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	scopeClause, scopeParams := allowedHashFilter("p.hash", allowedHashes)
	query := fmt.Sprintf(`SELECT p.hash, p.content, bm25(paragraphs_fts) AS bm25_score
		FROM paragraphs_fts
		JOIN paragraphs p ON p.rowid = paragraphs_fts.rowid
		WHERE paragraphs_fts MATCH ?
		  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
		  %s
		ORDER BY bm25_score ASC
		LIMIT ?`, scopeClause)
	args := append([]any{matchQuery}, scopeParams...)
	args = append(args, max(1, limit))
	rows, err := c.Query(query, args...)
	if err != nil {
		zap.L().Warn("FTS 查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	defer rows.Close()
	return scanFTSSearchHits(rows, maxDocLen)
}

// FtsSearchTokenizedParagraphsBM25 使用预分词段落 FTS5 shadow index 执行 BM25 检索
func (s *Store) FtsSearchTokenizedParagraphsBM25(matchQuery string, limit int, maxDocLen int, conn *Conn, allowedHashes []string) []FTSSearchHit {
	if strings.TrimSpace(matchQuery) == "" {
		return []FTSSearchHit{}
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("paragraph tokenized FTS 查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	scopeClause, scopeParams := allowedHashFilter("p.hash", allowedHashes)
	query := fmt.Sprintf(`SELECT p.hash, p.content, bm25(paragraphs_tokenized_fts) AS bm25_score
		FROM paragraphs_tokenized_fts
		JOIN paragraphs p ON p.hash = paragraphs_tokenized_fts.paragraph_hash
		WHERE paragraphs_tokenized_fts MATCH ?
		  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
		  %s
		ORDER BY bm25_score ASC
		LIMIT ?`, scopeClause)
	args := append([]any{matchQuery}, scopeParams...)
	args = append(args, max(1, limit))
	rows, err := c.Query(query, args...)
	if err != nil {
		zap.L().Warn("paragraph tokenized FTS 查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	defer rows.Close()
	return scanFTSSearchHits(rows, maxDocLen)
}

// FtsSearchRelationsBM25 使用 FTS5 + bm25 执行关系全文检索
func (s *Store) FtsSearchRelationsBM25(matchQuery string, limit int, maxDocLen int, includeInactive bool, conn *Conn, allowedHashes []string) []RelationFTSSearchHit {
	if strings.TrimSpace(matchQuery) == "" {
		return []RelationFTSSearchHit{}
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("relations FTS 查询失败", zap.Error(err))
		return []RelationFTSSearchHit{}
	}
	activeClause := ""
	if !includeInactive {
		activeClause = " AND (r.is_inactive IS NULL OR r.is_inactive = 0)"
	}
	scopeClause, scopeParams := allowedHashFilter("r.hash", allowedHashes)
	query := fmt.Sprintf(`SELECT
			r.hash,
			r.subject,
			r.predicate,
			r.object,
			bm25(relations_fts) AS bm25_score
		FROM relations_fts
		JOIN relations r ON r.hash = relations_fts.relation_hash
		WHERE relations_fts MATCH ?
		%s
		%s
		ORDER BY bm25_score ASC
		LIMIT ?`, activeClause, scopeClause)
	args := append([]any{matchQuery}, scopeParams...)
	args = append(args, max(1, limit))
	rows, err := c.Query(query, args...)
	if err != nil {
		zap.L().Warn("relations FTS 查询失败", zap.Error(err))
		return []RelationFTSSearchHit{}
	}
	defer rows.Close()
	out := make([]RelationFTSSearchHit, 0, limit)
	for rows.Next() {
		var hit RelationFTSSearchHit
		var subject, predicate, object sql.NullString
		if err := rows.Scan(&hit.Hash, &subject, &predicate, &object, &hit.BM25Score); err != nil {
			zap.L().Warn("relations FTS 查询失败", zap.Error(err))
			return []RelationFTSSearchHit{}
		}
		hit.Subject = subject.String
		hit.Predicate = predicate.String
		hit.Object = object.String
		hit.Content = truncateRunes(fmt.Sprintf("%s %s %s", hit.Subject, hit.Predicate, hit.Object), maxDocLen)
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		zap.L().Warn("relations FTS 查询失败", zap.Error(err))
		return []RelationFTSSearchHit{}
	}
	return out
}

// NgramSearchParagraphs 按 ngram 倒排索引检索段落，避免 LIKE 全表扫描
func (s *Store) NgramSearchParagraphs(tokens []string, limit int, maxDocLen int, conn *Conn, allowedHashes []string) []FTSSearchHit {
	normalized := make([]string, 0, len(tokens))
	for _, token := range tokens {
		normalized = append(normalized, strings.ToLower(strings.TrimSpace(token)))
	}
	uniq := make([]string, 0, len(normalized))
	for _, token := range dedupeStrings(normalized) {
		if token != "" {
			uniq = append(uniq, token)
		}
	}
	if len(uniq) == 0 {
		return []FTSSearchHit{}
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("ngram 倒排查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(uniq)), ",")
	scopeClause, scopeParams := allowedHashFilter("p.hash", allowedHashes)
	query := fmt.Sprintf(`SELECT
			p.hash,
			p.content,
			COUNT(*) AS hit_terms
		FROM paragraph_ngrams ng
		JOIN paragraphs p ON p.hash = ng.paragraph_hash
		WHERE ng.term IN (%s)
		  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
		  %s
		GROUP BY p.hash, p.content
		ORDER BY hit_terms DESC
		LIMIT ?`, placeholders, scopeClause)
	args := make([]any, 0, len(uniq)+len(scopeParams)+1)
	for _, token := range uniq {
		args = append(args, token)
	}
	args = append(args, scopeParams...)
	args = append(args, max(1, limit))
	rows, err := c.Query(query, args...)
	if err != nil {
		zap.L().Warn("ngram 倒排查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	defer rows.Close()
	tokenCount := len(uniq)
	if tokenCount < 1 {
		tokenCount = 1
	}
	out := make([]FTSSearchHit, 0, limit)
	for rows.Next() {
		var hit FTSSearchHit
		var content sql.NullString
		var hitTerms int
		if err := rows.Scan(&hit.Hash, &content, &hitTerms); err != nil {
			zap.L().Warn("ngram 倒排查询失败", zap.Error(err))
			return []FTSSearchHit{}
		}
		score := float64(hitTerms) / float64(tokenCount)
		hit.Content = truncateRunes(content.String, maxDocLen)
		hit.BM25Score = -score
		hit.FallbackScore = score
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		zap.L().Warn("ngram 倒排查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	return out
}

// FtsDocCount 获取 FTS 文档数量
func (s *Store) FtsDocCount(conn *Conn) int {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0
	}
	count, err := countRows(c, "SELECT COUNT(1) FROM paragraphs_fts")
	if err != nil {
		return 0
	}
	return count
}

// ShrinkMemory 请求 SQLite 收缩当前连接缓存
func (s *Store) ShrinkMemory(conn *Conn) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return
	}
	if _, err := c.Exec("PRAGMA shrink_memory"); err != nil {
		zap.L().Debug("PRAGMA shrink_memory 失败", zap.Error(err))
	}
}

// scanFTSSearchHits 读取段落检索结果，对齐 Python 结果裁剪
func scanFTSSearchHits(rows *sql.Rows, maxDocLen int) []FTSSearchHit {
	out := make([]FTSSearchHit, 0, 16)
	for rows.Next() {
		var hit FTSSearchHit
		var content sql.NullString
		if err := rows.Scan(&hit.Hash, &content, &hit.BM25Score); err != nil {
			zap.L().Warn("FTS 查询失败", zap.Error(err))
			return []FTSSearchHit{}
		}
		hit.Content = truncateRunes(content.String, maxDocLen)
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		zap.L().Warn("FTS 查询失败", zap.Error(err))
		return []FTSSearchHit{}
	}
	return out
}
