package snowluma

import "MaiGo/internal/platform_io"

// ConfigSchemaMap 对齐 snowluma QQPluginSettings / config.py 各节
func ConfigSchemaMap() map[string]any {
	s := DefaultSettings()
	m := SettingsToMap(s)
	plugin := m["plugin"].(map[string]any)
	chat := m["chat_abilities"].(map[string]any)
	client := m["client"].(map[string]any)
	filters := m["filters"].(map[string]any)
	notice := m["notice"].(map[string]any)
	debug := m["debug"].(map[string]any)
	mute := m["mute"].(map[string]any)

	pluginFields := map[string]any{
		"enabled": platformio.NativeSchemaField("enabled", "boolean", plugin["enabled"], platformio.NativeFieldOpts{
			Label: "启用适配器", Description: "是否启用适配器。",
			Hint:  "关闭后插件会保持空闲，不会主动建立 WebSocket 连接。", Order: 0,
		}),
		"qq_face_parse_mode": platformio.NativeSchemaField("qq_face_parse_mode", "string", plugin["qq_face_parse_mode"], platformio.NativeFieldOpts{
			Label: "QQ表情解析模式", Description: "QQ 自带表情解析模式：转为中文描述或近似 Unicode emoji。",
			Hint: "[description]模式会把表情转成[流泪]这种形式[emoji]会转成近似emoji表情。",
			Choices: []any{"description", "emoji"}, Widget: "select", Order: 1,
		}),
		"enable_text_face_conversion": platformio.NativeSchemaField("enable_text_face_conversion", "boolean", plugin["enable_text_face_conversion"], platformio.NativeFieldOpts{
			Label: "启用文本转QQ表情", Description: "发送时将 /表情名 格式的文本转换为 QQ 原生表情，未知名称保持原样。", Order: 2,
		}),
		"enable_text_at_conversion": platformio.NativeSchemaField("enable_text_at_conversion", "boolean", plugin["enable_text_at_conversion"], platformio.NativeFieldOpts{
			Label: "启用文本转@", Description: "将群聊发送文本中的 @QQ号、@群名片或 @昵称转换为实际 @。", Order: 3,
		}),
		"config_version": platformio.NativeSchemaField("config_version", "string", plugin["config_version"], platformio.NativeFieldOpts{
			Label: "配置版本", Description: "当前配置结构版本。", Hidden: true, Disabled: true, Order: 99,
		}),
	}

	chatFields := map[string]any{
		"enable_private_chat_tool": boolField("enable_private_chat_tool", chat, "启用主动私聊工具", "启用主动私聊工具", 0),
		"enable_poke_tool":         boolField("enable_poke_tool", chat, "启用戳一戳工具", "启用戳一戳工具", 1),
		"enable_recall_tool":       boolField("enable_recall_tool", chat, "启用消息撤回工具", "启用消息撤回工具", 2),
		"enable_mute_tool":         boolField("enable_mute_tool", chat, "启用智能禁言工具", "启用智能禁言工具", 3),
		"enable_emoji_like_tool":   boolField("enable_emoji_like_tool", chat, "启用表情表态工具", "启用表情表态工具", 4),
		"enable_forward_tool":      boolField("enable_forward_tool", chat, "启用消息转发工具", "启用消息转发工具", 5),
	}

	clientFields := map[string]any{
		"client_type": platformio.NativeSchemaField("client_type", "string", client["client_type"], platformio.NativeFieldOpts{
			Label: "客户端类型", Description: "对端客户端类型：auto / onebot_v11 / napcat / snowluma。",
			Choices: []any{"auto", "onebot_v11", "napcat", "snowluma"}, Widget: "select", Order: 0,
		}),
		"server": platformio.NativeSchemaField("server", "string", client["server"], platformio.NativeFieldOpts{
			Label: "服务地址", Description: "正向 WebSocket 服务地址。", Order: 1,
		}),
		"port": platformio.NativeSchemaField("port", "integer", client["port"], platformio.NativeFieldOpts{
			Label: "端口", Description: "正向 WebSocket 端口。", Order: 2,
		}),
		"token": platformio.NativeSchemaField("token", "string", client["token"], platformio.NativeFieldOpts{
			Label: "访问令牌", Description: "访问令牌；可留空。", InputType: "password", Placeholder: "可留空", Order: 3,
		}),
		"heartbeat_interval": platformio.NativeSchemaField("heartbeat_interval", "number", client["heartbeat_interval"], platformio.NativeFieldOpts{
			Label: "心跳间隔（秒）", Description: "心跳间隔（秒）。", Step: 1, Order: 4,
		}),
		"reconnect_delay_sec": platformio.NativeSchemaField("reconnect_delay_sec", "number", client["reconnect_delay_sec"], platformio.NativeFieldOpts{
			Label: "重连等待（秒）", Description: "断线后重连等待（秒）。", Step: 1, Order: 5,
		}),
		"action_timeout_sec": platformio.NativeSchemaField("action_timeout_sec", "number", client["action_timeout_sec"], platformio.NativeFieldOpts{
			Label: "动作超时（秒）", Description: "动作调用超时（秒）。", Step: 1, Order: 6,
		}),
		"connection_id": platformio.NativeSchemaField("connection_id", "string", client["connection_id"], platformio.NativeFieldOpts{
			Label: "连接标识", Description: "多连接时的路由 scope 标识。", Order: 7,
		}),
	}

	filterFields := map[string]any{
		"regex_filter_enabled": platformio.NativeSchemaField("regex_filter_enabled", "boolean", filters["regex_filter_enabled"], platformio.NativeFieldOpts{
			Label: "启用正则过滤", Order: 0,
		}),
		"regex_filter_mode": platformio.NativeSchemaField("regex_filter_mode", "string", filters["regex_filter_mode"], platformio.NativeFieldOpts{
			Label: "正则过滤模式", Choices: []any{"blacklist", "whitelist"}, Widget: "select", Order: 1,
		}),
		"regex_filter_patterns": platformio.NativeSchemaField("regex_filter_patterns", "array", filters["regex_filter_patterns"], platformio.NativeFieldOpts{
			Label: "正则表达式列表", ItemType: "string", Order: 2,
		}),
		"regex_filter_show_dropped": platformio.NativeSchemaField("regex_filter_show_dropped", "boolean", filters["regex_filter_show_dropped"], platformio.NativeFieldOpts{
			Label: "显示正则过滤丢弃日志", Order: 3,
		}),
		"ban_qq_bot": platformio.NativeSchemaField("ban_qq_bot", "boolean", filters["ban_qq_bot"], platformio.NativeFieldOpts{
			Label: "屏蔽官方机器人", Order: 4,
		}),
		"ban_user_id": platformio.NativeSchemaField("ban_user_id", "array", filters["ban_user_id"], platformio.NativeFieldOpts{
			Label: "用户黑名单", ItemType: "string", Order: 6,
		}),
	}

	noticeFields := map[string]any{
		"enabled":                    boolField("enabled", notice, "启用通知事件", "启用通知事件", 0),
		"enable_poke":                 boolField("enable_poke", notice, "传递戳一戳", "传递戳一戳", 1),
		"enable_friend_recall":        boolField("enable_friend_recall", notice, "传递好友撤回", "传递好友撤回", 2),
		"enable_group_recall":         boolField("enable_group_recall", notice, "传递群消息撤回", "传递群消息撤回", 3),
		"enable_group_ban":            boolField("enable_group_ban", notice, "传递禁言", "传递禁言", 4),
		"enable_group_msg_emoji_like": boolField("enable_group_msg_emoji_like", notice, "传递消息表情回应", "传递消息表情回应", 5),
		"enable_group_upload":         boolField("enable_group_upload", notice, "传递群文件上传", "传递群文件上传", 6),
		"enable_group_increase":       boolField("enable_group_increase", notice, "传递入群", "传递入群", 7),
		"enable_group_decrease":       boolField("enable_group_decrease", notice, "传递退群", "传递退群", 8),
		"enable_group_admin":          boolField("enable_group_admin", notice, "传递管理员变动", "传递管理员变动", 9),
		"enable_essence":              boolField("enable_essence", notice, "传递精华消息", "传递精华消息", 10),
		"enable_group_name":           boolField("enable_group_name", notice, "传递群名变更", "传递群名变更", 11),
	}

	debugFields := map[string]any{
		"enable_ada_debug_raw_message_log": platformio.NativeSchemaField("enable_ada_debug_raw_message_log", "boolean", debug["enable_ada_debug_raw_message_log"], platformio.NativeFieldOpts{
			Label: "显示原始消息段", Order: 0,
		}),
		"enable_ada_debug_raw_outbound_message_log": platformio.NativeSchemaField("enable_ada_debug_raw_outbound_message_log", "boolean", debug["enable_ada_debug_raw_outbound_message_log"], platformio.NativeFieldOpts{
			Label: "显示原始发送段", Order: 1,
		}),
		"ignore_self_message": platformio.NativeSchemaField("ignore_self_message", "boolean", debug["ignore_self_message"], platformio.NativeFieldOpts{
			Label: "忽略自身消息", Order: 2,
		}),
	}

	muteFields := map[string]any{
		"admin_users": platformio.NativeSchemaField("admin_users", "array", mute["admin_users"], platformio.NativeFieldOpts{
			Label: "禁言保护名单", ItemType: "string", Order: 0,
		}),
		"allowed_groups": platformio.NativeSchemaField("allowed_groups", "array", mute["allowed_groups"], platformio.NativeFieldOpts{
			Label: "禁言群白名单", ItemType: "string", Order: 1,
		}),
		"min_duration": platformio.NativeSchemaField("min_duration", "integer", mute["min_duration"], platformio.NativeFieldOpts{
			Label: "最短时长（秒）", Order: 2,
		}),
		"max_duration": platformio.NativeSchemaField("max_duration", "integer", mute["max_duration"], platformio.NativeFieldOpts{
			Label: "最长时长（秒）", Order: 3,
		}),
	}

	sections := map[string]any{
		"plugin":         platformio.NativeSchemaSection("plugin", "插件设置", 0, pluginFields),
		"chat_abilities": platformio.NativeSchemaSection("chat_abilities", "聊天能力", 1, chatFields),
		"client":         platformio.NativeSchemaSection("client", "客户端连接", 2, clientFields),
		"filters":        platformio.NativeSchemaSection("filters", "消息过滤", 3, filterFields),
		"notice":         platformio.NativeSchemaSection("notice", "通知事件", 4, noticeFields),
		"debug":          platformio.NativeSchemaSection("debug", "调试", 5, debugFields),
		"mute":           platformio.NativeSchemaSection("mute", "禁言工具", 6, muteFields),
	}
	return platformio.NativeConfigSchema(PluginID, map[string]any{
		"name":        "OneBot 适配器",
		"version":     "1.3.1",
		"description": "QQ OneBot v11 适配器，支持通用标准模式和特殊适配",
		"author":      "MaiBot Team",
	}, sections)
}

func boolField(name string, section map[string]any, label, description string, order int) map[string]any {
	return platformio.NativeSchemaField(name, "boolean", section[name], platformio.NativeFieldOpts{
		Label: label, Description: description, Order: order,
	})
}
