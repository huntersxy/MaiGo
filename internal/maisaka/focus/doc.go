// Package focus 移植 MaiBot src/maisaka/focus 与相关决策纯函数
//
// 已覆盖：Focus 开关/白名单/共享组 scope、成员注意 roster、注意力漂移 prompt、引用计数与引用概率、会话级 SessionRuntime（引用 Counter + roster 快照）
// 未覆盖：FocusGroupRuntime 共享 Planner、检查点持久化、工具路由与 reconcile 运行态
package focus
