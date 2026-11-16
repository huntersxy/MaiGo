package host

// 宿主的 Bot 登记表；不依赖单 Bot 的配置、日志或数据库模块，对齐 src/host/registry.py

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/glebarez/go-sqlite"
)

const registrySchemaVersion = 1

var botIDPattern = regexp.MustCompile("^[0-9a-f]{32}$")

// BotRecord 一个登记的 Bot；Owned 为 false 时目录不归宿主所有，宿主只启动它
type BotRecord struct {
	BotID          string
	Name           string
	Directory      string
	Owned          bool
	CreatedAt      string
	Initialization string
	Error          string
	DesiredState   string
	RestartPolicy  string
}

const registrySchema = `
CREATE TABLE bots (
    bot_id TEXT PRIMARY KEY CHECK (length(bot_id) = 32),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    directory TEXT NOT NULL UNIQUE,
    owned INTEGER NOT NULL CHECK (owned IN (0, 1)),
    created_at TEXT NOT NULL,
    initialization TEXT NOT NULL CHECK (initialization IN ('initializing', 'initialized', 'failed')),
    error TEXT,
    desired_state TEXT NOT NULL CHECK (desired_state IN ('running', 'stopped')),
    restart_policy TEXT NOT NULL CHECK (restart_policy IN ('on-failure', 'never'))
)`

// Registry 宿主工作区的 Bot 登记表
type Registry struct {
	Root string
}

// NewRegistry 创建登记表句柄
func NewRegistry(root string) *Registry {
	return &Registry{Root: filepath.Clean(root)}
}

func (r *Registry) path() string {
	return filepath.Join(r.Root, "host", "registry.sqlite3")
}

// Exists 登记表文件是否存在
func (r *Registry) Exists() bool {
	info, err := os.Stat(r.path())
	return err == nil && !info.IsDir()
}

