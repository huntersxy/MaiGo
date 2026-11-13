package pluginruntime

import (
	"sort"
	"strings"
)

// FindDuplicatePluginIDs 扫描插件目录，找出被多个目录重复声明的插件 ID
// 对齐 Python integration.py:1829-1845 _find_duplicate_plugin_ids
func (validator *ManifestValidator) FindDuplicatePluginIDs(pluginDirs []string) map[string][]string {
	locations := map[string][]string{}
	for _, record := range validator.IterPluginManifests(pluginDirs, false, false) {
		manifest := record.Manifest()
		if manifest == nil {
			continue
		}
		pluginID := strings.TrimSpace(manifest.ID)
		if pluginID == "" {
			continue
		}
		locations[pluginID] = append(locations[pluginID], record.Path)
	}
	duplicates := map[string][]string{}
	for pluginID, paths := range locations {
		unique := dedupeSortedPaths(paths)
		if len(unique) > 1 {
			duplicates[pluginID] = unique
		}
	}
	return duplicates
}

// BuildDuplicatePluginBlockReasons 为每个重复插件 ID 构建可直接展示的隔离原因
// 对齐 Python integration.py:1847-1855 _build_duplicate_plugin_block_reasons
func (validator *ManifestValidator) BuildDuplicatePluginBlockReasons(pluginDirs []string) map[string]string {
	duplicates := validator.FindDuplicatePluginIDs(pluginDirs)
	reasons := make(map[string]string, len(duplicates))
	for pluginID, paths := range duplicates {
		reasons[pluginID] = "插件 ID 重复，已阻止加载；冲突目录: " + strings.Join(paths, ", ")
	}
	return reasons
}

// DiscoverLLMProviderConflicts 扫描 Manifest，发现 LLM Provider client_type 冲突
// 对齐 Python integration.py:424-465 _discover_llm_provider_conflicts
func (validator *ManifestValidator) DiscoverLLMProviderConflicts(pluginDirs []string, excludedPluginIDs map[string]struct{}) map[string]string {
	owners := map[string][]string{}
	for _, record := range validator.IterPluginManifests(pluginDirs, true, true) {
		manifest := record.Manifest()
		if manifest == nil {
			continue
		}
		pluginID := strings.TrimSpace(manifest.ID)
		if pluginID == "" {
			continue
		}
		if _, excluded := excludedPluginIDs[pluginID]; excluded {
			continue
		}
		for _, provider := range manifest.LLMProviders {
			clientType := strings.TrimSpace(provider.ClientType)
			if clientType == "" {
				continue
			}
			owners[clientType] = append(owners[clientType], pluginID)
		}
	}
	reasons := map[string]string{}
	for clientType, pluginIDs := range owners {
		unique := dedupeSortedStrings(pluginIDs)
		if len(unique) <= 1 {
			continue
		}
		reason := "LLM Provider client_type 冲突: " + clientType + " 被以下插件重复声明: " + strings.Join(unique, ", ")
		for _, pluginID := range unique {
			reasons[pluginID] = reason
		}
	}
	return reasons
}

// dedupeSortedPaths 去重并按字符串排序
func dedupeSortedPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	sort.Strings(unique)
	return unique
}

// dedupeSortedStrings 去重并按字符串排序
func dedupeSortedStrings(values []string) []string {
	return dedupeSortedPaths(values)
}
