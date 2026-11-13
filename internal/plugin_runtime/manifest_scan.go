package pluginruntime

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

// LoadFromPluginPath 从插件目录读取并校验 _manifest.json
func (validator *ManifestValidator) LoadFromPluginPath(pluginPath string, requireEntrypoint bool) ManifestValidation {
	pluginPath = strings.TrimSpace(pluginPath)
	result := ManifestValidation{Source: pluginPath, Errors: []string{}, Warnings: []string{}}
	if pluginPath == "" {
		result.Errors = append(result.Errors, "缺少 _manifest.json")
		return result
	}
	manifestPath := filepath.Join(pluginPath, "_manifest.json")
	if info, err := os.Stat(manifestPath); err != nil || !info.Mode().IsRegular() {
		result.Errors = append(result.Errors, "缺少 _manifest.json")
		return result
	}
	if requireEntrypoint {
		entrypoint := filepath.Join(pluginPath, "plugin.py")
		if info, err := os.Stat(entrypoint); err != nil || !info.Mode().IsRegular() {
			result.Errors = append(result.Errors, "缺少 plugin.py")
			return result
		}
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		result.Errors = append(result.Errors, "manifest 解析失败: "+err.Error())
		return result
	}
	var payload any
	if err := sonic.Unmarshal(raw, &payload); err != nil {
		result.Errors = append(result.Errors, "manifest 解析失败: "+err.Error())
		return result
	}
	object, ok := payload.(map[string]any)
	if !ok {
		result.Errors = append(result.Errors, "manifest 顶层必须为 JSON 对象")
		return result
	}
	if id, ok := object["id"].(string); ok && strings.TrimSpace(id) != "" {
		result.Source = strings.TrimSpace(id)
	}
	parsed := validator.ParseManifest(object, result.Source, isPluginDisabledByConfig(pluginPath))
	if parsed.Source == "" {
		parsed.Source = result.Source
	}
	return parsed
}

// ReadPluginIDFromPluginPath 读取单个插件目录的规范化插件 ID
func (validator *ManifestValidator) ReadPluginIDFromPluginPath(pluginPath string, requireEntrypoint bool) (string, bool) {
	result := validator.LoadFromPluginPath(pluginPath, requireEntrypoint)
	if result.Manifest == nil {
		return "", false
	}
	return result.Manifest.ID, true
}

// IterPluginManifests 扫描插件根目录，仅返回校验成功的 Manifest
func (validator *ManifestValidator) IterPluginManifests(pluginDirs []string, requireEntrypoint, enabledOnly bool) []ManifestRecord {
	records := []ManifestRecord{}
	for _, rawRoot := range pluginDirs {
		root := strings.TrimSpace(rawRoot)
		if root == "" {
			continue
		}
		resolvedRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolvedRoot)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(resolvedRoot)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(left, right int) bool {
			return strings.ToLower(entries[left].Name()) < strings.ToLower(entries[right].Name())
		})
		for _, entry := range entries {
			if !entry.IsDir() || isReservedPluginDependencyDirectory(entry.Name()) {
				continue
			}
			candidatePath := filepath.Join(resolvedRoot, entry.Name())
			if enabledOnly && isPluginDisabledByConfig(candidatePath) {
				continue
			}
			validation := validator.LoadFromPluginPath(candidatePath, requireEntrypoint)
			if validation.Manifest == nil {
				continue
			}
			records = append(records, ManifestRecord{Path: candidatePath, Validation: validation})
		}
	}
	return records
}

// BuildPluginDependencyMap 构建 plugin_id 到依赖插件 ID 列表的映射
func (validator *ManifestValidator) BuildPluginDependencyMap(pluginDirs []string, requireEntrypoint, enabledOnly bool) map[string][]string {
	dependencyMap := map[string][]string{}
	for _, record := range validator.IterPluginManifests(pluginDirs, requireEntrypoint, enabledOnly) {
		dependencyMap[record.Validation.Manifest.ID] = record.Validation.Manifest.PluginDependencyIDs()
	}
	return dependencyMap
}
