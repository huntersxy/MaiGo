# MaiBot WebUI API 路由清单（MaiGo 兼容层）

Python 来源：`MaiBot/src/webui/routes.py`、`routers/__init__.py` 及各 `routers/*.py`。

## 前缀说明

| 前缀 | 说明 |
| --- | --- |
| `/api/webui` | 主 Dashboard API（`routes.py` 中 `APIRouter(prefix="/api/webui")`） |
| `/api/config` | 配置兼容层（`config.compat_router`，旧前端） |
| `/api/chat` | 本地聊天室（`chat/routes.py`，独立注册） |
| `/api/*` | 记忆兼容层（`mountMemoryCompatRoutes` 白名单挂载；**故意不挂 `/api` catch-all**，以免盖住 `/api/files`） |
| `/api/files` | 二进制传输与上传（`internal/files`，非 `/api/webui` 前缀） |

## 文件传输 `/api/files`（P2-06，`internal/files`）

| 方法 | 路径 | 鉴权 | 状态 |
| --- | --- | --- | --- |
| GET/HEAD | `/api/files/transfers/{token}` | download ticket | **done**（Range/HEAD，租约与读取并发） |
| PUT | `/api/files/transfers/{token}` | upload ticket | **done**（一次性校验后流式写入） |
| POST/PUT | `/api/files/upload` | 管理员会话 + `session_id`/`name`，或 upload token | **done**（流式 begin/append/finish） |
| POST | `/api/files/uploads`、`/api/webui/files/uploads` | 管理员会话 + `session_id`/`name` | **done**（流式上传） |
| GET | `/api/files`、`/api/webui/files` | 管理员会话 | **done**（列表 + usage） |
| GET | `/api/files/usage`、`/api/webui/files/usage` | 管理员会话 | **done** |
| GET | `/api/webui/files/chats` | 管理员会话 | **done**（chat_sessions 列表，内部聊天显示为本地聊天室，虚拟群显示群名） |
| POST | `/api/webui/files/chat-uploads` | 管理员会话 + 活动聊天 `session_id` | **done**（校验活动聊天连接，普通内部聊天与虚拟群分别转存到对应文件会话） |
| POST | `/api/webui/files/{file_ref}/send` | 管理员会话 | **done**（普通内部聊天、WebUI 虚拟群与 QQ 群聊/私聊；QQ 经 Platform IO 上传 file 段，尽力写入 message_logs） |
| GET | `/api/webui/files/{file_ref}/download` | 管理员会话 | **done**（签发相对下载 URL） |
| GET | `/api/files/{file_ref}/text`、`/api/webui/files/{file_ref}/text` | 管理员会话 | **done**（TXT/Markdown/CSV/JSON，utf-8/utf-8-sig/utf-16/gb18030） |
| DELETE | `/api/files/{file_ref}`、`/api/webui/files/{file_ref}` | 管理员会话 | **done**（`cache_only=true` 仅清理远程缓存） |

鉴权：Cookie `maibot_session` 直接持有 `data/webui.json` 的 `access_token`，`require_auth` 每次与该令牌比对。Dashboard 原版登录走 `POST /auth/verify`（body `{ "token" }`），成功后写入 `maibot_session`；`/auth/logout` 清除该 Cookie；`/auth/check` 只读探测。

## Dashboard 静态 SPA（P2-03）

| 项 | 说明 |
| --- | --- |
| 挂载路径 | 根路径 `/`（对齐 Python `serve_spa`），`/dashboard/` 仅作历史别名 |
| 目录 | `MAIBOT_DASHBOARD_DIR` 优先；否则 `MAIBOT_INSTANCE_DIR/dashboard/dist` 或工作目录下 `../MaiBot/dashboard/dist`（须含 `index.html`） |
| 构建 | 使用 PyPI `maibot-dashboard` 包自带 dist（Docker 镜像已内置），无需在宿主机构建 |
| 运行时 | 根路径返回 `index.html`；未命中的 GET 先找 dist 静态文件，找不到回落 index（`/api` 前缀除外） |
| 缺口 | 统一 WS 已接 **logs / chat / plugin_progress / maisaka_monitor / plugin_runtime** |

状态图例：

- **done** — 已有可用实现（可能 `partial`）
- **stub** — 未完成能力的 `implemented:false` 占位；未注册路径返回 `{"detail":"Not Found"}`，运行时缺依赖用 503/502
- **todo** — 未挂载

## `/api/webui` 核心

