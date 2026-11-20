package llm

import (
	"fmt"
	"testing"
)

func TestDiagnoseErrorCollectsHTTPStatus(t *testing.T) {
	statusErr := &HTTPStatusError{
		StatusCode: 500,
		Message:    "upstream unavailable",
		Op:         "chat_completions",
		RequestID:  "req-abc",
	}
	err := attachRequestTraceID(fmt.Errorf("Replyer 模型调用失败: %w", statusErr), "logic-1")
	diag := DiagnoseError(err)
	if diag.Type != "HTTPStatusError" {
		t.Fatalf("Type = %q", diag.Type)
	}
	if diag.StatusCode != 500 {
		t.Fatalf("StatusCode = %d", diag.StatusCode)
	}
	if diag.ProviderRequestID != "req-abc" {
		t.Fatalf("ProviderRequestID = %q", diag.ProviderRequestID)
	}
	if diag.ProviderMessage != "upstream unavailable" {
		t.Fatalf("ProviderMessage = %q", diag.ProviderMessage)
	}
	if diag.RequestID != "logic-1" {
		t.Fatalf("RequestID = %q", diag.RequestID)
	}
}

func TestSnapshotErrorPayloadKeepsProviderRequestID(t *testing.T) {
	payload := snapshotErrorPayload(&HTTPStatusError{
		StatusCode: 502,
		Message:    "bad gateway",
		Op:         "chat_completions",
		RequestID:  "req-502",
	})
	if payload["status_code"] != 502 {
		t.Fatalf("status_code = %v", payload["status_code"])
	}
	if payload["provider_request_id"] != "req-502" {
		t.Fatalf("provider_request_id = %v", payload["provider_request_id"])
	}
	if payload["provider_message"] != "bad gateway" {
		t.Fatalf("provider_message = %v", payload["provider_message"])
	}
	if payload["operation"] != "chat_completions" {
		t.Fatalf("operation = %v", payload["operation"])
	}
}

func TestRetryReasonUsesProviderStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&HTTPStatusError{StatusCode: 500}, "HTTP 500"},
		{&HTTPStatusError{StatusCode: 400}, "HTTP 400"},
		{&EmptyResponseError{}, "模型返回空回复"},
		{&ParseError{}, "响应解析失败"},
		{&NetworkError{}, "网络错误"},
	}
	for _, tc := range cases {
		if got := retryReason(tc.err); got != tc.want {
			t.Fatalf("retryReason(%T) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
