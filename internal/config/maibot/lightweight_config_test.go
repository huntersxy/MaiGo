package maibot

import "testing"

func TestValidateLightweightPrivateChats(t *testing.T) {
	if err := validateLightweightPrivateChats([]TargetItem{{Platform: "qq", ItemId: "1", RuleType: "private"}}); err != nil {
		t.Fatalf("合法轻量私聊配置不应失败: %v", err)
	}
	if err := validateLightweightPrivateChats([]TargetItem{{Platform: "qq", ItemId: "1", RuleType: "channel"}}); err == nil {
		t.Fatal("非法 rule_type 应被拒绝")
	}
	if err := validateLightweightPrivateChats([]TargetItem{{Platform: "qq", ItemId: "1"}}); err != nil {
		t.Fatalf("缺省 rule_type 应按 group 处理: %v", err)
	}
}
