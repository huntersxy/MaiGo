# MaiGo —— Maibot On Golang

[![License](https://img.shields.io/badge/license-GPLv3-blue.svg)](https://github.com/huntersxy/maigo/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/stargazers)
[![GitHub issues](https://img.shields.io/github/issues/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/issues)
[![GitHub pull requests](https://img.shields.io/github/issues-pr/huntersxy/maigo.svg)](https://github.com/huntersxy/maigo/pulls)

## 描述

MaiGo 是 MaiBot 的 Golang 移植版本，致力于在保持原版逻辑与行为的同时实现更低的内存占用和可选的 Go 平台专属优化。

MaiBot 拥有丰富的功能模块，支持 QQ 群聊/私聊智能对话、多平台适配器接入（OneBot/NapCat/SnowLuma 通过插件）、LLM 多模型路由与任务分配、Maisaka 规划器、Focus 共享组多会话协同、消息去抖与心流调度、主动发言与退避、回复生成、长期记忆 A_Memorix、人物画像、黑话/表达方式/表情包学习、工具系统、插件运行时、MCP 客户端、WebUI 管理后台、文件传输与媒体处理、桌面宠物、ACP 外接 Agent、统计遥测、配置热重载、日志系统、自动更新等。


## 部署

### Docker

```bash
docker compose up -d --build
docker compose logs maigo | grep "WebUI 临时登录密码"
```

WebUI 在 `http://<host>:7468`。容器内以宿主模式运行，Bot 在 `bots/<id>/` 下由宿主拉起，
运行状态用 `docker compose exec maigo /app/maigo host status` 查询。

compose 默认使用清华 APK/PyPI 与 `goproxy.cn` 构建，可在 `.env` 中覆盖：

```bash
MAIGO_APK_MIRROR=mirrors.tuna.tsinghua.edu.cn
MAIGO_PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
MAIGO_GOPROXY=https://goproxy.cn,direct
MAIGO_GOSUMDB=sum.golang.google.cn
MAIBOT_PLUGIN_DEP_INDEX_URLS=https://pypi.tuna.tsinghua.edu.cn/simple
```

Docker Hub 拉取基础镜像失败时，把 `deploy/daemon.json.example` 的
`registry-mirrors` 合并进 Docker 的 `daemon.json` 并重启引擎。

端口冲突时：

```bash
MAIBOT_WEB_PORT=8080 docker compose up -d
```

首次启动自动生成配置与 `MAIBOT_SECRET_KEY`，密钥保存在 data 卷的 `.secret_key`。
模型 `api_key` 在 WebUI 填写，内置适配器在 WebUI「适配器设置」启用并配置。

### 非 Docker

Linux 需 cgo 与 `libwebp`。装好 `maibot-dashboard==1.10.5` 后把 `MAIBOT_DASHBOARD_DIR` 指向其 `dist`；
装好 `maibot==1.4.6` 后把 `MAIBOT_PLUGIN_PYTHON` 指向该解释器（依赖集合见 Dockerfile）。
非 Docker 首次启动需设置 `MAIBOT_SECRET_KEY`（`openssl rand -base64 32`）。

systemd 宿主部署（静态二进制 + 自动部署）见 [deploy/host/README.md](deploy/host/README.md)：
CI 在 push `main` 时构建并通知服务器，服务器从镜像加速源拉包、校验、切换，失败自动回滚。

## 榫卯（Sunmao）· Go 平台特调系统

主线代码与 Python 逐行对齐，Go 平台独有的并发、批处理等优化不写进主线，而是通过**榫卯**在启动装配时插入：结构（业务主线）不动，只替换局部构件。槽位是榫眼，实现是榫头。

- `internal/runtime/execution` — 执行模式与功能开关
- `internal/runtime/execution/policy` — 插槽与注册表（`Slot[T]` / `Registry`）
- `config/runtime.toml` — 持久化配置，与 Python 的 `bot_config.toml` 互不影响

每个槽位有两个实现：

| 实现 | 含义 |
| --- | --- |
| `compatibility` | 默认，行为与 Python 一致 |
| `native` | Go 特调实现，仅在 `mode = "native"` 且对应开关打开时装配 |

业务代码只读取槽位，不出现 `if native` 判断；新增特调只需定义接口、注册 typed 槽、在装配时 `Set`。

```toml
# config/runtime.toml
[execution]
mode = "compatibility"   # compatibility | native
worker_pool_size = 0     # 仅 native 生效，0 表示按 CPU 核心数

[features]
native_session_scheduler = false  # 已接入
native_message_committer = false  # 已接入
```

当前已接入的开关：`native_session_scheduler`（会话并发调度）、`native_message_committer`（入站提交定序）。其余开关在 `FeatureDescriptions()` 中按阶段登记，标记为未接入时 WebUI 只展示规划状态，不允许开启。

浏览器打开 `http://<host>:7468/go` 调整执行模式与开关；宿主模式下作用于当前选中的 Bot。

临时覆盖模式：`MAIBOT_EXECUTION_MODE=native`。设置接口在 `/api/webui/go/settings`。

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
