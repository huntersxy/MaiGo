package storage

// A_memorix SQLite 连接管理，逐条对齐 Python core/storage/sqlite_connection.py
// 语义：受管事务深度大于 0 时业务方法调用的 Commit 被延迟、Rollback 记为请求

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
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

const (
	// maxOpenConns 每份连接句柄的物理连接上限；读写与后台任务可并行取用
	maxOpenConns = 16
	// maxIdleConns 空闲物理连接保留数
	maxIdleConns = 4
	// connMaxIdleTime 空闲物理连接回收时间
	connMaxIdleTime = 5 * time.Minute
)

// managedState 一次顶层事务的受管状态，对齐 Python ManagedSQLiteConnection 实例字段。
// 基础连接没有事务状态，事务视图从池中取出独立物理连接后独占使用
type managedState struct {
	mu               sync.Mutex
	inTx             bool
	depth            int
	rollbackRequests map[int]struct{}
	savepointCounter int
}

func newManagedState() *managedState {
	return &managedState{rollbackRequests: make(map[int]struct{})}
}

// Conn 对应 Python ManagedSQLiteConnection 的受管语义。
// session 非空时是事务视图：所有语句固定走同一条物理连接，BEGIN/SAVEPOINT 状态不会被其他调用者打断
type Conn struct {
	mu      sync.Mutex
	db      *sql.DB
	session *sql.Conn
	state   *managedState
	closed  bool
}

func newConn(path string, timeout time.Duration) (*Conn, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, timeout))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(connMaxIdleTime)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Conn{db: db}, nil
}

// sqliteDSN 把 Python 建连时的 PRAGMA 落到每条物理连接上
func sqliteDSN(path string, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	query := url.Values{}
	for _, pragma := range []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"cache_size(-64000)",
		"temp_store(MEMORY)",
		"foreign_keys(1)",
		fmt.Sprintf("busy_timeout(%d)", timeout.Milliseconds()),
	} {
		query.Add("_pragma", pragma)
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + query.Encode()
}

// Commit 受管事务中为空操作，对齐 Python 延迟提交
func (c *Conn) Commit() error {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.depth > 0 {
		return nil
	}
	return c.forceCommitLocked()
}

// Rollback 受管事务中仅记录回滚请求，对齐 Python 语义
func (c *Conn) Rollback() error {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.depth > 0 {
		c.state.rollbackRequests[c.state.depth] = struct{}{}
		return nil
	}
	return c.forceRollbackLocked()
}

// ForceCommit 忽略受管深度直接提交
func (c *Conn) ForceCommit() error {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return c.forceCommitLocked()
}

// ForceRollback 忽略受管深度直接回滚
func (c *Conn) ForceRollback() error {
	if c == nil || c.state == nil {
		return nil
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return c.forceRollbackLocked()
}

func (c *Conn) forceCommitLocked() error {
	if !c.state.inTx {
		return nil
	}
	if _, err := c.exec("COMMIT"); err != nil {
		return err
	}
	c.state.inTx = false
	return nil
}

func (c *Conn) forceRollbackLocked() error {
	if !c.state.inTx {
		return nil
	}
	if _, err := c.exec("ROLLBACK"); err != nil {
		return err
	}
	c.state.inTx = false
	return nil
}

// NextSavepointName 生成 a_memorix_tx_N 名称，对齐 Python 计数器
func (c *Conn) NextSavepointName() string {
	if c == nil || c.state == nil {
		return ""
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.savepointCounter++
	return fmt.Sprintf("a_memorix_tx_%d", c.state.savepointCounter)
}

// BeginManagedScope 进入受管作用域并返回深度
func (c *Conn) BeginManagedScope() int {
	if c == nil || c.state == nil {
		return 0
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.depth++
	return c.state.depth
}

// EndManagedScope 结束作用域，返回该深度是否请求过回滚
func (c *Conn) EndManagedScope(scopeDepth int) bool {
	if c == nil || c.state == nil {
		return false
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	_, requested := c.state.rollbackRequests[scopeDepth]
	delete(c.state.rollbackRequests, scopeDepth)
	c.state.depth--
	return requested
}

// InTransaction 对齐 Python Connection.in_transaction
func (c *Conn) InTransaction() bool {
	if c == nil || c.state == nil {
		return false
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return c.state.inTx
}

func (c *Conn) setInTx(value bool) {
	if c == nil || c.state == nil {
		return
	}
	c.state.mu.Lock()
	c.state.inTx = value
	c.state.mu.Unlock()
}

// Exec 直连执行；事务视图固定使用自己的物理连接
func (c *Conn) Exec(query string, args ...any) (sql.Result, error) {
	return c.exec(query, args...)
}

func (c *Conn) exec(query string, args ...any) (sql.Result, error) {
	if c.session != nil {
		return c.session.ExecContext(context.Background(), query, args...)
	}
	return c.db.Exec(query, args...)
}

// ExecContext 带上下文执行
func (c *Conn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.session != nil {
		return c.session.ExecContext(ctx, query, args...)
	}
	return c.db.ExecContext(ctx, query, args...)
}

// Query 查询多行
func (c *Conn) Query(query string, args ...any) (*sql.Rows, error) {
	if c.session != nil {
		return c.session.QueryContext(context.Background(), query, args...)
	}
	return c.db.Query(query, args...)
}

// QueryContext 带上下文查询多行
func (c *Conn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.session != nil {
		return c.session.QueryContext(ctx, query, args...)
	}
	return c.db.QueryContext(ctx, query, args...)
}

// QueryRow 查询单行
func (c *Conn) QueryRow(query string, args ...any) *sql.Row {
	if c.session != nil {
		return c.session.QueryRowContext(context.Background(), query, args...)
	}
	return c.db.QueryRow(query, args...)
}

// QueryRowContext 带上下文查询单行
func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if c.session != nil {
		return c.session.QueryRowContext(ctx, query, args...)
	}
	return c.db.QueryRowContext(ctx, query, args...)
}

// Close 关闭基础连接或事务视图持有的物理连接
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	session := c.session
	db := c.db
	c.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	if db == nil {
		return nil
	}
	return db.Close()
}

// Closed 报告连接是否已关闭
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Manager 对应 Python SQLiteConnectionManager
// Python 按线程持连接；Go 侧基础连接从池中取用物理连接，事务独占一条直到提交或回滚
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
