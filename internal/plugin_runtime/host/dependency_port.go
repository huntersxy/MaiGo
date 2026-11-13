package host

import "context"

// DependencySyncResult 依赖同步的结果摘要。
//
// Host 只关心「哪些插件被阻止」「环境是否变化」「安装失败原因」三项；
// 具体流水线（依赖解析、pip 补装、requirements 合并）属于集成层，
// 由上层实现并通过 DependencyRunner 注入。
type DependencySyncResult struct {
	BlockedPluginReasons map[string]string
	EnvironmentChanged   bool
	InstallError         string
}

// DependencyRunner 插件依赖分析流水线端口。
//
// 对齐 MaiBot src/plugin_runtime/dependency_pipeline.py：该模块位于包根目录
// （集成层职责），但 Host 在「启用插件前补齐依赖」和「应用配置」两条路径上
// 需要触发它。Go 的包边界不允许 host 反向引用集成层，因此声明为端口，
// 由集成层实现后注入。
type DependencyRunner interface {
	// SyncBlockedReasons 分析插件目录依赖并返回阻止原因
	SyncBlockedReasons(ctx context.Context, pluginDirs []string, initialBlocked map[string]string) DependencySyncResult
}

// ManifestConflictScanner 重复插件 ID 与 LLM Provider 冲突的扫描端口。
//
// 对齐 MaiBot integration.py:639-663 的冲突扫描：重复 ID 的插件要隔离，
// 同一 client_type 的 LLM Provider 声明冲突也要阻止加载。
// 该逻辑依赖 manifest 校验器（集成层），同样通过端口注入。
type ManifestConflictScanner interface {
	// ScanManifestConflicts 返回 pluginID -> 阻止原因
	ScanManifestConflicts(pluginDirs []string, hostVersion string, forceCompatibility bool) map[string]string
}
