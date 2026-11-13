package storage

// A_memorix SQLite 连接管理，逐条对齐 Python core/storage/sqlite_connection.py
// 语义：受管事务深度大于 0 时业务方法调用的 Commit 被延迟、Rollback 记为请求

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// 与 Python 逐字一致的错误文案
var (
	// ErrManagerClosed 对应 Python "SQLite 连接管理器已关闭"
	ErrManagerClosed = errors.New("SQLite 连接管理器已关闭")
	// ErrRollbackRequested 对应 Python "事务中的操作请求了回滚"
	ErrRollbackRequested = errors.New("事务中的操作请求了回滚")
	// ErrUnmanagedConn 对应 Python "外部 SQLite 连接不支持受管事务；请注入 ManagedSQLiteConnection"
	ErrUnmanagedConn = errors.New("外部 SQLite 连接不支持受管事务；请注入 ManagedSQLiteConnection")
)

// DefaultTimeout 对齐 Python sqlite3.connect 的 timeout 默认值
const DefaultTimeout = 30 * time.Second

// Conn 对应 Python ManagedSQLiteConnection 的受管语义
// 额外持有独立数据库句柄，保证 BEGIN/SAVEPOINT 状态不被其他调用者打断
type Conn struct {
	mu    sync.Mutex
	db    *sql.DB
	inTx  bool
	depth int
	// 按深度记录回滚请求，对齐 Python _managed_rollback_requests
	rollbackRequests map[int]struct{}
	savepointCounter int
	closed           bool
}

func newConn(path string, timeout time.Duration) (*Conn, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// 单连接句柄，避免 database/sql 在事务中切换底层连接
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	c := &Conn{db: db, rollbackRequests: make(map[int]struct{})}
	if err := c.applyPragmas(timeout); err != nil {
		_ = db.Close()
		return nil, err
	}
	return c, nil
}

// applyPragmas 对齐 Python _create_connection 的 PRAGMA 顺序与取值
func (c *Conn) applyPragmas(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf("PRAGMA busy_timeout=%d", timeout.Milliseconds()),
	}
	for _, stmt := range pragmas {
		if _, err := c.db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// Commit 受管事务中为空操作，对齐 Python 延迟提交
func (c *Conn) Commit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.depth > 0 {
		return nil
	}
	return c.forceCommitLocked()
}

// Rollback 受管事务中仅记录回滚请求，对齐 Python 语义
func (c *Conn) Rollback() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.depth > 0 {
		c.rollbackRequests[c.depth] = struct{}{}
		return nil
	}
	return c.forceRollbackLocked()
}

// ForceCommit 忽略受管深度直接提交
func (c *Conn) ForceCommit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.forceCommitLocked()
}

// ForceRollback 忽略受管深度直接回滚
func (c *Conn) ForceRollback() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.forceRollbackLocked()
}

func (c *Conn) forceCommitLocked() error {
	if !c.inTx {
		return nil
	}
	_, err := c.db.Exec("COMMIT")
	if err != nil {
		return err
	}
	c.inTx = false
	return nil
}

func (c *Conn) forceRollbackLocked() error {
	if !c.inTx {
		return nil
	}
	_, err := c.db.Exec("ROLLBACK")
	if err != nil {
		return err
	}
	c.inTx = false
	return nil
}

// NextSavepointName 生成 a_memorix_tx_N 名称，对齐 Python 计数器
func (c *Conn) NextSavepointName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.savepointCounter++
	return fmt.Sprintf("a_memorix_tx_%d", c.savepointCounter)
}

// BeginManagedScope 进入受管作用域并返回深度
func (c *Conn) BeginManagedScope() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.depth++
	return c.depth
}

// EndManagedScope 结束作用域，返回该深度是否请求过回滚
func (c *Conn) EndManagedScope(scopeDepth int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, requested := c.rollbackRequests[scopeDepth]
	delete(c.rollbackRequests, scopeDepth)
	c.depth--
	return requested
}

// InTransaction 对齐 Python Connection.in_transaction
func (c *Conn) InTransaction() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inTx
}

// Exec 直连执行，业务方法在事务内直接复用同一连接
func (c *Conn) Exec(query string, args ...any) (sql.Result, error) {
	return c.db.Exec(query, args...)
}

// ExecContext 带上下文执行
func (c *Conn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

// Query 查询多行
func (c *Conn) Query(query string, args ...any) (*sql.Rows, error) {
	return c.db.Query(query, args...)
}

// QueryContext 带上下文查询多行
func (c *Conn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, query, args...)
}

// QueryRow 查询单行
func (c *Conn) QueryRow(query string, args ...any) *sql.Row {
	return c.db.QueryRow(query, args...)
}

// QueryRowContext 带上下文查询单行
func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

// Close 关闭连接
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.db.Close()
}

// Closed 报告连接是否已关闭
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Manager 对应 Python SQLiteConnectionManager
// Python 按线程持连接；Go 侧由调用方显式持有 Conn（NewConn 生成独立连接）
type Manager struct {
	path    string
	timeout time.Duration

	mu      sync.Mutex
	conns   map[*Conn]struct{}
	primary *Conn
	closed  bool
}

// NewManager 创建连接管理器，对齐 Python 构造参数（不立即建立连接）
func NewManager(path string, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Manager{path: path, timeout: timeout, conns: make(map[*Conn]struct{})}
}

// Path 返回数据库路径
func (m *Manager) Path() string { return m.path }

// Conn 返回主连接，不存在时创建
func (m *Manager) Conn() (*Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	if m.primary != nil && !m.primary.Closed() {
		return m.primary, nil
	}
	c, err := newConn(m.path, m.timeout)
	if err != nil {
		return nil, err
	}
	m.conns[c] = struct{}{}
	m.primary = c
	return c, nil
}

// NewConn 创建独立连接，供后台任务隔离使用
func (m *Manager) NewConn() (*Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	c, err := newConn(m.path, m.timeout)
	if err != nil {
		return nil, err
	}
	m.conns[c] = struct{}{}
	return c, nil
}

// CloseCurrent 关闭主连接，对齐 Python close_current
func (m *Manager) CloseCurrent() error {
	m.mu.Lock()
	c := m.primary
	m.primary = nil
	if c != nil {
		delete(m.conns, c)
	}
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}

// CloseAll 关闭全部连接并标记管理器已关闭
func (m *Manager) CloseAll() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	conns := make([]*Conn, 0, len(m.conns))
	for c := range m.conns {
		conns = append(conns, c)
	}
	m.conns = make(map[*Conn]struct{})
	m.primary = nil
	m.mu.Unlock()

	var firstErr error
	for _, c := range conns {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PruneInactiveConnections 回收已结束调用方持有的连接
// Go 无 Python 线程对象，连接由持有者显式关闭，这里恒返回 0
func (m *Manager) PruneInactiveConnections() int { return 0 }

// ConnectionCount 返回活跃连接数
func (m *Manager) ConnectionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.conns)
}

// Closed 报告管理器是否已关闭
func (m *Manager) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
