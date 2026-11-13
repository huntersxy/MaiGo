package host

import (
	"fmt"
	"strings"
)

// SetUploadStore 注入 WebUI 上传暂存库；nil 表示上传端口未接线
func (h *Host) SetUploadStore(store *UploadStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.uploadStore = store
	h.mu.Unlock()
}

// UploadStore 返回 WebUI 上传暂存库，未接线时为 nil
func (h *Host) UploadStore() *UploadStore {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.uploadStore
}

// SetPluginDataDirResolver 注入插件数据目录解析器
func (h *Host) SetPluginDataDirResolver(resolver func(pluginID string) (string, error)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pluginDataDir = resolver
	h.mu.Unlock()
	h.loadPluginManagementAliases()
}

// PluginDataDir 解析插件数据目录，未接线时返回明确错误
func (h *Host) PluginDataDir(pluginID string) (string, error) {
	if h == nil {
		return "", fmt.Errorf("Host 未初始化")
	}
	h.mu.RLock()
	resolver := h.pluginDataDir
	h.mu.RUnlock()
	if resolver == nil {
		return "", fmt.Errorf("插件数据目录端口未接线：主程序尚未注入对应服务")
	}
	return resolver(strings.TrimSpace(pluginID))
}

// StageWebUIUpload 为 WebUI 上传暂存图片并签发一次性领取 token
func (h *Host) StageWebUIUpload(pluginID string, raw []byte) (string, error) {
	if h == nil {
		return "", fmt.Errorf("Host 未初始化")
	}
	store := h.UploadStore()
	if store == nil {
		return "", fmt.Errorf("WebUI 上传端口未接线：主程序尚未注入上传暂存库")
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return "", fmt.Errorf("插件标识不能为空")
	}
	return store.Stage(pluginID, raw)
}