| 方法 | 路径 | MaiBot | Go 状态 |
| --- | --- | --- | --- |
| GET | `/health` | `routes.health_check` | **done** |
| GET | `/version-compatibility` | `routes.get_version_compatibility` | **done**（`version_compat.go` PEP440 子集比对 `RequiredWebUIVersion` / Baseline，oracle 对拍） |
| POST | `/auth/verify` | Token 验证 + Cookie | **done**（`access_token` → `maibot_session`，响应对齐 `TokenVerifyResponse`） |
| POST | `/auth/logout` | 清除 Cookie | **done**（清除 `maibot_session`） |
| GET | `/auth/check` | 认证状态 | **done**（`authenticated`/`token_source`/`requires_custom_token`，对齐 Dashboard `AuthStatus`） |
| POST | `/auth/update` | 更新 Token | **done**（校验 10 位/大小写/特殊符号；写入 `data/webui.json` 的 `access_token` 并清 Cookie） |
| POST | `/auth/regenerate` | 重新生成 Token | **done**（64 位 hex，`token_source=configured`，清 Cookie） |
| GET | `/setup/status` | 首次配置 | **done**（`is_first_setup`/`token_source`/`requires_custom_token`/`message`） |
| POST | `/setup/complete` | 标记首次配置完成 | **done**（写 `first_setup_completed=true` 与 `setup_completed_at`） |
| POST | `/setup/reset` | 重置首次配置 | **done**（可选 `reset_password` 生成临时 Token 并续上 Cookie，`Cache-Control: no-store`） |
| GET | `/ws-token` | WebSocket 临时 token | **done**（需 Cookie 会话；60s 一次性 token） |
| GET | `/ws/logs` | 日志 WebSocket（legacy JSON 行） | **done**（ring buffer 回放 + 实时推送；`token` query 或 `maibot_session`） |
| GET | `/ws` | 统一 WebSocket | **done**（`system.ready`、ping/pong；域 **logs** / **chat:main** / **plugin_progress:main** / **plugin_runtime:main** / **maisaka_monitor:main** 均已接；chat `session.open` 含虚拟身份与频道隔离；monitor 账本回放/`history.load`；plugin_progress snapshot+update；plugin_runtime `changed`） |

## 无尾斜杠精确别名（chi.Walk 盲区）

Python 的 `@router.get("")` / `@router.post("")` 生成的是无尾斜杠路径。Go 同时保留尾斜杠版本，并在父路由上追加精确别名，两者指向同一 handler。chi 的 `Walk` 只遍历 `Route` 子路由，不会列出父级精确 pattern，因此路由清单会少 6 条，但真实 HTTP 分发可达。

