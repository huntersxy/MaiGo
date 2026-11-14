package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maisaruntime "maibot-go/internal/runtime"
	"maibot-go/internal/runtime/execution"
	"maibot-go/internal/runtime/execution/scheduler"
	auth "maibot-go/internal/webui/middleware"
	webuiapi "maibot-go/internal/webui/routers"

	"github.com/go-chi/chi/v5"
)

const testSessionToken = "valid-test-token"

func newTestDeps(instanceDir string) webuiapi.Deps {
	authManager := auth.NewManager(func(token string) bool {
		return token == testSessionToken
	})
	return webuiapi.Deps{
		InstanceDir: instanceDir,
		AdminAuth:   authManager,
	}
}

func setupTestRouter(deps webuiapi.Deps) chi.Router {
	router := chi.NewRouter()
	webuiapi.Mount(router, deps)
	return router
}

func authenticatedRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{
		Name:  auth.WebUISessionCookieName,
		Value: testSessionToken,
	})
	return request
}

func serve(router chi.Router, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestGoDeveloperPage(t *testing.T) {
	router := setupTestRouter(newTestDeps(t.TempDir()))

	for _, path := range []string{"/go", "/go/"} {
		recorder := serve(router, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d", path, recorder.Code)
		}
		if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
			t.Fatalf("GET %s: expected text/html, got %s", path, contentType)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "开发者设置") || !strings.Contains(body, "runtime.toml") {
			t.Fatalf("GET %s: page is missing expected content", path)
		}
	}
}

