// Package turn_trigger 承载 Maisaka 消息触发：决定新消息何时进入 Planner
//
// Python 对照：src/maisaka/turn_trigger/
//   - scheduler.py   MessageTurnScheduler 流水线编排
//
// 群聊 Planner 轮数：按回合开始时的有效频率计算常规额度 ceil(16×频率)，
// 频率低于 0.5 时同一回合最多 8 轮，否则最多 16 轮。超额续轮产生轮数欠债；
// 欠债期间暂停静默查看与 wait 定时唤醒，概率放行批次可抵扣欠债。
//
// 运行时通过 RuntimeView 注入，避免与 internal/agent 形成循环依赖
package turn_trigger
