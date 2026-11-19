package telegram

import "MaiGo/internal/platform_io"

// ConfigSchemaMap 对齐 plugins/built_in/telegram_adapter/settings.py
func ConfigSchemaMap() map[string]any {
	defaults := DefaultSettings()
	pluginFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", defaults.Plugin.Enabled, platformio.NativeFieldOpts{
			Description: "启用 Telegram 机器人连接",
			Order:       0,
		}),
		"config_version": platformio.NativeSchemaField("config_version", "string", defaults.Plugin.ConfigVersion, platformio.NativeFieldOpts{
			Description: "配置版本",
			Order:       1,
		}),
	}
	telegramFields := map[string]any{
		"bot_token": platformio.NativeSchemaField("bot_token", "string", defaults.Telegram.BotToken, platformio.NativeFieldOpts{
			Description: "BotFather 签发的机器人 Token",
			Widget:      "password",
			Order:       0,
		}),
		"api_base_url": platformio.NativeSchemaField("api_base_url", "string", defaults.Telegram.APIBaseURL, platformio.NativeFieldOpts{
			Description: "Bot API 地址，可使用本地 Bot API 服务",
			Order:       1,
		}),
		"proxy_url": platformio.NativeSchemaField("proxy_url", "string", defaults.Telegram.ProxyURL, platformio.NativeFieldOpts{
			Description: "HTTP/SOCKS5 代理地址；留空直连，不读取环境代理",
			Widget:      "password",
			Order:       2,
		}),
		"poll_timeout_sec": platformio.NativeSchemaField("poll_timeout_sec", "integer", defaults.Telegram.PollTimeoutSec, platformio.NativeFieldOpts{
			Description: "长轮询等待时间（秒）",
			Min:         1,
			Max:         50,
			Order:       3,
		}),
		"reconnect_delay_sec": platformio.NativeSchemaField("reconnect_delay_sec", "number", defaults.Telegram.ReconnectDelaySec, platformio.NativeFieldOpts{
			Description: "网络故障后重试间隔（秒）",
			Min:         1,
			Max:         300,
			Order:       4,
		}),
		"max_download_mb": platformio.NativeSchemaField("max_download_mb", "integer", defaults.Telegram.MaxDownloadMB, platformio.NativeFieldOpts{
			Description: "单个媒体下载上限（MB）；云端 Bot API 下载上限为 20 MB",
			Min:         1,
			Max:         50,
			Order:       5,
		}),
		"media_timeout_sec": platformio.NativeSchemaField("media_timeout_sec", "integer", defaults.Telegram.MediaTimeoutSec, platformio.NativeFieldOpts{
			Description: "媒体转换超时（秒）",
			Min:         10,
			Max:         300,
			Order:       6,
		}),
	}
	sections := map[string]any{
		"plugin":   platformio.NativeSchemaSection("plugin", "插件", 0, pluginFields),
		"telegram": platformio.NativeSchemaSection("telegram", "Telegram", 1, telegramFields),
	}
	return platformio.NativeConfigSchema(PluginID, map[string]any{
		"name":        "Telegram 适配器",
		"version":     "0.1.17",
		"description": "单账号 Telegram Bot API 适配器，支持论坛话题、文本、图片、静态/动画/视频贴纸、语音、@、回复引用和转发。",
		"author":      "MaiBot Team",
	}, sections)
}
