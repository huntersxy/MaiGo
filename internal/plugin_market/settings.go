package pluginmarket

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// PluginSourceSettings 对齐 sources.PluginSourceSettings：信息源与下载源分别配置
type PluginSourceSettings struct {
	InformationSource    string  `json:"information_source"`
	DownloadSource       string  `json:"download_source"`
	CustomDownloadPrefix *string `json:"custom_download_prefix"`
}

// DefaultSourceSettings 对齐 PluginSourceSettings 的默认值
func DefaultSourceSettings() PluginSourceSettings {
	return PluginSourceSettings{InformationSource: "cnb", DownloadSource: "cnb"}
}

// Normalize 校验并规范化设置，对齐 pydantic 的 Literal 与自定义下载源约束
func (s *PluginSourceSettings) Normalize() error {
	s.InformationSource = strings.TrimSpace(s.InformationSource)
	if s.InformationSource == "" {
		s.InformationSource = "cnb"
	}
	switch s.InformationSource {
	case "cnb", "github":
	default:
		return fmt.Errorf("插件市场信息源必须是 cnb 或 github 之一")
	}
	s.DownloadSource = strings.TrimSpace(s.DownloadSource)
	if s.DownloadSource == "" {
		s.DownloadSource = "cnb"
	}
	switch s.DownloadSource {
	case "cnb", "github", "custom":
	default:
		return fmt.Errorf("插件市场下载源必须是 cnb、github 或 custom 之一")
	}
	if s.CustomDownloadPrefix != nil {
		trimmed := strings.TrimSpace(*s.CustomDownloadPrefix)
		if trimmed == "" {
			s.CustomDownloadPrefix = nil
		} else {
			s.CustomDownloadPrefix = &trimmed
		}
	}
	if s.DownloadSource == "custom" && (s.CustomDownloadPrefix == nil || *s.CustomDownloadPrefix == "") {
		return fmt.Errorf("自定义下载源必须填写 Git 克隆前缀")
	}
	return nil
}

// settingsPath 对齐 SETTINGS_FILE：实例目录下的 data/plugin_market.json
func settingsPath(instanceDir string) (string, error) {
	root, err := resolveInstanceRoot(instanceDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "data", "plugin_market.json"), nil
}

// quarantineSettingsFile 保留损坏设置原文件，把正式路径释放给下一次写入
func quarantineSettingsFile(path string) (string, error) {
	backup := path + ".corrupt-" + randomHex(16)
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

// LoadSourceSettings 对齐 load_source_settings：旧 use_github 与 official 源迁移为 CNB，损坏设置恢复默认
func LoadSourceSettings(instanceDir string) (PluginSourceSettings, error) {
	path, err := settingsPath(instanceDir)
	if err != nil {
		return PluginSourceSettings{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultSourceSettings(), nil
		}
		return PluginSourceSettings{}, err
	}
	fallback := func(cause error) (PluginSourceSettings, error) {
		backup, quarantineErr := quarantineSettingsFile(path)
		if quarantineErr != nil {
			return PluginSourceSettings{}, quarantineErr
		}
		zap.L().Warn("插件市场源设置损坏，已保留原文件并恢复默认源",
			zap.String("backup", backup), zap.Error(cause))
		return DefaultSourceSettings(), nil
	}
	var payload map[string]any
	if err := sonic.Unmarshal(raw, &payload); err != nil {
		return fallback(err)
	}
	if legacy, ok := payload["use_github"]; ok {
		if _, isBool := legacy.(bool); !isBool {
			return fallback(fmt.Errorf("插件市场 use_github 设置必须为布尔值"))
		}
		// 已移除官方信息源，旧开关一律迁移为 CNB 信息源。
		payload = map[string]any{"information_source": "cnb"}
	}
	if safeString(payload["information_source"]) == "official" {
		payload["information_source"] = "cnb"
	}
	settings := PluginSourceSettings{
		InformationSource: safeString(payload["information_source"]),
		DownloadSource:    safeString(payload["download_source"]),
	}
	if rawPrefix, ok := payload["custom_download_prefix"]; ok && rawPrefix != nil {
		prefix := safeString(rawPrefix)
		if prefix != "" {
			settings.CustomDownloadPrefix = &prefix
		}
	}
	if err := settings.Normalize(); err != nil {
		return fallback(err)
	}
	return settings, nil
}

// SaveSourceSettings 对齐 save_source_settings：校验自定义前缀并原子写入
func SaveSourceSettings(instanceDir string, settings PluginSourceSettings, _ urlGuardOptions) error {
	if err := settings.Normalize(); err != nil {
		return err
	}
	if settings.CustomDownloadPrefix != nil && *settings.CustomDownloadPrefix != "" {
		source, err := SelectGitSource("custom", *settings.CustomDownloadPrefix)
		if err != nil {
			return err
		}
		prefix := source.ClonePrefix
		settings.CustomDownloadPrefix = &prefix
	}
	path, err := settingsPath(instanceDir)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload, err := sonic.Marshal(settings)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".plugin-market-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		if _, statErr := os.Stat(tempName); statErr == nil {
			_ = os.Remove(tempName)
		}
	}()
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

// UpdateSourceSettings 保存市场源设置，复用服务当前出站校验开关
func (s *Service) UpdateSourceSettings(instanceDir string, settings PluginSourceSettings) error {
	options := s.guardForRequest()
	return SaveSourceSettings(instanceDir, settings, options)
}
