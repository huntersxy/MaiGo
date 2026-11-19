package telegram

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// Store 持久化游标 / 失败更新 / 贴纸 / 话题
type Store struct {
	path string
	db   *sql.DB
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Initialize() error {
	if s == nil {
		return fmt.Errorf("telegram store 为空")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS cursors (account TEXT PRIMARY KEY, offset INTEGER NOT NULL, updated_at REAL NOT NULL)`); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS stickers (account TEXT, hash TEXT, file_id TEXT NOT NULL, PRIMARY KEY(account, hash))`); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS message_groups (account TEXT, message_id TEXT, group_id TEXT NOT NULL, group_name TEXT NOT NULL, PRIMARY KEY(account, message_id))`); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS topics (account TEXT, chat_id TEXT, thread_id INTEGER, name TEXT NOT NULL, PRIMARY KEY(account, chat_id, thread_id))`); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS failed_updates (account TEXT, update_id INTEGER, payload TEXT NOT NULL, error TEXT NOT NULL, traceback TEXT NOT NULL, failed_at REAL NOT NULL, PRIMARY KEY(account, update_id))`); err != nil {
		_ = db.Close()
		return err
	}
	s.db = db
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) Offset(account string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("telegram store 未初始化")
	}
	var offset int64
	var updatedAt float64
	err := s.db.QueryRow(`SELECT offset, updated_at FROM cursors WHERE account = ?`, account).Scan(&offset, &updatedAt)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if time.Now().Unix()-int64(updatedAt) >= 7*86400 {
		return 0, nil
	}
	return offset, nil
}

func (s *Store) SaveOffset(account string, offset int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("telegram store 未初始化")
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO cursors VALUES (?, ?, ?)`, account, offset, float64(time.Now().Unix()))
	return err
}

func (s *Store) SaveFailedUpdate(account string, update map[string]any, errText, tracebackText string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("telegram store 未初始化")
	}
	updateID, ok := asInt64(update["update_id"])
	if !ok {
		return fmt.Errorf("update 缺少 update_id")
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := float64(time.Now().Unix())
	if _, err := tx.Exec(`INSERT OR REPLACE INTO failed_updates VALUES (?, ?, ?, ?, ?, ?)`, account, updateID, string(payload), errText, tracebackText, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO cursors VALUES (?, ?, ?)`, account, updateID+1, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveMessageGroup(account, messageID string, group map[string]string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("telegram store 未初始化")
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO message_groups VALUES (?, ?, ?, ?)`, account, messageID, group["group_id"], group["group_name"])
	return err
}

func (s *Store) MessageGroup(account, messageID string) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("telegram store 未初始化")
	}
	var groupID, groupName string
	err := s.db.QueryRow(`SELECT group_id, group_name FROM message_groups WHERE account = ? AND message_id = ?`, account, messageID).Scan(&groupID, &groupName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]string{"group_id": groupID, "group_name": groupName}, nil
}

func (s *Store) TopicName(account, chatID string, threadID int64, name string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("telegram store 未初始化")
	}
	if name != "" {
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO topics VALUES (?, ?, ?, ?)`, account, chatID, threadID, name); err != nil {
			return "", err
		}
	}
	var stored string
	err := s.db.QueryRow(`SELECT name FROM topics WHERE account = ? AND chat_id = ? AND thread_id = ?`, account, chatID, threadID).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return stored, nil
}

func (s *Store) Sticker(account, hash string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("telegram store 未初始化")
	}
	var fileID string
	err := s.db.QueryRow(`SELECT file_id FROM stickers WHERE account = ? AND hash = ?`, account, hash).Scan(&fileID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fileID, nil
}

func (s *Store) SaveSticker(account, hash, fileID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("telegram store 未初始化")
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO stickers VALUES (?, ?, ?)`, account, hash, fileID)
	return err
}

func asInt64(v any) (int64, bool) {
	switch typed := v.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		n, err := typed.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}
