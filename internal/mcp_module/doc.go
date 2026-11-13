// Package mcp 对接外部 MCP 服务器，为 Agent 提供共享的工具、Prompt 与 Resource。
//
// 进程级 Service 单例持有连接，Agent 每轮 generation 按 ConfigSignature 比对运行时
// 配置并在变化时重建管理器；执行中的调用经 Lease 持租约，旧管理器在租约释放后关闭。
//
// 工具默认用服务端裸名，跨服务器重名时按 MaiBot src/core/tooling.py _list_routes 改写为
// {provider_type}_{server}_{name}，超过 64 字节或仍冲突时追加 sha256 摘要。
//
// stdio 传输丢弃 stdout 上的非 JSON-RPC 噪声行（对齐 stdio_filter.py）；roots/list、
// sampling/createMessage、elicitation/create 由 HostCallbacks 处理。
package mcp
