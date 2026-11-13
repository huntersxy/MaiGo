// Package acpagent 对齐 MaiBot src/agent：外接 ACP Agent 与实验性 Planner 桥接。
//
// Python 对照：service.py（任务队列与 pause/resume）、acp.py（NDJSON stdio 与
// dsh Desktop TCP 桥）、acp_agent.py（会话与 turn）、mcp_server.py（MCP 工具服务）、
// provider.py（agent_talk/status/chats/recent）、planner.py / planner_state.py。
//
// 依赖缺失时返回真实错误，不伪造成功。差异见 agents/MODULE_ALIGNMENT.md。
package agent
