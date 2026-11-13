package desktoppet

import "errors"

var (
	// ErrUnsupportedPlatform 非 Windows
	ErrUnsupportedPlatform = errors.New("desktoppet: 桌宠目前仅支持 Windows 桌面环境")
)
