package prompt

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTemplateLocatesMainAndInstructions(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	main, err := m.ResolveTemplate("maisaka_chat", "zh-CN", "", false)
	if err != nil {
		t.Fatalf("解析 main 模板: %v", err)
	}
	if main.Category != "main" || main.Path != "prompts/zh-CN/main/maisaka_chat.prompt" {
		t.Fatalf("main 定位 = category %q path %q", main.Category, main.Path)
	}

	instruction, err := m.ResolveTemplate("reply_attachments", "zh-CN", "", false)
	if err != nil {
		t.Fatalf("解析 instructions 模板: %v", err)
	}
	if instruction.Category != "instructions" || instruction.Path != "prompts/zh-CN/instructions/reply_attachments.prompt" {
		t.Fatalf("instructions 定位 = category %q path %q", instruction.Category, instruction.Path)
	}

	ko, err := m.ResolveTemplate("reply_attachments", "ko", "", false)
	if err != nil {
		t.Fatalf("解析 ko 覆盖: %v", err)
	}
	if ko.Locale != "ko" || ko.Category != "instructions" {
		t.Fatalf("ko 定位 = locale %q category %q", ko.Locale, ko.Category)
	}

	fallback, err := m.ResolveTemplate("maisaka_chat", "ko", "", false)
	if err != nil {
		t.Fatalf("解析默认语言回退: %v", err)
	}
	if fallback.Locale == "ko" || fallback.Category != "main" {
		t.Fatalf("回退定位 = locale %q category %q", fallback.Locale, fallback.Category)
	}

	rendered, err := m.LoadPrompt("reply_attachments", "zh-CN", map[string]string{"attachments": "@甲 图片"})
	if err != nil {
		t.Fatalf("LoadPrompt instructions: %v", err)
	}
	if !strings.Contains(rendered, "@甲 图片") || !strings.Contains(rendered, "上述附件由系统发送") {
		t.Fatalf("instructions 渲染异常: %q", rendered)
	}
}

func TestListTemplatesAndMetadataUseCategories(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	infos := m.ListTemplates("zh-CN", false)
	byName := make(map[string]TemplateInfo, len(infos))
	for _, info := range infos {
		byName[info.Name] = info
	}
	for name, wantCategory := range map[string]string{
		"maisaka_chat":        "main",
		"reply_group":         "main",
		"reply_attachments":   "instructions",
		"planner_turn_action": "instructions",
	} {
		info, ok := byName[name]
		if !ok {
			t.Fatalf("ListTemplates 缺少 %q", name)
		}
		if info.Category != wantCategory || info.Locale != "zh-CN" {
			t.Fatalf("%q 分类 = %q locale %q", name, info.Category, info.Locale)
		}
		if info.Path != "prompts/zh-CN/"+wantCategory+"/"+name+".prompt" {
			t.Fatalf("%q 路径 = %q", name, info.Path)
		}
	}

	if meta := m.GetMetadata("reply_attachments", "zh-CN"); meta.DisplayName != "回复附件提示" || !meta.Advanced {
		t.Fatalf("instructions 元信息 = %+v", meta)
	}
	if meta := m.GetMetadata("maisaka_chat", "zh-CN"); meta.DisplayName != "规划器" {
		t.Fatalf("main 元信息 = %+v", meta)
	}
}

func TestLoadPromptCustomOverrideAndLegacyName(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	root := t.TempDir()
	localeDir := filepath.Join(root, "zh-CN")
	if err := os.MkdirAll(localeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localeDir, "retro_replyer.prompt"), []byte("旧名覆盖 {foo}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadFromDir(root); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	resolved, err := m.ResolveTemplate("retro_replyer", "zh-CN", "", true)
	if err != nil {
		t.Fatalf("解析旧名自定义模板: %v", err)
	}
	if resolved.Name != "reply_group" || !resolved.Customized {
		t.Fatalf("旧名解析 = name %q customized %v", resolved.Name, resolved.Customized)
	}
	rendered, err := m.LoadPrompt("retro_replyer", "zh-CN", map[string]string{"foo": "值"})
	if err != nil {
		t.Fatalf("LoadPrompt 旧名: %v", err)
	}
	if rendered != "旧名覆盖 值" {
		t.Fatalf("旧名渲染 = %q", rendered)
	}
}

func TestResolveTemplateCustomCategory(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	root := t.TempDir()
	categoryDir := filepath.Join(root, "zh-CN", "main")
	if err := os.MkdirAll(categoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(categoryDir, "reply_group.prompt"), []byte("分类覆盖 {foo}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadFromDir(root); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	resolved, err := m.ResolveTemplate("reply_group", "zh-CN", "main", true)
	if err != nil {
		t.Fatalf("解析分类自定义模板: %v", err)
	}
	if !resolved.Customized || resolved.Category != "main" || resolved.Template != "分类覆盖 {foo}" {
		t.Fatalf("分类自定义解析 = %+v", resolved)
	}

	builtin, err := m.ResolveTemplate("reply_group", "zh-CN", "", true)
	if err != nil {
		t.Fatalf("无分类解析: %v", err)
	}
	if builtin.Customized {
		t.Fatalf("无分类不应命中分类目录自定义模板: %+v", builtin)
	}
}

func TestEmbeddedFilesFlatPathCompatibility(t *testing.T) {
	raw, err := fs.ReadFile(EmbeddedFiles(), "zh-CN/reply_group.prompt")
	if err != nil {
		t.Fatalf("按旧平铺路径读取失败: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("旧平铺路径读到空模板")
	}
}
