# Host 能力（cap.call）

对齐 MaiBot `src/plugin_runtime/capabilities/registry.py` 的全表 105 项能力；Host 在
`RegisterHostCapabilities` 中全部注册，未注册的能力仍按 `capability_service` 语义返回
`E_METHOD_NOT_ALLOWED`。

## 已实现（按 Python mixin 分组）

| 分组 | 能力 | 说明 |
| --- | --- | --- |
| webui | `webui.claim_upload` | 认领 WebUI 临时上传并移动到插件数据目录 |
| internal_chat | `internal_chat.identity` / `internal_chat.send` | 内部统一聊天平台；发送走 `internalchat.Relay` |
| personality | `personality.list` / `get_current` / `switch` | 实例 TOML 人设库；`switch` 保注释写回并热重载 |
| desktop_pet | `desktop_pet.*`（12 项） | 由 `internal/desktop_pet` 外部注册，不在本包 |
| files | `files.list` / `read_text` / `download` / `upload` / `send` | 需 `file_scope` 插件凭证 |
| send | `send.text` / `emoji` / `image` / `forward` / `hybrid` / `command` / `custom` | 走 `PluginSendPort`；`forward` 经 Platform IO 合并转发节点出站，`command`/`custom` 返回明确未支持错误 |
| llm | `llm.generate` / `generate_with_tools` / `embed` / `transcribe_audio` / `get_available_models` | 任务/模型路由与 `_resolve_llm_capability_route` 对齐 |
| config | `config.get` / `config.get_plugin` / `config.get_all` | 点分路径读 runtime/bot 配置；插件配置走组件查询 |
| database | `database.query` / `save` / `get` / `delete` / `count` | 经 `PluginDatabasePort` 按模型名反射；delete 必须带 filters |
| chat | `chat.get_all_streams` / `get_avatar` / `get_group_streams` / `get_private_streams` / `open_session` / `get_stream_by_group_id` / `get_stream_by_user_id` | 序列化字段对齐 `chat.*`；`get_avatar` 经 `Sessions.SetAvatarService` / `AvatarService`，未注入时返回明确未接线错误 |
| message | `message.get_by_time` / `get_by_time_in_chat` / `get_by_id` / `get_recent` / `count_new` / `build_readable` | 数据来自 `message_logs` |
| maisaka | `maisaka.context.append` / `context.resolve_image` / `proactive.trigger` / `reply.generate` | `reply.generate` 走 Agent 长文生成端口；`context.append`/`resolve_image` 走 Agent 上下文端口；`proactive.trigger` 已接线 EnqueueProactiveTask / RunQueuedTurn |
| person | `person.get_id` / `get_value` / `get_id_by_name` | 走 `PluginPersonPort` |
| emoji | `emoji.get_by_description` / `get_random` / `get_count` / `get_emotions` / `get_all` / `get_info` / `register` / `delete` | 读取走 `emojisystem`；写操作走 `PluginEmojiPort` |
| frequency | `frequency.get_current_talk_value` / `set_adjust` / `get_adjust` | 走 `PluginFrequencyPort` |
| tool | `tool.get_definitions` | 返回当前插件可用 LLM 工具定义 |
| component | `component.get_all_plugins` / `get_plugin_info` / `get_plugin_config_schema` / `update_plugin_config` / `list_loaded_plugins` / `list_registered_plugins` / `enable` / `disable` / `load_plugin` / `unload_plugin` / `reload_plugin` | `update_plugin_config` 仅 `builtin.plugin-management` 可调；unload 与 Python 一样提示使用 reload |
| api | `api.call` / `get` / `list` / `replace_dynamic` | `replace_dynamic` 永远放行 |
| knowledge | `knowledge.search` | 走 `PluginKnowledgePort` |
| market | `market.search` / `get_detail` / `get_readme` | 只读 |
| web | `web.search` / `web.fetch` | 走 `PluginWebPort` |
| statistics | `statistics.local.models` / `model_trend` / `token_trend` / `token_distribution` / `message_trend` / `tool_trend` / `online_time_trend` | 走 `PluginStatisticsPort` |
| render | `render.html2png` | 走 `PluginRenderPort` |

