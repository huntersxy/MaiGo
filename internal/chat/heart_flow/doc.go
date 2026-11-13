// Package heartflow 对齐 MaiBot src/chat/heart_flow 的会话心流管理。
//
// MaisakaHeartFlowChatting 由 internal/maisaka.Runtime 承接（定时心跳、空闲触发、
// 消息防抖与 Planner 调用）；本包只负责会话实例生命周期（创建、LRU 淘汰、策略校验、
// 关停）、HeartFCMessageReceiver 的入站链路与 CycleDetail 记录结构。
//
// 依赖最小端口：SessionProvider、PolicyChecker、RuntimeFactory、ContextClearer、
// PlannerResetter；Runtime 由宿主传入，通常就是 *maisaka.Runtime。
package heartflow
