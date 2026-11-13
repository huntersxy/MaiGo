package htmlrender

import (
	"os"
	"strconv"
	"strings"
	"time"

	maibotcfg "maibot-go/internal/config/maibot"
)

const (
	defaultSidecarTimeout   = 30 * time.Second
	defaultStartupTimeout   = 20 * time.Second
	defaultRenderTimeout    = 15 * time.Second
	defaultConcurrencyLimit = 2
	defaultRestartAfter     = 200
)

// Config HTML 渲染配置
//
// 侧车（MAIBOT_HTMLRENDER_URL）与宿主浏览器可同时配置；侧车优先，未配置侧车时
// 使用本机 Chrome/Chromium/Edge 的 CDP 协议渲染
type Config struct {
	SidecarURL     string
	SidecarTimeout time.Duration
	SidecarToken   string

	Disabled           bool
	Mode               string
	BrowserWS          string
	BrowserPath        string
	BrowserInstallRoot string
	InstanceDir        string
	Headless           bool
	LaunchArgs         []string
	ConcurrencyLimit   int
	StartupTimeout     time.Duration
	RenderTimeout      time.Duration
	RestartAfterCount  int
}

// DefaultConfig 返回与 Python PluginRuntimeRenderConfig 对齐的默认值
func DefaultConfig() Config {
	return Config{
		SidecarTimeout:     defaultSidecarTimeout,
		Mode:               "auto",
		BrowserInstallRoot: "data/playwright-browsers",
		Headless:           true,
		ConcurrencyLimit:   defaultConcurrencyLimit,
		StartupTimeout:     defaultStartupTimeout,
		RenderTimeout:      defaultRenderTimeout,
		RestartAfterCount:  defaultRestartAfter,
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
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_BROWSER_WS")); v != "" {
		cfg.BrowserWS = v
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_BROWSER_PATH")); v != "" {
		cfg.BrowserPath = v
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_BROWSER_INSTALL_ROOT")); v != "" {
		cfg.BrowserInstallRoot = v
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_HEADLESS")); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			cfg.Headless = parsed
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_CONCURRENCY")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			cfg.ConcurrencyLimit = parsed
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_STARTUP_TIMEOUT")); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			cfg.StartupTimeout = time.Duration(sec * float64(time.Second))
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_RENDER_TIMEOUT")); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			cfg.RenderTimeout = time.Duration(sec * float64(time.Second))
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_RESTART_AFTER")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			cfg.RestartAfterCount = parsed
		}
	}
	if v := strings.TrimSpace(os.Getenv("MAIBOT_HTMLRENDER_LAUNCH_ARGS")); v != "" {
		cfg.LaunchArgs = strings.Fields(v)
	}
	return cfg
}

// ApplyPluginRuntimeConfig 把 bot_config.toml 的 plugin_runtime.render 叠加到配置
//
// TOML 的 bool 零值无法区分“未配置”和“显式 false”，因此 headless 仅在 true 时覆盖；
// 需要显式关闭无头模式时使用 MAIBOT_HTMLRENDER_HEADLESS=false
func ApplyPluginRuntimeConfig(cfg Config, render maibotcfg.PluginRuntimeRenderConfig, instanceDir string) Config {
	cfg.InstanceDir = strings.TrimSpace(instanceDir)
	if render.Enabled {
		cfg.Disabled = false
	} else if pluginRenderConfigExplicit(render) {
		// TOML 的 enabled=false 与“整个 section 缺失”在解码后同为 false
		// 只有 section 里存在其他字段时才认为用户显式关闭，避免缺省配置误禁用
		cfg.Disabled = true
	}
	if v := strings.TrimSpace(render.BrowserWsEndpoint); v != "" {
		cfg.BrowserWS = v
	}
	if v := strings.TrimSpace(render.ExecutablePath); v != "" {
		cfg.BrowserPath = v
	}
	if v := strings.TrimSpace(render.BrowserInstallRoot); v != "" {
		cfg.BrowserInstallRoot = v
	}
	if render.Headless {
		cfg.Headless = true
	}
	if len(render.LaunchArgs) > 0 {
		cfg.LaunchArgs = append([]string(nil), render.LaunchArgs...)
	}
	if render.ConcurrencyLimit > 0 {
		cfg.ConcurrencyLimit = render.ConcurrencyLimit
	}
	if render.StartupTimeoutSec > 0 {
		cfg.StartupTimeout = time.Duration(render.StartupTimeoutSec * float64(time.Second))
	}
	if render.RenderTimeoutSec > 0 {
		cfg.RenderTimeout = time.Duration(render.RenderTimeoutSec * float64(time.Second))
	}
	if render.RestartAfterRenderCount > 0 {
		cfg.RestartAfterCount = render.RestartAfterRenderCount
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
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}
	if cfg.RenderTimeout <= 0 {
		cfg.RenderTimeout = defaultRenderTimeout
	}
	if cfg.RestartAfterCount < 0 {
		cfg.RestartAfterCount = defaultRestartAfter
	}
	return cfg
}
