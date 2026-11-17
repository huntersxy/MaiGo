package maibot

// 宿主共享供应商、模型与联网服务定义；任务配置由 Bot 独立保存。
// 对齐 src/config/shared_models.py。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	// SharedModelsEnv 宿主下发的公共模型目录路径
	SharedModelsEnv = "MAIBOT_SHARED_MODEL_CATALOG"
	// CatalogFilename 公共模型目录文件名
	CatalogFilename = "model_catalog.toml"
	// CatalogVersion 公共模型目录版本
	CatalogVersion = "3.0.0"
)

// CatalogSections 公共目录管理的配置节
var CatalogSections = []string{"api_providers", "models"}

// SharedCatalogPath 返回宿主下发的公共模型目录路径；未设置返回空串
func SharedCatalogPath() string {
	return strings.TrimSpace(os.Getenv(SharedModelsEnv))
}

// CatalogRevision 公共模型目录内容摘要；文件缺失时为空内容摘要
func CatalogRevision(path string) string {
	raw := []byte{}
	if strings.TrimSpace(path) != "" {
		if content, err := os.ReadFile(path); err == nil {
			raw = content
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ReadSharedCatalogDoc 读取公共模型目录
func ReadSharedCatalogDoc(path string) (map[string]any, error) {
	doc := map[string]any{"api_providers": []any{}, "models": []any{}}
	if strings.TrimSpace(path) == "" {
		return doc, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, err
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	inner, _ := doc["inner"].(map[string]any)
	if version, _ := inner["version"].(string); version != CatalogVersion {
		return nil, errors.New("共享模型目录版本不支持")
	}
	return doc, nil
}

// MergeSharedCatalogDoc 用公共目录替换本地供应商、模型与联网服务
func MergeSharedCatalogDoc(local map[string]any) (map[string]any, error) {
	path := SharedCatalogPath()
	if path == "" {
		return cloneTOMLMap(local), nil
	}
	catalog, err := ReadSharedCatalogDoc(path)
	if err != nil {
		return nil, err
	}
	return MergeCatalogWith(local, catalog), nil
}

// MergeCatalogWith 用给定公共目录替换本地定义
func MergeCatalogWith(local, catalog map[string]any) map[string]any {
	result := cloneTOMLMap(local)
	for _, section := range CatalogSections {
		result[section] = cloneTOMLValue(catalog[section])
	}
	web, _ := result["web"].(map[string]any)
	if web == nil {
		web = map[string]any{}
		result["web"] = web
	}
	catalogWeb, _ := catalog["web"].(map[string]any)
	services, _ := catalogWeb["services"].(map[string]any)
	if services == nil {
		services = map[string]any{}
	}
	web["services"] = cloneTOMLValue(services)
	return result
}

// ExtractBotModelSettings 实例文件只保存任务等独立设置
func ExtractBotModelSettings(submitted map[string]any) map[string]any {
	result := cloneTOMLMap(submitted)
	delete(result, "_shared_catalog")
	delete(result, "_warnings")
	for _, section := range CatalogSections {
		delete(result, section)
	}
	if web, ok := result["web"].(map[string]any); ok {
		delete(web, "services")
		if len(web) == 0 {
			delete(result, "web")
		}
	}
	return result
}

// ExtractCatalog 提取公共模型定义；联网预览参数和任务选择留在 Bot
func ExtractCatalog(submitted map[string]any) map[string]any {
	result := map[string]any{"inner": map[string]any{"version": CatalogVersion}}
	for _, section := range CatalogSections {
		items, _ := submitted[section].([]any)
		result[section] = cloneTOMLValue(items)
	}
	services := map[string]any{}
	if web, ok := submitted["web"].(map[string]any); ok {
		if raw, ok := web["services"].(map[string]any); ok {
			services = cloneTOMLMap(raw)
		}
	}
	result["web"] = map[string]any{"services": services}
	return result
}

// ValidateCatalogDoc 校验公共目录名称唯一
func ValidateCatalogDoc(doc map[string]any) error {
	for _, section := range CatalogSections {
		items, ok := doc[section].([]any)
		if !ok {
			if doc[section] == nil {
				continue
			}
			return fmt.Errorf("%s 必须是列表", section)
		}
		names := map[string]struct{}{}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s 必须由表组成", section)
			}
			name := strings.TrimSpace(anyText(item["name"]))
			if name == "" {
				return fmt.Errorf("%s 存在空名称", section)
			}
			if _, exists := names[name]; exists {
				return fmt.Errorf("%s 存在重复名称", section)
			}
			names[name] = struct{}{}
		}
	}
	return nil
}

// MissingModelReferences 收集具体失效引用，目录修改不能扩大已有配置错误
func MissingModelReferences(doc map[string]any) map[string]struct{} {
	errorsFound := map[string]struct{}{}
	providers := map[string]struct{}{}
	if items, ok := doc["api_providers"].([]any); ok {
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				if name := strings.TrimSpace(anyText(item["name"])); name != "" {
					providers[name] = struct{}{}
				}
			}
		}
	}
	models := map[string]struct{}{}
	if items, ok := doc["models"].([]any); ok {
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				name := strings.TrimSpace(anyText(item["name"]))
				models[name] = struct{}{}
				provider := strings.TrimSpace(anyText(item["api_provider"]))
				if _, exists := providers[provider]; !exists {
					errorsFound[fmt.Sprintf("模型 %s 引用的供应商 %s 不存在", name, provider)] = struct{}{}
				}
			}
		}
	}
	tasks, _ := doc["model_task_config"].(map[string]any)
	for taskName, rawTask := range tasks {
		task, ok := rawTask.(map[string]any)
		if !ok {
			continue
		}
		list, _ := task["model_list"].([]any)
		if taskName == "web_search" {
			services := map[string]struct{}{}
			if web, ok := doc["web"].(map[string]any); ok {
				if items, ok := web["services"].(map[string]any); ok {
					for name := range items {
						services[name] = struct{}{}
					}
				}
			}
			for _, raw := range list {
				name := strings.TrimSpace(anyText(raw))
				if _, exists := services[name]; !exists {
					errorsFound["联网任务引用的服务 "+name+" 不存在"] = struct{}{}
				}
			}
			continue
		}
		var check func(map[string]any, string)
		check = func(node map[string]any, path string) {
			for key, value := range node {
				if key == "model_list" {
					if names, ok := value.([]any); ok {
						for _, raw := range names {
							name := strings.TrimSpace(anyText(raw))
							if name == "" {
								continue
							}
							if _, exists := models[name]; !exists {
								errorsFound[fmt.Sprintf("任务 %s 引用的模型 %s 不存在", path, name)] = struct{}{}
							}
						}
					}
					continue
				}
				if child, ok := value.(map[string]any); ok {
					check(child, path+"."+key)
				}
			}
		}
		check(task, "model_task_config."+taskName)
	}
	return errorsFound
}

func anyText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func cloneTOMLMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned, _ := cloneTOMLValue(source).(map[string]any)
	return cloned
}

func cloneTOMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneTOMLValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneTOMLValue(item)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneTOMLValue(item)
		}
		return out
	default:
		return typed
	}
}
