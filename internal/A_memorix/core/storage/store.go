package storage

// A_memorix 元数据存储门面，对齐 Python core/storage/metadata_store.py 的连接与事务部分
// 各 mixin 方法按 Python 文件拆分到 schema.go/fts.go/store_paragraphs.go 等文件

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// 与 Python 一致的错误文案
var (
	// ErrStoreNotConnected 对应 Python "MetadataStore 未连接数据库"
	ErrStoreNotConnected = errors.New("MetadataStore 未连接数据库")
	// ErrDataDirMissing 对应 Python "未指定数据目录"
	ErrDataDirMissing = errors.New("未指定数据目录")
	// ErrDataDirNotConfigured 对应 Python "MetadataStore 未配置 data_dir"
	ErrDataDirNotConfigured = errors.New("MetadataStore 未配置 data_dir")
)

// Store 元数据存储，对应 Python MetadataStore
type Store struct {
	mu sync.Mutex

	dataDir string
	dbName  string
	dbPath  string

	manager   *Manager
	override  *Conn
	segmenter Segmenter
}

// NewStore 创建元数据存储，对齐 Python __init__（不立即连接）
func NewStore(dataDir string, dbName string) *Store {
	if dbName == "" {
		dbName = "metadata.db"
	}
	return &Store{dataDir: dataDir, dbName: dbName}
}

// DataDir 返回数据目录
func (s *Store) DataDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataDir
}

// DBName 返回数据库文件名
func (s *Store) DBName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dbName
}

// Connect 连接数据库，对齐 Python connect(data_dir=None, enforce_schema=True)
func (s *Store) Connect(dataDir string, enforceSchema bool) error {
	s.mu.Lock()
	if dataDir == "" {
		dataDir = s.dataDir
	}
	if dataDir == "" {
		s.mu.Unlock()
		return ErrDataDirMissing
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		s.mu.Unlock()
		return err
	}
	dbPath := filepath.Join(dataDir, s.dbName)
	s.dbPath = dbPath
	oldManager := s.manager
	s.manager = NewManager(dbPath, DefaultTimeout)
	s.override = nil
	s.mu.Unlock()

	if oldManager != nil {
		_ = oldManager.CloseAll()
	}

	conn, err := s.Conn()
	if err != nil {
		return err
	}

	schemaExisted, err := s.tableExists(conn, "paragraphs")
	if err != nil {
		return err
	}
	if !schemaExisted {
		if err := s.EnsureSchema(); err != nil {
			return err
		}
	}
	if enforceSchema {
		if err := s.AssertSchemaCompatible(schemaExisted); err != nil {
			return err
		}
	}
	// 初始化 FTS schema（幂等）；失败时跳过 BM25 检索
	s.EnsureFTSSchema(nil)
	return nil
}

// isNoRows 判断查询是否无结果
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// tableExists 查询 sqlite_master 中是否存在指定表
func (s *Store) tableExists(c *Conn, name string) (bool, error) {
	var found string
	err := c.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&found)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return found != "", nil
}

// Close 关闭数据库连接
func (s *Store) Close() error {
	s.mu.Lock()
	override := s.override
	manager := s.manager
	s.override = nil
	s.manager = nil
	s.mu.Unlock()

	var firstErr error
	if override != nil {
		if err := override.Close(); err != nil {
			firstErr = err
		}
	}
	if manager != nil {
		if err := manager.CloseAll(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Conn 返回当前可用连接，对齐 Python _conn 属性
func (s *Store) Conn() (*Conn, error) {
	s.mu.Lock()
	override := s.override
	manager := s.manager
	s.mu.Unlock()
	if override != nil {
		return override, nil
	}
	if manager == nil {
		return nil, ErrStoreNotConnected
	}
	return manager.Conn()
}

// SetConnOverride 注入外部连接，对齐 Python _conn setter（测试与迁移工具使用）
func (s *Store) SetConnOverride(c *Conn) {
	s.mu.Lock()
	s.override = c
	s.mu.Unlock()
}

// ResolveConn 解析可用连接，对齐 Python _resolve_conn
func (s *Store) ResolveConn(c *Conn) (*Conn, error) {
	if c != nil {
		return c, nil
	}
	resolved, err := s.Conn()
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// Transaction 创建统一事务边界，异常时回滚、成功时提交
func (s *Store) Transaction(immediate bool, fn func(tx *Tx) error) error {
	s.mu.Lock()
	override := s.override
	manager := s.manager
	s.mu.Unlock()

	if override != nil {
		wrapper, err := NewConnTransaction(override, immediate)
		if err != nil {
			return err
		}
		tx, err := wrapper.Begin()
		if err != nil {
			return err
		}
		if bodyErr := fn(tx); bodyErr != nil {
			if rbErr := wrapper.Rollback(); rbErr != nil {
				return fmt.Errorf("%w（回滚失败：%v）", bodyErr, rbErr)
			}
			return bodyErr
		}
		return wrapper.Commit()
	}
	if manager == nil {
		return ErrStoreNotConnected
	}
	return manager.Transaction(immediate, fn)
}

// DBPath 获取 SQLite 数据库文件路径，对齐 Python get_db_path
func (s *Store) DBPath() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dbPath != "" {
		return s.dbPath, nil
	}
	if s.dataDir == "" {
		return "", ErrDataDirNotConfigured
	}
	return filepath.Join(s.dataDir, s.dbName), nil
}

// GetConnection 返回当前连接，对齐 Python get_connection
func (s *Store) GetConnection() (*Conn, error) {
	return s.Conn()
}
