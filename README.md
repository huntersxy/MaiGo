# maibot-go
该仓库任务：将Maibot从python移植到go。

## 目录地图（当前）

- `main.go`：进程入口，仅调用 `bootstrap.Run()`
- `internal/bootstrap/`：启动装配（Python 对照：`src/main.py` + `src/runtime.py`）
- `internal/maisaka/runtime/`：主聊天编排、门控、Replyer、压缩（Python 对照：`src/maisaka/runtime.py` + `src/chat/message_receive/`）
- `internal/agent/`：ACP 外接 Agent（Python 对照：`src/agent/`）
- `internal/platform_io/drivers/onebot/`：平台消息类型（OneBot 语义），不含连接实现
- `internal/common/database/`：SQLite 模型与记忆/话题/情感（Python 对照：`src/common/database/`，库文件 `data/MaiBot.db`）
- `internal/common/database/migrations/`：schema 版本迁移
- `internal/maisaka/builtin_tool/`：Agent 工具
- `internal/webui/`：WebUI（core 路由装配 / middleware / routers API / dashboardstatic 原版前端托管）
- `internal/config/`：env 启动配置 + 实例 TOML / model_config 动态配置
- 目录名对齐 MaiBot `src/`：`A_memorix/`、`chat/`、`common/`、`core/`、`config/`、`emoji_system/`、`files/`、`learners/`、`llm_models/`、`maisaka/`、`manager/`、`mcp_module/`、`person_info/`、`platform_io/`、`plugin_market/`、`plugin_runtime/`、`prompt/`、`runtime/`、`services/`、`web_search/`、`webui/`

## 代码导航

本仓库已建立 CodeGraph 本地索引（`.codegraph/` 已在 .gitignore，不提交）。跨包查定义、调用链、影响面时优先使用：

```bash
codegraph query <symbol>
codegraph explore "<模块或问题>"
codegraph node <symbol>
codegraph context "<任务>"
codegraph callers <symbol>
codegraph callees <symbol>
codegraph impact <symbol>
```

codegraph 是**导航效率**工具，不是省读的理由：它一次返回源码+调用链，省的是重复搜索，不是省掉阅读。需要细节时再用 `read` 打开具体文件；索引重建用 `codegraph index`。

## 生产部署（Docker，推荐）

```bash
cp .env.example .env   # 若存在；至少设置 MAIBOT_WEB_ADMIN_KEY / MAIBOT_SECRET_KEY
docker compose build
docker compose up -d
docker compose ps      # healthcheck 访问 /health
```

- 默认 `docker-compose.yml` **只启动 maibot-go**，平台接入走插件适配器。
- 平台接入：在 WebUI「适配器设置」安装并启用适配器插件，适配器通过 maim_message 与本体通信；
  本体不再内置 OneBot 直连配置，进程启动不依赖任何协议端在线。
- 前端只有原版一套：镜像内置 `maibot-dashboard==1.9.13` 的 `dist`，根路径 `/` 直接提供原版 Dashboard，无需在宿主机另装前端，也没有 Go 侧自建页面。
- 持久化卷：`maibot-config`（`/app/config`）、`maibot-data`（`/app/data`，含 `MaiBot.db`）、`maibot-logs`（`/app/logs`）、`maibot-plugins`（`/app/plugins`）、`stickers`、`media`。
- 首次部署把 `bot_config.toml`、`model_config.toml` 放入 config 卷；模型 `api_key` 不要沿用示例占位值。
- 已有 Python SQLite 库：v17 及以上结构会自动接管/升级；更老或无法识别的库会拒绝启动，先用 WebUI 数据迁移导出/导入。0.x 旧配置需显式设置 `MAIBOT_LEGACY_0X_UPGRADE_CONFIRMED=1`。
- 备份直接备份 `maibot-data` 卷（或其中的 `MaiBot.db`）即可。
- 非 Docker 的 Linux 部署需自备 cgo + `libwebp` 运行环境；Windows 使用仓库构建产物即可。
- 插件运行时：镜像内置 Python Runner（`/app/plugin-runtime`，来自 PyPI `maibot==1.3.19` + runner 依赖）。设置 `MAIBOT_PLUGIN_SPAWN=1` 即可加载 `plugins/` 下的 Python 插件；`MAIBOT_PLUGIN_PYTHON` 已默认指向 `/app/plugin-runtime/bin/python`。Runner 的临时目录 `/app/temp` 由 compose 挂 tmpfs。插件若声明额外 Python 依赖，需要可写的 site-packages 或自定义镜像；也可用 `MAIBOT_PLUGIN_PYTHON` 换成外部解释器。