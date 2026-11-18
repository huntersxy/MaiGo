package test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"

	"MaiGo/internal/services/system_probe"
	webuiapi "MaiGo/internal/webui/routers"
)

func newProbeDeps(t *testing.T) webuiapi.Deps {
	t.Helper()
	deps := newTestDeps(t.TempDir())
	deps.ResourceProbe = func() *system_probe.Probe {
		return system_probe.New(system_probe.Options{
			InstanceDir: t.TempDir(),
			Version:     "test",
			Timeout:     500 * time.Millisecond,
		})
	}
	return deps
}

func TestSystemResourceProbeRequiresAuth(t *testing.T) {
	router := setupTestRouter(newProbeDeps(t))
	for _, path := range []string{
		"/api/webui/system/resource-probe",
		"/api/webui/system/resource-probe?format=text",
	} {
		recorder := serve(router, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("未认证 GET %s 期望 401，实际 %d", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "Token 无效或已过期") {
			t.Fatalf("401 载荷不符: %s", recorder.Body.String())
		}
	}
}

func TestSystemResourceProbeJSON(t *testing.T) {
	router := setupTestRouter(newProbeDeps(t))
	recorder := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/system/resource-probe?format=json", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("JSON 探针期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("Content-Type 不符: %s", contentType)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("缺少 nosniff")
	}
	body := recorder.Body.Bytes()
	var viaJSON map[string]any
	if err := json.Unmarshal(body, &viaJSON); err != nil {
		t.Fatalf("encoding/json 解析失败: %v", err)
	}
	var viaSonic map[string]any
	if err := sonic.Unmarshal(body, &viaSonic); err != nil {
		t.Fatalf("sonic 解析失败: %v", err)
	}
	for _, key := range []string{"meta", "runtime", "app", "errors"} {
		if _, ok := viaJSON[key]; !ok {
			t.Fatalf("JSON 缺少 %s 段: %s", key, string(body))
		}
	}
	if runtime.GOOS != "linux" {
		errorsValue, _ := viaJSON["errors"].([]any)
		if len(errorsValue) == 0 {
			t.Fatal("非 Linux 平台 errors 不应为空")
		}
	}
}

func TestSystemResourceProbeText(t *testing.T) {
	router := setupTestRouter(newProbeDeps(t))
	recorder := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/system/resource-probe?format=text", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("text 探针期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Fatalf("Content-Type 不符: %s", contentType)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("缺少 nosniff")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "MaiGo system resource probe") || !strings.Contains(body, "[meta]") {
		t.Fatalf("text 缺少固定标题或 meta 段: %s", body)
	}
	if strings.Index(body, "[meta]") > strings.Index(body, "[errors]") {
		t.Fatal("text 段序错误")
	}
}

func TestSystemResourceProbeRejectsUnknownFormat(t *testing.T) {
	router := setupTestRouter(newProbeDeps(t))
	recorder := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/system/resource-probe?format=xml", ""))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未知 format 期望 422，实际 %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "detail") {
		t.Fatalf("422 载荷缺少 detail: %s", recorder.Body.String())
	}
}

func TestSystemResourceProbeNotWired(t *testing.T) {
	router := setupTestRouter(newTestDeps(t.TempDir()))
	recorder := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/system/resource-probe", ""))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未接线探针期望 503，实际 %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "detail") {
		t.Fatalf("503 载荷缺少 detail: %s", recorder.Body.String())
	}
}

func TestWriteProbeJSONEncodingFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	webuiapi.WriteProbeJSON(recorder, http.StatusOK, map[string]any{"bad": math.NaN()})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("不可编码载荷期望 500，实际 %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "编码失败") {
		t.Fatalf("500 载荷缺少 detail: %s", recorder.Body.String())
	}
}
