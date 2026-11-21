# 原生适配器对齐记录

本轮参考仓库内置的 QQ 官方 `0.4.7`、Telegram `0.1.17` 与微信 ClawBot `0.2.0` Python 源码。MaiBot 上游 `edf42446` 已包含 QQ 官方 `0.5.0`，其中的 UnionID 绑定与配置升级尚未迁移。

## Go 与 Python 的类型边界

- 通用 JSON 对象将整数保存在 `int64` 中，超出范围的整数保留为 `json.Number`。消息 ID 在宿主消息里使用字符串。Telegram Bot API 的 chat ID、message ID 和 UTF-16 偏移按协议发送为整数。QQ 上传的文件大小和分片大小按协议发送为字符串，序号仍为整数。
- 原生配置根据 SDK schema 处理默认值、布尔、整数、浮点和字符串列表。显式 `null`、小数形式的非整数和越界配置会报错。Python 配置整数可以任意大，Go 配置整数以目标架构的 `int` 为界。
- Go 适配器从宿主的 `OutboundMessageEnvelope` 获取路由、消息段和附件。QQ 私聊被动回复锚点与显示引用分别处理，Telegram 只有显式 reply 段才生成 Bot API 的回复参数。
- FFmpeg 按平台从 PATH 或既有插件环境的媒体工具目录解析。Go 的 JPEG、PNG、WebP 编码器与 Pillow 可以产生不同字节，平台侧行为按方法、尺寸、媒体类型和内容核对。
- Linux 发布版与 Docker 镜像静态链接 rlottie 渲染 Telegram TGS。自定义构建使用 `-tags=rlottie`、CGO 和 rlottie 开发库。TGS 按矢量贴纸规格处理，GIF 调色板由 Go 生成。APNG 和 WebP 出站按原帧序和时长生成 GIF，动画解码受 Go 的像素预算限制。

## 本轮覆盖与待补差异

| 适配器 | 本轮覆盖 | 待补差异 |
| --- | --- | --- |
| QQ 官方 | 长 ID、@ 与正文顺序、表情与引用图片、语音和文件入站、手工 ID 绑定和 WebUI、分段出站及上传、REFIDX、被动窗口与序号、群名及头像查询、三条指纹身份匹配、禁言/撤回/踢人 | QQ 真实账号的网关及管理权限验收 |
| Telegram | 长 ID、论坛话题、表态收发、UTF-16 @、图片/贴纸/语音收发、转发与相册、显式引用、头像 API、打字 Hook、TGS 入站、APNG/WebP 动图出站 | Telegram 真实账号的媒体和工具验收 |
| 微信 ClawBot | 扫码流程、多账号操作及空二维码类型、iLink 图片加解密与分段发送、出入站用户过滤、持久化去重和游标提交 | 真机扫码、媒体和掉线恢复的线上验收 |

协议回放覆盖真实 Go 入口。QQ 走本地 WebSocket 和 OpenAPI，Telegram 走本地 Bot API，微信走本地 iLink 与媒体接口。账号与令牌使用测试值。
