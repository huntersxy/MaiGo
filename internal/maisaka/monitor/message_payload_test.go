package monitor

import "testing"

func TestBuildMonitorReplyPreviewReadsWrappedText(t *testing.T) {
	preview := BuildMonitorReplyPreview(map[string]any{
		"reply_to":      "612184341",
		"reply_preview": map[string]any{"text": "这算法太不要脸了"},
		"reply_sender":  "玻璃",
	}, nil, nil)
	if preview == nil {
		t.Fatal("预览不应为空")
	}
	if got := preview["content"]; got != "这算法太不要脸了" {
		t.Fatalf("预览正文 = %#v, 不应包含结构化包装", got)
	}
}

func TestBuildMonitorReplyPreviewRejectsStructuredPayload(t *testing.T) {
	preview := BuildMonitorReplyPreview(map[string]any{
		"reply_to":      "612184341",
		"reply_preview": map[string]any{"url": "https://example.com/a.png"},
	}, nil, nil)
	if preview == nil {
		t.Fatal("预览不应为空")
	}
	if got := preview["content"]; got != "原消息已无法访问" {
		t.Fatalf("无文本载荷时应回退占位，实际 %#v", got)
	}
}
