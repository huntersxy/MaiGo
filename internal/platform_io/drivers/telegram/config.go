package telegram

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/platform_io"
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
	return SettingsToMap(DefaultSettings())
}

// SettingsToMap 序列化为嵌套 map（Validate / Inspect）
func SettingsToMap(s Settings) map[string]any {
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
	normalized, err := platformio.NormalizeNativeConfig(config, ConfigSchemaMap())
	if err != nil {
		return Settings{}, err
	}
	var settings Settings
	plugin := normalized["plugin"]
	settings.Plugin = PluginOptions{Enabled: plugin["enabled"].(bool), ConfigVersion: plugin["config_version"].(string)}
	tg := normalized["telegram"]
	settings.Telegram = TelegramOptions{
		BotToken: tg["bot_token"].(string), APIBaseURL: tg["api_base_url"].(string), ProxyURL: tg["proxy_url"].(string),
		PollTimeoutSec: tg["poll_timeout_sec"].(int), ReconnectDelaySec: tg["reconnect_delay_sec"].(float64),
		MaxDownloadMB: tg["max_download_mb"].(int), MediaTimeoutSec: tg["media_timeout_sec"].(int),
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

func asBool(value any) bool {
	b, _ := value.(bool)
	return b
}

func asInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		n := int(typed)
		return n, float64(n) == typed
	case json.Number:
		n, err := strconv.Atoi(string(typed))
		return n, err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		return n, err == nil
	default:
		return 0, false
	}
}
