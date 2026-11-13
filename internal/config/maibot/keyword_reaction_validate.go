package maibot

import (
	"fmt"
	"regexp"
)

// ValidateKeywordReaction 对齐 MaiBot KeywordRuleConfig.model_post_init
// Python 见 src/config/official_configs.py:4461-4517：keyword_rules 与 regex_rules 共用同一套校验
func ValidateKeywordReaction(bot BotConfigFile) error {
	rules := make([]KeywordRuleConfig, 0, len(bot.KeywordReaction.KeywordRules)+len(bot.KeywordReaction.RegexRules))
	rules = append(rules, bot.KeywordReaction.KeywordRules...)
	rules = append(rules, bot.KeywordReaction.RegexRules...)
	for _, rule := range rules {
		if len(rule.Keywords) == 0 && len(rule.Regex) == 0 {
			return fmt.Errorf("关键词规则必须至少包含keywords或regex中的一个")
		}
		if rule.Reaction == "" {
			return fmt.Errorf("关键词规则必须包含reaction")
		}
		for _, pattern := range rule.Regex {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("无效的正则表达式 '%s': %v", pattern, err)
			}
		}
	}
	return nil
}