| 方法 | 精确别名 | 尾斜杠版本 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/webui/avatar` | `/api/webui/avatar/` | 头像读取，登录校验后返回 204 或图片 |
| GET | `/api/webui/news` | `/api/webui/news/` | 首页资讯 |
| GET | `/api/webui/user-emojis` | `/api/webui/user-emojis/` | 用户表情包列表 |
| POST | `/api/webui/user-emojis` | `/api/webui/user-emojis/` | 用户表情包上传 |
| POST | `/api/webui/jargon` | `/api/webui/jargon/` | 黑话创建 |
| POST | `/api/webui/expression` | `/api/webui/expression/` | 表达方式创建 |

其中 V1-20 的 4 条为 avatar、news、user-emojis GET/POST；jargon、expression 是同类隐藏别名，不属于 V1 缺失。

计数口径：chi.Walk 可见 492 条 method+path / 441 unique paths；加上 6 条精确别名后，webuiapi.Mount 实际注册 498 / 447。生产再并入 filesvc.Mount 的 9 条，合计 507 / 456。V1 矩阵中的 492/441 是 Walk 口径，不是缺失。

验证：go test ./internal/webui/routers -run 'TestWebUIExactPathAliases|TestWebUIExactPathAliasesMatchTrailingSlash' -v；前者断言别名不落 404/405/3xx，后者断言 4 条 V1 别名与尾斜杠版本的状态码、JSON 响应和 Content-Type/Cache-Control/X-Avatar-Status 一致。

## `/api/webui/config`（`routers/config.py`，prefix `/config`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET/POST | `/bot`、`/model` | **done**（TOML 读写；POST 先做配置校验，失败 400） |
| GET/POST | `/bot/raw` | **done**（bot_config.toml 原文读写；非法 TOML 400） |
| POST | `/bot/section/{section_name}`、`/model/section/{section_name}` | **done**（节合并/替换） |
| GET/POST/PATCH/DELETE | `/model/versions`、`/model/versions/{version_id}`、`.../activate` | **done**（副本目录 `config/versions/model` + manifest） |
| GET/PUT/DELETE | `/prompts`、`/prompts/{language}/{filename}`、`/default`、`/versions/*` | **done**（自定义版本在 `data/custom_prompts`，默认模板覆盖在 `data/prompts`，占位符校验） |
| GET | `/prompt-blocks/{language}` | **done**（与回复生成共用的群聊／私聊指导语） |
| GET | `/api/webui/schedules` | **done**（只读聚合真实会话日程，支持 `include_completed`） |
| GET | `/maisaka-prompt-preview` | **done**（限 `logs/maisaka_prompt` 的 html/json/txt） |
| GET/POST | `/adapter-config/path`、`/adapter-config` | **done**（允许根校验、TOML 语法校验） |
| GET | `/schema/bot`、`/schema/model`、`/schema/section/{section_name}` | **done**（嵌入 `config_schema.json`，缺失配置节返回 404） |

## `/api/config` 兼容（`config.compat_router`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/schema`、`/schema/bot` | **done**（复用 Dashboard 配置 schema） |
| GET/POST | `/raw` | **done**（bot_config.toml 原文读写，POST 校验 TOML） |
| 其他 | `/*` | **done**（`{"detail":"Not Found"}`） |

## `/api/chat`（`routers/chat/routes.py`）

挂载函数：`mountChatRoutes`（`routes_chat.go`），由 `router.go` 的 `/api/chat` 块调用

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/history` | **done**（`chat_history_db.go` 从 DB 读，`group_id` 取虚拟群频道；对齐 Python） |
| DELETE | `/history` | **done**（按会话解析后删 `message_logs`，对齐 Python `service.clear_history`） |
| GET | `/platforms` | **done**（`person_info` 平台计数） |
| GET | `/persons` | **done**（平台必填，ILIKE 搜索，last_known_time 倒序） |
| GET | `/sessions` | **done**（聊天流列表、显示名、消息/表达/黑话计数） |
| POST | `/sessions/adapter-status` | **done**（无运行中适配器时返回 `adapter_unavailable` 放行） |
| GET | `/resolve-target` / POST `/resolve-targets` | **done**（按 platform/item_id/rule_type 解析真实聊天流，批量上限 200） |
| GET/DELETE | `/sessions/{id}` | **done**（详情含表达/黑话学习规则、发言频率、私聊暂停、专属 Prompt；删除清理消息/表达/工具记录/统计/高频词/黑话与适配器覆盖） |
| GET | `/sessions/{sessionID}/messages` | **done**（`message_logs` by MaiBot session_id） |
| PUT/DELETE | `/sessions/{id}/talk-frequency` | **done**（写 `bot_config.toml` `chat.reply_timing.talk_value_rules`；私聊 400） |
| PUT | `/sessions/{id}/private-reply-pause` | **done**（写 `private_reply_pause_rules`，`paused=null` 删除覆盖） |
| PUT | `/sessions/{id}/learning/{kind}` | **done**（expression/jargon `learning_list`） |
| PUT/DELETE | `/sessions/{id}/prompts[/{index}]` | **done**（`chat.reply_style.chat_prompts`） |
| PUT | `/sessions/{id}/adapters/policy` | **done**（按运行中 `driver_id` 解析身份后 `SetChatOverride`；未运行 404） |
| GET/PUT | `/adapters/policy/defaults` | **done**（platformio 全局默认动作） |
| GET | `/adapters/plugins/{plugin_id}/policy` | **done**（优先运行中完整身份，未运行退回 plugin_id 只读） |
| PUT | `/adapters/plugins/{plugin_id}/policy` | **done**（写入要求运行中身份；未运行 404，不猜测归属） |
| GET | `/info` | **done** |
| 其他 | `/*` | NotFound |

已知差异：配置写入使用 go-toml 重排，不保留原注释；写入后无 Python `config_manager.reload_config` 热重载，因此不返回 500“热重载失败”。

## `/api/webui/memory`（`routers/memory.py`，prefix `/memory`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/records/search` | **done**（paragraph/entity/relation/fact/episode 分桶，对齐 `_memory_records_search`） |
| GET/POST | `/facts`, `/graph`, `/episodes`, `/profiles`, … | **done**（`internal/webui/routers/routes_memory_admin.go` 全量挂载；task-52 扫描 /memory 135 条 0 缺失） |

## `/api/webui/search`（`routers/search.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| POST | `/ai/stream` | **done**（NDJSON 进度/结果流；`search.Service` + 可注入 `SearchRunner`，默认 LLM 工具检索；进程内 TTL/LRU 缓存） |

## `/api/webui/system`（`routers/system.py`，prefix `/system`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/status` | **done**（running/uptime/version/start_time，Go 构建来源通过 build_revision 返回 Git 提交号） |
| POST | `/request-restart` | **done**（写 `pending_binary.json` 或沿用 `MAIBOT_UPDATE_PENDING_PATH`；Worker 子进程优雅退出 **42**；单进程模式 **409**） |
| POST | `/restart` | **done**（Dashboard 兼容：仅触发 **42**，不写 pending） |
| GET | `/update-notice` | **done**（读 `data/update_notice_state.json`；首次初始化不公告；`force=true` 返回调试公告） |
| POST | `/update-notice/ack` | **done**（写入 `webui_ack_version`） |
| GET | `/update-history` | **done**（`offset`/`limit` 1-10/`before_version`/`section=webui`；非法 section 422） |
| GET | `/local-cache` | **done**（config/data/plugins/logs 目录统计；database `backend=sqlite`） |
| GET | `/local-cache/database` | **done**（MaiBot.db/-wal/-shm 体积 + PRAGMA 分页空闲 + 表白名单 `cleanup_supported`/`cleanup_date_column`；`backend=sqlite`） |
| POST | `/local-cache/database/vacuum` | **done**（真实 SQLite VACUUM + wal_checkpoint；无库文件 404） |
| GET/DELETE | `/local-cache/data-entries` | **done**（保护 data 根、MaiBot.db(-wal/-shm) 与缓存目录） |
| GET | `/local-cache/images` | **done**（分页、日期过滤、date_groups） |
| GET | `/local-cache/images/preview` | **done**（inline 返回图片） |
| DELETE | `/local-cache/images`、`/local-cache/images/bulk` | **done**（`date_range` / `older_than_recent_days` 1/7/30） |
| GET/DELETE | `/local-cache/log-directories` | **done**（按日志目录真实删除） |
| POST | `/local-cache/cleanup` | **done**（images/emoji/log_files；`database_logs` 对齐 `_delete_database_records`：8 表白名单、`all`/`older_than_days`、可选 vacuum） |
| GET | `/maisaka-monitor/media/{media_kind}/{media_hash}` | **done**（读 images 元数据表 + 落盘文件；未注入 Memory/store 时 500） |
| POST | `/reload-config` | **done**（与 Python 同款 stub：`success` + “配置重载功能待实现”；真热重载双方均未实现） |

## `/api/webui/data-transfer`（`routers/data_transfer.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| POST | `/export` | **done**（后台任务；zip 含 manifest.json、config/data，可选 plugins/logs；排除 runtime writer 锁与 prompt_imgs；图快照先固定再打包） |
| GET/DELETE | `/jobs/{job_id}` | **done**（DELETE 处理中或下载中 409） |
| GET | `/exports` | **done**（`*.export.json` 历史恢复 + 旧 zip 补录） |
| GET | `/export/{job_id}/download` | **done**（刷新保留期；未完成或文件失效 400） |
| POST | `/export/{job_id}/cancel` | **done** |
| POST | `/import` | **done**（multipart；`.zip` 校验、导入范围校验、manifest/顶层目录/符号链接/越界路径校验） |

## `/api/webui/avatar`（`routers/avatar.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| PUT | `/webui-user` | **done**（multipart；user_id 规则、5 MB 上限、图片签名识别；写入 `data/avatar/internal/`） |
| GET | `/` | **done**（internal/webui 本地缓存 + ETag/304；外部平台走 `Deps.Avatar`/`AvatarService`，未注入仍 502） |

## `/api/webui/user-emojis`（`routers/user_emoji.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/` | **done**（按 mtime 倒序；`limit` 100） |
| POST | `/` | **done**（multipart；2 MB / 100 个上限、uuid hex ID、JPG/PNG/WebP/GIF） |
| GET | `/{emoji_id}/content` | **done**（immutable 缓存头） |
| DELETE | `/{emoji_id}` | **done** |

## `/api/webui/bot-accounts`、`/api/webui/agent`

| 子 prefix | Go 状态 |
| --- | --- |
| `/bot-accounts` | **done**（经 `BotAccountService` 列表/禁用/恢复/删除；生产由 `core/app.go` 注入服务 + `BotAccountOnline`；nil 服务 → 503） |
| `/agent` | **done**（可注入 `Deps.ACPAgent`：status/control/events/message；未接线时 status 200 disabled、control 409、events 200 disabled、message 空 422/未启动 409；events 的 `queued`/`pending` 对齐 Python inbox / backend.Pending） |

## `/api/webui/statistics`（`routers/statistics.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/models` | **done**（返回 `[]stats.ModelStatistics`，字段与 Python `ModelStatistics` 对齐） |
| GET | `/chats` | **done**（Go 扩展；`stats.QueryTopChats` / `message_traffic_chat_hourly`） |
| GET | `/dashboard` | **done**（`DashboardData` 字段与 Python 对齐） |
| GET | `/summary` | **done**（`StatisticsSummary` 字段与 Python 对齐） |
| GET | `/task-usage` | **done**（`task_name`/`hours` 校验，`model_task_usage` 聚合） |
| GET | `/detailed` | **done**（无快照返回 503「详细统计正在生成，请稍后重试」；快照由 `internal/services/statistics/detailed_report.go` 15 分钟后台任务（首跑立即）生成，请求内不再同步构建） |

## `/api/webui/person`（`routers/person.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/list` | **done**（SQLite 直查 `person_info`，NULL 保真、LIKE 搜索、Python 排序 `last_known_time IS NULL` 最后） |
| GET | `/{person_id}` | **done**（可空字段原样输出 `null`；`group_nick_name` 非法 JSON/空值返回 null） |
| GET | `/stats/summary` | **done**（`personinfo` 汇总） |
| POST | `/batch/delete` | **done** |
| DELETE | `/{person_id}` | **done** |
| PATCH / POST | `/{person_id}` | **done**（`personinfo.UpdateProfileFields`，body 对齐 Dashboard `PersonUpdateRequest`；Dashboard 用 PATCH，POST 为兼容别名） |

## `/api/webui/expression`（`routers/expression.py`，27 条）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/chats`、`/chat-targets` | **done**（`ExpressionChats` / `ExpressionChatTargets`） |
| GET | `/list` | **done**（分页/搜索/聊天流/审核筛选/排序/含旧版） |
| POST | `/` | **done**（新建表达方式） |
| GET / PATCH / DELETE | `/{expression_id}` | **done**（详情、增量更新、删除） |
| PATCH | `/{expression_id}/review-status` | **done**（人工审核状态） |
| POST | `/batch/delete`、`/review/batch` | **done**（批量删除、批量审核） |
| POST | `/export`、`/import`、`/clear` | **done**（导出/导入/清空） |
| GET | `/stats/summary`、`/review/stats`、`/review/list` | **done**（统计与待审核列表） |
| GET | `/groups` | **done**（读取 bot_config `[expression].expression_groups`，按 target 通配匹配） |
| GET | `/vector-build-progress` | **partial**（`use_vector_expression`/索引路径与 id 计数；Go 原生索引无 embedding profile，缺 `waiting_profile`/fingerprint 判定） |
| GET | `/clusters`、`/clusters/{cluster_id}/members` | **partial**（读 Python 版索引的 `clusters`；Go 原生 `items` 索引不产生聚类） |
| GET | `/runtime/vectors/spaces` | **partial**（`embedding_profiles` 或单 legacy 空间；Go 原生索引无 profile 元数据） |
| DELETE | `/runtime/vectors/spaces/{space_id}` | **partial**（仅删除 Python 格式 `embedding_profiles`；Go 原生索引返回 404） |
| POST | `/legacy-import/preview`、`/preview-file`、`/import` | **done**（SQLite 只读预览、上传临时库、映射导入；配置账号优先，未命中回退空 account_id） |
| GET | `/review/logs` | **done**（文件审核日志，limit 1..200、passed/chat_id 过滤） |
| POST | `/review/logs/{review_log_id}/approve` | **done**（恢复已有表达方式；API 响应字段对齐 Python ExpressionResponse） |
| 其他 | — | 27 条已全部注册 |

## `/api/webui/jargon`（`routers/jargon.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/list` | **done**（`learners.GORMStore.ListJargons`，含 legacy 空义判定） |
| POST | `/batch/set-jargon` | **done**（query `ids` + `is_jargon` → `SetJargonIsJargon`） |
| GET | `/chats`、`/groups`、`/stats/summary` | **done**（chats 含空；groups 读取 `[jargon].jargon_groups`，显式 `*/*` 才 global） |
| GET/POST/PATCH/DELETE | 详情、创建、更新、删除、批量删除、导入导出 | **done** |

## `/api/webui/emoji`（`routers/emoji/`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/list` | **done**（`images`/`EMOJI` 字段，对齐 MaiBot） |
| 其他 | 详情/更新/删除/注册/封禁/上传/缩略图/批量删除/统计 | **done**（15 条全部注册；缩略图缓存清理/预热/清空为 Go 扩展） |

