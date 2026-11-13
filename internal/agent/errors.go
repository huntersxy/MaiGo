package agent

import "errors"

var (
	// ErrDisabled 外接 Agent 未启用
	ErrDisabled = errors.New("acpagent: 外接 Agent 未启用")

	// ErrNotConnected ACP 桥未建立或已关闭
	ErrNotConnected = errors.New("acpagent: ACP 未连接")
)
