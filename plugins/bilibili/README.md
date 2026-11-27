# Bilibili（MaiBot / MaiGo 移植版）

从 [astrbot_plugin_bilibili](https://github.com/Soulter/astrbot_plugin_bilibili) 移植到 MaiBot SDK 的 Python Runner 插件。

## 功能

- UP 主动态 / 直播订阅推送
- BV / b23 链接解析
- 哔哩哔哩小程序分享解析
- Bangumi / B 站热门视频 LLM 工具
- 订阅管理 WebUI API

## 安装

放到实例 `plugins/` 目录（本仓库已内置在 `plugins/bilibili`）。
依赖由 `_manifest.json` 声明，宿主会按插件依赖流水线安装。
首次使用请配置 `sessdata`，或私聊管理员执行 `/bili_login`。

管理员命令（`/bili_login`、`/bili_logout`、`/bili_card_style`、全局订阅管理）走 MaiBot `permission="operator"`，
需要把使用者写进 `bot_config.toml` 的 `plugin.permission`，例如 `qq:123456789`。

## 命令

- `/bili_sub <UID> [过滤器...]` / `订阅动态`
- `/bili_sub_list` / `订阅列表`
- `/bili_sub_del <UID>` / `订阅删除`
- `/bili_global_list` / `全局列表`
- `/bili_sub_test <UID>` / `订阅测试`
- `/bili_card_style [样式]` / `卡片样式`
- `/bili_login` / `/bili_logout`

注意：没有单独的 `/bili` 命令；订阅列表用 `/bili_sub_list`。

## MaiBot 适配说明

- 订阅键使用 MaiBot `stream_id`（也兼容原 AstrBot UMO）
- 图片渲染走 `render.html2png`
- 主动推送走 `send.hybrid` / `send.forward`
- AI 摘要走 `llm.generate`
