package utils

// A_memorix 摘要导入的模型选择、自检与向量持久化辅助

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// summaryModelSelectorError 对齐模板 model_name 迁移提示
const summaryModelSelectorError = "summarization.model_name 必须为 List[str]。 请执行 scripts/release_vnext_migrate.py migrate。"

// summaryTaskPriority 对齐 _pick_default_summary_task 的候选顺序
var summaryTaskPriority = []string{"memory", "utils", "planner", "tool_use"}

// normalizeSummaryModelSelectors 对齐 _normalize_summary_model_selectors
func normalizeSummaryModelSelectors(raw any) ([]string, error) {
	if raw == nil {
		return []string{"auto"}, nil
	}
	selectors := make([]string, 0)
	switch typed := raw.(type) {
	case []string:
		for _, item := range typed {
			if stripped := pyStrip(item); stripped != "" {
				selectors = append(selectors, stripped)
			}
		}
	case []any:
		for _, item := range typed {
			itemText, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s", summaryModelSelectorError)
			}
			if stripped := pyStrip(itemText); stripped != "" {
				selectors = append(selectors, stripped)
			}
		}
	default:
		return nil, fmt.Errorf("%s", summaryModelSelectorError)
	}
	if len(selectors) == 0 {
		return []string{"auto"}, nil
	}
	return selectors, nil
}

// pickDefaultSummaryTask 对齐 _pick_default_summary_task
func pickDefaultSummaryTask(available map[string]TextGenerationTaskConfig) (string, TextGenerationTaskConfig, bool) {
	for _, taskName := range summaryTaskPriority {
		cfg, exists := available[taskName]
		if !exists {
			continue
		}
		if TaskHasModelList(cfg) {
			return taskName, cfg, true
		}
	}
	return "", TextGenerationTaskConfig{}, false
}

// summaryModelListContains 对齐 Python 列表成员判断，列表项不做 strip
func summaryModelListContains(modelList []string, modelName string) bool {
	for _, item := range modelList {
		if item == modelName {
			return true
		}
	}
	return false
}

// resolveSummaryModelTask 对齐 _resolve_summary_model_task
func (i *SummaryImporter) resolveSummaryModelTask() (string, TextGenerationTaskConfig, bool, error) {
	available := GetTextGenerationModelTasks(i.modelTasks, false)
	if len(available) == 0 {
		return "", TextGenerationTaskConfig{}, false, nil
	}
	summarization := summaryNestedMap(i.pluginConfig, "summarization")
	rawSelector := any([]string{"auto"})
	if value, exists := summarization["model_name"]; exists {
		rawSelector = value
	}
	selectors, err := normalizeSummaryModelSelectors(rawSelector)
	if err != nil {
		return "", TextGenerationTaskConfig{}, false, err
	}
	defaultTaskName, defaultTaskConfig, hasDefault := pickDefaultSummaryTask(available)
	modelDict := i.modelDict
	if modelDict == nil {
		modelDict = map[string]any{}
	}
	baseName := ""
	baseConfig := TextGenerationTaskConfig{}
	hasBase := false
	assignBase := func(taskName string, taskConfig TextGenerationTaskConfig) {
		if !hasBase {
			baseName = taskName
			baseConfig = taskConfig
			hasBase = true
		}
	}
	for _, rawSelector := range selectors {
		selector := pyStrip(rawSelector)
		if selector == "" {
			continue
		}
		if pyLower(selector) == "auto" {
			if !hasBase && hasDefault {
				assignBase(defaultTaskName, defaultTaskConfig)
			}
			continue
		}
		if strings.Contains(selector, ":") {
			parts := strings.SplitN(selector, ":", 2)
			taskName := pyStrip(parts[0])
			modelName := pyStrip(parts[1])
			taskConfig, exists := available[taskName]
			if !exists {
				i.logger.Warning(fmt.Sprintf("总结模型选择器 '%s' 的任务 '%s' 不存在，已跳过", selector, taskName))
				continue
			}
			if modelName == "" || pyLower(modelName) == "auto" {
				assignBase(taskName, taskConfig)
				continue
			}
			if summaryModelListContains(taskConfig.ModelList, modelName) {
				if !hasBase {
					assignBase(taskName, taskConfig)
					i.logger.Info(fmt.Sprintf("总结模型选择器 '%s' 已定位到任务 '%s'；当前 LLM 服务按任务候选列表执行，不单独覆盖具体模型。", selector, taskName))
				}
				continue
			}
			i.logger.Warning(fmt.Sprintf("总结模型选择器 '%s' 的模型 '%s' 不在任务 '%s' 中，已跳过", selector, modelName, taskName))
			continue
		}
		if taskConfig, exists := available[selector]; exists {
			assignBase(selector, taskConfig)
			continue
		}
		if _, exists := modelDict[selector]; exists {
			if taskName, taskConfig, ok := FindTextGenerationTaskForModel(available, selector); ok {
				if !hasBase {
					assignBase(taskName, taskConfig)
					i.logger.Info(fmt.Sprintf("总结模型选择器 '%s' 已定位到任务 '%s'；当前 LLM 服务按任务候选列表执行，不单独覆盖具体模型。", selector, taskName))
				}
			} else {
				i.logger.Warning(fmt.Sprintf("总结模型选择器 '%s' 未归属于任何任务，已跳过", selector))
			}
			continue
		}
		i.logger.Warning(fmt.Sprintf("总结模型选择器 '%s' 无法识别，已跳过", selector))
	}
	if !hasBase && hasDefault {
		assignBase(defaultTaskName, defaultTaskConfig)
	}
	if !hasBase {
		return "", TextGenerationTaskConfig{}, false, nil
	}
	return baseName, baseConfig.Copy(), true, nil
}

