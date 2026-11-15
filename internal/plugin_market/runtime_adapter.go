package pluginmarket

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/plugin_runtime"
)

// NewRuntimeCoordinator 用插件运行时 Host 构造安装链所需的运行时协调器
func NewRuntimeCoordinator(host *pluginruntime.Host) RuntimeCoordinator {
	return &runtimeHostAdapter{host: host, pipeline: pluginruntime.NewPluginDependencyPipeline("")}
}

// NewRuntimeCoordinatorWithRunner 构造协调器并带上重启 Runner 所需的启动参数
// 依赖环境变化后按同一套参数重启，避免丢失分组与 Python 解释器配置
func NewRuntimeCoordinatorWithRunner(host *pluginruntime.Host, runnerOptions pluginruntime.StartRunnerOptions) RuntimeCoordinator {
	return &runtimeHostAdapter{
		host:          host,
		pipeline:      pluginruntime.NewPluginDependencyPipeline(""),
		runnerOptions: runnerOptions,
	}
}

// runtimeHostAdapter 把 pluginruntime.Host 适配为 RuntimeCoordinator
type runtimeHostAdapter struct {
	host *pluginruntime.Host
	// pipeline 安装后同步插件 Python 依赖，对齐 Python _sync_plugin_dependencies
	pipeline *pluginruntime.PluginDependencyPipeline
	// runnerOptions 依赖环境变化后重启 Runner 使用；零值时按默认第三方分组重启
	runnerOptions pluginruntime.StartRunnerOptions
}

// ActivateInstalledPlugin 同步依赖后通过 Runner 加载刚安装的插件
// 对齐 Python integration.py:345-360 activate_installed_plugin：
// 先跑依赖流水线，环境或阻止列表变化时重启运行时，否则直接加载目标插件
func (a *runtimeHostAdapter) ActivateInstalledPlugin(ctx context.Context, pluginID string) error {
	if a == nil || a.host == nil {
		return nil
	}
	syncResult, changed := a.host.SyncPluginDependencies(ctx, a.pipeline, a.host.PluginDirs(), nil)
	if syncResult.EnvironmentChanged || len(changed) > 0 {
		// 依赖环境或隔离状态变化后，已有 Runner 进程需要读取最新状态
		if err := a.host.RestartRunner(ctx, a.runnerOptions); err != nil {
			return fmt.Errorf("安装后重启插件运行时失败: %w", err)
		}
	}
	result, err := a.host.ReloadPlugins(ctx, []string{pluginID}, "install", nil)
	if err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("插件加载失败：%s", formatFailedPlugins(result.FailedPlugins))
	}
	return nil
}

// UnloadPlugins 停止指定插件并返回已停止列表
func (a *runtimeHostAdapter) UnloadPlugins(ctx context.Context, pluginIDs []string, reason string) ([]string, error) {
	if a == nil || a.host == nil {
		return nil, nil
	}
	result, err := a.host.UnloadPlugins(ctx, pluginIDs, reason)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return result.UnloadedPlugins, fmt.Errorf("插件卸载失败：%s", formatFailedPlugins(result.FailedPlugins))
	}
	return result.UnloadedPlugins, nil
}

// ReloadPluginsGlobally 重新加载指定插件，失败时返回 false
func (a *runtimeHostAdapter) ReloadPluginsGlobally(ctx context.Context, pluginIDs []string, reason string) (bool, error) {
	if a == nil || a.host == nil {
		return false, nil
	}
	result, err := a.host.ReloadPlugins(ctx, pluginIDs, reason, nil)
	if err != nil {
		return false, err
	}
	return result.Success, nil
}

// formatFailedPlugins 输出失败插件与原因，便于安装链错误信息定位
func formatFailedPlugins(failed map[string]string) string {
	if len(failed) == 0 {
		return "运行时未返回失败详情"
	}
	names := make([]string, 0, len(failed))
	for name := range failed {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+": "+failed[name])
	}
	return strings.Join(parts, "; ")
}
