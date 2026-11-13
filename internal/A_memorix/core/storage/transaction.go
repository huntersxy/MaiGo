package storage

// A_memorix 受管事务，对齐 Python core/storage/sqlite_connection.py 与 transaction.py

import (
	"database/sql"
	"fmt"
)

func (c *Conn) setInTx(value bool) {
	c.mu.Lock()
	c.inTx = value
	c.mu.Unlock()
}

// Tx 表示一次事务作用域，业务代码通过它执行语句
type Tx struct {
	conn      *Conn
	savepoint string
}

// Conn 返回底层受管连接
func (t *Tx) Conn() *Conn { return t.conn }

// Savepoint 返回本次作用域使用的保存点名称（顶层事务为空）
func (t *Tx) Savepoint() string { return t.savepoint }

// Exec 执行语句
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.conn.Exec(query, args...)
}

// Query 查询多行
func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.conn.Query(query, args...)
}

// QueryRow 查询单行
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRow(query, args...)
}

// beginScope 开启作用域，对齐 Python transaction() 的 BEGIN/SAVEPOINT 选择
func beginScope(c *Conn, immediate bool) (*Tx, error) {
	tx := &Tx{conn: c}
	if c.InTransaction() {
		tx.savepoint = c.NextSavepointName()
		if _, err := c.Exec("SAVEPOINT " + tx.savepoint); err != nil {
			return nil, err
		}
		return tx, nil
	}
	stmt := "BEGIN"
	if immediate {
		stmt = "BEGIN IMMEDIATE"
	}
	if _, err := c.Exec(stmt); err != nil {
		return nil, err
	}
	c.setInTx(true)
	return tx, nil
}

// rollbackScope 回滚当前作用域，对齐 Python _rollback_scope_without_depth_change
func rollbackScope(c *Conn, savepoint string) error {
	if savepoint != "" {
		if _, err := c.Exec("ROLLBACK TO SAVEPOINT " + savepoint); err != nil {
			return err
		}
		_, err := c.Exec("RELEASE SAVEPOINT " + savepoint)
		return err
	}
	return c.ForceRollback()
}

// Transaction 在管理器主连接上执行受管事务
func (m *Manager) Transaction(immediate bool, fn func(tx *Tx) error) error {
	c, err := m.Conn()
	if err != nil {
		return err
	}
	return TransactionOn(c, immediate, fn)
}

// TransactionOn 在指定受管连接上执行事务，嵌套调用自动降级为 SAVEPOINT
func TransactionOn(c *Conn, immediate bool, fn func(tx *Tx) error) error {
	if c == nil {
		return ErrUnmanagedConn
	}
	tx, err := beginScope(c, immediate)
	if err != nil {
		return err
	}
	depth := c.BeginManagedScope()

	if bodyErr := fn(tx); bodyErr != nil {
		c.EndManagedScope(depth)
		if rbErr := rollbackScope(c, tx.savepoint); rbErr != nil {
			return fmt.Errorf("%w（回滚失败：%v）", bodyErr, rbErr)
		}
		return bodyErr
	}

	if c.EndManagedScope(depth) {
		if rbErr := rollbackScope(c, tx.savepoint); rbErr != nil {
			return fmt.Errorf("%w（回滚失败：%v）", ErrRollbackRequested, rbErr)
		}
		return ErrRollbackRequested
	}

	if tx.savepoint != "" {
		if _, err := c.Exec("RELEASE SAVEPOINT " + tx.savepoint); err != nil {
			return err
		}
		return nil
	}
	return c.ForceCommit()
}

// ConnTransaction 为外部注入的受管连接提供事务语义，对齐 Python ConnectionTransaction
// Python 侧要求 ManagedSQLiteConnection，Go 中以 *Conn 表示
type ConnTransaction struct {
	conn      *Conn
	immediate bool
	savepoint string
	active    bool
}

// NewConnTransaction 创建连接事务包装
func NewConnTransaction(c *Conn, immediate bool) (*ConnTransaction, error) {
	if c == nil {
		return nil, ErrUnmanagedConn
	}
	return &ConnTransaction{conn: c, immediate: immediate}, nil
}

// Begin 开启事务，已处于事务时使用 a_memorix_override_N 保存点
func (t *ConnTransaction) Begin() (*Tx, error) {
	if t.active {
		return nil, fmt.Errorf("事务已开启")
	}
	tx := &Tx{conn: t.conn}
	if t.conn.InTransaction() {
		t.savepoint = t.conn.NextSavepointName()
		if _, err := t.conn.Exec("SAVEPOINT " + t.savepoint); err != nil {
			return nil, err
		}
		tx.savepoint = t.savepoint
	} else {
		stmt := "BEGIN"
		if t.immediate {
			stmt = "BEGIN IMMEDIATE"
		}
		if _, err := t.conn.Exec(stmt); err != nil {
			return nil, err
		}
		t.conn.setInTx(true)
	}
	t.active = true
	return tx, nil
}

// Commit 结束事务
func (t *ConnTransaction) Commit() error {
	if !t.active {
		return nil
	}
	t.active = false
	if t.savepoint != "" {
		_, err := t.conn.Exec("RELEASE SAVEPOINT " + t.savepoint)
		return err
	}
	return t.conn.Commit()
}

// Rollback 回滚事务
func (t *ConnTransaction) Rollback() error {
	if !t.active {
		return nil
	}
	t.active = false
	if t.savepoint != "" {
		return rollbackScope(t.conn, t.savepoint)
	}
	return t.conn.Rollback()
}
