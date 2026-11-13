package maibot

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// 对齐 MaiBot src/config/legacy_upgrade_confirmation.py
const (
	// LegacyUpgradeConfirmEnv 显式确认 0.x -> 1.x 升级的环境变量
	LegacyUpgradeConfirmEnv = "MAIBOT_LEGACY_0X_UPGRADE_CONFIRMED"
	// Legacy0XBotConfigBoundary 主配置文件 0.x 边界
	Legacy0XBotConfigBoundary = "8.9.4"
	// Legacy0XModelConfigBoundary 模型配置文件 0.x 边界
	Legacy0XModelConfigBoundary = "1.14.1"
)

// LegacyUpgradeReason 描述一条启动前需要确认的 0.x 升级风险
type LegacyUpgradeReason struct {
	Path           string
	CurrentVersion string
	TargetVersion  string
	Message        string
}

// ParseLegacySemver 严格解析三段式版本号，对齐 Python _parse_semver
func ParseLegacySemver(version string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		// 对齐 Python _parse_semver：只要求可转整数，不拒绝负数
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return out, false
		}
		out[i] = value
	}
	return out, true
}

func legacySemverLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// LegacyConfigNeedsConfirmation 对齐 Python _needs_legacy_config_confirmation
func LegacyConfigNeedsConfirmation(currentVersion, targetVersion, boundaryVersion string) bool {
	current, okCurrent := ParseLegacySemver(currentVersion)
	boundary, okBoundary := ParseLegacySemver(boundaryVersion)
	target, okTarget := ParseLegacySemver(targetVersion)
	if !okCurrent || !okBoundary || !okTarget {
		return false
	}
	return legacySemverLess(current, boundary) && legacySemverLess(current, target)
}

func readConfigInnerVersion(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Inner struct {
			Version string `toml:"version"`
		} `toml:"inner"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Inner.Version)
}

// CollectLegacyUpgradeReasons 收集启动前需要用户确认的 0.x 配置升级风险
// 旧版 data/MaiBot.db 迁移探测另挂 legacy_sqlite_checked；此处只收集配置升级确认项
func CollectLegacyUpgradeReasons(instanceRoot string) []LegacyUpgradeReason {
	root := filepath.Clean(instanceRoot)
	configDir := filepath.Join(root, "config")
	var reasons []LegacyUpgradeReason

	botPath := filepath.Join(configDir, botConfigFileName)
	botVersion := readConfigInnerVersion(botPath)
	if LegacyConfigNeedsConfirmation(botVersion, BotConfigTargetVersion, Legacy0XBotConfigBoundary) {
		reasons = append(reasons, LegacyUpgradeReason{
			Path:           botPath,
			CurrentVersion: botVersion,
			TargetVersion:  BotConfigTargetVersion,
			Message: "检测到主配置文件版本较旧，将更新配置文件：" + botPath +
				" (" + botVersion + " -> " + BotConfigTargetVersion + ")",
		})
	}

	modelPath := filepath.Join(configDir, modelConfigFileName)
	modelVersion := readConfigInnerVersion(modelPath)
	if LegacyConfigNeedsConfirmation(modelVersion, ModelConfigTargetVersion, Legacy0XModelConfigBoundary) {
		reasons = append(reasons, LegacyUpgradeReason{
			Path:           modelPath,
			CurrentVersion: modelVersion,
			TargetVersion:  ModelConfigTargetVersion,
			Message: "检测到模型配置文件版本较旧，将更新配置文件：" + modelPath +
				" (" + modelVersion + " -> " + ModelConfigTargetVersion + ")",
		})
	}
	return reasons
}

// RequireLegacyUpgradeConfirmation 在自动升级前要求显式确认
// Python 在交互终端提示输入 y；Go 服务化启动改为读取环境变量，未确认时返回可诊断错误
func RequireLegacyUpgradeConfirmation(instanceRoot string, getenv func(string) string) error {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv(LegacyUpgradeConfirmEnv)) == "1" {
		return nil
	}
	reasons := CollectLegacyUpgradeReasons(instanceRoot)
	if len(reasons) == 0 {
		return nil
	}
	lines := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		lines = append(lines, "- "+reason.Message)
	}
	return fmt.Errorf("检测到当前实例可能是从 0.x.x 版本升级而来，继续启动将自动更新配置；"+
		"建议先备份 config/ 与 data/。设置 %s=1 后重启以确认升级：\n%s",
		LegacyUpgradeConfirmEnv, strings.Join(lines, "\n"))
}
