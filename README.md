# MaiGo

[![License](https://img.shields.io/badge/license-GPLv3-blue.svg)](https://github.com/huntersxy/maigo/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/stargazers)
[![GitHub issues](https://img.shields.io/github/issues/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/issues)
[![GitHub pull requests](https://img.shields.io/github/issues-pr/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/pulls)

## 描述

MaiGo 是 MaiBot 的 Golang 移植版本，致力于在保持原版逻辑与行为的同时实现更低的内存占用和可选的 Go 平台专属优化。它提供了丰富的功能模块，支持 QQ 群聊/私聊智能对话、多平台适配器接入（OneBot/NapCat/SnowLuma 通过插件）、LLM 多模型路由与任务分配、Maisaka 规划器、Focus 共享组多会话协同、消息去抖与心流调度、主动发言与退避、回复生成、长期记忆 A_Memorix、人物画像、黑话/表达方式/表情包学习、工具系统、插件运行时、MCP 客户端、WebUI 管理后台、文件传输与媒体处理、桌面宠物、ACP 外接 Agent、统计遥测、配置热重载、日志系统、自动更新等。

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
- ACP 外接 Agent
- 统计遥测
- 配置热重载
- 日志系统
- 自动更新


## 部署

### Docker

```bash
docker compose up -d --build
docker compose logs maigo | grep "WebUI 登录 Token"
```

WebUI 在 `http://<host>:7468`。无需创建 `.env`；端口冲突时：

```bash
MAIBOT_WEB_PORT=8080 docker compose up -d
```

首次启动自动生成配置与 `MAIBOT_SECRET_KEY`，密钥保存在 data 卷的 `.secret_key`。
模型 `api_key` 在 WebUI 填写，内置适配器在 WebUI「适配器设置」启用并配置。

### 非 Docker

Linux 需 cgo 与 `libwebp`。装好 `maibot-dashboard==1.10.2` 后把 `MAIBOT_DASHBOARD_DIR` 指向其 `dist`；
装好 `maibot==1.4.2` 后把 `MAIBOT_PLUGIN_PYTHON` 指向该解释器（依赖集合见 Dockerfile）。
非 Docker 首次启动需设置 `MAIBOT_SECRET_KEY`（`openssl rand -base64 32`）。

## 贡献

欢迎任何形式的贡献！请遵循以下步骤：

1. **Fork** 本仓库。
2. **创建分支**：`git checkout -b feature/your-feature`。
3. **提交代码**：遵循代码规范，保持代码清晰。
4. **编写测试**：确保新功能通过单元测试。
5. **提交 PR**：在 `main` 分支上发起 Pull Request，描述所做更改。
6. **代码审查**：项目维护者将进行审查，可能会提出修改建议。

提交 PR 前请确保 `go build ./...` 与 `go test ./...` 通过。

## 许可证

本项目采用 **GPL v3** 许可证。详情请参见 [LICENSE](LICENSE)。

---
