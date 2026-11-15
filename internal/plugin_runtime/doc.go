// Package pluginruntime 实现 MaiBot 插件运行时的 Go 侧 Host。
//
// 分层：protocol（Envelope/Codec/错误码）、transport（TCP/UDS/Named Pipe）、
// rpc（Runner RPC、cap.call 授权、日志桥）、capabilities（全表能力实现）、
// host（能力注册与授权、组件注册、Hook 调度、工具与命令执行）、supervisor
// （Runner 阶段状态机、重启上限、健康巡检、插件装载状态）。
//
// 已知差异（消息网关未复刻完整驱动状态机、WebUI schema 校验为结构化子集等）
// 以 MaiBot 原版实现为准。
package pluginruntime
