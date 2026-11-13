package computer

import "errors"

var (
	// ErrNotImplemented 共享电脑未启用且未显式开启本机调试
	// 名字沿用历史；语义是「当前配置下不提供电脑操作」，不是功能缺失
	ErrNotImplemented = errors.New("computer: 共享电脑未启用且未开启本机模式")

	// ErrDisabled 电脑服务未启用
	ErrDisabled = errors.New("computer: 电脑服务未启用")

	// ErrUnknownTool 未知工具名
	ErrUnknownTool = errors.New("computer: 未知电脑工具")

	// ErrCommandNotAllowed 本机模式命令未通过 MAIBOT_COMPUTER_LOCAL_ALLOWLIST
	ErrCommandNotAllowed = errors.New("computer: 本机命令不在允许列表中")
)
