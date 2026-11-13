// Package internalchat 实现 WebUI 与插件共用的内部聊天平台
// 对齐 MaiBot src/core/internal_chat.py 与 src/webui/routers/chat 的广播协议
// hub.go 按 private/group 频道隔离连接与历史，virtual.go 对应 VirtualIdentityConfig
// profile.go 对应 src/services/internal_chat_profile.py，relay.go 对应 relay_plugin_chat_message
package internalchat