## `/api/webui/plugins`（`routers/plugin/`，prefix `/plugins`）

MaiBot Dashboard 市场列表为 **`GET /api/webui/plugins/marketplace`**（非 `plugins/market/list`）。

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/marketplace` | **done**（官方源/Git/CNB/releases 合并快照 + `List`；query `compatible_only`/`force_refresh`） |
| GET/PUT | `/marketplace/source` | **done**（信息源、下载源、自定义下载前缀） |
| GET | `/marketplace/stats/summary` | **done**（官方统计汇总 + 缓存元数据） |
| GET | `/marketplace/user-states` | **done**（点赞/点踩/评分用户态） |
| POST | `/marketplace/like`、`/dislike`、`/rate`、`/download` | **done**（engagement 计数与下载源解析） |
| POST | `/marketplace/install`、`/plugins/install` | **done**（Git 克隆或发布包真实解压到 `plugins/<plugin_id>`，`_manifest.json` 校验失败回滚） |
| POST | `/marketplace/uninstall`、`/plugins/uninstall` | **done**（先写 `config.toml` 的 `[plugin].enabled=false` 并卸载运行时，再删除插件目录） |
| POST | `/marketplace/update`、`/plugins/update` | **done**（Git `fetch` + `merge --ff-only`；非 Git 重新克隆并原子替换；发布版本安装的插件走版本选择更新；请求断连返回 499） |
| GET | `/marketplace/{plugin_id}` 及 `/stats`、`/readme`、`/changelog`、`/feedback`、`/user-state` | **done**（详情、统计、文档、反馈、用户态） |
| POST | `/plugins/install-zip` | **done**（本地 ZIP 安装） |
| GET | `/plugins/config/{plugin_id}/bundle` | **done**（运行时 inspect + 磁盘 raw；虚构 general 节折叠；config_error 透出） |
| GET | `/plugins/config/{plugin_id}/schema` | **done**（config_error→400，schema 为空→500） |
| GET/PUT | `/plugins/config/{plugin_id}/raw` | **done**（PUT 先做 TOML 语法校验，备份到 `config_back/` 后落盘并回报 runtime_status） |
| GET/PUT | `/plugins/config/{plugin_id}` | **done**（PUT 支持 dotted key、虚构 general 字段还原、运行时校验后保存；保存为规范化 TOML，不保留原注释） |
| POST | `/plugins/config/{plugin_id}/reset`、`/toggle` | **done**（reset 移动为备份；toggle 只改 `[plugin].enabled`，保留注释与其余键） |
| GET | `/plugins/config/{plugin_id}/widgets`、POST `/widgets/{action_id}` | **done**（`Cache-Control: no-store`，运行时按钮组件） |
| GET | `/plugins/installed` | **done**（扫描插件目录 + manifest + 启用状态 + 运行时注册状态；usage 取 `PluginHost.UsageSnapshot`，circuit_status 取 `circuitbreaker.Default().Statuses()`） |
| GET | `/plugins/usage` | **done**（`PluginHost.UsageSnapshot` 返回每插件累计运行秒数） |
| GET | `/plugins/marketplace/review-identity` | **done**（读取实例 `config/bot_config.toml` 的 `bot.nickname`） |
| GET/PUT | `/plugins/source`、`/plugins/user-states`、`/plugins/like`、`/plugins/dislike`、`/plugins/rate`、`/plugins/download` | **done**（Go 兼容裸路径别名；Python 实际只挂 `/marketplace/*`，两套路径同处理器） |
| GET | `/plugins/{plugin_id}` 及 `/stats`、`/readme`、`/changelog`、`/feedback`、`/user-state` | **done**（Go 兼容裸路径别名；Python 实际只挂 `/marketplace/{plugin_id}`，两套路径同处理器） |
| GET | `/version`、`/git-status` | **done**（公开；版本解析与 Git 探测） |
| POST | `/clone` | **done**（需登录；安全路径校验、下载源/自定义 URL、最多 3 次尝试） |
| GET | `/icon/{plugin_id}`、`/market-icon` | **done**（需登录；本地图标路径/后缀/512KiB 校验，CNB 图标代理） |
| GET/POST | `/runtime/webui`、reload、document、uploads、queries/actions | **done**（list/document/invoke；reload 走 Runner `plugin.webui.reload_documents` + `ReplaceWebUIDocuments`；uploads 校验页面声明/静态 API/确认标记后用 `Host.StageWebUIUpload` 暂存并注入 `upload_id`） |
| GET | `/plugins/runtime/plugins/{plugin_id}/components`、`/runtime/commands`、`/runtime/home-cards`、`/runtime/hooks` | **done**（来自 `pluginruntime.ComponentRegistry`/HookSpecRegistry；home-card 内容按 Python 规则裁剪与链接白名单） |
| WS | `/api/webui/ws` 的 `plugin_progress:main` | **done**（`pluginmarket.SetProgressReporter` → 订阅时 snapshot，安装/更新/卸载时 update；旧版 `/plugins/ws/plugin-progress` 在 Python 未挂载，Go 不注册） |

安装/卸载/更新处理器位于 `internal/webui/routers/plugin/handlers_plugins.go`，配置页与已安装列表位于同包 `handlers_plugins_config.go`/`config_helpers.go`/`handlers_plugins_installed.go`，运行时组件查询位于 `handlers_plugins_runtime.go`，路由集中在 `plugin/routes_plugins.go`。运行时激活依赖 `RuntimeCoordinator`：装配时需 `service.SetRuntime(pluginmarket.NewRuntimeCoordinator(pluginHost))`，配置页另需 `Deps.PluginHost`（由 `internal/webui/core` 的 `New(..., pluginHost)` 注入）。未接线时安装成功但不会重载插件，配置页返回 503。

## `/api/webui/mcp`（`routers/mcp.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/status` | **done**（需鉴权；`Agent.MCPStatus` → `mcp.StatusSnapshot`，对齐 MaiBot 字段并扩展 `prompt_count`/`resource_count` 与各服务器 `prompts`/`resources` 列表摘要） |
| POST | `/test` | **done**（需鉴权；草稿 `MCPServerItemConfig` 或 store `id`/`name` 引用；`mcp.TestConnection` 一次性连接并 `ListTools`，响应对齐 `MCPConnectionTestResponse`） |

## 桌宠皮肤 `/api/webui/desktop-pet`（对齐 `routers/desktop_pet.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/skins` | **done**（需鉴权；`desktoppet.ListSkins`，内置皮肤在首，非法包进 `errors`） |
| GET | `/skins/{skin_id}` | **done**（需鉴权；资产内联为 data URI，校验失败 422） |
| POST | `/skins` | **done**（需鉴权；请求体 32 MiB 上限 413，zip 校验/重复 ID 失败 422，成功返回规范化清单） |

## 推理过程 `/api/webui/reasoning-process`（对齐 `routers/reasoning_process.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/stages` | **done**（阶段目录与 session 数/最近修改时间） |
| DELETE | `/stages/{stage}` | **done**（`all`/空→planner；返回删除文件数） |
| GET | `/files` | **done**（stage/session/action/search 过滤、分页、replyer 去重） |
| GET | `/file` | **done**（`.txt`/`.json`；元数据与预览；jargon 事件 JSON 缩进） |
| GET | `/image` | **done**（仅 `data/images`、`data/emoji`、`data/prompt_imgs`、`data/html_imgs` 内文件） |
| GET | `/html` | **done**（`.html`，`X-Robots-Tag: noindex, nofollow`） |
| GET | `/tool-call-records` | **done**（时间窗口内跨 stage 反查 call_id） |
| GET | `/tool-call-source` | **done**（最多 12 条倒序来源记录） |
| GET | `/active-context` | **done**（读 `agent.Agent.ChatActiveContext`：滚动摘要/黑话/runtime_active + prompt 日志表达习惯；未知会话 404） |
| POST | `/replay` | **done**（`Deps.ReasoningReplay` 可注入；默认走 agent LLM 路由器：校验 model_config 模型/任务、反序列化 ContextItem（文本/图片/工具调用/工具结果/reasoning）、调用模型并返回 output_items；ProviderActivity/Opaque 明确报不支持；未接线 503） |

记录索引与元数据解析位于 `reasoning_logs.go`/`reasoning_metadata.go`/`reasoning_hydrate.go`，处理器与路由位于 `handlers_reasoning.go`/`routes_reasoning.go`；重放实现位于 `reasoning_replay_types.go`/`reasoning_replay_handler.go`/`reasoning_replay_context.go`/`reasoning_replay_llm.go`。`message_avatars` 由 `reasoning_message_avatars.go` 从 prompt JSON 抽 `msg_id` 后回查 `mai_messages`；`session_infos` 由 `reasoning_session_resolve.go` 按 `chat_sessions` + 配置账号回填 `resolved_session_id`/展示名。

## 自更新 `/api/webui/system/self-update`（对齐 `routers/self_update.py`）

| 方法 | 路径 | Go 状态 |
| --- | --- | --- |
| GET | `/status` | **done**（`selfupdate.CurrentStatus` 本地版本、更新源、blockers；受实例目录与构建版本影响） |
| POST | `/check` | **done**（`selfupdate.CheckStatus` 真实远端检查，支持 source_id/custom_prefix/force） |
| POST | `/check-stream` | **done**（ndjson `progress`/`heartbeat`/`result`/`error`） |
| POST | `/apply` | **done**（提交 expected_commit/target_commit 给 `selfupdate.RequestUpdate` 并调度 worker 重启；缺少目标或冲突返回 409） |

## `/api/webui` 其他子路由

| 子 prefix | 模块 |
| --- | --- |
| `/learning/text` | `routers/text_learning.py` — **done**（`/prepare` `/extract`，见 `text_learning_ext.go`） |
| `/files` | `routers/files.py` — **done**，由 `internal/files` 在 `App.routes` 挂载 `/api/webui/files`（列表/chats/usage/upload/chat-upload/download/send/text/delete）及 `/api/files` 传输下载 |
| `/models` | `routers/model.py` — **done**（`POST /test-model` 真实调用 chat/embedding/image_embedding；replyer 文本与工具调用分支） |
| `/news` | `routers/news.py` — **done**（`GET ""` 需鉴权；`GET /status` 运维快照） |
| `/html-render/status` | （Go 扩展）HTML 渲染侧车状态 — **done**（`sidecar_http`；渲染委托 Python 原版 Playwright 侧车，未配置时返回真实错误） |
| `/go` | （Go 平台专属，正式功能）开发者设置页 — **done**。`GET /go`、`GET /go/` 渲染内嵌页面；`/api/webui/go/*` 统一挂在 `requireAuthMiddleware` 之后：`GET /settings`（含 `session_scheduler` 运行快照）、`POST /settings`、`POST /toggle/{feature_key}`、`POST /mode`、`POST /reset`、`POST /restart`，读写 `config/runtime.toml`。阶段 2 的 `native_session_scheduler` 已接入聊天主链；其余未接入运行时的 feature 只能关闭，开启返回 409。页面样式复用原版 Dashboard 的 shadcn 设计令牌；Go 测试位于仓库根目录 `test/`。宿主模式下页面由宿主输出，`/api/webui/go/*` 转发到当前选中的 Bot |

WebSocket（挂在 `/api/webui` 下无额外 prefix 或 plugin 子路径）：

- `GET /ws-token` — **done**
- `GET /ws/logs` — **done**（`internal/common/logger` JSON ring + `internal/webui/routers/websocket/handlers_ws.go`）
- `GET /ws` — **done**（**logs** / **chat:main** / **plugin_progress:main** / **plugin_runtime:main** / **maisaka_monitor:main**；chat `session.open`（含虚拟身份）、频道隔离与监控账本回放；plugin_progress snapshot+update；plugin_runtime **changed**）
- `routers/websocket/plugin_runtime.py` — **done**（`Host.SubscribeStatusChanges` → `plugin_runtime:main changed`；无快照 payload）

## 记忆 `/api/*` 兼容（`memory.compat_router`，prefix `/api`）

**done**：`routes_memory_admin.go` 的 `mountMemoryCompatRoutes` 已按白名单挂上 `/api/graph`、`/api/timeline`、episodes、person_profile、import、retrieval_tuning 等兼容路径。
**故意不挂 `/api` catch-all**，以免覆盖 `internal/files` 的 `/api/files/transfers/{token}`。

## 未实现路由的默认行为

- `/api/webui/*` 未注册路径 → `{"detail":"Not Found"}`（`handleWebUINotFound`）
- `/api/chat/*` 未注册路径 → `{"detail":"Not Found"}`；已注册端点见上表
- `/api/config/*`（除已列 GET）→ `{"detail":"Not Found"}`

## 相通组 `/api/webui/sharing-groups`（v1.4.4）

| 方法 | 路径 | 状态 |
| --- | --- | --- |
| GET | `/api/webui/sharing-groups` | **done**（快照 + 解析成员） |
| POST | `/api/webui/sharing-groups` | **done** |
| PUT | `/api/webui/sharing-groups/{group_id}` | **done**（revision 乐观锁） |
| DELETE | `/api/webui/sharing-groups/{group_id}?revision=` | **done** |

## 在线聊天迁移 `/api/webui/chat-migrations`（v1.4.4）

| 方法 | 路径 | 状态 |
| --- | --- | --- |
| GET | `/api/webui/chat-migrations` | **done** |
| POST | `/api/webui/chat-migrations/preview` | **done** |
| GET | `/api/webui/chat-migrations/{job_id}` | **done** |
| POST | `/api/webui/chat-migrations/{job_id}/execute` | **done** |
| POST | `/api/webui/chat-migrations/{job_id}/recover` | **done** |

## 远端桌宠 `/api/webui/desktop-pet/remote`（v1.4.4）

| 方法 | 路径 | 状态 |
| --- | --- | --- |
| WS | `/api/webui/desktop-pet/remote/ws` | **done**（握手/心跳/事件） |
| POST | `/api/webui/desktop-pet/remote/chat/{request_id}` | **done**（幂等重放） |
| POST | `/api/webui/desktop-pet/remote/todos` | **done** |
| GET | `/api/webui/desktop-pet/remote/todos/images/{image_id}` | **done** |
