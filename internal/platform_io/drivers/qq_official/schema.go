package qq_official

import "MaiGo/internal/platform_io"

// ConfigSchemaMap 对齐 settings.py + plugin.py::build_config_schema（含 other_source_sync）
func ConfigSchemaMap() map[string]any {
	defaults := DefaultConfigMap()
	plugin := defaults["plugin"].(map[string]any)
	qq := defaults["qq_official"].(map[string]any)
	mute := defaults["mute"].(map[string]any)
	recall := defaults["recall"].(map[string]any)
	kick := defaults["kick"].(map[string]any)
	filterSec := defaults["filter"].(map[string]any)
	advanced := defaults["advanced"].(map[string]any)

	pluginFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", plugin["enabled"], platformio.NativeFieldOpts{
			Description: "启用 QQ 官方机器人连接",
			Order:       0,
		}),
		"config_version": platformio.NativeSchemaField("config_version", "string", plugin["config_version"], platformio.NativeFieldOpts{
			Description: "配置版本",
			Order:       1,
		}),
	}
	qqFields := map[string]any{
		"app_id": platformio.NativeSchemaField("app_id", "string", qq["app_id"], platformio.NativeFieldOpts{
			Label:       "AppID",
			Description: "QQ 开放平台机器人的 AppID",
			Order:       0,
		}),
		"app_secret": platformio.NativeSchemaField("app_secret", "string", qq["app_secret"], platformio.NativeFieldOpts{
			Label:       "AppSecret",
			Description: "QQ 开放平台机器人的 AppSecret（ClientSecret）",
			Widget:      "password",
			Order:       1,
		}),
		"api_base_url": platformio.NativeSchemaField("api_base_url", "string", qq["api_base_url"], platformio.NativeFieldOpts{
			Description: "QQ 开放平台 API 地址",
			Order:       2,
		}),
	}
	syncFields := map[string]any{
		"unified_account_id": platformio.NativeSchemaField("unified_account_id", "string", qq["unified_account_id"], platformio.NativeFieldOpts{
			Label:       "关联QQ号",
			Description: "统一账号 ID（如 NapCat 机器人 QQ 号）；非空时聊天流按此 ID 归属，与其它 qq 适配器共享上下文",
			Order:       0,
		}),
		"silent_user_sync": platformio.NativeSchemaField("silent_user_sync", "boolean", qq["silent_user_sync"], platformio.NativeFieldOpts{
			Label:       "静默同步群成员身份",
			Description: "在已绑定群中与 SnowLuma 静默匹配 QQ 号、昵称和群名片；需填写关联QQ号，三条不同正文一致后生效",
			Order:       2,
		}),
		"enable_mapping": platformio.NativeSchemaField("enable_mapping", "boolean", qq["enable_mapping"], platformio.NativeFieldOpts{
			Label:       "启用 QQ 号映射",
			Description: "开启后使用已关联的 QQ 号；关闭时聊天身份保留原始 OpenID",
			Order:       1,
		}),
		"snowluma_identity_only": platformio.NativeSchemaField("snowluma_identity_only", "boolean", qq["snowluma_identity_only"], platformio.NativeFieldOpts{
			Label:       "SL 仅作身份查询（默认关闭）",
			Description: "官方连接就绪时持续续期，让关联 SL/NapCat 账号仅采集身份，暂停其消息和通知入站投递并撤销出站路由，避免双重发送；15 秒未续期自动恢复收发",
			Order:       3,
		}),
	}
	muteFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", mute["enabled"], platformio.NativeFieldOpts{
			Description: "启用 set_mute 禁言设置工具；时长为 0 解除禁言，正整数施加禁言",
			Order:       0,
		}),
		"allowed_groups": targetListField("allowed_groups", "group", "允许群", "搜索并选择群；留空不限制", mute["allowed_groups"], 1),
		"admin_users":    targetListField("admin_users", "user", "保护用户", "搜索并选择不允许被操作的用户", mute["admin_users"], 2),
		"min_duration": platformio.NativeSchemaField("min_duration", "integer", mute["min_duration"], platformio.NativeFieldOpts{
			Description: "最短禁言时长（秒）",
			Min:         1,
			Order:       3,
		}),
		"max_duration": platformio.NativeSchemaField("max_duration", "integer", mute["max_duration"], platformio.NativeFieldOpts{
			Description: "最长禁言时长（秒），官方上限 30 天",
			Min:         1,
			Max:         2592000,
			Order:       4,
		}),
	}
	recallFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", recall["enabled"], platformio.NativeFieldOpts{
			Description: "启用 recall_message 撤回工具，默认开启，关闭时不向模型提供该工具",
			Order:       0,
		}),
		"allowed_groups": targetListField("allowed_groups", "group", "允许群", "搜索并选择群；留空不限制", recall["allowed_groups"], 1),
	}
	kickFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", kick["enabled"], platformio.NativeFieldOpts{
			Description: "启用 kick_member 踢人工具；需要 QQ 官方移除群成员接口权限",
			Order:       0,
		}),
		"allowed_groups": targetListField("allowed_groups", "group", "允许群", "搜索并选择群；留空不限制", kick["allowed_groups"], 1),
		"admin_users":    targetListField("admin_users", "user", "保护用户", "搜索并选择不允许被操作的用户", kick["admin_users"], 2),
	}
	filterFields := map[string]any{
		"ignore_other_bots": platformio.NativeSchemaField("ignore_other_bots", "boolean", filterSec["ignore_other_bots"], platformio.NativeFieldOpts{
			Label:       "屏蔽其他机器人消息",
			Description: "忽略其它机器人发送的消息（官方事件的 author.bot=true，例如群里另一个官方机器人）",
			Order:       0,
		}),
	}
	advancedFields := map[string]any{
		"show_debug_info": platformio.NativeSchemaField("show_debug_info", "boolean", advanced["show_debug_info"], platformio.NativeFieldOpts{
			Label:       "显示调试信息",
			Description: "在日志中逐条显示消息按主动还是被动方式发送；被动发送同时显示目标消息 ID 和回复序号",
			Order:       0,
		}),
		"reconnect_delay_sec": platformio.NativeSchemaField("reconnect_delay_sec", "number", advanced["reconnect_delay_sec"], platformio.NativeFieldOpts{
			Label:       "断线重连间隔（秒）",
			Description: "断线后等待多久重新连接，单位为秒",
			Min:         1,
			Max:         300,
			Order:       1,
		}),
	}

	otherSource := platformio.NativeSchemaSection("qq_official", "其他来源信息同步", 2, syncFields)
	sections := map[string]any{
		"plugin":            platformio.NativeSchemaSection("plugin", "插件", 0, pluginFields),
		"qq_official":       platformio.NativeSchemaSection("qq_official", "QQ 开放平台", 1, qqFields),
		"other_source_sync": otherSource,
		"mute":              platformio.NativeSchemaSection("mute", "禁言工具", 3, muteFields),
		"recall":            platformio.NativeSchemaSection("recall", "撤回工具", 4, recallFields),
		"kick":              platformio.NativeSchemaSection("kick", "踢人工具", 5, kickFields),
		"filter":            platformio.NativeSchemaSection("filter", "消息过滤", 6, filterFields),
		"advanced":          platformio.NativeSchemaSection("advanced", "高级设置", 7, advancedFields),
	}
	return platformio.NativeConfigSchema(PluginID, map[string]any{
		"name":        "QQ 官方机器人适配器",
		"version":     SupportedConfigVersion,
		"description": "直连 QQ 开放平台官方 API，支持群聊与单聊的文本、@、图片、表情、语音收发及视频、文件发送，提供禁言、解除禁言、撤回及可选踢人工具。",
		"author":      "MaiBot Team",
	}, sections)
}

func targetListField(name, kind, label, hint string, defaults any, order int) map[string]any {
	field := platformio.NativeSchemaField(name, "array", defaults, platformio.NativeFieldOpts{
		Label: label, Hint: hint, Widget: "target", ItemType: "object", Order: order,
	})
	field["item_fields"] = map[string]any{
		"platform": platformio.NativeSchemaField("platform", "string", PlatformQQ, platformio.NativeFieldOpts{}),
		"type":     platformio.NativeSchemaField("type", "string", kind, platformio.NativeFieldOpts{}),
		"id":       platformio.NativeSchemaField("id", "string", "", platformio.NativeFieldOpts{Required: true}),
		"name":     platformio.NativeSchemaField("name", "string", "", platformio.NativeFieldOpts{}),
	}
	return field
}
