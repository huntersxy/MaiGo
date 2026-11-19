package weixin_clawbot

import "MaiGo/internal/platform_io"

// ConfigSchemaMap 对齐 plugins/built_in/weixin_clawbot_adapter/settings.py
func ConfigSchemaMap() map[string]any {
	defaults := DefaultSettings()
	pluginFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", defaults.Plugin.Enabled, platformio.NativeFieldOpts{
			Description: "启用微信连接；启用后在「微信账号管理」中添加账号扫码",
			Order:       0,
		}),
		"config_version": platformio.NativeSchemaField("config_version", "string", defaults.Plugin.ConfigVersion, platformio.NativeFieldOpts{
			Description: "配置版本",
			Order:       1,
		}),
	}
	allowedUsers := defaults.Weixin.AllowedUsers
	if allowedUsers == nil {
		allowedUsers = []string{}
	}
	weixinFields := map[string]any{
		"allowed_users": platformio.NativeSchemaField("allowed_users", "array", allowedUsers, platformio.NativeFieldOpts{
			Description: "所有账号共享的 iLink 用户 ID 过滤列表；一般留空，允许所有收到的用户",
			ItemType:    "string",
			Order:       0,
		}),
		"retry_seconds": platformio.NativeSchemaField("retry_seconds", "integer", defaults.Weixin.RetrySeconds, platformio.NativeFieldOpts{
			Description: "连接异常重试间隔（秒）",
			Min:         1,
			Max:         60,
			Order:       1,
		}),
		"max_image_mb": platformio.NativeSchemaField("max_image_mb", "integer", defaults.Weixin.MaxImageMB, platformio.NativeFieldOpts{
			Description: "单张图片大小上限（MB）",
			Min:         1,
			Max:         8,
			Order:       2,
		}),
	}
	sections := map[string]any{
		"plugin": platformio.NativeSchemaSection("plugin", "插件", 0, pluginFields),
		"weixin": platformio.NativeSchemaSection("weixin", "微信", 1, weixinFields),
	}
	return platformio.NativeConfigSchema(PluginID, map[string]any{
		"name":        "微信 ClawBot 适配器",
		"version":     "0.2.0",
		"description": "通过微信 iLink / ClawBot 接入麦麦，支持多账号扫码绑定、账号备注、私聊文字、图片与语音转写。",
		"author":      "MaiBot Team",
	}, sections)
}
