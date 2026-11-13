package platformio

import "errors"

var (
	errStopPartial             = errors.New("部分适配器停止失败")
	errBindingKindMismatch     = errors.New("路由绑定的 driver_kind 与适配器不一致")
	errBindingPlatformMismatch = errors.New("路由绑定的 platform 与适配器不一致")
	errBindingAccountMismatch  = errors.New("路由绑定的 account_id 与适配器冲突")
	errBindingScopeMismatch    = errors.New("路由绑定的 scope 与适配器冲突")
)
