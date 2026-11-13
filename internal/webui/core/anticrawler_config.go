package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "maibot-go/internal/config/maibot"
)

// antiCrawlerConfig 对齐 MaiBot src/config/official_configs.py:5415-5474 的 [webui] 默认值
// 仅保留反爬与 Cookie 相关字段，避免 WebUI 反向依赖实例配置持有者
func defaultAntiCrawlerConfig() antiCrawlerConfig {
	return antiCrawlerConfig{
		mode:           "basic",
		allowedIPs:     "127.0.0.1",
		trustedProxies: "",
		trustXFF:       false,
		secureCookie:   false,
	}
}

type antiCrawlerConfig struct {
	mode           string
	allowedIPs     string
	trustedProxies string
	trustXFF       bool
	secureCookie   bool
}

// loadAntiCrawlerConfig 从实例目录读取 bot_config.toml
// 空实例目录或配置文件不存在时使用 Python 默认值；TOML 损坏必须拒绝启动
func loadAntiCrawlerConfig(instanceDir string) (antiCrawlerConfig, error) {
	cfg := defaultAntiCrawlerConfig()
	instanceDir = strings.TrimSpace(instanceDir)
	if instanceDir == "" {
		return cfg, nil
	}

	path := filepath.Join(instanceDir, "config", "bot_config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return antiCrawlerConfig{}, fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	file := struct {
		WebUI maibotcfg.WebUIConfig `toml:"webui"`
	}{}
	file.WebUI.AntiCrawlerMode = cfg.mode
	file.WebUI.AllowedIps = cfg.allowedIPs
	file.WebUI.TrustedProxies = cfg.trustedProxies
	file.WebUI.TrustXff = cfg.trustXFF
	file.WebUI.SecureCookie = cfg.secureCookie
	if err := toml.Unmarshal(raw, &file); err != nil {
		return antiCrawlerConfig{}, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	cfg.mode = normalizeAntiCrawlerMode(file.WebUI.AntiCrawlerMode)
	cfg.allowedIPs = file.WebUI.AllowedIps
	cfg.trustedProxies = file.WebUI.TrustedProxies
	cfg.trustXFF = file.WebUI.TrustXff
	cfg.secureCookie = file.WebUI.SecureCookie
	return cfg, nil
}

// normalizeAntiCrawlerMode 对齐 anti_crawler.py:266-329，未知模式回落 basic
func normalizeAntiCrawlerMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "false", "strict", "loose", "basic":
		return mode
	default:
		return "basic"
	}
}