// Create 只能创建一次；已存在的注册表（包括空文件或损坏文件）不会被重建
func (r *Registry) Create() error {
	if err := os.MkdirAll(filepath.Dir(r.path()), 0o777); err != nil {
		return err
	}
	file, err := os.OpenFile(r.path(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", r.path())
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(registrySchema); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", registrySchemaVersion))
	return err
}

func (r *Registry) open(writable bool) (*sql.DB, error) {
	if !r.Exists() {
		return nil, fmt.Errorf("宿主工作区未初始化: %s", r.Root)
	}
	db, err := sql.Open("sqlite", r.path())
	if err != nil {
		return nil, err
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		_ = db.Close()
		return nil, err
	}
	if version != registrySchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("不支持的宿主注册表版本: %d，当前支持 %d", version, registrySchemaVersion)
	}
	if writable {
		if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func (r *Registry) storedDirectory(directory string) string {
	directory = filepath.Clean(directory)
	if pathContains(r.Root, directory) {
		relative, err := filepath.Rel(r.Root, directory)
		if err == nil {
			if relative == "." {
				return "."
			}
			return filepath.ToSlash(relative)
		}
	}
	return directory
}

func (r *Registry) record(row *sql.Rows) (BotRecord, error) {
	var record BotRecord
	var owned int
	var errText sql.NullString
	err := row.Scan(&record.BotID, &record.Name, &record.Directory, &owned, &record.CreatedAt,
		&record.Initialization, &errText, &record.DesiredState, &record.RestartPolicy)
	if err != nil {
		return BotRecord{}, err
	}
	if filepath.IsAbs(record.Directory) {
		record.Directory = filepath.Clean(record.Directory)
	} else {
		record.Directory = filepath.Clean(filepath.Join(r.Root, record.Directory))
	}
	record.Owned = owned == 1
	record.Error = errText.String
	return record, nil
}

// ListBots 按创建时间列出全部 Bot
func (r *Registry) ListBots() ([]BotRecord, error) {
	db, err := r.open(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT * FROM bots ORDER BY created_at, bot_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BotRecord, 0)
	for rows.Next() {
		record, err := r.record(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// GetBot 按 ID 读取一个 Bot
func (r *Registry) GetBot(botID string) (BotRecord, error) {
	if !botIDPattern.MatchString(botID) {
		return BotRecord{}, fmt.Errorf("无效的 Bot ID: %s", botID)
	}
	db, err := r.open(false)
	if err != nil {
		return BotRecord{}, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT * FROM bots WHERE bot_id = ?", botID)
	if err != nil {
		return BotRecord{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return BotRecord{}, fmt.Errorf("未找到 Bot: %s", botID)
	}
	return r.record(rows)
}

// AddBot 登记新目录；目录不得与已登记目录相同或互相包含（工作区 bots/ 目录除外）
func (r *Registry) AddBot(name, directory string, owned bool, initialization string) (BotRecord, error) {
	name, err := validateBotName(name)
	if err != nil {
		return BotRecord{}, err
	}
	directory = filepath.Clean(directory)
	botsRoot := filepath.Join(r.Root, "bots")
	existing, err := r.ListBots()
	if err != nil {
		return BotRecord{}, err
	}
	for _, item := range existing {
		if pathContains(item.Directory, directory) && !pathContains(botsRoot, directory) {
			return BotRecord{}, fmt.Errorf("目录已属于 Bot %s: %s", item.Name, item.Directory)
		}
		if pathContains(directory, item.Directory) {
			return BotRecord{}, fmt.Errorf("目录包含已登记的 Bot %s: %s", item.Name, item.Directory)
		}
	}
	botID := newBotID()
	db, err := r.open(true)
	if err != nil {
		return BotRecord{}, err
	}
	defer db.Close()
	_, err = db.Exec(
		"INSERT INTO bots (bot_id, name, directory, owned, created_at, initialization, desired_state, restart_policy) "+
			"VALUES (?, ?, ?, ?, ?, ?, 'running', 'on-failure')",
		botID, name, r.storedDirectory(directory), boolToInt(owned), time.Now().UTC().Format(time.RFC3339Nano), initialization,
	)
	if err != nil {
		return BotRecord{}, err
	}
	return r.GetBot(botID)
}

func (r *Registry) update(botID, assignments string, values ...any) (BotRecord, error) {
	db, err := r.open(true)
	if err != nil {
		return BotRecord{}, err
	}
	defer db.Close()
	result, err := db.Exec("UPDATE bots SET "+assignments+" WHERE bot_id = ?", append(values, botID)...)
	if err != nil {
		return BotRecord{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return BotRecord{}, err
	}
	if affected != 1 {
		return BotRecord{}, fmt.Errorf("未找到 Bot: %s", botID)
	}
	return r.GetBot(botID)
}

// SetInitialization 更新初始化状态与错误
func (r *Registry) SetInitialization(botID, state, errorText string) (BotRecord, error) {
	var value any
	if strings.TrimSpace(errorText) != "" {
		value = errorText
	}
	return r.update(botID, "initialization = ?, error = ?", state, value)
}

// SetDesiredState 更新期望运行状态
func (r *Registry) SetDesiredState(botID, state string) (BotRecord, error) {
	return r.update(botID, "desired_state = ?", state)
}

// RenameBot 修改名称
func (r *Registry) RenameBot(botID, name string) (BotRecord, error) {
	validated, err := validateBotName(name)
	if err != nil {
		return BotRecord{}, err
	}
	return r.update(botID, "name = ?", validated)
}

// SetRestartPolicy 修改重启策略
func (r *Registry) SetRestartPolicy(botID, policy string) (BotRecord, error) {
	return r.update(botID, "restart_policy = ?", policy)
}

// SetDirectory 更新目录与归属
func (r *Registry) SetDirectory(botID, directory string, owned bool) (BotRecord, error) {
	return r.update(botID, "directory = ?, owned = ?", r.storedDirectory(directory), boolToInt(owned))
}

// RemoveBot 删除登记
func (r *Registry) RemoveBot(botID string) error {
	db, err := r.open(true)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec("DELETE FROM bots WHERE bot_id = ?", botID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("未找到 Bot: %s", botID)
	}
	return nil
}

func validateBotName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if len([]rune(trimmed)) < 1 || len([]rune(trimmed)) > 100 {
		return "", fmt.Errorf("Bot 名称应为 1 到 100 个字符")
	}
	return trimmed, nil
}

func pathContains(parent, child string) bool {
	parentText := strings.ToLower(filepath.Clean(parent))
	childText := strings.ToLower(filepath.Clean(child))
	if childText == parentText {
		return true
	}
	return strings.HasPrefix(childText, strings.TrimRight(parentText, string(os.PathSeparator))+string(os.PathSeparator))
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// newBotID 生成 32 位十六进制 Bot ID
func newBotID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}
