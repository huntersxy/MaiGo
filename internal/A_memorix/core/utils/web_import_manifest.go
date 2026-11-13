package utils

// Web 导入清单管理，对齐 web_import_manager.py 的 manifest 相关方法

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// loadManifest 对齐 _load_manifest
func (m *WebImportManager) loadManifest() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadManifestLocked()
}

// loadManifestLocked 无锁加载清单
func (m *WebImportManager) loadManifestLocked() map[string]any {
	if m.manifestLoad {
		return m.manifest
	}
	m.manifestLoad = true
	m.manifest = map[string]any{}
	data, err := os.ReadFile(m.manifestPath)
	if err != nil {
		return m.manifest
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return m.manifest
	}
	if object, ok := payload.(map[string]any); ok {
		m.manifest = object
	}
	return m.manifest
}

// saveManifest 对齐 _save_manifest
func (m *WebImportManager) saveManifest(payload map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveManifestLocked(payload)
}

// saveManifestLocked 无锁保存清单
func (m *WebImportManager) saveManifestLocked(payload map[string]any) {
	_ = os.MkdirAll(filepath.Dir(m.manifestPath), 0o755)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err == nil {
		_ = os.WriteFile(m.manifestPath, data, 0o644)
	}
	m.manifest = payload
	m.manifestLoad = true
}

// clearManifest 对齐 _clear_manifest
func (m *WebImportManager) clearManifest() { m.saveManifest(map[string]any{}) }

// normalizeManifestPath 对齐 _normalize_manifest_path
func (m *WebImportManager) normalizeManifestPath(rawPath string) string {
	text := strings.TrimSpace(rawPath)
	if text == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "\\", "/")))
}

// matchManifestItemForSource 对齐 _match_manifest_item_for_source
func (m *WebImportManager) matchManifestItemForSource(source string, item map[string]any) bool {
	sourceText := strings.TrimSpace(source)
	if sourceText == "" || !strings.Contains(sourceText, ":") {
		return false
	}
	parts := strings.SplitN(sourceText, ":", 2)
	sourceKind := strings.ToLower(strings.TrimSpace(parts[0]))
	sourceValue := strings.TrimSpace(parts[1])
	if sourceValue == "" {
		return false
	}
	itemKind := strings.ToLower(strings.TrimSpace(importValueText(item["source_kind"])))
	itemName := strings.TrimSpace(importValueText(item["name"]))
	itemPathNorm := m.normalizeManifestPath(importValueText(item["source_path"]))
	itemSources := []string{}
	if list, ok := item["sources"].([]any); ok {
		itemSources = webImportDedupeSources(list)
	}
	for _, itemSource := range itemSources {
		if strings.ToLower(sourceText) == strings.ToLower(itemSource) {
			return true
		}
	}
	if sourceKind == "raw_scan" || sourceKind == "lpmm_openie" {
		sourcePathNorm := m.normalizeManifestPath(sourceValue)
		if sourcePathNorm != "" && itemPathNorm != "" && sourcePathNorm == itemPathNorm && itemKind == sourceKind {
			return true
		}
	}
	if sourceKind == "web_import" {
		return (itemKind == "upload" || itemKind == "paste") && itemName == sourceValue
	}
	if sourceKind == "lpmm_openie" {
		return itemKind == "lpmm_openie" && itemName == filepath.Base(strings.ReplaceAll(sourceValue, "\\", "/"))
	}
	return false
}

