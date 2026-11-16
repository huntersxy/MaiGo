package schedules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTodoStoreSessionDefaultsAndCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	store := NewTodoStore(path)
	item, err := store.Add("提醒喝水", map[string]any{"delay_seconds": 60.0})
	if err != nil {
		t.Fatal(err)
	}
	if item["session_id"] != "" || item["session_name"] != "主人私聊" {
		t.Fatalf("默认会话字段不符: %#v", item)
	}
	if err := store.Cancel(item["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if len(store.List(false)) != 0 {
		t.Fatal("已取消事项不应出现在未完成列表")
	}
	if len(store.List(true)) != 1 {
		t.Fatal("历史列表应保留已取消事项")
	}
	if _, err := store.Update(item["id"].(string), nil, map[string]any{}); err == nil {
		t.Fatal("已取消事项不应允许修改")
	}
}

func TestTodoStoreQuarantinesBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewTodoStore(path)
	if len(store.List(true)) != 0 {
		t.Fatal("损坏文件应以空列表启动")
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("损坏文件未隔离: %#v", matches)
	}
}
