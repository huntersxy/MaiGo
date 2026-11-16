package maibot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// MaiBot 侧 bot_config / model_config 的 inner.version 目标值
// 默认在版本升级或旧版迁移后写回磁盘；MAIBOT_CONFIG_WRITEBACK=0/false 可显式关闭（见 writeback.go）
// 见 MaiBot src/config/config.py CONFIG_VERSION / MODEL_CONFIG_VERSION
const (
	BotConfigTargetVersion   = "8.16.15"
	ModelConfigTargetVersion = "1.17.25"
)

const (
	botConfigFileName   = "bot_config.toml"
	modelConfigFileName = "model_config.toml"
	configSubdir        = "config"
)

// InnerMeta 对应 TOML 顶层 [inner] 表
type InnerMeta struct {
	Version string `toml:"version"`
}

// ConfigUpgradeReport 记录一次加载中 bot/model 版本升级的范围与变更路径
type ConfigUpgradeReport struct {
	BotFrom   string
	BotTo     string
	ModelFrom string
	ModelTo   string
	Reasons   []string
}

// InstanceConfig 是 MaiBot 实例目录下的 TOML 配置快照
type InstanceConfig struct {
	Dir string

	BotConfigPath   string
	ModelConfigPath string

	BotInner   InnerMeta
	ModelInner InnerMeta

	Bot   BotConfigFile
	Model ModelConfigFile

	Upgrade         ConfigUpgradeReport
	LegacyMigration LegacyMigrationReport

	// BotUnknownKeys / ModelUnknownKeys 是原始 TOML 中未声明的根级键，仅用于告警
	BotUnknownKeys   []string
	ModelUnknownKeys []string

	// botRaw 是升级前 bot_config.toml 的原始键树，用于区分显式配置与缺失键
	botRaw map[string]any
}

// LoadOptions 控制实例配置加载时的迁移与写回行为
type LoadOptions struct {
	// DisableLegacyMigration 为 true 时跳过旧版 .env/TOML 结构迁移
	DisableLegacyMigration bool
	// Getenv 用于测试注入环境变量；nil 时使用 os.Getenv
	Getenv func(string) string
	// ForceWriteback 为 true 时即使 MAIBOT_CONFIG_WRITEBACK=0 也写回升级后的配置
	ForceWriteback bool
}

// LoadFromInstanceDir 从 MaiBot 实例根目录加载 config/bot_config.toml 与 config/model_config.toml
//
// 与 internal/config.Store（数据库动态配置）并存：
//   - TOML 覆盖 MaiBot 官方 WebUI/模板语义（bot、personality、chat、expression、a_memorix、webui 等）
//   - RuntimeConfig 仍承载 persona/agent/compactor 等；bot_config 重叠字段经 runtime_bridge.go 合并进内存快照（不回写 Store）
//
// 对齐 MaiBot config.py 的加载顺序：读取原始 TOML → 旧版 .env/结构迁移 → 版本升级钩子 → 校验 → 默认写回
func LoadFromInstanceDir(instanceRoot string) (*InstanceConfig, error) {
	return LoadFromInstanceDirWithOptions(instanceRoot, LoadOptions{})
}

// LoadFromInstanceDirWithOptions 按显式选项加载实例配置
func LoadFromInstanceDirWithOptions(instanceRoot string, opts LoadOptions) (*InstanceConfig, error) {
	out := newInstanceConfig(instanceRoot)
	if err := ensureInstanceConfigFiles(out.Dir); err != nil {
		return nil, err
	}
	if err := loadBotConfigFile(out.BotConfigPath, out.Dir, out, opts); err != nil {
		return nil, err
	}
	if err := loadModelConfigFile(out.ModelConfigPath, out); err != nil {
		return nil, err
	}
	return finalizeLoadedConfig(out, opts)
}

