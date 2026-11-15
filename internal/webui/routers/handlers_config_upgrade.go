package webuiapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	memory "MaiGo/internal/common/database"
	maibotcfg "MaiGo/internal/config/maibot"

	"github.com/pelletier/go-toml/v2"
)

const (
	// legacy0XBotConfigBoundary 对齐 Python LEGACY_0X_BOT_CONFIG_BOUNDARY
	legacy0XBotConfigBoundary = "8.9.4"
	// legacy0XModelConfigBoundary 对齐 Python LEGACY_0X_MODEL_CONFIG_BOUNDARY
	legacy0XModelConfigBoundary = "1.14.1"
)

type configUpgradeFileStatus struct {
	Path              string `json:"path"`
	Missing           bool   `json:"missing"`
	CurrentVersion    string `json:"current_version"`
	TargetVersion     string `json:"target_version"`
	LegacyBoundary    string `json:"legacy_boundary"`
	NeedsConfirmation bool   `json:"needs_confirmation"`
	ReadError         string `json:"read_error,omitempty"`
}

type configUpgradeStatusResponse struct {
	InstanceDir       string                  `json:"instance_dir"`
	NeedsConfirmation bool                    `json:"needs_confirmation"`
	Reasons           []string                `json:"reasons"`
	Bot               configUpgradeFileStatus `json:"bot"`
	Model             configUpgradeFileStatus `json:"model"`
	Database          map[string]any          `json:"database"`
}

// handleConfigUpgradeStatus 对应 GET /api/webui/config/upgrade-status
// 只读展示 MaiBot 0.x -> 1.x 配置升级提示，不执行升级，也不伪造数据库检测结果
func (d Deps) handleConfigUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.configDir()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	botPath := filepath.Join(dir, botConfigFileName)
	modelPath := filepath.Join(dir, modelConfigFileName)
	bot := inspectConfigUpgradeFile(botPath, maibotcfg.BotConfigTargetVersion, legacy0XBotConfigBoundary)
	model := inspectConfigUpgradeFile(modelPath, maibotcfg.ModelConfigTargetVersion, legacy0XModelConfigBoundary)

	dbPath := filepath.Join(d.InstanceDir, "data", "MaiBot.db")
	legacyDB := false
	if d.Memory != nil {
		legacyDB = memory.DetectLegacy0XDatabase(d.Memory.GetDB())
	}
	reasons := make([]string, 0, 3)
	if legacyDB {
		reasons = append(reasons, "检测到旧版 0.x 数据库结构，将更新数据库："+dbPath)
	}
	if bot.NeedsConfirmation {
		reasons = append(reasons, "检测到主配置文件版本较旧，将更新配置文件："+
			bot.Path+" ("+bot.CurrentVersion+" -> "+bot.TargetVersion+")")
	}
	if model.NeedsConfirmation {
		reasons = append(reasons, "检测到模型配置文件版本较旧，将更新配置文件："+
			model.Path+" ("+model.CurrentVersion+" -> "+model.TargetVersion+")")
	}
	writeJSON(w, http.StatusOK, configUpgradeStatusResponse{
		InstanceDir:       d.InstanceDir,
		NeedsConfirmation: len(reasons) > 0,
		Reasons:           reasons,
		Bot:               bot,
		Model:             model,
		Database: map[string]any{
			"legacy_sqlite_checked": true,
			"backend":               "sqlite",
			"database_path":         dbPath,
			"legacy":                legacyDB,
		},
	})
}

func inspectConfigUpgradeFile(path, targetVersion, legacyBoundary string) configUpgradeFileStatus {
	status := configUpgradeFileStatus{
		Path:           path,
		TargetVersion:  targetVersion,
		LegacyBoundary: legacyBoundary,
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			status.Missing = true
		} else {
			status.ReadError = err.Error()
		}
		return status
	}
	var doc struct {
		Inner struct {
			Version string `toml:"version"`
		} `toml:"inner"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		status.ReadError = err.Error()
		return status
	}
	status.CurrentVersion = strings.TrimSpace(doc.Inner.Version)
	if status.CurrentVersion == "" {
		return status
	}
	currentKey := parseVersionKey(status.CurrentVersion)
	legacyKey := parseVersionKey(legacyBoundary)
	targetKey := parseVersionKey(targetVersion)
	// 对齐 Python _needs_legacy_config_confirmation：当前版本低于边界且低于目标版本
	status.NeedsConfirmation = compareVersionKeys(currentKey, legacyKey) < 0 &&
		compareVersionKeys(currentKey, targetKey) < 0
	return status
}
