package maibot

import "testing"

func TestUpgradeGroupTalkValueRules(t *testing.T) {
	disabled := false
	bot := BotConfigFile{}
	bot.Chat.ReplyTiming.TalkValueRules = []TalkRulesItem{
		{Platform: "qq", ItemId: "1", RuleType: "group", Time: "20:00-22:00", Value: 0.8},
		{Platform: "qq", ItemId: "2", RuleType: "group", Time: "*", Value: 0.3, Enabled: &disabled},
	}
	reasons := upgradeGroupTalkValueRules(&bot)
	if len(reasons) != 1 {
		t.Fatalf("迁移原因 = %v", reasons)
	}
	groups := bot.Chat.ReplyTiming.TalkRuleGroups
	if groups.ActiveGroup != DefaultTalkRuleGroupName || len(groups.Groups) != 1 {
		t.Fatalf("规则组未生成: %+v", groups)
	}
	if len(groups.Groups[0].Rules) != 2 || len(bot.Chat.ReplyTiming.TalkValueRules) != 0 {
		t.Fatalf("旧规则未迁入: %+v", bot.Chat.ReplyTiming)
	}
	if groups.Groups[0].Rules[1].RuleEnabled() {
		t.Fatal("停用状态未保留")
	}
}

func TestGroupTalkFrequencyUsesActiveGroup(t *testing.T) {
	enabled := true
	disabled := false
	rt := ChatReplyTimingConfig{TalkValue: 0.5}
	rt.TalkRuleGroups = TalkRuleGroups{
		ActiveGroup: "夜间",
		Groups: []TalkRuleGroup{
			{Name: "默认", Rules: []TalkRulesItem{{Platform: "qq", ItemId: "1", RuleType: "group", Time: "*", Value: 0.9}}},
			{Name: "夜间", Rules: []TalkRulesItem{
				{Platform: "qq", ItemId: "1", RuleType: "group", Time: "*", Value: 0.2, Enabled: &enabled},
				{Platform: "qq", ItemId: "1", RuleType: "group", Time: "*", Value: 0.01, Enabled: &disabled},
			}},
		},
	}
	if got := GroupTalkFrequency(rt, "qq", 1); got != 0.2 {
		t.Fatalf("频率 = %v, 期望 0.2", got)
	}
}

func TestValidateModelConfigAllowsIncompleteConfig(t *testing.T) {
	if err := ValidateModelConfig(ModelConfigFile{}, nil); err != nil {
		t.Fatalf("空配置应允许启动: %v", err)
	}
	file := ModelConfigFile{}
	file.ApiProviders = []APIProvider{{Name: "p"}}
	file.Models = []ModelInfo{{Name: "m", ModelIdentifier: "id", ApiProvider: "missing"}}
	if err := ValidateModelConfig(file, nil); err != nil {
		t.Fatalf("不完整引用应允许启动: %v", err)
	}
	if len(ModelConfigWarnings(file)) == 0 {
		t.Fatal("应产生配置告警")
	}
}
