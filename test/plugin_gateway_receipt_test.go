package test

import (
	"testing"

	platformio "MaiGo/internal/platform_io"
)

// TestParsePluginGatewayReceipt 覆盖 Runner 的包裹形状：
// {"success": bool, "result": <插件返回值>}。
// 现场问题：只认嵌套 payload，导致已发送被判失败并反复重发。
func TestParsePluginGatewayReceipt(t *testing.T) {
	cases := []struct {
		name       string
		response   map[string]any
		wantStatus platformio.DeliveryStatus
		wantID     string
		wantErr    string
	}{
		{
			name: "插件发送成功",
			response: map[string]any{
				"success": true,
				"result":  map[string]any{"success": true, "external_message_id": "abc123"},
			},
			wantStatus: platformio.DeliveryStatusSent,
			wantID:     "abc123",
		},
		{
			name: "插件业务失败",
			response: map[string]any{
				"success": true,
				"result":  map[string]any{"success": false, "error": "出站消息缺少目标 OpenID"},
			},
			wantStatus: platformio.DeliveryStatusFailed,
			wantErr:    "出站消息缺少目标 OpenID",
		},
		{
			name: "调用级失败",
			response: map[string]any{
				"success": false,
				"result":  "插件执行异常",
			},
			wantStatus: platformio.DeliveryStatusFailed,
			wantErr:    "插件执行异常",
		},
		{
			name:       "result 为字符串",
			response:   map[string]any{"success": true, "result": "msg-1"},
			wantStatus: platformio.DeliveryStatusSent,
			wantID:     "msg-1",
		},
		{
			name:       "result 为空",
			response:   map[string]any{"success": true, "result": nil},
			wantStatus: platformio.DeliveryStatusSent,
		},
	}
	for _, tc := range cases {
		status, id, _, errText := platformio.ParsePluginGatewayReceipt(tc.response)
		if status != tc.wantStatus {
			t.Fatalf("%s: 期望状态 %v，实际 %v", tc.name, tc.wantStatus, status)
		}
		if id != tc.wantID {
			t.Fatalf("%s: 期望 external id %q，实际 %q", tc.name, tc.wantID, id)
		}
		if errText != tc.wantErr {
			t.Fatalf("%s: 期望错误 %q，实际 %q", tc.name, tc.wantErr, errText)
		}
	}
}