// InvalidateManifestForSources 对齐 invalidate_manifest_for_sources
func (m *WebImportManager) InvalidateManifestForSources(sources []any) map[string]any {
	requested := []string{}
	seen := map[string]struct{}{}
	for _, raw := range sources {
		source := strings.TrimSpace(importValueText(raw))
		if source == "" {
			continue
		}
		key := strings.ToLower(source)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		requested = append(requested, source)
	}
	result := map[string]any{
		"requested_sources": requested,
		"removed_count":     0,
		"removed_keys":      []string{},
		"remaining_count":   0,
		"unmatched_sources": []string{},
		"warnings":          []string{},
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	manifest := m.loadManifestLocked()
	validItems := [][2]any{}
	malformedKeys := []string{}
	keys := make([]string, 0, len(manifest))
	for key := range manifest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := manifest[key]
		if object, ok := item.(map[string]any); ok {
			validItems = append(validItems, [2]any{key, object})
		} else {
			malformedKeys = append(malformedKeys, key)
		}
	}
	keysToRemove := map[string]struct{}{}
	unmatched := []string{}
	for _, source := range requested {
		matched := false
		for _, pair := range validItems {
			if m.matchManifestItemForSource(source, pair[1].(map[string]any)) {
				keysToRemove[pair[0].(string)] = struct{}{}
				matched = true
			}
		}
		if !matched {
			unmatched = append(unmatched, source)
		}
	}
	if len(keysToRemove) > 0 {
		for key := range keysToRemove {
			delete(manifest, key)
		}
		m.saveManifestLocked(manifest)
	}
	removedKeys := make([]string, 0, len(keysToRemove))
	for key := range keysToRemove {
		removedKeys = append(removedKeys, key)
	}
	sort.Strings(removedKeys)
	result["removed_keys"] = removedKeys
	result["removed_count"] = len(removedKeys)
	result["remaining_count"] = len(manifest)
	result["unmatched_sources"] = unmatched
	warnings := []string{}
	if len(malformedKeys) > 0 {
		preview := strings.Join(malformedKeys[:webImportMinInt(5, len(malformedKeys))], ", ")
		extra := ""
		if len(malformedKeys) > 5 {
			extra = fmt.Sprintf(" ... (+%d)", len(malformedKeys)-5)
		}
		warnings = append(warnings, fmt.Sprintf("manifest 条目结构异常，已跳过 %d 项: %s%s", len(malformedKeys), preview, extra))
	}
	result["warnings"] = warnings
	return result
}

// manifestKeyForFile 对齐 _manifest_key_for_file
func (m *WebImportManager) manifestKeyForFile(file *WebImportFileRecord, contentHash string, dedupePolicy string) string {
	if dedupePolicy == "content_hash" {
		return "hash:" + contentHash
	}
	if file.SourcePath != "" {
		return "path:" + strings.ToLower(strings.ReplaceAll(file.SourcePath, "\\", "/"))
	}
	return "hash:" + contentHash
}

// manifestItemSources 对齐 _manifest_item_sources
func (m *WebImportManager) manifestItemSources(file *WebImportFileRecord, item map[string]any) []string {
	if list, ok := item["sources"].([]any); ok {
		if sources := webImportDedupeSources(list); len(sources) > 0 {
			return sources
		}
	}
	sourceKind := strings.TrimSpace(importValueText(item["source_kind"]))
	if sourceKind == "" {
		sourceKind = file.SourceKind
	}
	sourcePath := strings.TrimSpace(importValueText(item["source_path"]))
	if sourcePath == "" {
		sourcePath = file.SourcePath
	}
	name := strings.TrimSpace(importValueText(item["name"]))
	if name == "" {
		name = file.Name
	}
	sources := []string{}
	if sourcePath != "" {
		sources = append(sources, sourceKind+":"+sourcePath)
	}
	if sourceKind == "upload" || sourceKind == "paste" {
		if name != "" {
			sources = append(sources, "web_import:"+name)
		}
	}
	if sourceKind == "lpmm_openie" && name != "" {
		sources = append(sources, "lpmm_openie:"+name)
	}
	for _, source := range webImportDefaultSourcesForFile(file) {
		sources = append(sources, source)
	}
	return webImportDedupeSources(webImportStringsToAny(sources))
}

