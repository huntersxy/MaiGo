// Package server 提供旧版 maim_message WebSocket 服务骨架（/ws），供外部适配器
// 接入 Platform IO。
//
// 默认关闭，MAIBOT_MAIM_MESSAGE_ENABLE=1 启用；监听地址、端口与 token 分别由
// MAIBOT_MAIM_MESSAGE_HOST/PORT/TOKEN 指定。
//
// 协议对齐 maim_message MessageServer：握手头 platform 与 authorization（原始
// token，无 Bearer 前缀），入站帧为 MessageBase JSON。
package server
