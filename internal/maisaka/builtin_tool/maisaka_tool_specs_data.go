package tools

const maisakaToolSpecsJSON = `
{
  "query_image_memory": {
    "description": "查询当前聊天中关于某张图片的记忆。",
    "name": "query_image_memory",
    "parameters_schema": {
      "properties": {
        "component_path": {
          "description": "消息内图片组件路径。",
          "type": "string"
        },
        "limit": {
          "default": 8,
          "maximum": 20,
          "minimum": 1,
          "type": "integer"
        },
        "message_id": {
          "description": "包含目标图片的消息 ID。",
          "type": "string"
        }
      },
      "required": [
        "message_id",
        "component_path"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "query_person_profile": {
    "description": "查询人物画像。当需要查询与某人的关系时使用。",
    "name": "query_person_profile",
    "parameters_schema": {
      "properties": {
        "limit": {
          "default": 8,
          "description": "证据上限。",
          "type": "integer"
        },
        "person_id": {
          "description": "内部人物ID；明确给出时填。",
          "type": "string"
        },
        "person_name": {
          "description": "名称/昵称/关键词；通常填这个。",
          "type": "string"
        }
      },
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "read_tool_result": {
    "description": "取回某次工具调用保存的原始结果。联网工具预览或超长结果的整理结论会附有编号（如 r3）；当你需要其中没有的细节时，用这个编号分段读取原文，或用 keyword 直接查找相关片段。已有内容够用时不需要调用。",
    "name": "read_tool_result",
    "parameters_schema": {
      "properties": {
        "keyword": {
          "description": "可选。只返回原文中包含该关键词的片段及其前后文；不填则按 offset 顺序读取。",
          "type": "string"
        },
        "max_chars": {
          "description": "可选。本次最多读取多少字，默认 2000。",
          "maximum": 2800,
          "minimum": 1,
          "type": "integer"
        },
        "offset": {
          "description": "可选。从原文第几个字开始读，默认 0；上一次读取会告诉你下一段的 offset。",
          "minimum": 0,
          "type": "integer"
        },
        "result_id": {
          "description": "工具返回的原始结果编号，例如 r3。",
          "type": "string"
        }
      },
      "required": [
        "result_id"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "reply": {
    "description": "根据当前思考生成并发送可见回复；可以使用参数附加回复内容。",
    "name": "reply",
    "parameters_schema": {
      "properties": {
        "after_reply": {
          "default": "wait",
          "description": "回复发送成功后的行为：continue 继续思考和调用工具；wait（默认）暂停，未填 wait_seconds 则结束本轮，填写 wait_seconds 则定时再判断。wait 会取消同批后续工具。",
          "enum": [
            "continue",
            "wait"
          ],
          "type": "string"
        },
        "attach_emoji": {
          "anyOf": [
            {
              "additionalProperties": false,
              "description": "只填一项就按该项匹配，两项都填就合并分数。",
              "minProperties": 1,
              "properties": {
                "content": {
                  "description": "期望图中出现的人物、物体、动作或文字。",
                  "minLength": 1,
                  "type": "string"
                },
                "emotion": {
                  "description": "想表达的情绪或语气。",
                  "minLength": 1,
                  "type": "string"
                }
              },
              "type": "object"
            },
            {
              "description": "想表达的情绪或意图，例如轻松地自嘲。",
              "minLength": 1,
              "type": "string"
            },
            {
              "additionalProperties": false,
              "properties": {
                "index": {
                  "description": "该消息中第几张表情包，从 0 开始。",
                  "minimum": 0,
                  "type": "integer"
                },
                "msg_id": {
                  "description": "含表情包的上下文消息编号。",
                  "minLength": 1,
                  "type": "string"
                }
              },
              "required": [
                "msg_id"
              ],
              "type": "object"
            }
          ],
          "description": "可选。在文字后发送一张表情包。可引用上下文表情，或按情绪、画面内容匹配。省略表示不发送表情包。"
        },
        "attach_pic": {
          "default": [],
          "description": "可选。随本次回复附加一张或多张上下文图片。每项使用 msg_id + index，或使用 media_index=tool_result:<call_id>:<item_index> 指向工具返回媒体。",
          "items": {
            "properties": {
              "index": {
                "default": 0,
                "description": "同一消息中的图片序号，从 0 开始。",
                "type": "integer"
              },
              "media_index": {
                "default": "",
                "description": "工具返回媒体索引，例如 tool_result:call_x:1；与 msg_id 二选一。",
                "type": "string"
              },
              "msg_id": {
                "default": "",
                "description": "图片所在的消息编号。",
                "type": "string"
              }
            },
            "type": "object"
          },
          "type": "array"
        },
        "expression_intent": {
          "description": "可选。给 replyer 表达方式选择使用的结构化意图，不是回复正文。当这次回复需要特定语气、场景或话术时填写，避免表达选择只按关键词匹配。",
          "properties": {
            "avoid": {
              "description": "需要避免的表达类型、误判方向或不该注入的具体结论。",
              "items": {
                "type": "string"
              },
              "type": "array"
            },
            "focus": {
              "description": "本次表达主要应该贴合的目标消息、片段或话题。",
              "type": "string"
            },
            "prefer": {
              "description": "优先考虑的表达类型或话术倾向。",
              "items": {
                "type": "string"
              },
              "type": "array"
            },
            "reply_act": {
              "description": "这次回复要完成的动作，例如澄清、安抚、调侃、追问、解释边界。",
              "type": "string"
            },
            "scene": {
              "description": "当前表达场景，例如技术排查、截图分享、撒娇玩笑、价格询问。",
              "type": "string"
            },
            "tone": {
              "description": "期望语气，例如轻松、可靠、吐槽、委婉、简短肯定。",
              "type": "string"
            }
          },
          "type": "object"
        },
        "msg_id": {
          "description": "要回复的消息msg_id。",
          "type": "string"
        },
        "reply_reference": {
          "description": "提供本次回复的上下文关键信息与回复方向，包括当前聊天状态、人物关系、事实信息、回忆信息；不是回复正文。",
          "minLength": 1,
          "type": "string"
        },
        "reply_style": {
          "description": "可选。控制本次回复的篇幅和表达方式",
          "enum": [
            "简短表达",
            "正常回复",
            "长回复"
          ],
          "type": "string"
        },
        "wait_seconds": {
          "description": "可选。after_reply=wait 时最多等待的秒数，到期后即使没有新消息也会继续思考。不填表示结束本轮。",
          "minimum": 0,
          "type": "integer"
        }
      },
      "required": [
        "msg_id",
        "reply_reference"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "schedule": {
    "description": "创建，查询或者修改日程，日程可以在定时唤起提醒，当需要在特定时间点做事时使用。新增必须填写 text 和未来时间：delay_seconds 或 ISO 8601 due_at 二选一。修改前先查询真实 todo_id；update 省略时间保留计划。创建日程只登记任务。",
    "name": "schedule",
    "parameters_schema": {
      "additionalProperties": false,
      "not": {
        "required": [
          "delay_seconds",
          "due_at"
        ]
      },
      "properties": {
        "action": {
          "enum": [
            "add",
            "list",
            "update",
            "cancel",
            "complete"
          ],
          "type": "string"
        },
        "delay_seconds": {
          "description": "从现在起多少秒后唤醒",
          "exclusiveMinimum": 0,
          "type": "number"
        },
        "due_at": {
          "description": "未来 ISO 8601 时间，建议携带时区，如 2026-11-14T09:00:00+08:00",
          "type": "string"
        },
        "include_completed": {
          "description": "list 时包含已完成或取消的历史，默认 false",
          "type": "boolean"
        },
        "text": {
          "description": "到期要执行的任务",
          "maxLength": 200,
          "minLength": 1,
          "type": "string"
        },
        "todo_id": {
          "description": "查询返回的真实日程 id",
          "minLength": 1,
          "type": "string"
        }
      },
      "required": [
        "action"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "subagent": {
    "description": "把一个自包含的查询或整理任务交给子代理。子代理在独立上下文里工作，可以自己多次调用你授权的工具，子代理在后台运行：调用后立刻返回任务 id，你可以继续处理聊天，不需要为它等待；它结束时结论会作为一条 <subagent_result> 消息送回给你，中间的原始结果不会进入你的上下文。需要时用 subagent_status 查看进度，用 subagent_stop 终止不再需要的任务。适合需要多次查询、交叉比对，或会返回大量原始内容的任务；只需要一次简单查询时直接调用对应工具即可。子代理看不到当前聊天，也不能发言：发言、等待和群管理操作必须由你自己完成。",
    "name": "subagent",
    "parameters_schema": {
      "properties": {
        "task": {
          "description": "完整、自包含的任务说明。子代理看不到聊天记录，要写清楚要查什么、相关的名称或关键词、以及你希望得到什么形式的结论。",
          "minLength": 1,
          "type": "string"
        },
        "tools": {
          "description": "授权子代理使用的工具名列表，只填查询、检索、读取类工具；可以包含 deferred tools 列表里的工具，不需要先 tool_search。",
          "items": {
            "type": "string"
          },
          "minItems": 1,
          "type": "array"
        }
      },
      "required": [
        "task",
        "tools"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "subagent_status": {
    "description": "查看后台子代理任务的状态。不填 task_id 时列出全部任务；填了则给出该任务的详情，已结束的任务会附上结论。子代理结束时结论会自动送回，一般不需要主动查询。",
    "name": "subagent_status",
    "parameters_schema": {
      "properties": {
        "task_id": {
          "description": "要查看的任务 id，例如 subagent-1；不填则列出全部任务。",
          "type": "string"
        }
      },
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "subagent_stop": {
    "description": "终止一个仍在运行的后台子代理任务。被终止的任务不会再送回结论。",
    "name": "subagent_stop",
    "parameters_schema": {
      "properties": {
        "task_id": {
          "description": "要终止的任务 id，例如 subagent-1。",
          "minLength": 1,
          "type": "string"
        }
      },
      "required": [
        "task_id"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "tool_search": {
    "description": "在 deferred tools 列表中按名称或关键词搜索工具，并将命中的工具加入后续轮次的可用工具列表。",
    "name": "tool_search",
    "parameters_schema": {
      "properties": {
        "limit": {
          "description": "最多返回多少个工具。",
          "minimum": 1,
          "type": "integer"
        },
        "query": {
          "description": "要搜索的工具名、前缀或关键词。",
          "type": "string"
        }
      },
      "required": [
        "query"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "view_forward_message": {
    "description": "根据 msg_id 逐层查看合并转发消息。首次调用只展开顶层；遇到嵌套转发时，使用返回内容中的 path 再次调用以继续展开。",
    "name": "view_forward_message",
    "parameters_schema": {
      "properties": {
        "msg_id": {
          "description": "转发消息的 msg_id。",
          "type": "string"
        },
        "path": {
          "description": "可选的嵌套转发路径。首次查看时省略，后续使用工具返回内容中的 path 逐层展开。",
          "items": {
            "minimum": 0,
            "type": "integer"
          },
          "type": "array"
        }
      },
      "required": [
        "msg_id"
      ],
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  },
  "wait": {
    "description": "暂停当前思考，不发送消息。不填 seconds 表示结束本轮，等待新消息；填写 seconds 表示定时等待，到期后即使没有新消息也会继续思考。定时等待可能被新消息提前唤醒。",
    "name": "wait",
    "parameters_schema": {
      "additionalProperties": false,
      "properties": {
        "seconds": {
          "description": "等待秒数。",
          "minimum": 0,
          "type": "integer"
        }
      },
      "type": "object"
    },
    "provider_name": "maisaka_builtin",
    "provider_type": "builtin"
  }
}
`
