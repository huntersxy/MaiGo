// Package maimmsg 对齐 Python 包 maim_message 的线级 JSON 形态
//
// 权威来源：maim_message/message_base.py（MessageBase、BaseMessageInfo、Seg 等）
// MaiBot 在 platform_io 与 legacy WS/API 路径上使用该结构；Go 侧 platformio 已有 practical 类型，
// 本包供后续 WS 编解码与 Platform IO 驱动共用 wire 字段名
//
// 当前阶段：类型与 JSON 往返，不含网络服务
package maimmsg
