package memory

import (
	"path/filepath"
	"testing"
	"time"
)

// TestFormatPythonTimeUsesLocalWallClock 验证落库文本与 Python datetime.now() 的本地墙钟一致
func TestFormatPythonTimeUsesLocalWallClock(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("test", 8*3600)
	t.Cleanup(func() { time.Local = previous })

	instant := time.Date(2026, 11, 17, 10, 35, 31, 597048000, time.UTC)
	if got, want := formatPythonTime(instant), "2026-11-17 18:35:31.597048"; got != want {
		t.Fatalf("formatPythonTime() = %q, want %q", got, want)
	}

	local := time.Date(2026, 11, 17, 18, 35, 31, 0, time.Local)
	if got, want := formatPythonTime(local), "2026-11-17 18:35:31"; got != want {
		t.Fatalf("formatPythonTime(local) = %q, want %q", got, want)
	}
}

// TestSQLiteDriverWritesCompareWithLocalRange 验证按本地时间窗口查询能命中 Go 新写入的记录
func TestSQLiteDriverWritesCompareWithLocalRange(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "time.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(`CREATE TABLE sample (ts DATETIME NOT NULL)`).Error; err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	now := time.Now()
	if err := db.Exec(`INSERT INTO sample(ts) VALUES(?)`, now).Error; err != nil {
		t.Fatalf("写入时间失败: %v", err)
	}
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM sample WHERE ts >= ?`, now.Add(-time.Hour)).Scan(&count).Error; err != nil {
		t.Fatalf("查询时间失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("本地时间窗口命中 %d 条，期望 1 条", count)
	}
	var got time.Time
	if err := db.Raw(`SELECT ts FROM sample`).Scan(&got).Error; err != nil {
		t.Fatalf("读取时间失败: %v", err)
	}
	if want := now.Format("2006-01-02 15:04:05.999999"); got.Format("2006-01-02 15:04:05.999999") != want {
		t.Fatalf("读取时间 = %s, want %s", got.Format(time.DateTime), want)
	}
	if got.Location() != time.Local {
		t.Fatalf("读取时间时区 = %s, want %s", got.Location(), time.Local)
	}
}