## 端口接线

`HostCapabilityDeps` 的 `Sender/LLM/Database/Sessions/Persons/EmojiAdmin/Frequency/Knowledge/Market/Web/Statistics/Render/Maisaka/Components/Uploads/Gateway/PluginDataDir` 由主程序注入（当前经 `internal/plugin_runtime/hostports`）。端口为 nil 时能力仍然注册，但返回 `success=false` 的「<能力名> 端口未接线：主程序尚未注入对应服务」，不会伪造成功。`Maisaka` 端口由 Agent 在 `SetPluginHost` 注入长文生成与上下文运行时；`Gateway` 入站处理器同样由 Agent 注册。

`component.load_plugin` 与 `component.reload_plugin` 在未注入 `Components` 端口时回退到 Host 自身的 `plugin.reload` 通道，使 `/pm plugin load|reload` 仍可执行；`component.update_plugin_config` 在未注入端口时回退到 Host 内置的插件 `config.toml` 读写与校验。

`send.forward` 经 `platformio.ForwardNodesFromPluginMessages` 转成 `SegmentForward`，由 Platform IO 选择 onebot/maim 合并转发出站；空节点或非法消息段返回真实错误，不降级为多条文本。

## Supervisor

`Host` 维护每个 RunnerGroup 的阶段状态（`starting`/`waiting_ready`/`running`/`stopping`/`restarting`/`failed`），`StartRunner` 在 Python 子进程模式下进入 `waiting_ready`，`runner.ready` 到达后转 `running`；`WaitForRunnerReady` 支持启动超时。`StartRunnerGroups` 按内置、第三方顺序启动，失败或就绪超时逆序回滚；`StopRunnerGroupsReverse` 逆序关停。`DefaultRunnerGroupPlan` 给内置组传入 trusted 目录与 `trusted_or_adapter` 过滤，第三方组使用 `not_adapter`。`RestartRunner` 受 `SetMaxRestartAttempts` 限制，重启成功后按已注册但未加载的插件调用 `plugin.recover`，并清零重启计数。

## 插件组件

六类组件均可注册、查询与执行：ACTION（转 legacy TOOL，`Host.ExecuteAction`）、TOOL（`plugin.invoke_tool`）、COMMAND（`plugin.invoke_command`）、MESSAGE_GATEWAY（`host.route_message` / `host.update_message_gateway_state`）、HOME_CARD（`Host.HomeCards`）、REPLY_EXTENSION（`plugin.invoke_reply_extension`）。HOOK_HANDLER/EVENT_HANDLER 由 Hook 注册表与事件分发处理，API 由 API 注册表处理。

LLM Provider 由 `LLMProviderRegistry` 管理并按插件替换/清理，冲突与重复声明返回与 Python 一致的错误；插件 Provider 同步到主 LLM 解析器。`plugin.register_components` 返回 `registered_components/registered_apis/llm_providers/message_gateways`，`plugin.unregister_plugin` 返回 `removed_apis/removed_llm_providers`。

## 插件工具执行（LLM tool）

`Host.ExecuteTool` 对齐 `component_query.invoke_tool_as_tool`：按短名查找当前会话可用的 TOOL，校验 `platforms`，构造 `stream_id`/`chat_id`/`group_id`/`user_id`/`platform` 上下文（`plugin.invoke_action` 额外写 `action_data`），调用 Runner 的 `plugin.invoke_tool`，并解析 dict、`[success, message]`、`content_items` 等返回形状。

文件能力开启且会话非空时，`Host` 会通过 `FileScopeIssuer`（由 `filesvc.Service` 实现）签发 1 小时 `file_scope`，写入 payload 的 `_file_scope`，执行结束 defer `Revoke`。Agent 的 `pluginTool` 用真实 `chat_sessions.session_id` 作为 scope 会话，因此 `files.*` 能按聊天流隔离。

Agent 只暴露 Host 中 `enabled`、非 legacy ACTION、非 `visibility=hidden` 的 TOOL，与内置工具重名时跳过；按 `visibility`/`core_tool` 将工具标记为 deferred，经 `tool_search` 发现后由 `BeforeModelRewriteState` 恢复。

## 插件命令执行（聊天命令）

