package hooks

// chatHookSpecs 对齐 MaiBot src/chat/message_receive/bot.py register_chat_hook_specs
func chatHookSpecs() []HookSpec {
	return []HookSpec{
		{
			Name:        "chat.receive.before_process",
			Description: "在入站消息执行 `SessionMessage.process()` 之前触发，可拦截或改写消息。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "当前入站消息的序列化 SessionMessage。",
				},
			}, "message"),
		},
		{
			Name:        "chat.receive.after_process",
			Description: "在入站消息完成轻量预处理后触发，可改写文本、消息体或中止后续链路。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "已完成 `process()` 的序列化 SessionMessage。",
				},
			}, "message"),
		},
		{
			Name:        "chat.command.before_execute",
			Description: "在命令匹配成功、实际执行前触发，可拦截命令或改写命令上下文。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "当前命令消息的序列化 SessionMessage。",
				},
				"command_name":   map[string]any{"type": "string", "description": "命中的命令名称。"},
				"plugin_id":      map[string]any{"type": "string", "description": "命令所属插件 ID。"},
				"matched_groups": map[string]any{"type": "object", "description": "命令正则命名捕获结果。"},
			}, "message", "command_name", "plugin_id", "matched_groups"),
		},
		{
			Name:        "chat.command.after_execute",
			Description: "在命令执行结束后触发，可调整返回文本和是否继续主链处理。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "当前命令消息的序列化 SessionMessage。",
				},
				"command_name":            map[string]any{"type": "string", "description": "命令名称。"},
				"plugin_id":               map[string]any{"type": "string", "description": "命令所属插件 ID。"},
				"matched_groups":          map[string]any{"type": "object", "description": "命令正则命名捕获结果。"},
				"success":                 map[string]any{"type": "boolean", "description": "命令执行是否成功。"},
				"response":                map[string]any{"type": "string", "description": "命令返回文本。"},
				"intercept_message_level": map[string]any{"type": "integer", "description": "命令拦截等级。"},
				"continue_process":        map[string]any{"type": "boolean", "description": "命令执行后是否继续后续消息处理。"},
			}, "message", "command_name", "plugin_id", "matched_groups", "success", "intercept_message_level", "continue_process"),
			DenyAbort: true,
		},
	}
}
