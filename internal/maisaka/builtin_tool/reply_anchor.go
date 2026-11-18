package tools

import "context"

type replyAnchorContextKey struct{}

// WithReplyAnchor 把被动回复锚点放进工具上下文，供附件发送沿用
func WithReplyAnchor(ctx context.Context, anchor int64) context.Context {
	if ctx == nil || anchor <= 0 {
		return ctx
	}
	return context.WithValue(ctx, replyAnchorContextKey{}, anchor)
}

// ReplyAnchorFromContext 读取工具上下文里的被动回复锚点
func ReplyAnchorFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	anchor, _ := ctx.Value(replyAnchorContextKey{}).(int64)
	return anchor
}
