package hooks

// 本文件覆盖表情包、表达方式、黑话三类学习相关 Hook，对齐 MaiBot 各自的 register_*_hook_specs

// emojiHookSpecs 对齐 src/emoji_system/emoji_manager.py register_emoji_hook_specs
func emojiHookSpecs() []HookSpec {
	return []HookSpec{
		{
			Name:        "emoji.receive.before_cache",
			Description: "聊天表情未命中缓存且即将写入时触发，可跳过该表情的缓存和内置识别。",
			ParametersSchema: hookSchema(map[string]any{
				"emoji_hash":         map[string]any{"type": "string", "description": "表情包的 SHA-256 哈希。"},
				"binary_data_base64": map[string]any{"type": "string", "description": "原始表情包的 Base64 数据。"},
				"session_id":         map[string]any{"type": "string", "description": "来源聊天会话 ID。"},
			}, "emoji_hash", "binary_data_base64", "session_id"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "emoji.maintenance.before_register",
			Description: "定时维护尝试注册候选表情前触发，可跳过当前候选并继续扫描。",
			ParametersSchema: hookSchema(map[string]any{
				"file_name": map[string]any{"type": "string", "description": "候选表情包文件名。"},
				"file_size": map[string]any{"type": "integer", "description": "候选文件的字节数。"},
			}, "file_name", "file_size"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "emoji.register.after_build_description",
			Description: "视觉模型生成表情包描述后触发，可改写描述或中止本次描述生成。",
			ParametersSchema: hookSchema(map[string]any{
				"emoji": map[string]any{
					"type":        "object",
					"description": "当前表情包的序列化信息，主要包含 file_hash、description、emotions 等字段。",
				},
				"description":  map[string]any{"type": "string", "description": "当前生成出的表情包描述。"},
				"image_format": map[string]any{"type": "string", "description": "表情图片格式。"},
			}, "emoji", "description", "image_format"),
			DefaultTimeoutMS: 5000,
		},
	}
}

// expressionHookSpecs 对齐 src/learners/expression_learner.py register_expression_hook_specs
func expressionHookSpecs() []HookSpec {
	return []HookSpec{
		{
			Name:        "expression.select.before_select",
			Description: "表达方式选择流程开始前触发，可改写会话上下文、选择参数或中止本次选择。",
			ParametersSchema: hookSchema(map[string]any{
				"chat_id":        map[string]any{"type": "string", "description": "当前聊天流 ID。"},
				"chat_info":      map[string]any{"type": "string", "description": "用于选择表达方式的聊天上下文。"},
				"max_num":        map[string]any{"type": "integer", "description": "最大可选表达方式数量。"},
				"target_message": map[string]any{"type": "string", "description": "当前目标回复消息文本。"},
				"reply_reason":   map[string]any{"type": "string", "description": "规划器给出的回复理由。"},
				"think_level":    map[string]any{"type": "integer", "description": "表达方式选择思考级别。"},
			}, "chat_id", "chat_info", "max_num", "think_level"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "expression.select.after_selection",
			Description: "表达方式选择完成后触发，可改写最终选中的表达方式列表与 ID。",
			ParametersSchema: hookSchema(map[string]any{
				"chat_id":        map[string]any{"type": "string", "description": "当前聊天流 ID。"},
				"chat_info":      map[string]any{"type": "string", "description": "用于选择表达方式的聊天上下文。"},
				"max_num":        map[string]any{"type": "integer", "description": "最大可选表达方式数量。"},
				"target_message": map[string]any{"type": "string", "description": "当前目标回复消息文本。"},
				"reply_reason":   map[string]any{"type": "string", "description": "规划器给出的回复理由。"},
				"think_level":    map[string]any{"type": "integer", "description": "表达方式选择思考级别。"},
				"selected_expressions": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "object"},
					"description": "当前已选中的表达方式列表。",
				},
				"selected_expression_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "当前已选中的表达方式 ID 列表。",
				},
			}, "chat_id", "chat_info", "max_num", "think_level", "selected_expressions", "selected_expression_ids"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "expression.learn.after_extract",
			Description: "表达方式学习解析出表达候选后触发，可改写候选集或直接终止本轮学习。",
			ParametersSchema: hookSchema(map[string]any{
				"session_id":    map[string]any{"type": "string", "description": "当前会话 ID。"},
				"message_count": map[string]any{"type": "integer", "description": "本轮参与学习的消息数量。"},
				"expressions": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "object"},
					"description": "解析出的表达方式候选列表。",
				},
				"jargon_entries": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "object"},
					"description": "兼容字段。黑话学习已由独立 learner 处理，此处固定为空列表。",
				},
			}, "session_id", "message_count", "expressions", "jargon_entries"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "expression.learn.before_upsert",
			Description: "表达方式写入数据库前触发，可改写情景/风格文本或跳过本条写入。",
			ParametersSchema: hookSchema(map[string]any{
				"session_id": map[string]any{"type": "string", "description": "当前会话 ID。"},
				"situation":  map[string]any{"type": "string", "description": "即将写入的情景文本。"},
				"style":      map[string]any{"type": "string", "description": "即将写入的风格文本。"},
			}, "session_id", "situation", "style"),
			DefaultTimeoutMS: 5000,
		},
	}
}

// jargonHookSpecs 对齐 src/learners/jargon_miner.py register_jargon_hook_specs
func jargonHookSpecs() []HookSpec {
	return []HookSpec{
		{
			Name:        "jargon.extract.before_persist",
			Description: "黑话条目准备写入数据库前触发，可改写去重后的条目列表或跳过本次持久化。",
			ParametersSchema: hookSchema(map[string]any{
				"session_id":   map[string]any{"type": "string", "description": "当前会话 ID。"},
				"session_name": map[string]any{"type": "string", "description": "当前会话展示名称。"},
				"entries": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "object"},
					"description": "即将持久化的黑话条目列表。",
				},
			}, "session_id", "session_name", "entries"),
			DefaultTimeoutMS: 5000,
		},
		{
			Name:        "jargon.inference.before_finalize",
			Description: "黑话含义推断完成、写回数据库前触发，可改写最终判定与含义结果。",
			ParametersSchema: hookSchema(map[string]any{
				"session_id":   map[string]any{"type": "string", "description": "当前会话 ID。"},
				"session_name": map[string]any{"type": "string", "description": "当前会话展示名称。"},
				"content":      map[string]any{"type": "string", "description": "当前黑话词条。"},
				"count":        map[string]any{"type": "integer", "description": "当前词条累计命中次数。"},
				"raw_content_list": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "用于推断的原始上下文片段列表。",
				},
				"comparison_result": map[string]any{"type": "object", "description": "比较阶段输出结果。"},
				"is_jargon":         map[string]any{"type": "boolean", "description": "当前推断是否判定为黑话。"},
				"meaning":           map[string]any{"type": "string", "description": "当前推断出的黑话含义。"},
				"is_complete":       map[string]any{"type": "boolean", "description": "当前是否已完成全部推断流程。"},
				"last_inference_count": map[string]any{
					"type":        "integer",
					"description": "本次推断完成后应写回的 last_inference_count。",
				},
			}, "session_id", "session_name", "content", "count", "raw_content_list", "comparison_result", "is_jargon", "meaning", "is_complete", "last_inference_count"),
			DefaultTimeoutMS: 5000,
		},
	}
}