// webImportStringsToAny 字符串切片转 any 切片
func webImportStringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// sourceHasLiveParagraphs 对齐 _source_has_live_paragraphs
func (m *WebImportManager) sourceHasLiveParagraphs(source string) bool {
	if m.deps.Store == nil {
		return false
	}
	rows, err := m.deps.Store.GetLiveParagraphsBySource(source)
	if err != nil {
		m.deps.Logger.Warning(fmt.Sprintf("校验导入清单来源失败: source=%s, err=%v", source, err))
		return false
	}
	for _, row := range rows {
		deleted := webImportCoerceBool(row["is_deleted"], false)
		if !deleted {
			return true
		}
	}
	return false
}

// manifestItemHasLiveSources 对齐 _manifest_item_has_live_sources
func (m *WebImportManager) manifestItemHasLiveSources(file *WebImportFileRecord, item map[string]any) bool {
	for _, source := range m.manifestItemSources(file, item) {
		if m.sourceHasLiveParagraphs(source) {
			return true
		}
	}
	return false
}

// isManifestHit 对齐 _is_manifest_hit
func (m *WebImportManager) isManifestHit(file *WebImportFileRecord, contentHash string, dedupePolicy string) bool {
	key := m.manifestKeyForFile(file, contentHash, dedupePolicy)
	manifest := m.loadManifest()
	item, ok := manifest[key].(map[string]any)
	if !ok {
		return false
	}
	if importValueText(item["hash"]) != contentHash || !webImportCoerceBool(item["imported"], false) {
		return false
	}
	if m.manifestItemHasLiveSources(file, item) {
		return true
	}
	m.deps.Logger.Info(fmt.Sprintf("导入清单命中但未找到对应 live 段落，清理清单并继续导入: key=%s sources=%v", key, m.manifestItemSources(file, item)))
	m.mu.Lock()
	delete(manifest, key)
	m.saveManifestLocked(manifest)
	m.mu.Unlock()
	return false
}

// recordManifestImport 对齐 _record_manifest_import
func (m *WebImportManager) recordManifestImport(file *WebImportFileRecord, contentHash string, dedupePolicy string, taskID string) {
	key := m.manifestKeyForFile(file, contentHash, dedupePolicy)
	manifest := m.loadManifest()
	sources := []string{}
	for _, source := range file.ImportedSources {
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		sources = webImportDefaultSourcesForFile(file)
	}
	manifest[key] = map[string]any{
		"hash":        contentHash,
		"imported":    true,
		"timestamp":   m.now(),
		"task_id":     taskID,
		"name":        file.Name,
		"source_path": file.SourcePath,
		"source_kind": file.SourceKind,
		"sources":     webImportDedupeSources(webImportStringsToAny(sources)),
	}
	m.saveManifest(manifest)
}

// scanFiles 对齐 _scan_files
func (m *WebImportManager) scanFiles(basePath string, recursive bool, globPattern string, allowedExts map[string]struct{}) ([]string, error) {
	out := []string{}
	info, err := os.Stat(basePath)
	if err != nil {
		return nil, err
	}
	candidates := []string{}
	if !info.IsDir() {
		candidates = append(candidates, basePath)
	} else {
		pattern := filepath.Join(basePath, globPattern)
		if recursive {
			matches, err := filepath.Glob(filepath.Join(basePath, "**", globPattern))
			if err == nil {
				candidates = append(candidates, matches...)
			}
		}
		matches, err := filepath.Glob(pattern)
		if err == nil {
			candidates = append(candidates, matches...)
		}
		if recursive {
			_ = filepath.WalkDir(basePath, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return nil
				}
				matched, matchErr := filepath.Match(globPattern, filepath.Base(path))
				if matchErr == nil && matched {
					candidates = append(candidates, path)
				}
				return nil
			})
		}
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		fileInfo, err := os.Stat(candidate)
		if err != nil || fileInfo.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(candidate))
		if allowedExts != nil {
			if _, ok := allowedExts[ext]; !ok {
				continue
			}
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[absolute]; ok {
			continue
		}
		seen[absolute] = struct{}{}
		out = append(out, absolute)
	}
	sort.Slice(out, func(i int, j int) bool {
		return strings.ToLower(filepath.ToSlash(out[i])) < strings.ToLower(filepath.ToSlash(out[j]))
	})
	return out, nil
}