func TestGoSettingsRequiresAuth(t *testing.T) {
	router := setupTestRouter(newTestDeps(t.TempDir()))

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/webui/go/settings", nil),
		httptest.NewRequest(http.MethodPost, "/api/webui/go/settings", bytes.NewBufferString(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/webui/go/toggle/sqlite_read_pool", bytes.NewBufferString(`{"enabled":false}`)),
		httptest.NewRequest(http.MethodPost, "/api/webui/go/reset", bytes.NewBufferString(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/webui/go/mode", bytes.NewBufferString(`{"mode":"compatibility"}`)),
		httptest.NewRequest(http.MethodPost, "/api/webui/go/restart", bytes.NewBufferString(`{}`)),
	}
	for _, request := range requests {
		recorder := serve(router, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401, got %d", request.Method, request.URL.Path, recorder.Code)
		}
	}
}

type goSettingsPayload struct {
	Success               bool   `json:"success"`
	ExecutionMode         string `json:"execution_mode"`
	WorkerPoolSize        int    `json:"worker_pool_size"`
	NativeModeImplemented bool   `json:"native_mode_implemented"`
	NativeModeActive      bool   `json:"native_mode_active"`
	EnvExecutionMode      string `json:"env_execution_mode"`
	Features              []struct {
		Key         string `json:"key"`
		Implemented bool   `json:"implemented"`
		Phase       int    `json:"phase"`
		Enabled     bool   `json:"enabled"`
	} `json:"features"`
	SessionScheduler *struct {
		Workers      int `json:"workers"`
		RunningTasks int `json:"running_tasks"`
		QueuedTasks  int `json:"queued_tasks"`
	} `json:"session_scheduler"`
}

func featureEnabled(t *testing.T, recorder *httptest.ResponseRecorder, key string) bool {
	t.Helper()
	var payload struct {
		Features []struct {
			Key     string `json:"key"`
			Enabled bool   `json:"enabled"`
		} `json:"features"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode features: %v\n%s", err, recorder.Body.String())
	}
	for _, feature := range payload.Features {
		if feature.Key == key {
			return feature.Enabled
		}
	}
	t.Fatalf("feature %s not found in response", key)
	return false
}

func decodeGoSettings(t *testing.T, recorder *httptest.ResponseRecorder) goSettingsPayload {
	t.Helper()
	var payload goSettingsPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
	}
	return payload
}

func TestGoSettingsLifecycle(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	instanceDir := t.TempDir()
	router := setupTestRouter(newTestDeps(instanceDir))

	// 1. 默认读取：兼容模式、全部关闭、全部未接入。
	initial := decodeGoSettings(t, serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")))
	if !initial.Success {
		t.Fatal("expected success=true")
	}
	if initial.ExecutionMode != string(execution.ModeCompatibility) {
		t.Fatalf("expected compatibility mode, got %s", initial.ExecutionMode)
	}
	if !initial.NativeModeImplemented {
		t.Fatal("expected native_mode_implemented=true")
	}
	if initial.NativeModeActive {
		t.Fatal("expected native_mode_active=false by default")
	}
	if len(initial.Features) == 0 {
		t.Fatal("expected a non-empty features list")
	}
	for _, feature := range initial.Features {
		if feature.Enabled {
			t.Fatalf("expected feature %s to be disabled", feature.Key)
		}
		expectedImplemented := feature.Key == "native_session_scheduler"
		if feature.Implemented != expectedImplemented {
			t.Fatalf("feature %s implemented=%v, want %v", feature.Key, feature.Implemented, expectedImplemented)
		}
		if feature.Phase <= 0 {
			t.Fatalf("expected feature %s to have a phase", feature.Key)
		}
	}

	// 2. 未接入的功能不允许开启。
	enableResponse := serve(router, authenticatedRequest(
		http.MethodPost,
		"/api/webui/go/toggle/native_event_dispatcher",
		`{"enabled":true}`,
	))
	if enableResponse.Code != http.StatusConflict {
		t.Fatalf("expected 409 when enabling an unimplemented feature, got %d: %s", enableResponse.Code, enableResponse.Body.String())
	}

	// 3. 未知功能键返回 400。
	unknownResponse := serve(router, authenticatedRequest(
		http.MethodPost,
		"/api/webui/go/toggle/not_a_feature",
		`{"enabled":true}`,
	))
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown feature, got %d", unknownResponse.Code)
	}

	// 4. 仍然允许显式关闭（用于修复手工写入的配置）。
	disableResponse := serve(router, authenticatedRequest(
		http.MethodPost,
		"/api/webui/go/toggle/native_session_scheduler",
		`{"enabled":false}`,
	))
	if disableResponse.Code != http.StatusOK {
		t.Fatalf("expected 200 when disabling a feature, got %d: %s", disableResponse.Code, disableResponse.Body.String())
	}

	// 5. 已接入的会话并发调度允许开启，并与 native 模式共同生效。
	enableScheduler := serve(router, authenticatedRequest(
		http.MethodPost,
		"/api/webui/go/toggle/native_session_scheduler",
		`{"enabled":true}`,
	))
	if enableScheduler.Code != http.StatusOK {
		t.Fatalf("expected 200 when enabling the scheduler, got %d: %s", enableScheduler.Code, enableScheduler.Body.String())
	}
	if !featureEnabled(t, enableScheduler, "native_session_scheduler") {
		t.Fatal("expected native_session_scheduler to be enabled")
	}

	// 6. 保存执行模式与工作池大小。
	saveResponse := serve(router, authenticatedRequest(
		http.MethodPost,
		"/api/webui/go/settings",
		`{"execution_mode":"native","worker_pool_size":6}`,
	))
	if saveResponse.Code != http.StatusOK {
		t.Fatalf("expected 200 when saving settings, got %d: %s", saveResponse.Code, saveResponse.Body.String())
	}
	saved := decodeGoSettings(t, serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")))
	if saved.ExecutionMode != string(execution.ModeNative) || saved.WorkerPoolSize != 6 {
		t.Fatalf("saved settings mismatch: %+v", saved)
	}
	if !saved.NativeModeActive {
		t.Fatal("expected native_mode_active=true with native mode and scheduler enabled")
	}

	// 6. 非法模式与工作池大小被拒绝。
	invalidRequests := []string{
		`{"execution_mode":"turbo"}`,
		`{"worker_pool_size":-1}`,
		`{"worker_pool_size":1025}`,
	}
	for _, body := range invalidRequests {
		recorder := serve(router, authenticatedRequest(http.MethodPost, "/api/webui/go/settings", body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, recorder.Code, recorder.Body.String())
		}
	}

	// 7. 重置恢复默认值。
	resetResponse := serve(router, authenticatedRequest(http.MethodPost, "/api/webui/go/reset", `{}`))
	if resetResponse.Code != http.StatusOK {
		t.Fatalf("expected 200 for reset, got %d: %s", resetResponse.Code, resetResponse.Body.String())
	}
	reset := decodeGoSettings(t, serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")))
	if reset.ExecutionMode != string(execution.ModeCompatibility) || reset.WorkerPoolSize != 0 {
		t.Fatalf("reset settings mismatch: %+v", reset)
	}
	if reset.NativeModeActive {
		t.Fatal("expected native_mode_active=false after reset")
	}
	for _, feature := range reset.Features {
		if feature.Enabled {
			t.Fatalf("expected feature %s to be disabled after reset", feature.Key)
		}
	}

	if _, err := os.Stat(execution.RuntimeTomlPath(instanceDir)); err != nil {
		t.Fatalf("expected runtime.toml to be written: %v", err)
	}
}

func TestGoSettingsIncludesSchedulerStats(t *testing.T) {
	deps := newTestDeps(t.TempDir())
	deps.SessionSchedulerStats = func() any {
		return scheduler.Stats{Workers: 4, RunningTasks: 2, QueuedTasks: 1}
	}
	router := setupTestRouter(deps)

	payload := decodeGoSettings(t, serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")))
	if payload.SessionScheduler == nil {
		t.Fatal("expected session_scheduler stats")
	}
	if payload.SessionScheduler.Workers != 4 ||
		payload.SessionScheduler.RunningTasks != 2 ||
		payload.SessionScheduler.QueuedTasks != 1 {
		t.Fatalf("unexpected scheduler stats: %+v", payload.SessionScheduler)
	}
}

func TestGoSettingsReportsEnvOverride(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "native")
	router := setupTestRouter(newTestDeps(t.TempDir()))

	payload := decodeGoSettings(t, serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")))
	if payload.ExecutionMode != string(execution.ModeNative) {
		t.Fatalf("expected env override to win, got %s", payload.ExecutionMode)
	}
	if payload.EnvExecutionMode != "native" {
		t.Fatalf("expected env_execution_mode=native, got %q", payload.EnvExecutionMode)
	}
	if payload.NativeModeActive {
		t.Fatal("native mode without an enabled engine must not be active")
	}

	// 重置写入的是配置文件；响应仍应返回环境变量覆盖后的生效模式。
	reset := decodeGoSettings(t, serve(router, authenticatedRequest(http.MethodPost, "/api/webui/go/reset", `{}`)))
	if reset.ExecutionMode != string(execution.ModeNative) {
		t.Fatalf("expected reset response to report effective env mode, got %s", reset.ExecutionMode)
	}
	if reset.NativeModeActive {
		t.Fatal("reset disables all engines, so native mode must not be active")
	}
}

func TestGoSettingsMalformedConfigCanBeReset(t *testing.T) {
	instanceDir := t.TempDir()
	path := execution.RuntimeTomlPath(instanceDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[execution\nmode = \"native\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	router := setupTestRouter(newTestDeps(instanceDir))
	if recorder := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for malformed config, got %d", recorder.Code)
	}

	if recorder := serve(router, authenticatedRequest(http.MethodPost, "/api/webui/go/reset", `{}`)); recorder.Code != http.StatusOK {
		t.Fatalf("expected reset to recover, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/go/settings", "")); recorder.Code != http.StatusOK {
		t.Fatalf("expected settings after reset, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGoSettingsRestartRequiresWorker(t *testing.T) {
	t.Setenv(maisaruntime.EnvWorkerProcess, "")
	router := setupTestRouter(newTestDeps(t.TempDir()))

	recorder := serve(router, authenticatedRequest(http.MethodPost, "/api/webui/go/restart", `{}`))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409 outside a worker process, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
