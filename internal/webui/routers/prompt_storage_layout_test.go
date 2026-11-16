package webuiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

type promptCatalogFixture struct {
	Success   bool     `json:"success"`
	Languages []string `json:"languages"`
	Files     map[string][]struct {
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		Advanced    bool   `json:"advanced"`
		Customized  bool   `json:"customized"`
	} `json:"files"`
}

func writePromptFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newPromptFixtureDeps(t *testing.T) Deps {
	t.Helper()
	root := t.TempDir()
	writePromptFixture(t, root, "prompts/zh-CN/main/reply_group.prompt", "群聊默认 {foo}")
	writePromptFixture(t, root, "prompts/zh-CN/main/.meta.toml",
		"[reply_group]\ndisplay_name = \"群聊模板\"\nadvanced = true\ndescription = \"群聊说明\"\n")
	writePromptFixture(t, root, "prompts/zh-CN/instructions/reply_attachments.prompt", "附件默认 {attachments}")
	writePromptFixture(t, root, "prompts/zh-CN/instructions/.meta.toml",
		"[reply_attachments]\ndisplay_name = \"附件模板\"\nadvanced = true\ndescription = \"附件说明\"\n")
	return Deps{InstanceDir: root}
}

func TestConfigPromptsListSupportsCategoryDirectories(t *testing.T) {
	deps := newPromptFixtureDeps(t)
	recorder := httptest.NewRecorder()
	deps.handleConfigPromptsList(recorder, httptest.NewRequest(http.MethodGet, "/config/prompts", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var catalog promptCatalogFixture
	if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	infos := catalog.Files["zh-CN"]
	byName := make(map[string]int, len(infos))
	for index, info := range infos {
		byName[info.Name] = index
	}
	mainIndex, ok := byName["reply_group.prompt"]
	if !ok {
		t.Fatalf("列表缺少 main 模板: %+v", infos)
	}
	if info := infos[mainIndex]; info.Kind != "main" || info.DisplayName != "群聊模板" || !info.Advanced {
		t.Fatalf("main 条目 = %+v", info)
	}
	instructionIndex, ok := byName["reply_attachments.prompt"]
	if !ok {
		t.Fatalf("列表缺少 instructions 模板: %+v", infos)
	}
	if info := infos[instructionIndex]; info.Kind != "instruction" || info.DisplayName != "附件模板" {
		t.Fatalf("instructions 条目 = %+v", info)
	}
}

func TestConfigPromptsListUsesEmbeddedCategorizedTemplates(t *testing.T) {
	deps := Deps{InstanceDir: t.TempDir()}
	recorder := httptest.NewRecorder()
	deps.handleConfigPromptsList(recorder, httptest.NewRequest(http.MethodGet, "/config/prompts", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var catalog promptCatalogFixture
	if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	infos := catalog.Files["zh-CN"]
	byName := make(map[string]int, len(infos))
	for index, info := range infos {
		byName[info.Name] = index
	}
	mainIndex, ok := byName["maisaka_chat.prompt"]
	if !ok || infos[mainIndex].Kind != "main" {
		t.Fatalf("嵌入 main 模板列表异常: %+v", infos)
	}
	instructionIndex, ok := byName["reply_attachments.prompt"]
	if !ok || infos[instructionIndex].Kind != "instruction" {
		t.Fatalf("嵌入 instructions 模板列表异常: %+v", infos)
	}
	if infos[instructionIndex].DisplayName != "回复附件提示" {
		t.Fatalf("嵌入 instructions 元信息 = %+v", infos[instructionIndex])
	}
}

func TestPromptFileResolutionUsesCategoryAndCustomOverride(t *testing.T) {
	deps := newPromptFixtureDeps(t)

	payload, status, err := deps.loadPromptFile("zh-CN", "retro_replyer.prompt")
	if err != nil || status != 0 {
		t.Fatalf("解析旧名失败: status=%d err=%v", status, err)
	}
	if payload.path != "zh-CN/main/reply_group.prompt" || payload.defaultText != "群聊默认 {foo}" {
		t.Fatalf("旧名解析 = path %q text %q", payload.path, payload.defaultText)
	}

	writePromptFixture(t, deps.InstanceDir, "data/custom_prompts/zh-CN/retro_replyer.prompt", "自定义群聊 {foo}")
	customizedPayload, status, err := deps.loadPromptFile("zh-CN", "reply_group.prompt")
	if err != nil || status != 0 {
		t.Fatalf("解析自定义覆盖失败: status=%d err=%v", status, err)
	}
	if !customizedPayload.customized || customizedPayload.effective != "自定义群聊 {foo}" {
		t.Fatalf("自定义覆盖 = customized %v content %q", customizedPayload.customized, customizedPayload.effective)
	}

	instructionPayload, status, err := deps.loadPromptFile("zh-CN", "reply_attachments.prompt")
	if err != nil || status != 0 {
		t.Fatalf("解析 instructions 文件失败: status=%d err=%v", status, err)
	}
	if instructionPayload.path != "zh-CN/instructions/reply_attachments.prompt" {
		t.Fatalf("instructions 路径 = %q", instructionPayload.path)
	}
}

func TestConfigPromptVersionsResolveCategoryPath(t *testing.T) {
	deps := newPromptFixtureDeps(t)
	request := httptest.NewRequest(http.MethodGet, "/config/prompts/zh-CN/reply_attachments.prompt/versions", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("language", "zh-CN")
	routeContext.URLParams.Add("filename", "reply_attachments.prompt")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()
	deps.handleConfigPromptVersions(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if body["filename"] != "reply_attachments.prompt" || body["language"] != "zh-CN" {
		t.Fatalf("版本响应 = %+v", body)
	}
}
