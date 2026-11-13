package imagememory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// WritebackJournal 对齐 Python ImageWritebackJournal，持久化聊天图片写回任务
type WritebackJournal struct {
	db *sql.DB
}

// OpenWritebackJournal 打开或创建图片写回任务库
func OpenWritebackJournal(path string) (*WritebackJournal, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return nil, fmt.Errorf("创建图片写回任务目录失败: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开图片写回任务库失败: %w", err)
	}
	journal := &WritebackJournal{db: db}
	if err := journal.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return journal, nil
}

func (j *WritebackJournal) initialize() error {
	if _, err := j.db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("设置图片写回任务库 WAL 失败: %w", err)
	}
	const schema = "CREATE TABLE IF NOT EXISTS image_writeback_jobs (" +
		"session_id TEXT NOT NULL," +
		"message_id TEXT NOT NULL," +
		"status TEXT NOT NULL DEFAULT 'pending'," +
		"attempts INTEGER NOT NULL DEFAULT 0," +
		"retry_at REAL NOT NULL DEFAULT 0," +
		"last_error TEXT NOT NULL DEFAULT ''," +
		"updated_at REAL NOT NULL," +
		"PRIMARY KEY (session_id, message_id))"
	if _, err := j.db.Exec(schema); err != nil {
		return fmt.Errorf("创建图片写回任务表失败: %w", err)
	}
	return nil
}

// Close 关闭任务库
func (j *WritebackJournal) Close() error {
	if j == nil || j.db == nil {
		return nil
	}
	return j.db.Close()
}

func writebackNow() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// Enqueue 登记一条图片写回任务，重复登记保持原状
func (j *WritebackJournal) Enqueue(sessionID, messageID string) error {
	if sessionID == "" || messageID == "" {
		return fmt.Errorf("图片入库任务缺少真实聊天流或消息 ID")
	}
	_, err := j.db.Exec(
		"INSERT OR IGNORE INTO image_writeback_jobs(session_id,message_id,updated_at) VALUES (?,?,?)",
		sessionID, messageID, writebackNow(),
	)
	if err != nil {
		return fmt.Errorf("登记图片写回任务失败: %w", err)
	}
	return nil
}

// NextJob 取一条到期的待处理任务
func (j *WritebackJournal) NextJob() (map[string]any, error) {
	row := j.db.QueryRow(
		"SELECT * FROM image_writeback_jobs WHERE status='pending' AND retry_at<=? "+
			"ORDER BY retry_at,updated_at LIMIT 1",
		writebackNow(),
	)
	return scanWritebackJob(row)
}

// Complete 标记任务完成
func (j *WritebackJournal) Complete(sessionID, messageID string) error {
	_, err := j.db.Exec(
		"UPDATE image_writeback_jobs SET status='done',last_error='',updated_at=? WHERE session_id=? AND message_id=?",
		writebackNow(), sessionID, messageID,
	)
	if err != nil {
		return fmt.Errorf("完成图片写回任务失败: %w", err)
	}
	return nil
}

// Fail 记录失败并安排重试，超过上限后置为 failed
func (j *WritebackJournal) Fail(job map[string]any, message string, maxAttempts int) error {
	sessionID := writebackString(job["session_id"])
	messageID := writebackString(job["message_id"])
	if sessionID == "" || messageID == "" {
		return fmt.Errorf("图片写回任务缺少聊天流或消息 ID")
	}
	attempts := writebackInt(job["attempts"]) + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	now := writebackNow()
	delay := 1 << min(attempts, 9)
	if delay > 300 {
		delay = 300
	}
	status := "pending"
	if attempts >= maxAttempts {
		status = "failed"
	}
	_, err := j.db.Exec(
		"UPDATE image_writeback_jobs SET status=?,attempts=?,retry_at=?,last_error=?,updated_at=? "+
			"WHERE session_id=? AND message_id=?",
		status, attempts, now+float64(delay), message, now, sessionID, messageID,
	)
	if err != nil {
		return fmt.Errorf("记录图片写回失败状态失败: %w", err)
	}
	return nil
}

// ListJobs 分页列出写回任务
func (j *WritebackJournal) ListJobs(status string, limit, offset int) (map[string]any, error) {
	where := ""
	args := []any{}
	if status != "" {
		where = " WHERE status=?"
		args = append(args, status)
	}
	var total int
	if err := j.db.QueryRow("SELECT COUNT(*) FROM image_writeback_jobs"+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("统计图片写回任务失败: %w", err)
	}
	rows, err := j.db.Query(
		"SELECT * FROM image_writeback_jobs"+where+" ORDER BY updated_at DESC LIMIT ? OFFSET ?",
		append(args, limit, offset)...,
	)
	if err != nil {
		return nil, fmt.Errorf("查询图片写回任务失败: %w", err)
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, scanErr := scanWritebackJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取图片写回任务失败: %w", err)
	}
	return map[string]any{"success": true, "items": items, "total": total}, nil
}

// RetryFailed 把失败任务重新排回 pending
func (j *WritebackJournal) RetryFailed() (int64, error) {
	result, err := j.db.Exec(
		"UPDATE image_writeback_jobs SET status='pending',attempts=0,retry_at=0,updated_at=? WHERE status='failed'",
		writebackNow(),
	)
	if err != nil {
		return 0, fmt.Errorf("重排图片写回任务失败: %w", err)
	}
	return result.RowsAffected()
}

type writebackScanner interface {
	Scan(dest ...any) error
}

func scanWritebackJob(row writebackScanner) (map[string]any, error) {
	var sessionID, messageID, status, lastError string
	var attempts int64
	var retryAt, updatedAt float64
	if err := row.Scan(&sessionID, &messageID, &status, &attempts, &retryAt, &lastError, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("读取图片写回任务失败: %w", err)
	}
	return map[string]any{
		"session_id": sessionID,
		"message_id": messageID,
		"status":     status,
		"attempts":   attempts,
		"retry_at":   retryAt,
		"last_error": lastError,
		"updated_at": updatedAt,
	}, nil
}

func writebackString(value any) string {
	text, _ := value.(string)
	return text
}

func writebackInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
