package learners

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	maibotcfg "maibot-go/internal/config/maibot"
)

// LoadLearningSettingsFromConfig 将实例 TOML 映射为学习配置快照
func LoadLearningSettingsFromConfig(cfg *maibotcfg.InstanceConfig) LearningSettings {
	settings := DefaultLearningSettings()
	if cfg == nil {
		return NormalizeLearningSettings(settings)
	}
	bot := cfg.Bot
	settings.ExpressionCheckedOnly = bot.Expression.ExpressionCheckedOnly
	settings.ExpressionSelfReflect = bot.Expression.ExpressionSelfReflect
	settings.MaxExpressionLearners = bot.Expression.MaxExpressionLearner
	settings.UseVectorExpression = bot.Expression.UseVectorExpression
	settings.ExpressionVectorIndexPath = bot.Expression.ExpressionVectorIndexPath
	settings.VectorCandidatePoolSize = bot.Expression.ExpressionVectorCandidatePoolSize
	settings.BotNickname = bot.Bot.Nickname
	settings.BotAliasNames = append([]string(nil), bot.Bot.AliasNames...)
	settings.ExpressionBlacklist = convertLearningBlacklist(bot.Expression.LearningBlacklist)
	settings.JargonBlacklist = convertLearningBlacklist(bot.Jargon.LearningBlacklist)
	settings.ExpressionGroups = convertLearningGroups(bot.Expression.ExpressionGroups, true)
	settings.JargonGroups = convertLearningGroups(bot.Jargon.JargonGroups, false)
	settings.ExpressionLearningRules = convertLearningRules(bot.Expression.LearningList)
	settings.JargonLearningRules = convertLearningRules(bot.Jargon.LearningList)
	return NormalizeLearningSettings(settings)
}

type learningSettingsCacheEntry struct {
	root         string
	modTime      time.Time
	size         int64
	modelModTime time.Time
	modelSize    int64
	settings     LearningSettings
	err          error
	cached       bool
}

var learningSettingsCache struct {
	mu    sync.Mutex
	entry learningSettingsCacheEntry
}

// LoadLearningSettingsWithError 与 Python global_config 加载语义对齐：
// 未设置实例目录时使用官方默认值；实例目录存在但读取/解析失败时返回错误，不能静默伪装成默认开启
func LoadLearningSettingsWithError() (LearningSettings, error) {
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		return NormalizeLearningSettings(DefaultLearningSettings()), nil
	}
	configPath := filepath.Join(root, "config", "bot_config.toml")
	modelPath := filepath.Join(root, "config", "model_config.toml")
	info, statErr := os.Stat(configPath)
	modelInfo, modelStatErr := os.Stat(modelPath)
	if statErr == nil && modelStatErr == nil {
		learningSettingsCache.mu.Lock()
		entry := learningSettingsCache.entry
		if entry.cached && entry.root == root &&
			entry.modTime.Equal(info.ModTime()) && entry.size == info.Size() &&
			entry.modelModTime.Equal(modelInfo.ModTime()) && entry.modelSize == modelInfo.Size() {
			learningSettingsCache.mu.Unlock()
			return entry.settings, entry.err
		}
		learningSettingsCache.mu.Unlock()
	}

	cfg, loadErr := maibotcfg.LoadFromInstanceDir(root)
	if loadErr != nil {
		wrapped := fmt.Errorf("加载实例学习配置 %s: %w", configPath, loadErr)
		after, afterErr := os.Stat(configPath)
		afterModel, afterModelErr := os.Stat(modelPath)
		cacheLearningSettings(root, after, afterErr, afterModel, afterModelErr, LearningSettings{}, wrapped)
		return LearningSettings{}, wrapped
	}
	settings := LoadLearningSettingsFromConfig(cfg)
	after, afterErr := os.Stat(configPath)
	afterModel, afterModelErr := os.Stat(modelPath)
	cacheLearningSettings(root, after, afterErr, afterModel, afterModelErr, settings, nil)
	return settings, nil
}

func cacheLearningSettings(root string, info os.FileInfo, statErr error, modelInfo os.FileInfo, modelStatErr error, settings LearningSettings, err error) {
	if statErr != nil || info == nil || modelStatErr != nil || modelInfo == nil {
		return
	}
	learningSettingsCache.mu.Lock()
	learningSettingsCache.entry = learningSettingsCacheEntry{
		root:         root,
		modTime:      info.ModTime(),
		size:         info.Size(),
		modelModTime: modelInfo.ModTime(),
		modelSize:    modelInfo.Size(),
		settings:     settings,
		err:          err,
		cached:       true,
	}
	learningSettingsCache.mu.Unlock()
}

// ResetLearningSettingsCache 清空进程内学习配置缓存，供测试或显式重载使用
func ResetLearningSettingsCache() {
	learningSettingsCache.mu.Lock()
	learningSettingsCache.entry = learningSettingsCacheEntry{}
	learningSettingsCache.mu.Unlock()
}

func convertLearningRules(items []maibotcfg.LearningItem) []ChatLearningRule {
	out := make([]ChatLearningRule, 0, len(items))
	for _, item := range items {
		platform := strings.TrimSpace(item.Platform)
		itemID := strings.TrimSpace(item.ItemId)
		if platform == "*" {
			platform = ""
		}
		if itemID == "*" {
			itemID = ""
		}
		ruleType := strings.ToLower(strings.TrimSpace(item.Type))
		if ruleType == "" {
			ruleType = strings.ToLower(strings.TrimSpace(item.RuleType))
		}
		out = append(out, ChatLearningRule{
			Platform: platform,
			ItemID:   itemID,
			RuleType: ruleType,
			Use:      item.Use,
			Learn:    item.Learn,
		})
	}
	return out
}

func convertLearningBlacklist(entries []maibotcfg.LearningBlacklistUser) []LearningBlacklistUser {
	out := make([]LearningBlacklistUser, 0, len(entries))
	for _, entry := range entries {
		platform := strings.TrimSpace(entry.Platform)
		userID := strings.TrimSpace(entry.UserId)
		if platform == "" || userID == "" {
			continue
		}
		out = append(out, LearningBlacklistUser{Platform: platform, UserID: userID})
	}
	return out
}

// convertLearningGroups 转换 expression_groups/jargon_groups，并兼容 8.10.20 前的组内键名
func convertLearningGroups(groups []maibotcfg.ChatStreamGroup, expression bool) []LearningGroup {
	out := make([]LearningGroup, 0, len(groups))
	for _, group := range groups {
		targets := group.Targets
		if len(targets) == 0 {
			if expression {
				targets = group.LegacyInnerExpressionGroups
			} else {
				targets = group.LegacyInnerJargonGroups
			}
		}
		converted := make([]LearningGroupTarget, 0, len(targets))
		for _, target := range targets {
			platform := strings.TrimSpace(target.Platform)
			itemID := strings.TrimSpace(target.ItemId)
			if platform == "" || itemID == "" {
				continue
			}
			converted = append(converted, LearningGroupTarget{
				Platform: platform,
				ItemID:   itemID,
				RuleType: strings.TrimSpace(target.RuleType),
			})
		}
		if len(converted) > 0 {
			out = append(out, LearningGroup{Targets: converted})
		}
	}
	return out
}
