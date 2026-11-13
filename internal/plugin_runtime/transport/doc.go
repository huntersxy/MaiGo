// Package transport 提供 Host ↔ Runner IPC 传输，对齐 src/plugin_runtime/transport/
//
// Windows 使用 Named Pipe（ListenNamedPipe / DialNamedPipe）；Unix 使用 UDS；任意平台可用 TCP
//
// 分帧协议（与 MaiBot transport.base 一致）：
//
//	4 字节大端无符号长度前缀 + MsgPack 载荷（单帧即一条 Envelope 的 encode 结果）
//	单帧最大 16 MiB
package transport
