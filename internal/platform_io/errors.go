package platformio

import "errors"

var (
	errEmptyRoutePlatform       = errors.New("RouteKey.platform 不能为空")
	errDuplicateAdapter         = errors.New("适配器已注册")
	errAdapterNotFound          = errors.New("适配器未注册")
	errManagerRunning           = errors.New("Manager 运行中不允许直接 Register，请改用 Add")
	errManagerRunningUnregister = errors.New("Manager 运行中不允许直接 Unregister，请改用 RemoveDriver")
)
