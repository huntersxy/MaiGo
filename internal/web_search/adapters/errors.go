package adapters

import "errors"

// ErrWebSearch 可安全返回给模型的联网错误
var ErrWebSearch = errors.New("web search")

// Error 可安全返回给模型的联网错误
//
// 与父包 websearch.Error 语义一致；放在 adapters 是为了让各 adapter 自足，
// 不必反向引用父包（Python 侧 adapters/*.py 只 import ..models 与 .http）。
type Error struct {
	msg string
}

func (e *Error) Error() string { return e.msg }

// NewError 构造联网错误
func NewError(msg string) error {
	return &Error{msg: msg}
}

// ErrNotConfigured StepSearch MCP 未注入管理器或未配置服务器/工具名
var ErrNotConfigured = errors.New("websearch: StepSearch MCP 未配置")
