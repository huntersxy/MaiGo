package telegram

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	PluginID               = "maibot-team.telegram-adapter"
	GatewayName            = "telegram_gateway"
	DirectoryName          = "telegram_adapter"
	SupportedConfigVersion = "0.1.0"
	PlatformTelegram       = "telegram"
	RouteTypeDuplex        = "duplex"
	ProtocolBotAPI         = "telegram_bot_api"
	DefaultAPIBaseURL      = "https://api.telegram.org"
	DefaultPollTimeoutSec  = 30
	DefaultReconnectSec    = 5.0
	DefaultMaxDownloadMB   = 20
	DefaultMediaTimeoutSec = 60
)

// PluginOptions 对齐 [plugin]
type PluginOptions struct {
	Enabled       bool
	ConfigVersion string
}

// TelegramOptions 对齐 [telegram]
type TelegramOptions struct {
	BotToken          string
	APIBaseURL        string
	ProxyURL          string
	PollTimeoutSec    int
	ReconnectDelaySec float64
	MaxDownloadMB     int
	MediaTimeoutSec   int
}

// Settings 完整配置
type Settings struct {
	Plugin   PluginOptions
	Telegram TelegramOptions
}

// DefaultSettings 返回 schema 默认值（enabled=false, config_version=0.1.0）
func DefaultSettings() Settings {
	return Settings{
		Plugin: PluginOptions{
			Enabled:       false,
			ConfigVersion: SupportedConfigVersion,
		},
		Telegram: TelegramOptions{
			BotToken:          "",
			APIBaseURL:        DefaultAPIBaseURL,
			ProxyURL:          "",
			PollTimeoutSec:    DefaultPollTimeoutSec,
			ReconnectDelaySec: DefaultReconnectSec,
			MaxDownloadMB:     DefaultMaxDownloadMB,
			MediaTimeoutSec:   DefaultMediaTimeoutSec,
		},
	}
}

// DefaultConfigMap 供 NativeRegistration / Repair 落盘
func DefaultConfigMap() map[string]any {
	s := DefaultSettings()
	return map[string]any{
		"plugin": map[string]any{
			"enabled":        s.Plugin.Enabled,
			"config_version": s.Plugin.ConfigVersion,
		},
		"telegram": map[string]any{
			"bot_token":           s.Telegram.BotToken,
			"api_base_url":        s.Telegram.APIBaseURL,
			"proxy_url":           s.Telegram.ProxyURL,
			"poll_timeout_sec":    s.Telegram.PollTimeoutSec,
			"reconnect_delay_sec": s.Telegram.ReconnectDelaySec,
			"max_download_mb":     s.Telegram.MaxDownloadMB,
			"media_timeout_sec":   s.Telegram.MediaTimeoutSec,
		},
	}
}

// ParseSettings 从配置 map 解析；缺键走默认
func ParseSettings(config map[string]any) (Settings, error) {
	settings := DefaultSettings()
	if config == nil {
		return settings, nil
	}
	if plugin, ok := asMap(config["plugin"]); ok {
		if v, ok := plugin["enabled"]; ok {
			settings.Plugin.Enabled = asBool(v)
		}
		if v := strings.TrimSpace(asString(plugin["config_version"])); v != "" {
			settings.Plugin.ConfigVersion = v
		}
	}
	if tg, ok := asMap(config["telegram"]); ok {
		settings.Telegram.BotToken = strings.TrimSpace(asString(tg["bot_token"]))
		if v := strings.TrimSpace(asString(tg["api_base_url"])); v != "" {
			settings.Telegram.APIBaseURL = strings.TrimRight(v, "/")
		}
		settings.Telegram.ProxyURL = strings.TrimSpace(asString(tg["proxy_url"]))
		if n, ok := asInt(tg["poll_timeout_sec"]); ok {
			settings.Telegram.PollTimeoutSec = clampInt(n, 1, 50, DefaultPollTimeoutSec)
		}
		if f, ok := asFloat(tg["reconnect_delay_sec"]); ok {
			settings.Telegram.ReconnectDelaySec = clampFloat(f, 1, 300, DefaultReconnectSec)
		}
		if n, ok := asInt(tg["max_download_mb"]); ok {
			settings.Telegram.MaxDownloadMB = clampInt(n, 1, 50, DefaultMaxDownloadMB)
		}
		if n, ok := asInt(tg["media_timeout_sec"]); ok {
			settings.Telegram.MediaTimeoutSec = clampInt(n, 10, 300, DefaultMediaTimeoutSec)
		}
	}
	if settings.Plugin.ConfigVersion == "" {
		settings.Plugin.ConfigVersion = SupportedConfigVersion
	}
	return settings, nil
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asString(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func asBool(v any) bool {
	switch typed := v.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on", "enabled":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func asInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		return n, err == nil
	default:
		return 0, false
	}
}

func asFloat(v any) (float64, bool) {
	switch typed := v.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func clampInt(v, lo, hi, def int) int {
	if v < lo || v > hi {
		return def
	}
	return v
}

func clampFloat(v, lo, hi, def float64) float64 {
	if v < lo || v > hi {
		return def
	}
	return v
}
