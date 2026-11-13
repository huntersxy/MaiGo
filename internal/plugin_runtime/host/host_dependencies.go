package host

import (
	"context"
	"sort"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/common/version"
)

// SyncPluginDependencies 执行插件 Python 依赖分析/补装，并把最终阻止列表写回 Host
// 返回执行结果与本次发生变化的插件 ID，对齐 integration._sync_plugin_dependencies
func (h *Host) SyncPluginDependencies(
	ctx context.Context,
	runner DependencyRunner,
	pluginDirs []string,
	initialBlockedPluginReasons map[string]string,
) (DependencySyncResult, []string) {
	if runner == nil {
		runner = h.dependencyRunner
	}
	if runner == nil {
		// 未注入流水线时只做冲突扫描，返回空结果而不是 panic：
		// 单测与最小化部署不加载依赖流水线，插件仍应能按现状启动。
		blocked := mergeBlockedReasons(initialBlockedPluginReasons, h.conflictBlockedReasons(pluginDirs))
		h.SetBlockedPluginReasons(blocked)
		return DependencySyncResult{BlockedPluginReasons: blocked}, nil
	}
	// 对齐 Python integration.py:639-663：先扫重复插件 ID 与 LLM Provider 冲突，再交给依赖流水线
	merged := mergeBlockedReasons(initialBlockedPluginReasons, h.conflictBlockedReasons(pluginDirs))
	result := runner.SyncBlockedReasons(ctx, pluginDirs, merged)
	previous := h.BlockedPluginReasons()
	next := result.BlockedPluginReasons
	changedSet := make(map[string]struct{})
	for pluginID, reason := range previous {
		if next[pluginID] != reason {
			changedSet[pluginID] = struct{}{}
		}
	}
	for pluginID, reason := range next {
		if previous[pluginID] != reason {
			changedSet[pluginID] = struct{}{}
		}
	}
	h.SetBlockedPluginReasons(next)
	changed := make([]string, 0, len(changedSet))
	for pluginID := range changedSet {
		changed = append(changed, pluginID)
	}
	sort.Strings(changed)
	return result, changed
}

// conflictBlockedReasons 扫描重复插件 ID 与 LLM Provider client_type 冲突
// 对齐 Python integration.py:639-663：重复 ID 先记日志，冲突原因并入阻止列表
func (h *Host) conflictBlockedReasons(pluginDirs []string) map[string]string {
	if h.manifestConflicts == nil {
		// 未注入扫描器时不做冲突判定；插件按原有目录顺序加载
		return nil
	}
	reasons := h.manifestConflicts.ScanManifestConflicts(pluginDirs, version.String(), h.ForcePluginCompatibility())
	if len(reasons) > 0 {
		details := make([]string, 0, len(reasons))
		for pluginID, reason := range reasons {
			details = append(details, pluginID+": "+reason)
		}
		sort.Strings(details)
		zap.L().Error("检测到重复插件 ID，冲突插件将被隔离，其余插件继续加载",
			zap.String("details", strings.Join(details, "; ")))
	}
	return reasons
}

// mergeBlockedReasons 合并两组阻止原因，后者覆盖同键
func mergeBlockedReasons(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(extra))
	for pluginID, reason := range base {
		merged[pluginID] = reason
	}
	for pluginID, reason := range extra {
		merged[pluginID] = reason
	}
	return merged
}
