package maisaka

// BaseReplyFrequency 返回当前会话未乘倍率的回复频率，供 Host 频率能力只读查询
func (r *Runtime) BaseReplyFrequency() float64 {
	if r == nil {
		return 0
	}
	return r.opts.ReplyFrequency()
}

// TalkFrequencyAdjust 返回当前会话的回复频率倍率，供 Host 频率能力只读查询
func (r *Runtime) TalkFrequencyAdjust() float64 {
	if r == nil {
		return 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.talkFrequencyAdjust
}