// summaryConfigGetter 把纯配置字典包装为点路径读取器，对齐 _get_config_value
func summaryConfigGetter(values map[string]any) ConfigGetter {
	if values == nil {
		return nil
	}
	return func(key string, defaultValue any) any {
		current := any(values)
		for _, part := range strings.Split(key, ".") {
			mapping, ok := current.(map[string]any)
			if !ok {
				return defaultValue
			}
			value, exists := mapping[part]
			if !exists {
				return defaultValue
			}
			current = value
		}
		return current
	}
}

// summaryVectorSnapshot 把存储包装为自检快照，缺失统计时回默认值
type summaryVectorSnapshot struct {
	store SummaryVectorStore
}

// Dimension 对齐 store.dimension
func (s summaryVectorSnapshot) Dimension() int {
	return s.store.Dimension()
}

// NumVectors 对齐 getattr(store, "num_vectors", 0)
func (s summaryVectorSnapshot) NumVectors() int {
	if counter, ok := s.store.(interface{ NumVectors() int }); ok {
		return counter.NumVectors()
	}
	return 0
}

// HasData 对齐 getattr(store, "has_data", False)
func (s summaryVectorSnapshot) HasData() bool {
	if checker, ok := s.store.(interface{ HasData() bool }); ok {
		return checker.HasData()
	}
	return false
}

// ensureRuntimeSelfCheck 对齐 _ensure_runtime_self_check，返回允许降级标志与提示
func (i *SummaryImporter) ensureRuntimeSelfCheck(ctx context.Context) (bool, string) {
	var report RuntimeSelfCheckReport
	host, hasHost := i.host.(SummarySelfCheckHost)
	if hasHost {
		report = host.EnsureRuntimeSelfCheck(ctx)
	} else {
		params := &SelfCheckParams{
			Config:           summaryConfigGetter(i.pluginConfig),
			EmbeddingManager: i.embedding,
			Logger:           i.logger,
		}
		if i.vectorStore != nil {
			params.VectorStore = summaryVectorSnapshot{store: i.vectorStore}
		}
		report = EnsureRuntimeSelfCheck(ctx, params, i.selfCheckCache, false)
	}
	if report.OK {
		return true, ""
	}
	message := report.CompletenessMessage()
	if i.allowMetadataOnlyWrite() {
		i.logger.Warning("总结导入进入 metadata-only 回退模式: " + message)
		return true, "embedding_degraded_metadata_only"
	}
	return false, message
}

// parseLLMResponse 对齐 _parse_llm_response，取首尾花括号间 JSON
func (i *SummaryImporter) parseLLMResponse(response string) map[string]any {
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start < 0 || end <= start {
		return map[string]any{}
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(response[start:end+1]), &data); err != nil {
		i.logger.Warning(fmt.Sprintf("解析总结 JSON 失败: %v", err))
		return map[string]any{}
	}
	if data == nil {
		return map[string]any{}
	}
	return data
}

// persistVectorStore 对齐 _persist_vector_store
func (i *SummaryImporter) persistVectorStore(store SummaryVectorStore) error {
	if store == nil {
		return fmt.Errorf("向量存储未初始化")
	}
	if i.host != nil {
		host, ok := i.host.(SummaryPersistHost)
		if !ok {
			return fmt.Errorf("宿主缺少向量持久化能力")
		}
		host.PersistVectorStore(store)
		return nil
	}
	var fingerprint map[string]any
	if provider, ok := i.embedding.(SummaryEmbeddingFingerprintProvider); ok {
		fingerprint = provider.GetEmbeddingFingerprint(store.Dimension())
	}
	return store.Save(fingerprint)
}
