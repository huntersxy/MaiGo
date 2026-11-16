package search

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	database "MaiGo/internal/common/database"
)

func openHelpTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取数据库连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&database.WebUIHelpSession{}, &database.WebUIHelpTurn{}); err != nil {
		t.Fatalf("迁移答疑表失败: %v", err)
	}
	return db
}

func TestHelpHistoryLifecycle(t *testing.T) {
	db := openHelpTestDB(t)
	sessionID := "11111111-1111-1111-1111-111111111111"
	turnID, history, ok, err := BeginHelpTurn(db, sessionID, "怎么配置模型？", true)
	if err != nil || !ok || len(history) != 0 {
		t.Fatalf("新建会话失败: turn=%d ok=%v history=%v err=%v", turnID, ok, history, err)
	}
	response := &Response{Answer: "在模型配置页添加供应商。", Suggestions: []string{"先测试连接"}, Sources: []Source{{Title: "文档", URL: "https://docs.maibot.chat"}}}
	if err := FinishHelpTurn(db, turnID, response, ""); err != nil {
		t.Fatalf("保存答疑失败: %v", err)
	}
	detail, ok, err := GetHelpSession(db, sessionID)
	if err != nil || !ok || len(detail.Turns) != 1 || detail.Turns[0].Reply == nil || detail.Turns[0].Reply.Answer == "" {
		t.Fatalf("读取会话失败: ok=%v detail=%+v err=%v", ok, detail, err)
	}
	sessions, err := ListHelpSessions(db)
	if err != nil || len(sessions) != 1 || sessions[0].Title != "怎么配置模型？" {
		t.Fatalf("会话列表不符合预期: %+v err=%v", sessions, err)
	}
	nextTurn, history, ok, err := BeginHelpTurn(db, sessionID, "那怎么测试？", false)
	if err != nil || !ok || len(history) != 1 || history[0].Question != "怎么配置模型？" {
		t.Fatalf("续聊上下文不符合预期: turn=%d ok=%v history=%+v err=%v", nextTurn, ok, history, err)
	}
	if err := FinishHelpTurn(db, nextTurn, nil, "调用失败"); err != nil {
		t.Fatalf("保存错误失败: %v", err)
	}
	deleted, err := DeleteHelpSession(db, sessionID)
	if err != nil || !deleted {
		t.Fatalf("删除会话失败: deleted=%v err=%v", deleted, err)
	}
	if _, ok, _ := GetHelpSession(db, sessionID); ok {
		t.Fatal("会话删除后仍可读取")
	}
}