// LoadFromInstanceDirScoped 按热重载范围只解析变更的 TOML，未变更侧复用 base 快照
//
// 对齐 Python ConfigManager.reload_config(changed_scopes)：只有 bot 命中才读 bot_config.toml，
// 只有 model 命中才读 model_config.toml；版本升级、校验与写回仍对整份配置执行
func LoadFromInstanceDirScoped(instanceRoot string, base *InstanceConfig, scopes []ReloadScope, opts LoadOptions) (*InstanceConfig, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("配置热重载未命中有效范围")
	}
	out := newInstanceConfig(instanceRoot)
	if base != nil {
		copied := *base
		out = &copied
		out.Dir = filepath.Clean(instanceRoot)
		out.BotConfigPath = filepath.Join(out.Dir, configSubdir, botConfigFileName)
		out.ModelConfigPath = filepath.Join(out.Dir, configSubdir, modelConfigFileName)
	}
	if err := ensureInstanceConfigFiles(out.Dir); err != nil {
		return nil, err
	}
	// 未变更侧不重复执行旧版迁移，避免把 base 的迁移报告当成本次结果再次写回
	out.LegacyMigration = LegacyMigrationReport{}
	out.Upgrade = ConfigUpgradeReport{}

	loadBotScope := base == nil || hasReloadScope(scopes, ReloadScopeBot)
	if !loadBotScope {
		// 仅重载 model 时不能替 bot 侧写一次性迁移完成标记，否则后续 bot 变更会跳过迁移
		opts.DisableLegacyMigration = true
	}
	if loadBotScope {
		if err := loadBotConfigFile(out.BotConfigPath, out.Dir, out, opts); err != nil {
			return nil, err
		}
	}
	if base == nil || hasReloadScope(scopes, ReloadScopeModel) {
		if err := loadModelConfigFile(out.ModelConfigPath, out); err != nil {
			return nil, err
		}
	}
	return finalizeLoadedConfig(out, opts)
}

func newInstanceConfig(instanceRoot string) *InstanceConfig {
	root := filepath.Clean(instanceRoot)
	cfgDir := filepath.Join(root, configSubdir)
	return &InstanceConfig{
		Dir:             root,
		BotConfigPath:   filepath.Join(cfgDir, botConfigFileName),
		ModelConfigPath: filepath.Join(cfgDir, modelConfigFileName),
	}
}

