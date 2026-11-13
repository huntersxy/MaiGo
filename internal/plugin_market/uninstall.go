package pluginmarket

import (
	"context"
	"os"
	"strings"

	"go.uber.org/zap"
)

// Uninstall 对齐 management.uninstall_plugin：先禁用并停止运行时，再删除插件目录
func (s *Service) Uninstall(ctx context.Context, pluginID string) (map[string]any, error) {
	pluginID = strings.TrimSpace(pluginID)
	if err := ValidatePluginID(pluginID); err != nil {
		return nil, err
	}
	s.reportStep("uninstall", pluginID, 10, "开始卸载插件: "+pluginID)
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return nil, err
	}
	pluginPath, err := FindInstalledPlugin(pluginsRoot, pluginID)
	if err != nil {
		return nil, err
	}
	if pluginPath == "" {
		s.reportProgressError("uninstall", pluginID, "插件不存在", "插件未安装或已被删除")
		return nil, notFoundError("插件未安装")
	}
	manifest, err := requiredManifest(pluginPath)
	if err != nil {
		return nil, err
	}
	pluginName := manifestNameText(manifest, pluginID)
	runtimePluginID, err := requireManifestPluginID(manifest)
	if err != nil {
		return nil, err
	}
	s.reportStep("uninstall", pluginID, 30, "正在卸载运行中的插件: "+pluginName)
	s.releaseRuntimeBeforeDelete(ctx, runtimePluginID, pluginPath)
	s.reportStep("uninstall", pluginID, 45, "正在删除插件文件: "+pluginPath)
	s.reportStep("uninstall", pluginID, 50, "正在删除 "+pluginName+"...")
	if err := RemoveTree(pluginPath); err != nil {
		if os.IsPermission(err) {
			s.reportProgressError("uninstall", pluginID, "卸载失败", "权限不足，无法删除插件文件")
			return nil, &MarketError{StatusCode: 500, Message: "权限不足，无法删除插件文件"}
		}
		s.reportProgressError("uninstall", pluginID, "卸载失败", err.Error())
		return nil, &MarketError{StatusCode: 500, Message: "服务器错误: " + err.Error()}
	}
	s.reportSuccess("uninstall", pluginID, "成功卸载插件: "+pluginName)
	return map[string]any{
		"success":     true,
		"message":     "插件卸载成功",
		"plugin_id":   pluginID,
		"plugin_name": pluginName,
	}, nil
}

// releaseRuntimeBeforeDelete 对齐 _release_plugin_runtime_before_delete：运行时失败只记录警告并继续删除
func (s *Service) releaseRuntimeBeforeDelete(ctx context.Context, runtimePluginID, pluginPath string) {
	if err := writePluginDisabledForUninstall(pluginPath); err != nil {
		zap.L().Warn("卸载前写入插件禁用配置失败，将继续删除文件",
			zap.String("plugin_id", runtimePluginID), zap.Error(err))
	}
	coordinator := s.runtimeCoordinator()
	if coordinator == nil {
		return
	}
	if _, err := coordinator.ReloadPluginsGlobally(ctx, []string{runtimePluginID}, "uninstall"); err != nil {
		zap.L().Warn("插件删除前运行时卸载失败，将继续尝试删除文件",
			zap.String("plugin_id", runtimePluginID), zap.Error(err))
	}
}
