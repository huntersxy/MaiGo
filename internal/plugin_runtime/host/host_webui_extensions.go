package host

import (
	"fmt"
	"strings"
)

// ApplyWebUIExtension 校验并保存插件 WebUI 扩展快照；nil 表示插件未声明
func (h *Host) ApplyWebUIExtension(pluginID string, raw map[string]any) error {
	if h == nil {
		return fmt.Errorf("Host 未初始化")
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("plugin_id 不能为空")
	}
	normalized, err := ValidateWebUIExtension(raw)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if normalized == nil {
		delete(h.webuiExtensions, pluginID)
	} else {
		h.webuiExtensions[pluginID] = normalized
	}
	h.mu.Unlock()
	h.notifyStatusChanges()
	return nil
}

// GetWebUIExtensions 返回全部插件 WebUI 扩展快照
func (h *Host) GetWebUIExtensions() map[string]map[string]any {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]map[string]any, len(h.webuiExtensions))
	for pluginID, extension := range h.webuiExtensions {
		out[pluginID] = cloneAnyMap(extension)
	}
	return out
}

// ReplaceWebUIDocuments 热替换自定义页面 HTML，对齐 replace_webui_documents
func (h *Host) ReplaceWebUIDocuments(pluginID string, payload map[string]any) error {
	if h == nil {
		return fmt.Errorf("Host 未初始化")
	}
	pluginID = strings.TrimSpace(pluginID)
	h.mu.RLock()
	extension := cloneAnyMap(h.webuiExtensions[pluginID])
	h.mu.RUnlock()
	if extension == nil {
		return fmt.Errorf("插件 %s 未注册 WebUI 扩展", pluginID)
	}
	rawDocuments, ok := payload["documents"].([]any)
	if !ok || len(rawDocuments) > 20 {
		return fmt.Errorf("documents 必须是列表")
	}
	type documentKey struct{ ID, Entry string }
	documents := map[documentKey]string{}
	expected := map[documentKey]struct{}{}
	totalBytes := 0
	pages, _ := extension["pages"].([]any)
	for _, rawPage := range pages {
		page, ok := rawPage.(map[string]any)
		if !ok || stringValue(page["mode"]) != "custom" {
			continue
		}
		expected[documentKey{ID: stringValue(page["id"]), Entry: stringValue(page["entry"])}] = struct{}{}
	}
	for index, rawDocument := range rawDocuments {
		document, ok := rawDocument.(map[string]any)
		if !ok {
			return fmt.Errorf("documents[%d] 必须是对象", index)
		}
		key := documentKey{ID: stringValue(document["id"]), Entry: stringValue(document["entry"])}
		content := stringValue(document["document"])
		if content == "" {
			return fmt.Errorf("自定义页面必须非空")
		}
		totalBytes += len(content)
		if totalBytes > MaxWebUIDocumentsBytes {
			return fmt.Errorf("自定义页面总量不能超过 8 MiB")
		}
		if _, duplicate := documents[key]; duplicate {
			return fmt.Errorf("页面声明已改变或重复，请重载插件")
		}
		documents[key] = content
	}
	if len(documents) != len(expected) {
		return fmt.Errorf("页面声明已改变或重复，请重载插件")
	}
	for key := range documents {
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("页面声明已改变或重复，请重载插件")
		}
	}
	for _, rawPage := range pages {
		page, ok := rawPage.(map[string]any)
		if !ok || stringValue(page["mode"]) != "custom" {
			continue
		}
		key := documentKey{ID: stringValue(page["id"]), Entry: stringValue(page["entry"])}
		page["document"] = documents[key]
	}
	return h.ApplyWebUIExtension(pluginID, extension)
}
