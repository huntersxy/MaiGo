package news

import "errors"

// ErrSourceNotConfigured 未配置资讯源地址
//
// 正常启动时 NewService 会填入默认源，本错误只在显式置空时出现；
// 与 Python 一致：资讯是可选功能，未配置不算缺陷。
var ErrSourceNotConfigured = errors.New("news: 未配置资讯源地址")

// SourceError 对齐 NewsSourceError：远端不可用或数据无效
type SourceError struct {
	Msg string
}

func (e *SourceError) Error() string {
	if e == nil || e.Msg == "" {
		return "news: 资讯源错误"
	}
	return e.Msg
}
