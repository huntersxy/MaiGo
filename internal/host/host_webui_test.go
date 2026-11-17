package host

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestWebUI(t *testing.T) (*WebUI, Workspace, chan struct{}) {
	t.Helper()
	workspace, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stopping := make(chan struct{})
	webui, err := NewWebUI(NewSupervisor(workspace), workspace.Config, stopping)
	if err != nil {
		t.Fatal(err)
	}
	return webui, workspace, stopping
}

func TestHostModeRequiresSession(t *testing.T) {
	webui, _, _ := newTestWebUI(t)
	handler := webui.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/host/mode", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("未登录 mode 应返回 200，实际 %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["authenticated"] != false || payload["host"] != true {
		t.Fatalf("未登录 mode 载荷 = %#v", payload)
	}
}

func TestHostBotListRequiresSession(t *testing.T) {
	webui, _, _ := newTestWebUI(t)
	request := httptest.NewRequest(http.MethodGet, "/api/host/bots", nil)
	recorder := httptest.NewRecorder()
	webui.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", recorder.Code)
	}
}

func TestHostTicketLoginFlow(t *testing.T) {
	webui, _, _ := newTestWebUI(t)
	ticket, err := webui.sessions.IssueTicket()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"ticket":"` + ticket + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/host/session", body)
	request.Header.Set("X-MaiBot-Host", "1")
	recorder := httptest.NewRecorder()
	webui.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("票据登录应成功，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录应写入会话 Cookie")
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/host/bots", nil)
	for _, cookie := range cookies {
		listRequest.AddCookie(cookie)
	}
	listRecorder := httptest.NewRecorder()
	webui.Handler().ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("登录后应能读取 Bot 列表，实际 %d", listRecorder.Code)
	}
	if !strings.Contains(listRecorder.Body.String(), "workspace") {
		t.Fatalf("Bot 列表载荷 = %s", listRecorder.Body.String())
	}
}

func TestProxyPathRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"api/host/bots", "api/webui/auth/login", "index.html", "api/../secret"} {
		if allowedProxyPath(path) {
			t.Fatalf("路径应被拒绝: %s", path)
		}
	}
	if !allowedProxyPath("api/webui/status") {
		t.Fatal("业务路径应放行")
	}
}

func TestHostMutationRequiresHeader(t *testing.T) {
	webui, _, _ := newTestWebUI(t)
	request := httptest.NewRequest(http.MethodPost, "/api/host/selection", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	webui.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("缺少管理标识应 403，实际 %d", recorder.Code)
	}
}
