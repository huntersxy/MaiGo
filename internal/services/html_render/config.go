package htmlrender

import (
	"os"
	"strconv"
	"strings"
	"time"

	maibotcfg "MaiGo/internal/config/maibot"
)

const (
	defaultSidecarTimeout   = 30 * time.Second
	defaultRenderTimeout    = 15 * time.Second
	defaultConcurrencyLimit = 2
)

// Config HTML 渲染配置。
//
// 渲染统一委托给 Python 原版 Playwright 侧车（MAIBOT_HTMLRENDER_URL）；
// Go 侧只保留请求规范化、并发限制和 HTTP 客户端配置。
type Config struct {
	SidecarURL     string
	SidecarTimeout time.Duration
	SidecarToken   string

	Disabled         bool
	Mode             string
	ConcurrencyLimit int
	RenderTimeout    time.Duration
}

// DefaultConfig 返回与 Python PluginRuntimeRenderConfig 对齐的默认值
func DefaultConfig() Config {
	return Config{
		SidecarTimeout:   defaultSidecarTimeout,
		Mode:             "auto",
		ConcurrencyLimit: defaultConcurrencyLimit,
		RenderTimeout:    defaultRenderTimeout,
	}
}

// LoadConfigFromEnv 读取 MAIBOT_HTMLRENDER_* 环境变量
func LoadConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.SidecarURL = strings.TrimRight(strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_URL")), "/")
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_TIMEOUT")); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			cfg.SidecarTimeout = time.Duration(sec * float64(time.Second))
		}
	}
	cfg.SidecarToken = strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_TOKEN"))
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_MODE")); v != "" {
		cfg.Mode = v
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_CONCURRENCY")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			cfg.ConcurrencyLimit = parsed
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_RENDER_TIMEOUT")); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			cfg.RenderTimeout = time.Duration(sec * float64(time.Second))
		}
	}
	return normalizeConfig(cfg)
}

// ApplyPluginRuntimeConfig 把 bot_config.toml 的 plugin_runtime.render 叠加到配置。
//
// 浏览器字段仍保留在配置结构中用于兼容原版 schema，但 Go 侧不再据此启动浏览器；
// 渲染始终走 Python 侧车。
func ApplyPluginRuntimeConfig(cfg Config, render maibotcfg.PluginRuntimeRenderConfig, _ string) Config {
	if render.Enabled {
		cfg.Disabled = false
	} else if pluginRenderConfigExplicit(render) {
		// TOML 的 enabled=false 与“整个 section 缺失”在解码后同为 false
		cfg.Disabled = true
	}
	if render.ConcurrencyLimit > 0 {
		cfg.ConcurrencyLimit = render.ConcurrencyLimit
	}
	if render.RenderTimeoutSec > 0 {
		cfg.RenderTimeout = time.Duration(render.RenderTimeoutSec * float64(time.Second))
	}
	return normalizeConfig(cfg)
}

func pluginRenderConfigExplicit(render maibotcfg.PluginRuntimeRenderConfig) bool {
	return strings.TrimSpace(render.BrowserWsEndpoint) != "" ||
		strings.TrimSpace(render.ExecutablePath) != "" ||
		strings.TrimSpace(render.BrowserInstallRoot) != "" ||
		render.Headless ||
		len(render.LaunchArgs) > 0 ||
		render.ConcurrencyLimit > 0 ||
		render.StartupTimeoutSec > 0 ||
		render.RenderTimeoutSec > 0 ||
		render.AutoDownloadChromium ||
		render.DownloadConnectionTimeoutSec > 0 ||
		render.RestartAfterRenderCount > 0
}

func normalizeConfig(cfg Config) Config {
	if cfg.SidecarTimeout <= 0 {
		cfg.SidecarTimeout = defaultSidecarTimeout
	}
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = "auto"
	}
	if cfg.ConcurrencyLimit <= 0 {
		cfg.ConcurrencyLimit = defaultConcurrencyLimit
	}
	if cfg.RenderTimeout <= 0 {
		cfg.RenderTimeout = defaultRenderTimeout
	}
	return cfg
}
