# AGENTS

本目录负责 WebUI：HTTP 路由装配、鉴权、API 路由、原版 Dashboard 静态托管与 WebUI 业务服务。

## 入口与职责

- `core/app.go`：应用依赖与 chi 路由装配，接入 API、文件服务与原版 Dashboard。
- `core/anticrawler*.go`：防爬虫与 robots.txt。
- `core/compression.go`、`core/rate_limiter.go`：文本压缩与登录限流。
- `core/files_chat.go`：WebUI 与内部聊天/文件服务之间的桥接。
- `auth/`：WebUI 会话与访问密钥鉴权。
- `routers/`：`/api/webui/*` 与 WebSocket 路由（对齐 Python `src/webui/routers/`）。
- `dashboardstatic/`：原版 Dashboard 构建产物的目录解析与静态托管。
- `services/ai_search/`：搜索工具所需的文档与配置查询。
- `middleware/`、`assets/`、`views/` 已随 Go 自建后台一并移除；前端统一复用原版 Dashboard。

## 页面与文案

- 前端不再由 Go 侧重写：页面一律来自 `maibot-dashboard` 包的 `dist`，Go 只提供静态托管与 API。
- `MAIBOT_DASHBOARD_DIR` 可显式指定 dist 目录；未设置时按 `ResolveRoot` 的候选顺序查找。
- 根路径 `/` 返回 Dashboard `index.html`，未命中的 GET 先找静态文件再回落 index（`/api` 除外），对齐 Python `serve_spa`。

## 修改注意

- 不新增第二套 Go 侧后台页面；需要改界面时改原版 Dashboard 或只补 API。
- API 路由增删要同步 `routers/ROUTES.md`。
