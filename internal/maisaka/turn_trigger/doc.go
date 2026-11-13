// Package turn_trigger 承载 Maisaka 消息触发：决定新消息何时进入 Planner
//
// Python 对照：src/maisaka/turn_trigger/
//   - scheduler.py   MessageTurnScheduler 流水线编排
//   - gates.py       DynamicReplyTurnGate 动态回复门控
//   - dynamic_gate.py DynamicReplyGate 虚拟轮次与阈值窗口
//   - idle_backoff.py IdleBackoffController 连续空闲退避
//   - reply_likelihood.py 回复概率拟合权重
//
// 运行时通过 RuntimeView 注入，避免与 internal/agent 形成循环依赖
package turn_trigger
