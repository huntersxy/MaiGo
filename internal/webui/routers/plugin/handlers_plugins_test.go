package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"MaiGo/internal/plugin_market"
)

func TestWriteInstallFailureCarriesStructuredConflict(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeInstallFailure(recorder, &pluginmarket.MarketError{
		StatusCode: http.StatusConflict,
		Message:    "存在本地代码修改，可备份后强制更新",
		Payload: &pluginmarket.CodeConflict{
			Code:         "local_changes",
			Message:      "存在本地代码修改，可备份后强制更新",
			Files:        []string{"plugin.py"},
			TargetCommit: strings.Repeat("d", 40),
		},
	})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("状态码应为 409，实际: %d", recorder.Code)
	}
	var body struct {
		Detail pluginmarket.CodeConflict `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Detail.Code != "local_changes" || len(body.Detail.Files) != 1 || body.Detail.TargetCommit == "" {
		t.Fatalf("409 响应应携带结构化冲突详情: %s", recorder.Body.String())
	}
}

func TestMarketplaceUpdateRejectsInvalidExpectedCommit(t *testing.T) {
	deps := Deps{PluginMarket: pluginmarket.NewService()}
	request := httptest.NewRequest(http.MethodPost, "/plugins/update",
		strings.NewReader(`{"plugin_id":"demo.plugin","force":true,"expected_commit":"not-a-commit"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	deps.handlePluginMarketplaceUpdate(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 expected_commit 应返回 422，实际: %d %s", recorder.Code, recorder.Body.String())
	}
}
