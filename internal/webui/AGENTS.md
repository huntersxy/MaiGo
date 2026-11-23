# AGENTS

本目录负责 WebUI：HTTP 路由装配、鉴权、API 路由、原版 Dashboard 静态托管与 WebUI 业务服务。

## 入口与职责

- `core/app.go`：应用依赖与 chi 路由装配，接入 API、文件服务与原版 Dashboard。
- `core/anticrawler*.go`：防爬虫与 robots.txt。
- `core/compression.go`、`core/rate_limiter.go`：文本压缩与登录限流。
- `core/files_chat.go`：WebUI 与内部聊天/文件服务之间的桥接。
- `middleware/`：WebUI 会话与访问密钥鉴权（目录名 `middleware`，包名 `auth`）。
- `routers/`：`/api/webui/*` 与 WebSocket 路由（对齐 Python `src/webui/routers/`）。
- `dashboardstatic/`：原版 Dashboard 构建产物的目录解析与静态托管。
- `services/ai_search/`：搜索工具所需的文档与配置查询。
- `gopage/`：内嵌的 Go 平台开发者设置页与输出处理器，Bot 与宿主共用；其余前端仍复用原版 Dashboard。

## 页面与文案

- 前端默认不重写：页面来自 `maibot-dashboard` 包的 `dist`，Go 只提供静态托管与 API。
- 唯一例外是已立项的 `/go` 开发者设置页：它服务于执行模式与 Go 优化开关，样式必须复用原版 Dashboard 的设计令牌（`--color-*`、圆角、阴影、明暗主题），不另起一套视觉风格。
- `MAIBOT_DASHBOARD_DIR` 可显式指定 dist 目录；未设置时按 `ResolveRoot` 的候选顺序查找。
- 根路径 `/` 返回 Dashboard `index.html`，未命中的 GET 先找静态文件再回落 index（`/api` 除外），对齐 Python `serve_spa`。

## 修改注意

- 不新增第二套 Go 侧后台页面；例外是 `/go` 开发者设置页，其他界面改动仍应改原版 Dashboard 或只补 API。
- `/go` 的配置 API 固定为 `/api/webui/go/*`，必须挂在 `requireAuthMiddleware` 之后；不要再增加无鉴权的 `/go/api` 别名。
- 原生引擎只在生效模式为 `native` 且对应开关开启时使用；`GET /api/webui/go/settings` 分别报告文件配置与生效状态。切换全局模式不能改写局部开关。
- 保存、单项开关、模式和重置均交由 `execution.Controller` 排空旧任务、持久化并发布；WebUI 不直接写 `runtime.toml`，不得把草稿或未发布的值显示为生效状态。
- Go 测试统一放在仓库根目录 `test/`，不要与包内代码混放。
- API 路由增删要同步 `routers/ROUTES.md`。
