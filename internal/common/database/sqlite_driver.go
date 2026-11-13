package memory

// SQLite 驱动包装：把 time.Time 绑定成 Python SQLAlchemy 的朴素时间文本。
//
// glebarez/go-sqlite 默认写 `2006-01-02 15:04:05.999999999-07:00`（带时区），而
// SQLAlchemy 写的是 naive 形式。混用会让 SQLAlchemy 读回 aware datetime 并在比较时
// 抛 TypeError，库文件无法与 MaiBot 互通。这里包装 driver.Conn，在 BindValue 时
// 改写为朴素格式，使 Go 写出的字节与 Python 一致。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"time"

	gosqlite "github.com/glebarez/go-sqlite"
)

// maibotSQLiteDriverName 包装驱动的注册名，与上游 "sqlite" 并存不冲突
const maibotSQLiteDriverName = "maibot_sqlite"

// sqliteTimeLayout Python SQLAlchemy 写 SQLite DATETIME 的格式
//
// 对应 Python `str(datetime)` / SQLAlchemy 的 SQLite DATETIME 处理器：
// 有微秒时输出 6 位微秒，无微秒时输出到秒。Go 的 .999999 会自动去掉末尾零，
// 与 Python 一致（Python 也是微秒为 0 时省略小数部分）。
const sqliteTimeLayout = "2006-01-02 15:04:05.999999"

var registerSQLiteDriverOnce sync.Once

// registerSQLiteDriver 注册包装驱动，重复调用安全
func registerSQLiteDriver() {
	registerSQLiteDriverOnce.Do(func() {
		sql.Register(maibotSQLiteDriverName, &pythonTimeDriver{inner: &gosqlite.Driver{}})
	})
}

// pythonTimeDriver 把时间绑定格式改写为 Python 朴素格式
type pythonTimeDriver struct {
	inner driver.Driver
}

func (d *pythonTimeDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &pythonTimeConn{Conn: conn}, nil
}

// pythonTimeConn 包装连接，拦截参数绑定
type pythonTimeConn struct {
	driver.Conn
}

func (c *pythonTimeConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &pythonTimeStmt{Stmt: stmt}, nil
}

func (c *pythonTimeConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := preparer.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &pythonTimeStmt{Stmt: stmt}, nil
	}
	return c.Prepare(query)
}

func (c *pythonTimeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *pythonTimeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, pythonTimeArgs(args))
}

func (c *pythonTimeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, pythonTimeArgs(args))
}

func (c *pythonTimeConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *pythonTimeConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

// pythonTimeStmt 包装预编译语句
type pythonTimeStmt struct {
	driver.Stmt
}

func (s *pythonTimeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.Stmt.Exec(pythonTimeValues(args))
}

func (s *pythonTimeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.Stmt.Query(pythonTimeValues(args))
}

// ExecContext 上游 stmt 未实现 StmtExecContext，这里降级到 Value 语义
func (s *pythonTimeStmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.Exec(namedToValues(args))
}

// QueryContext 上游 stmt 未实现 StmtQueryContext，这里降级到 Value 语义
func (s *pythonTimeStmt) QueryContext(_ context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.Query(namedToValues(args))
}

// namedToValues 把 NamedValue 转成 Value，位置参数按序号回填以保持顺序
func namedToValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// pythonTimeValues 改写 []driver.Value 里的时间
func pythonTimeValues(args []driver.Value) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, arg := range args {
		if t, ok := arg.(time.Time); ok {
			out[i] = formatPythonTime(t)
			continue
		}
		out[i] = arg
	}
	return out
}

// pythonTimeArgs 改写 []driver.NamedValue 里的时间
func pythonTimeArgs(args []driver.NamedValue) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	copy(out, args)
	for i, arg := range out {
		if t, ok := arg.Value.(time.Time); ok {
			out[i].Value = formatPythonTime(t)
		}
	}
	return out
}

// formatPythonTime 输出 Python 的朴素时间文本（不带时区）
//
// 统一先转 UTC 再截掉时区，保证同一时刻在两侧落库字节一致。
func formatPythonTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}

// sqliteTimeString 供调试与测试断言使用
func sqliteTimeString(t time.Time) string {
	return fmt.Sprint(formatPythonTime(t))
}
