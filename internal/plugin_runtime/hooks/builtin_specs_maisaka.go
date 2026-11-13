package hooks

// maisakaHookSpecs 对齐 MaiBot src/maisaka/chat_loop_service.py register_maisaka_hook_specs
func maisakaHookSpecs() []HookSpec {
	return []HookSpec{
		{
			Name:        "maisaka.planner.before_request",
			Description: "在 Maisaka 向模型发起规划请求前触发，可改写 Context Items 与工具定义。",
			ParametersSchema: hookSchema(map[string]any{
				"items": map[string]any{
					"type":        "array",
					"description": "即将发给模型的 Context Item 列表；不包含 replay payload。",
				},
				"item_schema_version": map[string]any{
					"type":        "integer",
					"description": "Context Item Hook 载荷版本。",
				},
				"tool_definitions":       map[string]any{"type": "array", "description": "当前候选工具定义列表。"},
				"selected_history_count": map[string]any{"type": "integer", "description": "当前选中的上下文消息数量。"},
				"built_message_count":    map[string]any{"type": "integer", "description": "实际发送给模型的消息数量。"},
				"selection_reason":       map[string]any{"type": "string", "description": "上下文选择说明。"},
				"session_id":             map[string]any{"type": "string", "description": "当前会话 ID。"},
			}, "items", "item_schema_version", "tool_definitions", "selected_history_count", "built_message_count", "selection_reason", "session_id"),
			DefaultTimeoutMS: 6000,
			DenyAbort:        true,
		},
		{
			Name:        "maisaka.planner.after_response",
			Description: "在 Maisaka 收到模型响应后触发，可按 Item 调整输出。",
			ParametersSchema: hookSchema(map[string]any{
				"output_items": map[string]any{
					"type":        "array",
					"description": "模型返回的 Context Output Items；不包含 replay payload。",
				},
				"item_schema_version": map[string]any{
					"type":        "integer",
					"description": "Context Item Hook 载荷版本。",
				},
				"selected_history_count": map[string]any{"type": "integer", "description": "当前选中的上下文消息数量。"},
				"built_message_count":    map[string]any{"type": "integer", "description": "实际发送给模型的消息数量。"},
				"selection_reason":       map[string]any{"type": "string", "description": "上下文选择说明。"},
				"session_id":             map[string]any{"type": "string", "description": "当前会话 ID。"},
				"prompt_tokens":          map[string]any{"type": "integer", "description": "输入 Token 数。"},
				"completion_tokens":      map[string]any{"type": "integer", "description": "输出 Token 数。"},
				"total_tokens":           map[string]any{"type": "integer", "description": "总 Token 数。"},
			}, "output_items", "item_schema_version", "selected_history_count", "built_message_count", "selection_reason", "session_id", "prompt_tokens", "completion_tokens", "total_tokens"),
			DefaultTimeoutMS: 6000,
			DenyAbort:        true,
		},
		{
			Name:        "maisaka.replyer.before_request",
			Description: "在 Maisaka replyer 向模型发起请求前触发，可读取或改写本次 reply 工具透传参数。",
			ParametersSchema: hookSchema(map[string]any{
				"session_id":   map[string]any{"type": "string", "description": "当前会话 ID。"},
				"request_type": map[string]any{"type": "string", "description": "当前 replyer 请求类型。"},
				"task_name": map[string]any{
					"type":        "string",
					"description": "本次 replyer 请求使用的模型任务名；Hook 可改写该值。",
				},
				"model_name": map[string]any{
					"type":        "string",
					"description": "本次 replyer 请求指定使用的具体模型名；留空时按任务策略选择。",
				},
				"extra_prompt": map[string]any{
					"type":        "string",
					"description": "Hook 可追加到本次 replyer 提示词中的额外回复要求。",
				},
				"attempt":     map[string]any{"type": "integer", "description": "当前生成尝试序号，从 1 开始。"},
				"retry_count": map[string]any{"type": "integer", "description": "当前已经重新生成的次数。"},
				"max_retries": map[string]any{
					"type":        "integer",
					"description": "本轮 replyer 最多允许重新生成多少次。",
				},
				"reply_message_id":        map[string]any{"type": "string", "description": "被回复消息 ID；无目标消息时为空字符串。"},
				"reply_reason":            map[string]any{"type": "string", "description": "本次 replyer 生成的回复理由。"},
				"selected_expression_ids": map[string]any{"type": "array", "description": "本次 replyer 选中的表达方式编号列表。"},
				"reply_tool_args": map[string]any{
					"type":        "object",
					"description": "reply 工具透传给 replyer 的额外参数，不含目标消息 ID 等内部参数。",
				},
			}, "session_id", "request_type", "task_name", "model_name", "extra_prompt", "attempt", "retry_count", "max_retries", "reply_message_id", "reply_reason", "selected_expression_ids", "reply_tool_args"),
			DefaultTimeoutMS: 6000,
			DenyAbort:        true,
		},
		{
			Name:        "maisaka.replyer.before_model_request",
			Description: "在 Maisaka replyer 构造完本次模型请求后触发，可改写实际发送的 Context Items。",
			ParametersSchema: hookSchema(map[string]any{
				"items": map[string]any{
					"type":        "array",
					"description": "即将发给模型的 Context Items；不包含 replay payload。",
				},
				"item_schema_version": map[string]any{
					"type":        "integer",
					"description": "Context Item Hook 载荷版本。",
				},
				"session_id":   map[string]any{"type": "string", "description": "当前会话 ID。"},
				"request_type": map[string]any{"type": "string", "description": "当前 replyer 请求类型。"},
				"task_name":    map[string]any{"type": "string", "description": "本次 replyer 实际使用的模型任务名。"},
				"requested_model_name": map[string]any{
					"type":        "string",
					"description": "before_request Hook 请求指定的具体模型名；留空表示按任务策略选择。",
				},
				"selected_model_name": map[string]any{
					"type":        "string",
					"description": "当前尝试实际选中的模型名；未进入具体模型尝试时为空字符串。",
				},
				"selected_model_visual": map[string]any{
					"type":        "boolean",
					"description": "当前尝试选中的模型是否启用 visual 能力。",
				},
				"attempt":     map[string]any{"type": "integer", "description": "当前生成尝试序号，从 1 开始。"},
				"retry_count": map[string]any{"type": "integer", "description": "当前已经重新生成的次数。"},
				"max_retries": map[string]any{
					"type":        "integer",
					"description": "本轮 replyer 最多允许重新生成多少次。",
				},
				"reply_message_id":        map[string]any{"type": "string", "description": "被回复消息 ID；无目标消息时为空字符串。"},
				"reply_reason":            map[string]any{"type": "string", "description": "本次 replyer 生成的回复理由。"},
				"selected_expression_ids": map[string]any{"type": "array", "description": "本次 replyer 选中的表达方式编号列表。"},
				"reply_tool_args": map[string]any{
					"type":        "object",
					"description": "reply 工具透传给 replyer 的额外参数，不含目标消息 ID 等内部参数。",
				},
			}, "items", "item_schema_version", "session_id", "request_type", "task_name", "requested_model_name", "selected_model_name", "selected_model_visual", "attempt", "retry_count", "max_retries", "reply_message_id", "reply_reason", "selected_expression_ids", "reply_tool_args"),
			DefaultTimeoutMS: 6000,
			DenyAbort:        true,
		},
		{
			Name:        "maisaka.replyer.after_response",
			Description: "在 Maisaka replyer 收到模型响应后触发，可要求重新生成或改写回复文本。",
			ParametersSchema: hookSchema(map[string]any{
				"response": map[string]any{
					"type":        "string",
					"description": "replyer 可见正文的兼容只读投影；改写时仅替换正文 Item。",
				},
				"output_items": map[string]any{
					"type":        "array",
					"description": "replyer 模型返回的 Context Output Items；不包含 replay payload。",
				},
				"item_schema_version": map[string]any{
					"type":        "integer",
					"description": "Context Item Hook 载荷版本。",
				},
				"session_id":   map[string]any{"type": "string", "description": "当前会话 ID。"},
				"request_type": map[string]any{"type": "string", "description": "当前 replyer 请求类型。"},
				"task_name":    map[string]any{"type": "string", "description": "本次 replyer 实际使用的模型任务名。"},
				"requested_model_name": map[string]any{
					"type":        "string",
					"description": "Hook 请求指定的具体模型名；留空表示按任务策略选择。",
				},
				"attempt":     map[string]any{"type": "integer", "description": "当前生成尝试序号，从 1 开始。"},
				"retry_count": map[string]any{"type": "integer", "description": "当前已经重新生成的次数。"},
				"max_retries": map[string]any{
					"type":        "integer",
					"description": "本轮 replyer 最多允许重新生成多少次。",
				},
				"reply_message_id":        map[string]any{"type": "string", "description": "被回复消息 ID；无目标消息时为空字符串。"},
				"selected_expression_ids": map[string]any{"type": "array", "description": "本次 replyer 选中的表达方式编号列表。"},
				"reply_tool_args": map[string]any{
					"type":        "object",
					"description": "reply 工具透传给 replyer 的额外参数，不含目标消息 ID 等内部参数。",
				},
				"prompt_tokens":     map[string]any{"type": "integer", "description": "输入 Token 数。"},
				"completion_tokens": map[string]any{"type": "integer", "description": "输出 Token 数。"},
				"total_tokens":      map[string]any{"type": "integer", "description": "总 Token 数。"},
				"retry": map[string]any{
					"type":        "boolean",
					"description": "Hook 处理器可置为 true，要求 replyer 重新生成。",
				},
				"retry_reason": map[string]any{
					"type":        "string",
					"description": "可选的重新生成约束原因；留空时只重新生成，不追加下一轮 replyer 提示词。",
				},
				"matched_regex":             map[string]any{"type": "string", "description": "触发重新生成的正则或规则名称。"},
				"matched_regex_pattern":     map[string]any{"type": "string", "description": "触发重新生成的正则文本。"},
				"matched_regex_description": map[string]any{"type": "string", "description": "触发重新生成的规则说明。"},
			}, "response", "output_items", "item_schema_version", "session_id", "request_type", "task_name", "requested_model_name", "attempt", "retry_count", "max_retries", "reply_message_id", "selected_expression_ids", "reply_tool_args", "prompt_tokens", "completion_tokens", "total_tokens"),
			DefaultTimeoutMS: 6000,
			DenyAbort:        true,
		},
		{
			Name:        "maisaka.reply.before_post_process",
			Description: "在 Maisaka 对最终可见回复执行文本后处理前触发，可按本次回复调整后处理策略。",
			ParametersSchema: hookSchema(map[string]any{
				"response":            map[string]any{"type": "string", "description": "即将执行文本后处理的最终回复正文。"},
				"session_id":          map[string]any{"type": "string", "description": "当前会话 ID。"},
				"reply_message_id":    map[string]any{"type": "string", "description": "被回复消息 ID。"},
				"reply_tool_args":     map[string]any{"type": "object", "description": "本次 reply 工具除内部参数外的透传参数。"},
				"skip_post_process":   map[string]any{"type": "boolean", "description": "是否跳过本次回复的全部文本后处理。"},
				"enable_splitter":     map[string]any{"type": "boolean", "description": "本次回复是否允许按全局配置进行文本拆分。"},
				"enable_chinese_typo": map[string]any{"type": "boolean", "description": "本次回复是否允许按全局配置注入中文错别字。"},
			}, "response", "session_id", "reply_message_id", "reply_tool_args", "skip_post_process", "enable_splitter", "enable_chinese_typo"),
			DefaultTimeoutMS: 6000,
			DenyAbort:        true,
		},
	}
}
