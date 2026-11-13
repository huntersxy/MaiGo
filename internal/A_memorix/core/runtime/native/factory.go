package native

// 宿主接线入口，把原生装配暴露为 hostadapter.ServicesFactory

import (
	"fmt"

	memruntime "maibot-go/internal/A_memorix/core/runtime"
)

// ServicesFactory 返回 hostadapter.ServicesFactory，宿主传入插件根目录与配置后完成装配
func ServicesFactory(opts Options) func(kernel *memruntime.Kernel) (memruntime.KernelServices, error) {
	return func(kernel *memruntime.Kernel) (memruntime.KernelServices, error) {
		runtime, err := OpenKernel(kernel, opts)
		if err != nil {
			return memruntime.KernelServices{}, fmt.Errorf("A_Memorix 原生内核装配失败: %w", err)
		}
		return runtime.Services(), nil
	}
}

// OpenFactory 返回可直接交给 memorix.HostServiceOptions 的工厂函数签名实现
func OpenFactory(opts Options) func(pluginRoot string, config map[string]any) (*Runtime, error) {
	return func(pluginRoot string, config map[string]any) (*Runtime, error) {
		next := opts
		next.PluginRoot = pluginRoot
		next.Config = config
		return Open(next)
	}
}