func hasReloadScope(scopes []ReloadScope, scope ReloadScope) bool {
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

func finalizeLoadedConfig(out *InstanceConfig, opts LoadOptions) (*InstanceConfig, error) {
	root := out.Dir
	botVerBefore := out.BotInner.Version
	modelVerBefore := out.ModelInner.Version
	reasons, err := ApplyUpgrades(out)
	if err != nil {
		return nil, fmt.Errorf("实例配置版本升级: %w", err)
	}
	if err := applyBotConfigDefaults(&out.Bot, out.botRaw); err != nil {
		return nil, err
	}
	out.Upgrade = ConfigUpgradeReport{
		BotFrom:   botVerBefore,
		BotTo:     out.BotInner.Version,
		ModelFrom: modelVerBefore,
		ModelTo:   out.ModelInner.Version,
		Reasons:   reasons,
	}
	if err := ValidateBotConfig(out.Bot); err != nil {
		return nil, fmt.Errorf("bot_config 校验: %w", err)
	}
	if err := ValidateFilesConfig(out.Bot); err != nil {
		return nil, fmt.Errorf("bot_config files 校验: %w", err)
	}
	if err := ValidateKeywordReaction(out.Bot); err != nil {
		return nil, fmt.Errorf("bot_config 关键词反应校验: %w", err)
	}
	if err := ValidateModelConfig(out.Model, nil); err != nil {
		return nil, fmt.Errorf("model_config 校验: %w", err)
	}
	if err := ValidateReplyPostProcess(out.Bot); err != nil {
		return nil, fmt.Errorf("bot_config 回复后处理校验: %w", err)
	}
	if len(out.BotUnknownKeys) > 0 {
		zap.L().Warn("bot_config.toml 包含未知根级配置项，已忽略",
			zap.String("instance_dir", root), zap.Strings("keys", out.BotUnknownKeys))
	}
	if len(out.ModelUnknownKeys) > 0 {
		zap.L().Warn("model_config.toml 包含未知根级配置项，已忽略",
			zap.String("instance_dir", root), zap.Strings("keys", out.ModelUnknownKeys))
	}
	if out.LegacyMigration.Applied {
		zap.L().Warn("检测到旧版配置结构变更，已迁移",
			zap.String("instance_dir", root),
			zap.Strings("reasons", out.LegacyMigration.Reasons))
	}
	if out.LegacyMigration.EnvApplied {
		zap.L().Warn("检测到旧版环境变量绑定配置，已迁移到主配置",
			zap.String("instance_dir", root),
			zap.Strings("reasons", out.LegacyMigration.Reasons))
	}
	if out.Upgrade.BotFrom != out.Upgrade.BotTo || out.Upgrade.ModelFrom != out.Upgrade.ModelTo || len(reasons) > 0 {
		zap.L().Info("MaiBot 实例配置已升级",
			zap.String("instance_dir", root),
			zap.String("bot_from", out.Upgrade.BotFrom),
			zap.String("bot_to", out.Upgrade.BotTo),
			zap.String("model_from", out.Upgrade.ModelFrom),
			zap.String("model_to", out.Upgrade.ModelTo),
			zap.Strings("reasons", reasons))
	}
	forceBotWrite := out.LegacyMigration.Applied
	if err := maybeWriteBackUpgradedConfigs(out, botVerBefore, modelVerBefore, forceBotWrite, opts.ForceWriteback); err != nil {
		return nil, err
	}
	if !opts.DisableLegacyMigration && !LegacyMigrationCompleted(root) {
		if err := MarkLegacyMigrationCompleted(root, out.LegacyMigration.Applied, joinLegacyReasons(out.LegacyMigration)); err != nil {
			return nil, fmt.Errorf("记录旧版配置迁移状态: %w", err)
		}
	}
	if out.LegacyMigration.EnvApplied {
		removeLegacyEnvFile(root)
	}
	return out, nil
}

func joinLegacyReasons(report LegacyMigrationReport) string {
	return strings.Join(report.Reasons, ",")
}

func loadBotConfigFile(path, root string, out *InstanceConfig, opts LoadOptions) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 bot_config.toml: %w", err)
	}
	var meta struct {
		Inner InnerMeta `toml:"inner"`
	}
	if err := toml.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("解析 bot_config.toml: %w", err)
	}
	if meta.Inner.Version == "" {
		return fmt.Errorf("bot_config.toml 缺少 inner.version")
	}

	finalRaw := raw
	var report LegacyMigrationReport
	if !opts.DisableLegacyMigration && !LegacyMigrationCompleted(root) {
		migratedRaw, migrationReport, err := migrateBotConfigRaw(raw, opts.Getenv)
		if err != nil {
			return err
		}
		report = migrationReport
		if report.Applied {
			finalRaw = migratedRaw
		}
	}

	decodedRaw := normalizeAMemorixImportRaw(finalRaw)
	if err := ValidateRawBotConstraints(decodedRaw); err != nil {
		return fmt.Errorf("bot_config.toml 校验失败: %w", err)
	}
	var doc botConfigDocument
	decoder := toml.NewDecoder(strings.NewReader(string(decodedRaw)))
	if err := decoder.Decode(&doc); err != nil {
		return fmt.Errorf("解析 bot_config.toml: %w", err)
	}
	applyLearningItemDefaults(&doc, decodedRaw)
	out.BotInner = doc.Inner
	out.Bot = doc.BotConfigFile
	var legacyTypo struct {
		ChineseTypo *ChineseTypoConfig `toml:"chinese_typo"`
	}
	if err := toml.Unmarshal(decodedRaw, &legacyTypo); err == nil {
		out.Bot.LegacyChineseTypo = legacyTypo.ChineseTypo
	}
	out.LegacyMigration = report
	out.BotUnknownKeys = filterLegacyUnknownKeys(unknownTopLevelKeys(finalRaw, BotConfigFile{}), "chinese_typo")
	out.botRaw = botConfigKeyTree(decodedRaw)
	out.Bot.LoadMeta = parseBotConfigLoadMeta(finalRaw)
	captureJargonGroupsLegacyRaw(finalRaw, &out.Bot)
	return nil
}

