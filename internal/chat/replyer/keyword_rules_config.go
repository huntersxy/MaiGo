package replyer

import (
	maibotcfg "maibot-go/internal/config/maibot"
)

// KeywordRulesFromConfig 把实例配置 keyword_reaction.keyword_rules 投影为 replyer 规则
// Python 见 src/chat/replyer/maisaka_generator_base.py:506-511
func KeywordRulesFromConfig(rules []maibotcfg.KeywordRuleConfig) []KeywordRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]KeywordRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, KeywordRule{
			Keywords: append([]string(nil), rule.Keywords...),
			Reaction: rule.Reaction,
		})
	}
	return out
}

// RegexRulesFromConfig 把实例配置 keyword_reaction.regex_rules 投影为 replyer 规则
// Python 见 src/chat/replyer/maisaka_generator_base.py:513-527
func RegexRulesFromConfig(rules []maibotcfg.KeywordRuleConfig) []RegexRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]RegexRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, RegexRule{
			Patterns: append([]string(nil), rule.Regex...),
			Reaction: rule.Reaction,
		})
	}
	return out
}