`Host.ExecuteCommand` 对齐 `component_query._build_command_executor`：按短名匹配 COMMAND 后，向 Runner 发送 `plugin.invoke_command`，参数为 `text`、`stream_id`、`group_id`、`platform`、`user_id`、`is_local_operator`、`matched_groups`，并附 `message`（不含二进制）与可用时的 `plugin_config`。

返回值解析 `payload.success` 与 `payload.result`：`[success, response, intercept]` 取 response 与 intercept，其他值作为文本；RPC 异常按失败、拦截等级 1 处理。Agent 在 `chat.receive.after_process` 后调用，先检查聊天级禁用命令、`plugin.disabled_commands` 与 `permission=operator` 白名单，再触发 `chat.command.before_execute` / `chat.command.after_execute`，`continue_process=false` 时落库并跳过思考链。

`/pm` 插件管理命令优先走 Python 侧内置插件 `src/plugins/built_in/plugin_management`。Runner 未显式传入插件目录时，`StartRunnerOptionsFromSpawnConfig` 会按 Python 语义自动发现内置目录 `src/plugins/built_in`、实例 `plugins` 目录与 `MAIBOT_PLUGIN_DIRS`；Host 提供 `DefaultRunnerGroupPlan` 的内置/第三方双 Supervisor 计划（内置组 trusted 目录、第三方组独立），main 尚未切换时仍可由单个 third_party Runner 同时承载两类插件。Python 插件未加载时，Host 在 `NewHost` 注册本地 COMMAND 组件 `builtin.plugin-management.management` 兜底，支持 `/pm help`、`/pm plugin list|list_enabled|load|unload|reload`、`/pm component list|enable|disable`、`/pm config get|set`、`/pm alias list|add|remove` 与 `/pm cmd|command` 目录；若 Python 插件随后注册同名组件，会按既有替换语义覆盖本地兜底。本地别名持久化到插件 `config.toml`（保注释 merge），`/pm cmd pic` 读取 `webui.webui_style` 后经 `render.html2png` 生成目录图片，单图走 `send.image`、多图走 `send.forward`；事件处理器的 `local` 启停按会话生效。

## 核心抽象

`internal/core/eventbus` 移植 `src/core/event_bus.py`：预注册事件类型、按权重降序的拦截链、异步非拦截处理器、消息深拷贝、panic 隔离、`CancelHandlerTasks`，并通过 `IPCBridge` 在继续执行时桥接插件运行时。`internal/core/tooling` 移植 `src/core/tooling.py` 的统一工具声明、内容项、执行结果与 `ToolRegistry` 重名分配规则。两者已接入插件 Hook、命令与工具分发：`DispatchEvent` 经 `eventbus.Emit`，`ExecuteTool` 经 `ToolRegistry.Invoke`，未配置时回退直连。

## RPC 响应形状

`cap.call` 成功时 Envelope payload 为 `{ "success": true, "result": <能力处理器返回值> }`；各能力处理器返回值对齐 Python mixin（通常内含 `"success": true` 与 `value`/`messages`/`result` 字段）。

## 依赖流水线

`internal/plugin_runtime/dependency_pipeline.go` 移植 `src/plugin_runtime/dependency_pipeline.py`：扫描插件 `_manifest.json`（跳过明确禁用插件与保留目录），按 PEP 440 解析版本约束，检测主程序约束冲突与插件间冲突，合并缺失依赖并用 `uv pip install` 或 `python -m pip install` 补装。安装按 `[tool.uv].index` 顺序逐源尝试，附带 `constraint-dependencies` 与当前环境全量版本约束文件；失败只记录并让插件继续尝试加载。`Host.SyncPluginDependencies` 把最终阻止列表写回 Host 并返回变化插件。

## Docker 布局迁移

`docker_layout_migration.go` 移植 `src/plugin_runtime/docker_layout_migration.py`：扫描源码目录中的完整插件（`plugin.py` 与 `_manifest.json`），按 manifest ID 分组，把误放在插件源码目录的非源码数据目录移动到独立数据根；旧/新目录都非空、符号链接、源码目录同名等情况写入冲突或歧义报告，报告原子写入并要求人工确认。