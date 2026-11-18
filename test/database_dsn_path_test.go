package test

import (
	"testing"

	memory "MaiGo/internal/common/database"
)

func TestSQLitePath(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"sqlite:///abs/MaiBot.db", "/abs/MaiBot.db"},
		{"sqlite:/abs/MaiBot.db", "/abs/MaiBot.db"},
		{"file:/abs/MaiBot.db", "/abs/MaiBot.db"},
		{"data/MaiBot.db", "data/MaiBot.db"},
	}
	for _, item := range cases {
		got, err := memory.SQLitePath(item.dsn)
		if err != nil {
			t.Fatalf("SQLitePath(%q) 返回错误: %v", item.dsn, err)
		}
		if got != item.want {
			t.Fatalf("SQLitePath(%q) = %q，期望 %q", item.dsn, got, item.want)
		}
	}
	got, err := memory.SQLitePath("")
	if err != nil {
		t.Fatalf("空 DSN 不应返回错误: %v", err)
	}
	if got != memory.DefaultSQLiteDSN() {
		t.Fatalf("空 DSN 应回落到默认路径: %q", got)
	}
	if _, err := memory.SQLitePath("postgres://localhost/maibot"); err == nil {
		t.Fatal("PostgreSQL DSN 应返回错误")
	}
}