// filterLegacyUnknownKeys 排除已删除但仍用于迁移的旧根级表
func filterLegacyUnknownKeys(keys []string, legacy ...string) []string {
	if len(keys) == 0 {
		return keys
	}
	skip := make(map[string]struct{}, len(legacy))
	for _, key := range legacy {
		skip[key] = struct{}{}
	}
	filtered := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := skip[key]; ok {
			continue
		}
		filtered = append(filtered, key)
	}
	return filtered
}

// normalizeAMemorixImportRaw 把 TOML 的 a_memorix.web.import 映射到 Python 属性 import_config
func normalizeAMemorixImportRaw(raw []byte) []byte {
	var data map[string]any
	if err := toml.Unmarshal(raw, &data); err != nil {
		return raw
	}
	amemorix, _ := data["a_memorix"].(map[string]any)
	web, _ := amemorix["web"].(map[string]any)
	if web == nil {
		return raw
	}
	if _, exists := web["import_config"]; exists {
		return raw
	}
	legacy, exists := web["import"]
	if !exists {
		return raw
	}
	web["import_config"] = legacy
	delete(web, "import")
	encoded, err := toml.Marshal(data)
	if err != nil {
		return raw
	}
	return encoded
}

// normalizeAMemorixImportForWrite 写回时把 import_config 还原为 TOML 的 import 键
func normalizeAMemorixImportForWrite(data []byte) []byte {
	var parsed map[string]any
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return data
	}
	amemorix, _ := parsed["a_memorix"].(map[string]any)
	web, _ := amemorix["web"].(map[string]any)
	if web == nil {
		return data
	}
	if _, exists := web["import"]; exists {
		return data
	}
	current, exists := web["import_config"]
	if !exists {
		return data
	}
	web["import"] = current
	delete(web, "import_config")
	encoded, err := toml.Marshal(parsed)
	if err != nil {
		return data
	}
	return encoded
}

func migrateBotConfigRaw(raw []byte, getenv func(string) string) ([]byte, LegacyMigrationReport, error) {
	var data map[string]any
	if err := toml.Unmarshal(raw, &data); err != nil {
		return nil, LegacyMigrationReport{}, fmt.Errorf("解析 bot_config.toml 原始结构: %w", err)
	}
	inner, _ := data["inner"]
	delete(data, "inner")

	envResult := MigrateLegacyBindEnvToBotConfig(data, getenv)
	legacyResult := TryMigrateLegacyBotConfig(data)
	report := LegacyMigrationReport{
		Applied:    envResult.Migrated || legacyResult.Migrated,
		EnvApplied: envResult.Migrated,
	}
	for _, reason := range []string{envResult.Reason, legacyResult.Reason} {
		if reason == "" {
			continue
		}
		report.Reasons = append(report.Reasons, splitLegacyReasons(reason)...)
	}
	if !report.Applied {
		return raw, report, nil
	}
	data["inner"] = inner
	migrated, err := toml.Marshal(data)
	if err != nil {
		return nil, report, fmt.Errorf("编码迁移后的 bot_config.toml: %w", err)
	}
	return migrated, report, nil
}

