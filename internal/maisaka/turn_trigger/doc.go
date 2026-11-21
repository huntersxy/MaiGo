// Package turn_trigger 承载 Maisaka 消息触发：决定新消息何时进入 Planner
//
// Python 对照：src/maisaka/turn_trigger/
//   - scheduler.py   MessageTurnScheduler 流水线编排
//
// 运行时通过 RuntimeView 注入，避免与 internal/agent 形成循环依赖
package turn_trigger
