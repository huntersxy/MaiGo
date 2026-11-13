package pluginmarket

import (
	"strings"
	"sync"
)

// 插件操作锁标签，对齐 management._PLUGIN_OPERATION_LABELS
var pluginOperationLabels = map[string]string{
	"install":   "安装",
	"uninstall": "卸载",
	"update":    "更新",
}

// pluginOperationGuard 对齐 management._reserve_plugin_operation 的进程内互斥
type pluginOperationGuard struct {
	mu     sync.Mutex
	active map[string]string
}

func newPluginOperationGuard() *pluginOperationGuard {
	return &pluginOperationGuard{active: map[string]string{}}
}

// Reserve 为插件保留操作权；冲突时立即返回 409，不等待也不覆盖原进度
func (s *Service) Reserve(pluginID, operation string) (func(), error) {
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return nil, err
	}
	keys := []string{"id:" + strings.ToLower(pluginID)}
	target, oldFormat, err := CandidatePaths(pluginsRoot, pluginID)
	if err == nil {
		keys = append(keys, "path:"+strings.ToLower(target), "path:"+strings.ToLower(oldFormat))
	}
	if installed, err := FindInstalledPlugin(pluginsRoot, pluginID); err == nil && installed != "" {
		keys = append(keys, "path:"+strings.ToLower(installed))
	}

	s.mu.RLock()
	guard := s.operations
	s.mu.RUnlock()
	if guard == nil {
		return func() {}, nil
	}
	guard.mu.Lock()
	for _, key := range keys {
		if conflicting, ok := guard.active[key]; ok {
			guard.mu.Unlock()
			label := pluginOperationLabels[conflicting]
			if label == "" {
				label = conflicting
			}
			return nil, conflictError("插件 %s 正在执行%s操作，请等待完成后重试", pluginID, label)
		}
	}
	for _, key := range keys {
		guard.active[key] = operation
	}
	guard.mu.Unlock()
	return func() {
		guard.mu.Lock()
		defer guard.mu.Unlock()
		for _, key := range keys {
			if guard.active[key] == operation {
				delete(guard.active, key)
			}
		}
	}, nil
}
