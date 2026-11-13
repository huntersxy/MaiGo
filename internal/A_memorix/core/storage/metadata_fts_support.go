package storage

// A_memorix FTS/ngram 通用辅助函数

import (
	"fmt"
	"strconv"
	"strings"
)

// countRows 执行 COUNT 查询，返回整数值
func countRows(c *Conn, query string, args ...any) (int, error) {
	var value int64
	if err := c.QueryRow(query, args...).Scan(&value); err != nil {
		return 0, err
	}
	return int(value), nil
}

// formatFloat 以 Python str(float) 风格输出时间戳，整数时省略小数位
func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// dedupeStrings 保序去重，对齐 Python list(dict.fromkeys(...))
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// truncateRunes 按码点截断，对齐 Python str[:n]
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// insertTokenizedBatch 批量写入预分词段落索引
func insertTokenizedBatch(c *Conn, pairs [][2]string) error {
	const batchSize = 500
	for start := 0; start < len(pairs); start += batchSize {
		end := start + batchSize
		if end > len(pairs) {
			end = len(pairs)
		}
		batch := pairs[start:end]
		placeholders := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*2)
		for _, pair := range batch {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, pair[0], pair[1])
		}
		query := fmt.Sprintf("INSERT INTO paragraphs_tokenized_fts(paragraph_hash, tokenized) VALUES %s",
			strings.Join(placeholders, ", "))
		if _, err := c.Exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}

// insertNgramBatch 写入单个段落的 ngram
func insertNgramBatch(c *Conn, paragraphHash string, terms []string) error {
	const batchSize = 500
	for start := 0; start < len(terms); start += batchSize {
		end := start + batchSize
		if end > len(terms) {
			end = len(terms)
		}
		batch := terms[start:end]
		placeholders := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*2)
		for _, term := range batch {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, term, paragraphHash)
		}
		query := fmt.Sprintf(
			"INSERT OR IGNORE INTO paragraph_ngrams(term, paragraph_hash) VALUES %s",
			strings.Join(placeholders, ", "),
		)
		if _, err := c.Exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}

// insertNgramPairs 批量写入 term/paragraph_hash 对
func insertNgramPairs(c *Conn, terms []string, hashes []string) error {
	if len(terms) != len(hashes) {
		return fmt.Errorf("ngram 批量写入参数不匹配: terms=%d hashes=%d", len(terms), len(hashes))
	}
	const batchSize = 500
	for start := 0; start < len(terms); start += batchSize {
		end := start + batchSize
		if end > len(terms) {
			end = len(terms)
		}
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*2)
		for i := start; i < end; i++ {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, terms[i], hashes[i])
		}
		query := fmt.Sprintf(
			"INSERT OR IGNORE INTO paragraph_ngrams(term, paragraph_hash) VALUES %s",
			strings.Join(placeholders, ", "),
		)
		if _, err := c.Exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}
