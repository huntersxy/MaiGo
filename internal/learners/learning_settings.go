package learners

import "strings"

// LearningBlacklistUser 对齐 global_config 的 learning_blacklist 项
type LearningBlacklistUser struct {
	Platform string
	UserID   string
}

// LearningGroupTarget 对齐 ChatStreamTarget 的会话组目标
type LearningGroupTarget struct {
	Platform string
	ItemID   string
	RuleType string
}

// LearningGroup 对齐 global_config 的 ChatStreamGroup
type LearningGroup struct {
	Targets []LearningGroupTarget
}

// LearningSettings 表达/黑话学习的全局配置快照
type LearningSettings struct {
	ExpressionCheckedOnly     bool
	ExpressionSelfReflect     bool
	MaxExpressionLearners     int
	UseVectorExpression       bool
	ExpressionVectorIndexPath string
	VectorCandidatePoolSize   int
	ExpressionBlacklist       []LearningBlacklistUser
	JargonBlacklist           []LearningBlacklistUser
	BotNickname               string
	BotAliasNames             []string
	ExpressionGroups          []LearningGroup
	JargonGroups              []LearningGroup
	ExpressionLearningRules   []ChatLearningRule
	JargonLearningRules       []ChatLearningRule
}

// DefaultLearningSettings 对齐 MaiBot 8.12.1 之后的默认值
func DefaultLearningSettings() LearningSettings {
	return LearningSettings{
		ExpressionSelfReflect: true,
		MaxExpressionLearners: 3,
		// 对齐 Python LearningItem 默认组：platform/item_id 留空表示全局规则
		ExpressionLearningRules: []ChatLearningRule{{RuleType: "group", Use: true, Learn: true}},
		JargonLearningRules:     []ChatLearningRule{{RuleType: "group", Use: true, Learn: true}},
	}
}

// NormalizeLearningSettings 补全零值字段，避免配置缺失时并发闸门直接关闭
func NormalizeLearningSettings(settings LearningSettings) LearningSettings {
	if settings.MaxExpressionLearners <= 0 {
		settings.MaxExpressionLearners = 3
	}
	if settings.VectorCandidatePoolSize <= 0 {
		settings.VectorCandidatePoolSize = 50
	}
	settings.BotNickname = strings.TrimSpace(settings.BotNickname)
	settings.ExpressionVectorIndexPath = strings.TrimSpace(settings.ExpressionVectorIndexPath)
	return settings
}

// IsLearningBlacklisted 判断平台+用户是否在学习黑名单中
func IsLearningBlacklisted(entries []LearningBlacklistUser, platform, userID string) bool {
	platform = strings.TrimSpace(platform)
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Platform) == platform && strings.TrimSpace(entry.UserID) == userID {
			return true
		}
	}
	return false
}
