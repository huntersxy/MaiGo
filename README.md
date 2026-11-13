# MaiGo

[![License](https://img.shields.io/badge/license-GPLv3-blue.svg)](https://github.com/huntersxy/maigo/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/stargazers)
[![GitHub issues](https://img.shields.io/github/issues/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/issues)
[![GitHub pull requests](https://img.shields.io/github/issues-pr/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/pulls)

## 描述

MaiGo 是 MaiBot 的 Golang 移植版本，致力于在保持原版逻辑与行为的同时实现更低的内存占用和可选的 Go 平台专属优化。它提供了丰富的功能模块，支持 QQ 群聊/私聊智能对话、多平台适配器接入（OneBot/NapCat/SnowLuma 通过插件）、LLM 多模型路由与任务分配、Maisaka 规划器、Focus 共享组多会话协同、消息去抖与心流调度、主动发言与退避、回复生成、长期记忆 A_Memorix、人物画像、黑话/表达方式/表情包学习、工具系统、插件运行时、MCP 客户端、WebUI 管理后台、文件传输与媒体处理、桌面宠物、电脑沙盒接管、ACP 外接 Agent、统计遥测、配置热重载、日志系统、自动更新等。

## 功能

- 聊天机器人框架
- QQ 群聊/私聊智能对话
- 多平台适配器接入（OneBot / NapCat / SnowLuma 通过插件）
- LLM 多模型路由与任务分配
- Maisaka 规划器（ReAct 循环 + 工具调用）
- Focus 共享组多会话协同
- 消息去抖与心流调度
- 主动发言与退避
- 回复生成（replyer + 表达风格学习）
- 长期记忆 A_Memorix（段落/实体/关系/事实/情景 + 向量检索 + 图片记忆）
- 人物画像
- 黑话/表达方式/表情包学习
- 工具系统（wait/stop/reply/搜索/子代理等 14 项内置 + MCP + 插件）
- 插件运行时（Python Runner + 消息网关 + 能力 RPC）
- MCP 客户端
- WebUI 管理后台（401 个 API + 原版 Dashboard）
- WebUI 内聊
- 文件传输与媒体处理
- 桌面宠物
- 电脑沙盒接管
- ACP 外接 Agent
- 统计遥测
- 配置热重载
- 日志系统
- 自动更新


## 部署

### Docker（推荐）

```bash
cp .env.example .env   # 至少设置 MAIBOT_WEB_ADMIN_KEY / MAIBOT_SECRET_KEY
docker compose build
docker compose up -d
docker compose ps      # healthcheck 访问 /health
```

- 默认 `docker-compose.yml` **只启动 maibot-go**，平台接入走插件适配器：在 WebUI「适配器设置」
  安装并启用适配器插件，适配器通过 maim_message 与本体通信。本体不内置 OneBot 直连，
  进程启动不依赖任何协议端在线。
- 前端只有原版一套：镜像内置 `maibot-dashboard==1.9.13` 的 `dist`（构建期从 PyPI 取），
  根路径 `/` 直接提供原版 Dashboard，无需在宿主机另装前端。
- 配置文件**首次启动自动生成**到 config 卷（`config/bot_config.toml`、`config/model_config.toml`），
  按 MaiBot 1.3.19 官方默认值写入，不需要手工放置。模型的 `api_key` 在 WebUI 或
  `model_config.toml` 里填真实值，不要沿用示例占位。
- 数据库是 SQLite，落在 data 卷的 `MaiBot.db`。已有的 Python SQLite 库（v17 及以上）
  会自动接管升级；更老或无法识别的库会拒绝启动，先用 WebUI 数据迁移导出/导入。
  0.x 旧配置需显式设置 `MAIBOT_LEGACY_0X_UPGRADE_CONFIRMED=1`。
- 持久化卷：`maibot-config`（`/app/config`）、`maibot-data`（`/app/data`）、
  `maibot-logs`（`/app/logs`）、`maibot-plugins`（`/app/plugins`）、`stickers`、`media`。
  备份直接备份 `maibot-data` 卷（或其中的 `MaiBot.db`）。
- 容器以只读根文件系统运行，可写路径只有上述卷和 tmpfs（`/tmp`、`/app/temp`）。
  因此**容器内不支持自更新**：镜像里没有 `.git`，自更新会以 `not_git_repo` 拒绝，
  升级方式是用新版本重新 `docker compose build` 并重建容器。
- 插件运行时：镜像内置 Python Runner（`/app/plugin-runtime`，来自 PyPI `maibot==1.3.19`
  + runner 依赖）。设置 `MAIBOT_PLUGIN_SPAWN=1` 即可加载 `plugins/` 下的 Python 插件；
  `MAIBOT_PLUGIN_PYTHON` 已默认指向 `/app/plugin-runtime/bin/python`。
  Runner 的临时目录 `/app/temp` 由 compose 挂 tmpfs。插件若声明额外 Python 依赖，
  需要可写的 site-packages 或自定义镜像；也可用 `MAIBOT_PLUGIN_PYTHON` 换成外部解释器。

### 非 Docker

- Linux 需自备 cgo + `libwebp` 运行环境；Windows 直接用仓库构建产物。
- 源码部署需设置 `MAIBOT_DASHBOARD_DIR` 指向含 `index.html` 的 Dashboard 构建目录，
  否则根路径返回 503。

## 贡献

欢迎任何形式的贡献！请遵循以下步骤：

1. **Fork** 本仓库。
2. **创建分支**：`git checkout -b feature/your-feature`。
3. **提交代码**：遵循代码规范，保持代码清晰。
4. **编写测试**：确保新功能通过单元测试。
5. **提交 PR**：在 `main` 分支上发起 Pull Request，描述所做更改。
6. **代码审查**：项目维护者将进行审查，可能会提出修改建议。

请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 了解详细贡献指南。

## 许可证

本项目采用 **GPL v3** 许可证。详情请参见 [LICENSE](LICENSE)。

---
