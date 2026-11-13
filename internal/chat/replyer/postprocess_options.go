package replyer

import "context"

// PostProcessOptions 单次回复的后处理覆盖项，对齐 Maisaka reply 工具的
// maisaka.reply.before_post_process Hook 返回值。
type PostProcessOptions struct {
	// Skip 跳过本次回复的全部文本后处理。
	Skip bool
	// EnableSplitter 覆盖本次回复的断句开关（nil 表示跟随配置）。
	EnableSplitter *bool
	// EnableTypo 覆盖本次回复的错别字注入开关（nil 表示跟随配置）。
	EnableTypo *bool
}

type postProcessOptionsKey struct{}

// WithPostProcessOptions 把单次回复的后处理覆盖项写入 context。
func WithPostProcessOptions(ctx context.Context, opts PostProcessOptions) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, postProcessOptionsKey{}, opts)
}

// PostProcessOptionsFrom 取出 context 中的后处理覆盖项。
func PostProcessOptionsFrom(ctx context.Context) (PostProcessOptions, bool) {
	if ctx == nil {
		return PostProcessOptions{}, false
	}
	opts, ok := ctx.Value(postProcessOptionsKey{}).(PostProcessOptions)
	return opts, ok
}
