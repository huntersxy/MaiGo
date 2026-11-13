package hostadapter

// A_memorix 原生内核工厂接线：把 native 包的可扩展装配表接入宿主服务

import (
	"maibot-go/internal/A_memorix"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/native"
)

// NativeServicesFactory 使用 A_Memorix 原生装配构造宿主内核服务集合
func NativeServicesFactory(options native.Options) ServicesFactory {
	return native.ServicesFactory(options)
}

// NewNativeKernelFactory 用原生装配构造宿主内核工厂，日志走默认 zap 记录器
// 可传入宿主装配选项；不传时保持空 Options，供测试与无实例配置场景使用
func NewNativeKernelFactory(options ...native.Options) memorix.KernelFactory {
	opts := native.Options{}
	if len(options) > 0 {
		opts = options[0]
	}
	return NewKernelFactory(memruntime.NewZapLogger("memorix-native"), native.ServicesFactory(opts))
}

// NewNativeKernelFactoryWithProvider 每次重建内核时重新读取装配选项
// 供配置热重载后刷新摘要模型、任务表与消息端口，避免沿用启动快照
func NewNativeKernelFactoryWithProvider(provider func() native.Options) memorix.KernelFactory {
	return NewKernelFactory(memruntime.NewZapLogger("memorix-native"), func(kernel *memruntime.Kernel) (memruntime.KernelServices, error) {
		options := native.Options{}
		if provider != nil {
			options = provider()
		}
		return native.ServicesFactory(options)(kernel)
	})
}
