package pluginmarket

import "context"

// ProgressUpdate 对齐 webui.routers.plugin.progress.update_progress 的进度快照
type ProgressUpdate struct {
	Operation     string  `json:"operation"`
	Stage         string  `json:"stage"`
	Progress      int     `json:"progress"`
	Message       string  `json:"message"`
	Error         *string `json:"error"`
	PluginID      *string `json:"plugin_id"`
	TotalPlugins  int     `json:"total_plugins"`
	LoadedPlugins int     `json:"loaded_plugins"`
	MirrorID      *string `json:"mirror_id,omitempty"`
	MirrorName    *string `json:"mirror_name,omitempty"`
	MirrorIndex   *int    `json:"mirror_index,omitempty"`
	TotalMirrors  *int    `json:"total_mirrors,omitempty"`
	Attempt       *int    `json:"attempt,omitempty"`
	MaxAttempts   *int    `json:"max_attempts,omitempty"`
}

// RuntimeCoordinator 对齐 plugin_runtime.integration.get_plugin_runtime_manager 的安装相关能力
type RuntimeCoordinator interface {
	// ActivateInstalledPlugin 首次安装后同步依赖并加载插件
	ActivateInstalledPlugin(ctx context.Context, pluginID string) error
	// UnloadPlugins 停止指定插件，返回已停止的插件 ID
	UnloadPlugins(ctx context.Context, pluginIDs []string, reason string) ([]string, error)
	// ReloadPluginsGlobally 重新加载指定插件，失败时返回 false
	ReloadPluginsGlobally(ctx context.Context, pluginIDs []string, reason string) (bool, error)
}

// reportStep 对齐 release_install.report_step
func (s *Service) reportStep(operation, pluginID string, progress int, message string) {
	update := ProgressUpdate{
		Operation: operation,
		Stage:     "loading",
		Progress:  progress,
		Message:   message,
		PluginID:  stringPointer(pluginID),
	}
	s.reportProgress(update)
}

// reportProgressError 对齐安装失败时的 error 进度
func (s *Service) reportProgressError(operation, pluginID, message, detail string) {
	update := ProgressUpdate{
		Operation: operation,
		Stage:     "error",
		Progress:  0,
		Message:   message,
		Error:     stringPointer(detail),
		PluginID:  stringPointer(pluginID),
	}
	s.reportProgress(update)
}

// reportSuccess 对齐安装成功时的 success 进度
func (s *Service) reportSuccess(operation, pluginID string, message string) {
	update := ProgressUpdate{
		Operation: operation,
		Stage:     "success",
		Progress:  100,
		Message:   message,
		PluginID:  stringPointer(pluginID),
	}
	s.reportProgress(update)
}
