package maibot

import (
	"fmt"
	"strconv"
	"strings"
)

// BotUpgradeFunc 在跨过 fromVer→toVer 时对 bot_config 内存快照做一次迁移
// 返回变更路径说明（对齐 Python migrate 返回的 reason 列表）
type BotUpgradeFunc func(*BotConfigFile) []string

// ModelUpgradeFunc 在跨过 fromVer→toVer 时对 model_config 内存快照做一次迁移
type ModelUpgradeFunc func(*ModelConfigFile) []string

type botUpgradeStep struct {
	from, to string
	fn       BotUpgradeFunc
}

type modelUpgradeStep struct {
	from, to string
	fn       ModelUpgradeFunc
}

// UpgradeRegistry 按版本区间注册顺序升级步骤
// 完整 Python config_upgrade_hooks.py 对等迁移见 registerDefaultUpgrades（bot 8.10.11–8.15.62）
type UpgradeRegistry struct {
	botSteps   []botUpgradeStep
	modelSteps []modelUpgradeStep
}

// NewUpgradeRegistry 创建空注册表
func NewUpgradeRegistry() *UpgradeRegistry {
	return &UpgradeRegistry{}
}

// Register 注册 bot_config.toml 从 fromVer 升到 toVer 时执行的钩子
func (r *UpgradeRegistry) Register(fromVer, toVer string, fn BotUpgradeFunc) {
	if fn == nil {
		fn = noopBotUpgrade
	}
	r.botSteps = append(r.botSteps, botUpgradeStep{from: fromVer, to: toVer, fn: fn})
}

// RegisterModel 注册 model_config.toml 从 fromVer 升到 toVer 时执行的钩子
func (r *UpgradeRegistry) RegisterModel(fromVer, toVer string, fn ModelUpgradeFunc) {
	if fn == nil {
		fn = noopModelUpgrade
	}
	r.modelSteps = append(r.modelSteps, modelUpgradeStep{from: fromVer, to: toVer, fn: fn})
}

// DefaultUpgradeRegistry 默认升级链（init 时由 registerDefaultUpgrades 填充）
var DefaultUpgradeRegistry = NewUpgradeRegistry()

func init() {
	registerDefaultUpgrades(DefaultUpgradeRegistry)
}

// ApplyUpgrades 若 inner.version 落后于目标版本，则沿注册表逐步升级内存配置
// 默认由 LoadFromInstanceDir 在版本变化后写回；MAIBOT_CONFIG_WRITEBACK=0/false 可关闭（见 writeback.go）
// legacy_migration / .env 绑定迁移在 ApplyUpgrades 前由 loadBotConfigFile 完成
func ApplyUpgrades(cfg *InstanceConfig) ([]string, error) {
	return DefaultUpgradeRegistry.Apply(cfg)
}

// Apply 对实例配置执行 bot/model 两侧升级
func (r *UpgradeRegistry) Apply(cfg *InstanceConfig) ([]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("ApplyUpgrades: 配置为空")
	}
	// 对齐 Python compare_versions 的 int(part)：版本段非数字时配置加载失败
	if _, err := parseVersionPartsStrict(cfg.BotInner.Version); err != nil {
		return nil, fmt.Errorf("bot_config version 非法: %w", err)
	}
	if _, err := parseVersionPartsStrict(cfg.ModelInner.Version); err != nil {
		return nil, fmt.Errorf("model_config version 非法: %w", err)
	}
	var reasons []string

	if compareVersion(cfg.BotInner.Version, BotConfigTargetVersion) < 0 {
		newVer, botReasons, err := r.applyBot(cfg.BotInner.Version, BotConfigTargetVersion, &cfg.Bot)
		if err != nil {
			return nil, err
		}
		cfg.BotInner.Version = newVer
		for _, reason := range botReasons {
			reasons = append(reasons, "bot:"+reason)
		}
	}

	if compareVersion(cfg.ModelInner.Version, ModelConfigTargetVersion) < 0 {
		newVer, modelReasons, err := r.applyModel(cfg.ModelInner.Version, ModelConfigTargetVersion, &cfg.Model)
		if err != nil {
			return nil, err
		}
		cfg.ModelInner.Version = newVer
		for _, reason := range modelReasons {
			reasons = append(reasons, "model:"+reason)
		}
	}

	return reasons, nil
}

func (r *UpgradeRegistry) applyBot(current, target string, bot *BotConfigFile) (string, []string, error) {
	var reasons []string
	for compareVersion(current, target) < 0 {
		step, ok := findNextBotStep(r.botSteps, current, target)
		if !ok {
			return current, reasons, fmt.Errorf("bot_config 升级链断档: 当前 %q，目标 %q", current, target)
		}
		if step.fn != nil {
			for _, path := range step.fn(bot) {
				reasons = append(reasons, step.to+":"+path)
			}
		}
		current = step.to
	}
	return current, reasons, nil
}

func (r *UpgradeRegistry) applyModel(current, target string, model *ModelConfigFile) (string, []string, error) {
	var reasons []string
	for compareVersion(current, target) < 0 {
		step, ok := findNextModelStep(r.modelSteps, current, target)
		if !ok {
			return current, reasons, fmt.Errorf("model_config 升级链断档: 当前 %q，目标 %q", current, target)
		}
		if step.fn != nil {
			for _, path := range step.fn(model) {
				reasons = append(reasons, step.to+":"+path)
			}
		}
		current = step.to
	}
	return current, reasons, nil
}

func findNextBotStep(steps []botUpgradeStep, current, target string) (*botUpgradeStep, bool) {
	var best *botUpgradeStep
	for i := range steps {
		s := &steps[i]
		if compareVersion(s.to, current) <= 0 {
			continue
		}
		if compareVersion(s.to, target) > 0 {
			continue
		}
		if compareVersion(s.from, current) > 0 {
			continue
		}
		if best == nil || compareVersion(s.to, best.to) < 0 {
			best = s
		}
	}
	return best, best != nil
}

func findNextModelStep(steps []modelUpgradeStep, current, target string) (*modelUpgradeStep, bool) {
	var best *modelUpgradeStep
	for i := range steps {
		s := &steps[i]
		if compareVersion(s.to, current) <= 0 {
			continue
		}
		if compareVersion(s.to, target) > 0 {
			continue
		}
		if compareVersion(s.from, current) > 0 {
			continue
		}
		if best == nil || compareVersion(s.to, best.to) < 0 {
			best = s
		}
	}
	return best, best != nil
}

func noopBotUpgrade(*BotConfigFile) []string     { return nil }
func noopModelUpgrade(*ModelConfigFile) []string { return nil }

// compareVersion 对齐 MaiBot config 语义：按点分整数段比较
func compareVersion(a, b string) int {
	pa := parseVersionParts(a)
	pb := parseVersionParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(pa) {
			ai = pa[i]
		}
		if i < len(pb) {
			bi = pb[i]
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

func parseVersionParts(version string) []int {
	parts, _ := parseVersionPartsStrict(version)
	if parts == nil {
		return nil
	}
	return parts
}

// parseVersionPartsStrict 严格解析点分版本，任一段非整数即报错
//
// 对齐 Python compare_versions 的 `[int(part) for part in ver.split(".")]`：
// 空串、非数字段、空白都会让配置加载失败，而不是静默按 0 继续升级链。
func parseVersionPartsStrict(version string) ([]int, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, nil
	}
	parts := strings.Split(version, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("版本段 %q 不是整数: %w", part, err)
		}
		out = append(out, n)
	}
	return out, nil
}