func splitLegacyReasons(reason string) []string {
	if reason == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(reason); i++ {
		if i == len(reason) || reason[i] == ',' {
			if i > start {
				out = append(out, reason[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func removeLegacyEnvFile(root string) {
	path := filepath.Join(root, ".env")
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			zap.L().Warn("旧版 .env 配置文件删除失败，请手动删除", zap.String("path", path), zap.Error(err))
		}
		return
	}
	zap.L().Warn("检测到旧版环境变量绑定配置迁移成功，已删除旧版 .env 文件", zap.String("path", path))
}

func loadModelConfigFile(path string, out *InstanceConfig) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 model_config.toml: %w", err)
	}
	var doc modelConfigDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("解析 model_config.toml: %w", err)
	}
	if doc.Inner.Version == "" {
		return fmt.Errorf("model_config.toml 缺少 inner.version")
	}
	if err := ValidateRawModelConstraints(raw); err != nil {
		return fmt.Errorf("model_config.toml 校验失败: %w", err)
	}
	out.ModelInner = doc.Inner
	out.Model = doc.ModelConfigFile
	out.ModelUnknownKeys = unknownTopLevelKeys(raw, ModelConfigFile{})
	out.Model.LegacyModelTypeKeySet = legacyModelTypeKeySet(raw)
	applyAPIProviderPresence(&out.Model, raw)
	applyModelPresence(&out.Model, raw)
	return nil
}

// applyAPIProviderPresence 标记 api_providers 中显式写出的 max_retry
// Python Pydantic 的 max_retry=0 与省略（默认 3）语义不同，Go 需要保留这个区别
func applyAPIProviderPresence(file *ModelConfigFile, raw []byte) {
	if file == nil || len(file.ApiProviders) == 0 {
		return
	}
	var doc struct {
		Providers []map[string]any `toml:"api_providers"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return
	}
	for i := range file.ApiProviders {
		if i >= len(doc.Providers) || doc.Providers[i] == nil {
			continue
		}
		_, file.ApiProviders[i].MaxRetrySet = doc.Providers[i]["max_retry"]
		_, file.ApiProviders[i].AuthHeaderPrefixSet = doc.Providers[i]["auth_header_prefix"]
	}
}

// applyModelPresence 标记任务温度/最大 token 与模型 send_temperature 是否显式出现
// Python 的 TaskConfig 默认 0.7/4096、ModelInfo.send_temperature 默认 true，Go 需要用出现标记区分显式 0 与省略
func applyModelPresence(file *ModelConfigFile, raw []byte) {
	if file == nil {
		return
	}
	var doc struct {
		Models          []map[string]any          `toml:"models"`
		ModelTaskConfig map[string]map[string]any `toml:"model_task_config"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return
	}
	for i := range file.Models {
		if i >= len(doc.Models) || doc.Models[i] == nil {
			continue
		}
		_, file.Models[i].SendTemperatureSet = doc.Models[i]["send_temperature"]
	}
	tasks := []struct {
		key string
		cfg *TaskConfig
	}{
		{"replyer", &file.ModelTaskConfig.Replyer},
		{"planner", &file.ModelTaskConfig.Planner},
		{"memory", &file.ModelTaskConfig.Memory},
		{"utils", &file.ModelTaskConfig.Utils},
		{"learner", &file.ModelTaskConfig.Learner},
		{"fast_model", &file.ModelTaskConfig.FastModel},
		{"emoji", &file.ModelTaskConfig.Emoji},
		{"vlm", &file.ModelTaskConfig.Vlm},
		{"voice", &file.ModelTaskConfig.Voice},
		{"embedding", &file.ModelTaskConfig.Embedding},
		{"image_embedding", &file.ModelTaskConfig.ImageEmbedding},
	}
	for _, task := range tasks {
		table := doc.ModelTaskConfig[task.key]
		if table == nil {
			continue
		}
		_, task.cfg.TemperatureSet = table["temperature"]
		_, task.cfg.MaxTokensSet = table["max_tokens"]
	}
}

// legacyModelTypeKeySet 对齐 Python「model_type in model」：仅键存在即视为显式配置
func legacyModelTypeKeySet(raw []byte) []bool {
	var doc struct {
		Models []map[string]any `toml:"models"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	if len(doc.Models) == 0 {
		return nil
	}
	out := make([]bool, len(doc.Models))
	for i, row := range doc.Models {
		if row == nil {
			continue
		}
		_, out[i] = row["model_type"]
	}
	return out
}

type botConfigDocument struct {
	Inner InnerMeta `toml:"inner"`
	BotConfigFile
}

type modelConfigDocument struct {
	Inner InnerMeta `toml:"inner"`
	ModelConfigFile
}
