package policy

// MessageCommitter 决定入站事件提交到业务链的执行方式。
//
// 业务代码只把「落库、入缓冲、思考调度」封装成 run 交给 Submit，
// 具体是就地执行还是按会话定序由装配的实现决定。
type MessageCommitter interface {
	// Submit 提交一条待执行事件；kind 为 group/private，targetID 为会话目标。
	// seq 是会话内到达序号，0 表示无需定序的自发事件。
	Submit(kind string, targetID int64, seq uint64, run func())
	// Skip 消费一个不会产生实际处理的序号，避免定序实现等待永久缺口。
	Skip(kind string, targetID int64, seq uint64)
	// Shutdown 停止接收并等待在途事件结束。
	Shutdown()
}

// InlineCommitter 是 compatibility 实现：就地执行，不排队。
//
// 对齐 Python PlatformIOManager._schedule_inbound_dispatch：入站消息各自
// 作为独立异步任务运行，没有队列、序号或重排。
type InlineCommitter struct{}

// Submit 立即执行事件。
func (InlineCommitter) Submit(_ string, _ int64, _ uint64, run func()) {
	if run != nil {
		run()
	}
}

// Skip 兼容实现没有定序水位。
func (InlineCommitter) Skip(string, int64, uint64) {}

// Shutdown 兼容实现没有后台资源。
func (InlineCommitter) Shutdown() {}
