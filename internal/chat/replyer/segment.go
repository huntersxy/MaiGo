package replyer

// Segment 对齐 utils.ProcessedResponseSegment
type Segment struct {
	Text           string
	QuotePrevious  bool
	Separator      string
	IsCorrection   bool
	TypingDelaySec float64 // 发送本条前模拟打字等待（秒），由 timing 计算
}
