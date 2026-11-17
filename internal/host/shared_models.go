package host

// 共享供应商与模型目录的管理；宿主永不加载某个 Bot 的全局配置。
// 对齐 src/host/shared_models.py。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
)

// SaveEffectiveConfig 模型定义与当前 Bot 设置按归属保存，删除引起的跨实例引用清理一并提交
func SaveEffectiveConfig(catalogPath, localPath string, submitted map[string]any, revision string) (string, error) {
	hostDir := filepath.Dir(catalogPath)
	modelLock, err := LockFilePath(filepath.Join(hostDir, "model-config.lock"))
	if err != nil {
		return "", ErrInstanceBusy
	}
	defer modelLock.Release()
	RecoverConfigTransaction(hostDir)
	currentRevision := maibotcfg.CatalogRevision(catalogPath)
	if strings.TrimSpace(revision) != "" && revision != currentRevision {
		return "", errors.New("公共模型配置已改变，请重新加载后保存")
	}
	previous, err := readCatalogDocument(catalogPath)
	if err != nil {
		return "", err
	}
	catalog := maibotcfg.ExtractCatalog(submitted)
	previousProviders := sectionNames(previous, "api_providers")
	nextProviders := sectionNames(catalog, "api_providers")
	removedProviders := difference(previousProviders, nextProviders)
	models := filterModelsByProvider(catalog["models"], removedProviders)
	catalog["models"] = models
	previousModels := sectionNames(previous, "models")
	nextModels := sectionNames(catalog, "models")
	removedModels := difference(previousModels, nextModels)
	if err := maibotcfg.ValidateCatalogDoc(catalog); err != nil {
		return "", err
	}
	local, err := readTOMLMap(localPath)
	if err != nil {
		return "", err
	}
	settings := maibotcfg.ExtractBotModelSettings(submitted)
	for _, section := range maibotcfg.CatalogSections {
		delete(local, section)
	}
	if web, ok := local["web"].(map[string]any); ok {
		delete(web, "services")
	}
	for key, value := range settings {
		local[key] = value
	}
	files, err := prepareBotConfigs(hostDir, previous, catalog, localPath, local, removedModels)
	if err != nil {
		return "", err
	}
	if !documentsEqual(catalog, previous) {
		encoded, err := encodeTOMLDocument(catalog)
		if err != nil {
			return "", err
		}
		files[catalogPath] = encoded
	}
	if err := CommitConfigFiles(hostDir, files); err != nil {
		return "", err
	}
	return maibotcfg.CatalogRevision(catalogPath), nil
}

func prepareBotConfigs(hostDir string, previous, catalog map[string]any, selectedPath string, selected map[string]any, removedModels map[string]struct{}) (map[string]string, error) {
	removeTaskReferences(selected, removedModels)
	selectedEncoded, err := encodeTOMLDocument(selected)
	if err != nil {
		return nil, err
	}
	files := map[string]string{selectedPath: selectedEncoded}
	registry := NewRegistry(filepath.Dir(hostDir))
	bots, err := registry.ListBots()
	if err != nil {
		return nil, err
	}
	for _, bot := range bots {
		if bot.Initialization != "initialized" {
			continue
		}
		localPath := filepath.Join(bot.Directory, "config", "model_config.toml")
		original, err := os.ReadFile(localPath)
		if err != nil {
			return nil, err
		}
		var local map[string]any
		if err := toml.Unmarshal(original, &local); err != nil {
			return nil, fmt.Errorf("%s: %w", localPath, err)
		}
		isSelected := sameFilePath(localPath, selectedPath)
		current := selected
		if !isSelected {
			current = local
			removeTaskReferences(current, removedModels)
		}
		newErrors := differenceKeys(
			maibotcfg.MissingModelReferences(maibotcfg.MergeCatalogWith(current, catalog)),
			maibotcfg.MissingModelReferences(maibotcfg.MergeCatalogWith(local, previous)),
		)
		if len(newErrors) > 0 {
			keys := make([]string, 0, len(newErrors))
			for key := range newErrors {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return nil, fmt.Errorf("%s: %s", bot.Name, strings.Join(keys, "；"))
		}
		if isSelected {
			continue
		}
		encoded, err := encodeTOMLDocument(current)
		if err != nil {
			return nil, err
		}
		if encoded != string(original) {
			files[localPath] = encoded
		}
	}
	return files, nil
}

func removeTaskReferences(document map[string]any, removedModels map[string]struct{}) {
	tasks, _ := document["model_task_config"].(map[string]any)
	for taskName, rawTask := range tasks {
		if taskName == "web_search" {
			continue
		}
		task, ok := rawTask.(map[string]any)
		if !ok {
			continue
		}
		cleanModelList(task, removedModels)
	}
}

func cleanModelList(node map[string]any, removedModels map[string]struct{}) {
	for key, value := range node {
		if key == "model_list" {
			if list, ok := value.([]any); ok {
				kept := make([]any, 0, len(list))
				for _, raw := range list {
					name := strings.TrimSpace(hostString(raw))
					if _, removed := removedModels[name]; removed {
						continue
					}
					kept = append(kept, raw)
				}
				node[key] = kept
			}
			continue
		}
		if child, ok := value.(map[string]any); ok {
			cleanModelList(child, removedModels)
		}
	}
}

func readCatalogDocument(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"api_providers": []any{}, "models": []any{}}, nil
		}
		return nil, err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func readTOMLMap(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func encodeTOMLDocument(document map[string]any) (string, error) {
	encoded, err := toml.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func sectionNames(document map[string]any, section string) map[string]struct{} {
	names := map[string]struct{}{}
	items, _ := document[section].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			name := strings.TrimSpace(hostString(item["name"]))
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

func filterModelsByProvider(raw any, removedProviders map[string]struct{}) []any {
	items, _ := raw.([]any)
	if len(removedProviders) == 0 {
		return items
	}
	kept := make([]any, 0, len(items))
	for _, entry := range items {
		item, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		provider := strings.TrimSpace(hostString(item["api_provider"]))
		if _, removed := removedProviders[provider]; removed {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func difference(left, right map[string]struct{}) map[string]struct{} {
	result := map[string]struct{}{}
	for key := range left {
		if _, exists := right[key]; !exists {
			result[key] = struct{}{}
		}
	}
	return result
}

func differenceKeys(left, right map[string]struct{}) map[string]struct{} {
	return difference(left, right)
}

func documentsEqual(left, right map[string]any) bool {
	leftEncoded, leftErr := encodeTOMLDocument(left)
	rightEncoded, rightErr := encodeTOMLDocument(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return leftEncoded == rightEncoded
}

func sameFilePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func hostString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
