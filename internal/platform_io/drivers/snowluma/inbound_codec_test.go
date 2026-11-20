package snowluma

import "testing"

func TestParseCQMessageEscapesOnlyOnce(t *testing.T) {
	segments, err := parseCQMessage(" &amp;#91;CQ:image&#93;[CQ:image,file=a&#44;b&amp;c] ")
	if err != nil || len(segments) != 3 {
		t.Fatalf("parseCQMessage returned %d segments, error %v", len(segments), err)
	}
	if asMap(segments[0])["data"] != " &#91;CQ:image]" || asMap(segments[2])["data"] != " " {
		t.Fatal("text escaped incorrectly")
	}
	image := asMap(segments[1])
	if image["type"] != "image" || asMap(image["data"])["file"] != "a,b&c" {
		t.Fatalf("image segment escaped incorrectly: %#v", image)
	}
}

func TestNormalizeInboundMessageTextDataIsString(t *testing.T) {
	payload := map[string]any{
		"post_type":    "message",
		"message_type": "group",
		"message_id":   "6943",
		"user_id":      "10001",
		"group_id":     "20002",
		"self_id":      "2971840857",
		"time":         float64(1700000000),
		"sender": map[string]any{
			"user_id":  "10001",
			"nickname": "测试用户",
		},
		"message": []any{
			map[string]any{
				"type": "text",
				"data": map[string]any{"text": "/今日老婆"},
			},
		},
	}
	out := NormalizeInboundMessage(payload, "2971840857", "")
	if out == nil {
		t.Fatal("NormalizeInboundMessage 返回 nil")
	}
	if got := asString(out["processed_plain_text"]); got != "/今日老婆" {
		t.Fatalf("processed_plain_text = %q, 期望 /今日老婆", got)
	}
	if got := out["is_command"]; got != true {
		t.Fatalf("is_command = %#v, 期望 true", got)
	}
	raw, ok := out["raw_message"].([]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("raw_message = %#v", out["raw_message"])
	}
	seg, ok := raw[0].(map[string]any)
	if !ok {
		t.Fatalf("raw_message[0] = %#v", raw[0])
	}
	if _, ok := seg["data"].(string); !ok {
		t.Fatalf("text.data 应为字符串，实际 %#v", seg["data"])
	}
	if seg["data"] != "/今日老婆" {
		t.Fatalf("text.data = %#v", seg["data"])
	}
}

func TestNormalizeInboundMessagePlainTextWithoutSlash(t *testing.T) {
	payload := map[string]any{
		"message_type": "private",
		"message_id":   "1",
		"user_id":      "10001",
		"message": []any{
			map[string]any{
				"type": "text",
				"data": map[string]any{"text": "今日老婆"},
			},
		},
	}
	out := NormalizeInboundMessage(payload, "2971840857", "")
	if asString(out["processed_plain_text"]) != "今日老婆" {
		t.Fatalf("processed_plain_text = %#v", out["processed_plain_text"])
	}
	if out["is_command"] != false {
		t.Fatalf("无斜杠正文不应标记 is_command: %#v", out["is_command"])
	}
}
