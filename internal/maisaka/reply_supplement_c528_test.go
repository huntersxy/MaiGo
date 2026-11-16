package maisaka

import (
	"testing"
)

func TestParseEmojiSupplementNodes(t *testing.T) {
	plan, err := ParseReplySupplementPlan(`{"parts":[{"type":"reply"},{"type":"emoji","emotion":"开心"}]}`)
	if err != nil {
		t.Fatalf("语义表情节点解析失败: %v", err)
	}
	if len(plan.Parts) != 2 || !plan.Parts[1].HasEmotion || plan.Parts[1].Emotion != "开心" {
		t.Fatalf("语义表情节点内容不符合预期: %+v", plan.Parts[1])
	}
	plan, err = ParseReplySupplementPlan(`{"parts":[{"type":"reply"},{"type":"emoji","source":"m1","index":1}]}`)
	if err != nil {
		t.Fatalf("上下文表情节点解析失败: %v", err)
	}
	if !plan.Parts[1].HasIndex || plan.Parts[1].Index != 1 || plan.Parts[1].Source != "m1" {
		t.Fatalf("上下文表情节点内容不符合预期: %+v", plan.Parts[1])
	}
	if _, err := ParseReplySupplementPlan(`{"parts":[{"type":"reply"},{"type":"emoji","source":"m1","emotion":"开心"}]}`); err == nil {
		t.Fatal("上下文引用与语义描述混用应当报错")
	}
	if _, err := ParseReplySupplementPlan(`{"parts":[{"type":"reply"},{"type":"emoji","description":"开心","content":"猫"}]}`); err == nil {
		t.Fatal("description 与 content 混用应当报错")
	}
}

func TestResolveSupplementEmojiReferences(t *testing.T) {
	target := ContextMessage{MessageID: "target", Role: RoleUser, Source: SourceUser}
	emojiMessage := ContextMessage{
		MessageID: "emoji-message", Role: RoleUser, Source: SourceReference,
		Emojis: []ImageComponent{{MediaRef: "hash", Description: "猫猫"}},
	}
	resources := SupplementResources{
		Messages:      map[string]ContextMessage{"target": target},
		Pictures:      map[string][]ImageComponent{},
		ContextEmojis: map[string][]ImageComponent{"emoji-message": emojiMessage.Emojis},
		AllowQuote:    true,
		SessionID:     "session",
	}
	plan, err := ParseReplySupplementPlan(`{"parts":[{"type":"reply"},{"type":"emoji","emotion":"开心","content":"猫"},{"type":"emoji","source":"emoji-message","index":0}]}`)
	if err != nil {
		t.Fatalf("解析补充编排失败: %v", err)
	}
	supplement, err := ResolveSupplement(plan, resources, map[string]any{})
	if err != nil {
		t.Fatalf("解析补充资源失败: %v", err)
	}
	if len(supplement.Components) != 3 {
		t.Fatalf("组件数量不符合预期: %+v", supplement.Components)
	}
	semantic := supplement.Components[1]
	if semantic.Kind != "emoji" || semantic.EmojiReference["emotion"] != "开心" || semantic.EmojiReference["content"] != "猫" {
		t.Fatalf("语义表情引用不符合预期: %+v", semantic)
	}
	contextRef := supplement.Components[2]
	if contextRef.Kind != "emoji" || contextRef.EmojiReference["msg_id"] != "emoji-message" || contextRef.EmojiReference["index"] != 0 {
		t.Fatalf("上下文表情引用不符合预期: %+v", contextRef)
	}

	missing := plan
	missing.Parts = []SupplementPart{{Type: SupplementNodeReply}, {Type: SupplementNodeEmoji, Source: "missing"}}
	supplement, err = ResolveSupplement(missing, resources, map[string]any{})
	if err != nil {
		t.Fatalf("缺失上下文表情不应阻断编排: %v", err)
	}
	if supplement.Components[1].EmojiError == "" {
		t.Fatalf("缺失上下文表情应记录错误: %+v", supplement.Components[1])
	}
}

func TestReplySupplementInventoryContextEmojis(t *testing.T) {
	resources := SupplementResources{
		ContextEmojis: map[string][]ImageComponent{
			"m1": {{MediaRef: "a"}, {MediaRef: "b"}},
		},
		SessionID: "session",
	}
	inventory := resources.Inventory()
	raw, ok := inventory["context_emojis"].([]map[string]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("资源清单缺少 context_emojis: %+v", inventory)
	}
	indices, _ := raw[0]["indices"].([]int)
	if len(indices) != 2 {
		t.Fatalf("表情索引不符合预期: %+v", raw[0])
	}
}
