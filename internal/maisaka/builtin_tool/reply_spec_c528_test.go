package tools

import "testing"

func TestReplyToolSpecHasSemanticEmojiReference(t *testing.T) {
	if _, err := NewReplyTool(); err != nil {
		t.Fatalf("reply 工具构造失败: %v", err)
	}
	spec, ok := MaisakaToolSpecFor("reply")
	if !ok {
		t.Fatal("缺少 reply 工具声明")
	}
	properties, _ := spec.Schema["properties"].(map[string]any)
	attach, _ := properties["attach_emoji"].(map[string]any)
	options, _ := attach["anyOf"].([]any)
	if len(options) != 3 {
		t.Fatalf("attach_emoji 应支持语义、字符串和上下文引用: %+v", attach)
	}
	for _, option := range options {
		entry, _ := option.(map[string]any)
		if entry["type"] == "integer" {
			t.Fatalf("旧版拼图序号不应再出现在 attach_emoji: %+v", entry)
		}
	}
	if _, ok := MaisakaToolSpecFor("send_emoji"); ok {
		t.Fatal("send_emoji 应已移除")
	}
	if _, ok := MaisakaToolSpecFor("show_emoji_list"); ok {
		t.Fatal("show_emoji_list 应已移除")
	}
}