func TestHelpArtifactValidation(t *testing.T) {
	store := &HelpArtifactStore{}
	if _, err := store.Publish(map[string]any{
		"kind": "svg", "title": "流程",
		"content": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect x="1" y="2" width="10" height="10" fill="#fff"/><text x="5" y="5">A</text></svg>`,
	}); err != nil {
		t.Fatalf("合法 SVG 被拒绝: %v", err)
	}
	if _, err := store.Publish(map[string]any{
		"kind": "svg", "title": "坏图", "content": `<svg><script>alert(1)</script></svg>`,
	}); err == nil {
		t.Fatal("含 script 的 SVG 应被拒绝")
	}
	if _, err := store.Publish(map[string]any{
		"kind": "image", "title": "图", "content": "http://example.com/a.png",
	}); err == nil {
		t.Fatal("非 HTTPS 图片应被拒绝")
	}
	if _, err := store.Publish(map[string]any{
		"kind": "markdown", "title": "报告", "content": "# 结果",
	}); err != nil {
		t.Fatalf("合法 Markdown 被拒绝: %v", err)
	}
}

func TestHelpHarnessSkillAndPlan(t *testing.T) {
	harness := NewHelpHarness("zh-CN")
	payload, event := harness.Execute("load_help_skill", map[string]any{"skill": "plugin"})
	if payload["error"] != nil || event == nil || event.Skill != "plugin" {
		t.Fatalf("加载技能失败: payload=%+v event=%+v", payload, event)
	}
	payload, event = harness.Execute("update_help_plan", map[string]any{
		"steps": []any{
			map[string]any{"title": "读取插件", "status": "in_progress"},
			map[string]any{"title": "修改配置", "status": "pending"},
		},
	})
	if payload["error"] != nil || event == nil || len(event.Plan) != 2 {
		t.Fatalf("更新计划失败: payload=%+v event=%+v", payload, event)
	}
	payload, _ = harness.Execute("update_help_plan", map[string]any{
		"steps": []any{
			map[string]any{"title": "a", "status": "in_progress"},
			map[string]any{"title": "b", "status": "in_progress"},
		},
	})
	if payload["error"] == nil {
		t.Fatal("两个 in_progress 步骤应被拒绝")
	}
}

func TestMarketStorePluginFlow(t *testing.T) {
	toolset := &PluginToolset{
		Search: func(context.Context, string, int, int, bool) (map[string]any, error) {
			return map[string]any{"plugins": []map[string]any{{
				"id": "demo.plugin", "name": "Demo", "description": "示例", "repository_url": "https://example.com/demo",
			}}}, nil
		},
		Detail: func(context.Context, string) (map[string]any, error) {
			return map[string]any{
				"plugin": map[string]any{
					"id": "demo.plugin", "name": "Demo", "repository_url": "https://example.com/demo",
				},
				"releases": map[string]any{
					"mode": "releases", "recommended_version": "1.0.0", "sync_error": nil,
					"versions": []any{map[string]any{"version": "1.0.0", "tag": "v1.0.0", "compatible": true}},
				},
			}, nil
		},
		Readme: func(context.Context, string, string, int, bool) (map[string]any, error) {
			return map[string]any{
				"plugin":  map[string]any{"id": "demo.plugin", "name": "Demo"},
				"content": "说明", "chars": 2, "truncated": false, "source_type": "remote",
				"source": "https://example.com/demo",
			}, nil
		},
		ListInstalled: func(context.Context) (map[string]any, error) {
			return map[string]any{"plugins": []map[string]any{}}, nil
		},
		ReadConfig: func(context.Context, string) (map[string]any, error) {
			return map[string]any{
				"plugin": map[string]any{"id": "demo.plugin", "name": "Demo"},
				"fields": []any{map[string]any{"path": []string{"general", "enabled"}, "value": true, "read_only": false}},
			}, nil
		},
		UpdateConfig: func(context.Context, string, []map[string]any) (map[string]any, error) {
			return map[string]any{
				"plugin": map[string]any{"id": "demo.plugin", "name": "Demo"},
				"config_update": map[string]any{
					"id": "update-1", "plugin_id": "demo.plugin", "name": "Demo",
					"changes": []any{}, "message": "配置已保存",
				},
			}, nil
		},
	}
	store := NewMarketStore(toolset)
	ctx := context.Background()
	search := store.Execute(ctx, "search_plugins", map[string]any{"query": "demo"})
	if len(mapSliceValue(search["plugins"])) != 1 {
		t.Fatalf("插件搜索不符合预期: %+v", search)
	}
	detail := store.Execute(ctx, "read_plugin_detail", map[string]any{"plugin_id": "demo.plugin"})
	if stringValue(detail["source_id"]) == "" {
		t.Fatalf("插件详情缺少来源: %+v", detail)
	}
	install := store.Execute(ctx, "prepare_plugin_install", map[string]any{"plugin_id": "demo.plugin"})
	if _, ok := install["install_request"]; !ok {
		t.Fatalf("安装请求不符合预期: %+v", install)
	}
	if _, exists := store.Execute(ctx, "read_plugin_config", map[string]any{"plugin_id": "demo.plugin"})["error"]; exists {
		t.Fatalf("读取插件配置失败: %+v", store)
	}
	update := store.Execute(ctx, "update_plugin_config", map[string]any{
		"plugin_id": "demo.plugin",
		"changes":   []any{map[string]any{"path": []any{"general", "enabled"}, "value": false}},
	})
	if _, ok := update["config_update"]; !ok {
		t.Fatalf("插件配置更新不符合预期: %+v", update)
	}
	if len(store.SelectPlugins([]string{"demo.plugin", "fake"})) != 1 {
		t.Fatalf("插件 ID 过滤不符合预期: %+v", store.Plugins)
	}
}
