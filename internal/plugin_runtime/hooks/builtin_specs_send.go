package hooks

// sendServiceHookSpecs 对齐 MaiBot src/services/send_service.py register_send_service_hook_specs
func sendServiceHookSpecs() []HookSpec {
	return []HookSpec{
		{
			Name:        "send_service.after_build_message",
			Description: "在出站 SessionMessage 构建完成后触发，可改写消息体或取消发送。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "待发送消息的序列化 SessionMessage。",
				},
				"stream_id":            map[string]any{"type": "string", "description": "目标会话 ID。"},
				"processed_plain_text": map[string]any{"type": "string", "description": "可选的预处理纯文本内容。"},
				"typing":               map[string]any{"type": "boolean", "description": "是否模拟打字。"},
				"set_reply":            map[string]any{"type": "boolean", "description": "是否附带引用回复。"},
				"storage_message":      map[string]any{"type": "boolean", "description": "发送成功后是否写库。"},
				"show_log":             map[string]any{"type": "boolean", "description": "是否输出发送日志。"},
			}, "message", "stream_id", "processed_plain_text", "typing", "set_reply", "storage_message", "show_log"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "send_service.before_send",
			Description: "在真正调用 Platform IO 发送前触发，可改写消息或取消本次发送。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "待发送消息的序列化 SessionMessage。",
				},
				"typing":           map[string]any{"type": "boolean", "description": "是否模拟打字。"},
				"set_reply":        map[string]any{"type": "boolean", "description": "是否附带引用回复。"},
				"reply_message_id": map[string]any{"type": "string", "description": "被引用消息 ID。"},
				"storage_message":  map[string]any{"type": "boolean", "description": "发送成功后是否写库。"},
				"show_log":         map[string]any{"type": "boolean", "description": "是否输出发送日志。"},
			}, "message", "typing", "set_reply", "storage_message", "show_log"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "send_service.after_send",
			Description: "在发送流程结束后触发，用于观察最终发送结果。",
			ParametersSchema: hookSchema(map[string]any{
				"message": map[string]any{
					"type":        "object",
					"description": "本次发送消息的序列化 SessionMessage。",
				},
				"sent":             map[string]any{"type": "boolean", "description": "本次发送是否成功。"},
				"typing":           map[string]any{"type": "boolean", "description": "是否模拟打字。"},
				"set_reply":        map[string]any{"type": "boolean", "description": "是否附带引用回复。"},
				"reply_message_id": map[string]any{"type": "string", "description": "被引用消息 ID。"},
				"storage_message":  map[string]any{"type": "boolean", "description": "发送成功后是否写库。"},
				"show_log":         map[string]any{"type": "boolean", "description": "是否输出发送日志。"},
			}, "message", "sent", "typing", "set_reply", "storage_message", "show_log"),
			DefaultTimeoutMS:   5000,
			DenyAbort:          true,
			DenyKwargsMutation: true,
		},
	}
}
