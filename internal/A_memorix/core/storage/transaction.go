package storage

// A_memorix 受管事务，对齐 Python core/storage/sqlite_connection.py 与 transaction.py

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx 表示一次事务作用域，业务代码通过它执行语句
type Tx struct {
	conn      *Conn
	savepoint string
}

// Conn 返回底层受管连接；顶层事务返回独占物理连接的事务视图
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

// beginScope 开启作用域，对齐 Python transaction() 的 BEGIN/SAVEPOINT 选择。
// 顶层事务从连接池取出专用物理连接并独占，嵌套作用域在同一连接上使用 SAVEPOINT
func beginScope(c *Conn, immediate bool) (*Tx, error) {
	if c == nil {
		return nil, ErrUnmanagedConn
	}
	if c.InTransaction() {
		tx := &Tx{conn: c}
		tx.savepoint = c.NextSavepointName()
		if _, err := c.Exec("SAVEPOINT " + tx.savepoint); err != nil {
			return nil, err
		}
		return tx, nil
	}
	session, err := c.db.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	stmt := "BEGIN"
	if immediate {
		stmt = "BEGIN IMMEDIATE"
	}
	if _, err := session.ExecContext(context.Background(), stmt); err != nil {
		_ = session.Close()
		return nil, err
	}
	view := &Conn{db: c.db, session: session, state: newManagedState()}
	view.state.inTx = true
	return &Tx{conn: view}, nil
}

// commitScope 提交当前作用域；顶层事务提交后归还物理连接
func commitScope(tx *Tx) error {
	if tx.savepoint != "" {
		_, err := tx.conn.Exec("RELEASE SAVEPOINT " + tx.savepoint)
		return err
	}
	return closeTop(tx, true)
}

// rollbackScope 回滚当前作用域；顶层事务回滚后归还物理连接
func rollbackScope(tx *Tx) error {
	if tx.savepoint != "" {
		if _, err := tx.conn.Exec("ROLLBACK TO SAVEPOINT " + tx.savepoint); err != nil {
			return err
		}
		_, err := tx.conn.Exec("RELEASE SAVEPOINT " + tx.savepoint)
		return err
	}
	return closeTop(tx, false)
}

// closeTop 结束顶层事务并关闭专用物理连接；业务方法已提前提交时不再重复提交
func closeTop(tx *Tx, commit bool) error {
	view := tx.conn
	if view == nil || view.session == nil {
		return nil
	}
	var execErr error
	if view.InTransaction() {
		stmt := "ROLLBACK"
		if commit {
			stmt = "COMMIT"
		}
		if _, err := view.exec(stmt); err != nil {
			execErr = err
		}
		view.setInTx(false)
	}
	closeErr := view.session.Close()
	if execErr != nil {
		return execErr
	}
	return closeErr
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
	scope := tx.conn
	depth := scope.BeginManagedScope()
	finished := false
	// 业务 panic 时同样要回滚并归还物理连接，否则连接池会被泄漏
	defer func() {
		if !finished {
			_ = rollbackScope(tx)
		}
	}()

	if bodyErr := fn(tx); bodyErr != nil {
		scope.EndManagedScope(depth)
		rbErr := rollbackScope(tx)
		finished = true
		if rbErr != nil {
			return fmt.Errorf("%w（回滚失败：%v）", bodyErr, rbErr)
		}
		return bodyErr
	}

	if scope.EndManagedScope(depth) {
		rbErr := rollbackScope(tx)
		finished = true
		if rbErr != nil {
			return fmt.Errorf("%w（回滚失败：%v）", ErrRollbackRequested, rbErr)
		}
		return ErrRollbackRequested
	}

	commitErr := commitScope(tx)
	finished = true
	return commitErr
}

// ConnTransaction 为外部注入的受管连接提供事务语义，对齐 Python ConnectionTransaction
// Python 侧要求 ManagedSQLiteConnection，Go 中以 *Conn 表示
type ConnTransaction struct {
	conn      *Conn
	immediate bool
	tx        *Tx
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
	tx, err := beginScope(t.conn, t.immediate)
	if err != nil {
		return nil, err
	}
	t.tx = tx
	t.active = true
	return tx, nil
}

// Commit 结束事务
func (t *ConnTransaction) Commit() error {
	if !t.active {
		return nil
	}
	t.active = false
	return commitScope(t.tx)
}

// Rollback 回滚事务
func (t *ConnTransaction) Rollback() error {
	if !t.active {
		return nil
	}
	t.active = false
	return rollbackScope(t.tx)
}
