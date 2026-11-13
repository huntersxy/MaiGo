package storage

// A_memorix 段落过期时间管理，逐条对齐 Python core/storage/metadata_cleanup.py
// 覆盖 set_paragraph_expiration 与 get_expired_paragraph_hashes

import (
	"errors"
	"fmt"
	"strings"
)

// ExpiredParagraphOptions 对齐 Python get_expired_paragraph_hashes 的关键字参数
type ExpiredParagraphOptions struct {
	// Now 指定的当前时间，nil 表示取系统时间
	Now *float64
	// Limit 单次返回上限，0 表示默认 1000
	Limit int
}

// SetParagraphExpiration 显式设置段落过期时间；普通段落保持 expires_at=NULL
// 对齐 Python set_paragraph_expiration
func (s *Store) SetParagraphExpiration(paragraphHashes []string, expiresAt *float64, reason string) (int, error) {
	hashes := make([]string, 0, len(paragraphHashes))
	for _, item := range paragraphHashes {
		if token := strings.TrimSpace(item); token != "" {
			hashes = append(hashes, token)
		}
	}
	if len(hashes) == 0 {
		return 0, nil
	}
	reasonToken := strings.TrimSpace(reason)
	if expiresAt != nil && reasonToken == "" {
		return 0, errors.New("设置段落过期时间必须提供 reason")
	}
	conn, err := s.Conn()
	if err != nil {
		return 0, err
	}
	var (
		expiresArg any
		reasonArg  any
	)
	if expiresAt != nil {
		expiresArg = *expiresAt
	}
	if reasonToken != "" {
		reasonArg = reasonToken
	}
	args := make([]any, 0, len(hashes)+4)
	args = append(args, expiresArg, expiresArg, reasonArg, nowTimestamp())
	for _, hash := range hashes {
		args = append(args, hash)
	}
	result, err := conn.Exec(fmt.Sprintf(`
		UPDATE paragraphs
		SET expires_at = ?,
			deletion_reason = CASE WHEN ? IS NULL THEN NULL ELSE ? END,
			updated_at = ?
		WHERE hash IN (%s)
		  AND (is_deleted IS NULL OR is_deleted = 0)
	`, cleanupPlaceholders(len(hashes))), args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := conn.Commit(); err != nil {
		return 0, err
	}
	if affected < 0 {
		return 0, nil
	}
	return int(affected), nil
}

// GetExpiredParagraphHashes 返回显式到期且没有永久或外部引用保护的段落
// 对齐 Python get_expired_paragraph_hashes
func (s *Store) GetExpiredParagraphHashes(opts ExpiredParagraphOptions) ([]string, error) {
	conn, err := s.Conn()
	if err != nil {
		return nil, err
	}
	current := nowTimestamp()
	if opts.Now != nil {
		current = *opts.Now
	}
	limit := opts.Limit
	if limit == 0 {
		limit = cleanupDefaultExpiredLimit
	}
	if limit < 1 {
		limit = 1
	}
	rows, err := cleanupQueryMaps(conn, `
		SELECT p.hash
		FROM paragraphs p
		WHERE (p.is_deleted IS NULL OR p.is_deleted = 0)
		  AND p.expires_at IS NOT NULL
		  AND p.expires_at <= ?
		  AND COALESCE(p.is_permanent, 0) = 0
		  AND NOT EXISTS (
		      SELECT 1
		      FROM external_memory_refs r
		      WHERE r.paragraph_hash = p.hash
		  )
		ORDER BY p.expires_at ASC, p.hash ASC
		LIMIT ?
	`, current, limit)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(rows))
	for _, row := range rows {
		hashes = append(hashes, cleanupStringValue(row["hash"]))
	}
	return hashes, nil
}
