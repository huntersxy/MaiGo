package replyer

// SendSegmentResult 单段可见回复的真实发送结果
//
// 对齐 Python maisaka/builtin_tool/reply.py::_build_send_result：
// 未发送的段 Success=false，已发送的段带真实 message_id。
// 定义在 replyer 是为了让发送循环（SendSegments）与工具层共用同一结构，
// 工具层通过类型别名保持原有字段访问方式。
type SendSegmentResult struct {
	Index     int    `json:"index"`
	Segment   string `json:"segment"`
	SetQuote  bool   `json:"set_quote"`
	Success   bool   `json:"success"`
	MessageID string `json:"message_id"`
}

// SendOutcome 一次可见回复发送的逐段结果
type SendOutcome struct {
	// Segments 按段记录真实发送结果，未发送的段 Success=false
	Segments []SendSegmentResult
	// SetQuote 调用方传入的引用意图
	SetQuote bool
	// EffectiveSetQuote 首段实际生效的引用
	EffectiveSetQuote bool
	// Sent 至少发送成功一段且没有段失败
	Sent bool
}
