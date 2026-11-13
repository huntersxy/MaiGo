package utils

// A_memorix 内部模型任务选择，对齐 Python core/utils/model_routing.py
// 宿主 LLM 服务通过接口注入，避免 utils 依赖 services 层

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// NonTextGenerationTaskNames 对齐 NON_TEXT_GENERATION_TASK_NAMES
var NonTextGenerationTaskNames = map[string]struct{}{
	"embedding": {},
	"voice":     {},
	"vlm":       {},
}

// aMemorixTextTaskPriority 对齐 A_MEMORIX_TEXT_TASK_PRIORITY
var aMemorixTextTaskPriority = []string{
	"memory",
	"utils",
	"lpmm_entity_extract",
	"lpmm_rdf_build",
	"planner",
	"replyer",
	"learner",
	"emoji",
	"tool_use",
}

// AMemorixTextTaskPriority 返回文本任务优先级副本
func AMemorixTextTaskPriority() []string {
	out := make([]string, len(aMemorixTextTaskPriority))
	copy(out, aMemorixTextTaskPriority)
	return out
}

// TextGenerationTaskConfig 对齐宿主 TaskConfig 的可用字段子集
type TextGenerationTaskConfig struct {
	ModelList         []string
	MaxTokens         int
	Temperature       float64
	SelectionStrategy string
	HardTimeout       float64
}

// Copy 返回深拷贝，避免调用方共享 model_list 切片
func (c TextGenerationTaskConfig) Copy() TextGenerationTaskConfig {
	out := c
	out.ModelList = append([]string{}, c.ModelList...)
	return out
}

// ResolvedLLMModel A_Memorix 内部使用的 LLM 选择结果
type ResolvedLLMModel struct {
	TaskName          string
	TaskConfig        TextGenerationTaskConfig
	SelectedModelName string
}

// IsSingleModel 对应 Python is_single_model 属性
func (m ResolvedLLMModel) IsSingleModel() bool { return m.SelectedModelName != "" }

// TextGenerationRequest 对齐 LLMServiceRequest 的字段子集
type TextGenerationRequest struct {
	TaskName    string
	RequestType string
	Prompt      string
	Temperature *float64
	MaxTokens   *int
}

// TextGenerationResult 对齐 LLMServiceResult 的字段子集
type TextGenerationResult struct {
	Success    bool
	Error      string
	Completion string
}

// TextGenerationGateway 宿主 LLM 生成能力
type TextGenerationGateway interface {
	// Generate 按任务候选列表生成
	Generate(ctx context.Context, req TextGenerationRequest) (TextGenerationResult, error)
	// GeneratePinned 以单一模型任务生成
	GeneratePinned(ctx context.Context, model ResolvedLLMModel, requestType string, prompt string, temperature *float64, maxTokens *int) (TextGenerationResult, error)
}

// TaskHasModelList 判断任务配置是否有可用模型候选
func TaskHasModelList(cfg TextGenerationTaskConfig) bool {
	for _, name := range cfg.ModelList {
		if pyStrip(name) != "" {
			return true
		}
	}
	return false
}

// IsTextGenerationTaskName 判断任务名是否适合普通文本生成
func IsTextGenerationTaskName(taskName string) bool {
	_, blocked := NonTextGenerationTaskNames[strings.ToLower(pyStrip(taskName))]
	return !blocked
}

// GetTextGenerationModelTasks 读取可用的文本生成任务配置
func GetTextGenerationModelTasks(tasks map[string]TextGenerationTaskConfig, includeEmpty bool) map[string]TextGenerationTaskConfig {
	out := make(map[string]TextGenerationTaskConfig, len(tasks))
	for taskName, cfg := range tasks {
		if !IsTextGenerationTaskName(taskName) {
			continue
		}
		if !includeEmpty && !TaskHasModelList(cfg) {
			continue
		}
		out[taskName] = cfg
	}
	return out
}

// iterPreferredTaskNames 先按偏好顺序产出，再按字典序补齐其余任务
func iterPreferredTaskNames(available map[string]TextGenerationTaskConfig, preferred []string) []string {
	names := make([]string, 0, len(available))
	yielded := make(map[string]struct{}, len(available))
	for _, taskName := range preferred {
		if _, ok := available[taskName]; ok {
			if _, done := yielded[taskName]; done {
				continue
			}
			yielded[taskName] = struct{}{}
			names = append(names, taskName)
		}
	}
	rest := make([]string, 0, len(available))
	for taskName := range available {
		if _, done := yielded[taskName]; done {
			continue
		}
		rest = append(rest, taskName)
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// PickTextGenerationTask 按 A_Memorix 优先级选择文本生成任务
func PickTextGenerationTask(available map[string]TextGenerationTaskConfig, preferred []string) (string, TextGenerationTaskConfig, bool) {
	if preferred == nil {
		preferred = aMemorixTextTaskPriority
	}
	for _, taskName := range iterPreferredTaskNames(available, preferred) {
		cfg := available[taskName]
		if TaskHasModelList(cfg) {
			return taskName, cfg, true
		}
	}
	return "", TextGenerationTaskConfig{}, false
}

// FindTextGenerationTaskForModel 按模型名查找其所属的文本生成任务
func FindTextGenerationTaskForModel(available map[string]TextGenerationTaskConfig, modelName string) (string, TextGenerationTaskConfig, bool) {
	normalized := pyStrip(modelName)
	if normalized == "" {
		return "", TextGenerationTaskConfig{}, false
	}
	names := make([]string, 0, len(available))
	for taskName := range available {
		names = append(names, taskName)
	}
	sort.Strings(names)
	for _, taskName := range names {
		cfg := available[taskName]
		for _, item := range cfg.ModelList {
			if pyStrip(item) == normalized {
				return taskName, cfg, true
			}
		}
	}
	return "", TextGenerationTaskConfig{}, false
}

// BuildSingleModelTask 构造只包含单个文本生成模型的任务配置
func BuildSingleModelTask(modelName string, template TextGenerationTaskConfig) TextGenerationTaskConfig {
	out := template.Copy()
	out.ModelList = []string{modelName}
	return out
}

// ResolveTextGenerationModelSelector 解析任务名或具体模型名选择器
func ResolveTextGenerationModelSelector(available map[string]TextGenerationTaskConfig, selector string) (string, TextGenerationTaskConfig, string, bool) {
	normalized := pyStrip(selector)
	if normalized == "" || strings.ToLower(normalized) == "auto" {
		return "", TextGenerationTaskConfig{}, "", false
	}
	if cfg, ok := available[normalized]; ok && TaskHasModelList(cfg) {
		return normalized, cfg, "", true
	}
	taskName, cfg, ok := FindTextGenerationTaskForModel(available, normalized)
	if !ok {
		return "", TextGenerationTaskConfig{}, "", false
	}
	return taskName, BuildSingleModelTask(normalized, cfg), normalized, true
}

// GenerateWithResolvedModel 按解析出的模型执行文本生成
func GenerateWithResolvedModel(
	ctx context.Context,
	gateway TextGenerationGateway,
	model ResolvedLLMModel,
	requestType string,
	prompt string,
	temperature *float64,
	maxTokens *int,
	logger Logger,
) TextGenerationResult {
	if gateway == nil {
		return TextGenerationResult{Success: false, Error: "LLM 网关未初始化"}
	}
	if model.IsSingleModel() {
		result, err := gateway.GeneratePinned(ctx, model, requestType, prompt, temperature, maxTokens)
		if err != nil {
			message := fmt.Sprintf("生成内容时出错: %v", err)
			OrLogger(logger).Error("[A_Memorix.ModelRouting] " + message)
			return TextGenerationResult{Success: false, Error: err.Error()}
		}
		return result
	}
	result, err := gateway.Generate(ctx, TextGenerationRequest{
		TaskName:    model.TaskName,
		RequestType: requestType,
		Prompt:      prompt,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		message := fmt.Sprintf("生成内容时出错: %v", err)
		OrLogger(logger).Error("[A_Memorix.ModelRouting] " + message)
		return TextGenerationResult{Success: false, Error: err.Error()}
	}
	return result
}

// ErrNoTextGenerationModel 对齐 Python RuntimeError("没有可用的文本生成模型配置")
var ErrNoTextGenerationModel = fmt.Errorf("没有可用的文本生成模型配置")

// ResolveDefaultTextGenerationTask 解析默认文本生成任务，避免落到 embedding
func ResolveDefaultTextGenerationTask(tasks map[string]TextGenerationTaskConfig) (string, error) {
	available := GetTextGenerationModelTasks(tasks, false)
	taskName, _, ok := PickTextGenerationTask(available, nil)
	if !ok {
		return "", ErrNoTextGenerationModel
	}
	return taskName, nil
}

// ResolveTextGenerationTaskNameFromModelConfig 根据旧版任务配置解析文本生成任务名
func ResolveTextGenerationTaskNameFromModelConfig(
	tasks map[string]TextGenerationTaskConfig,
	modelConfig TextGenerationTaskConfig,
	preferredTaskName string,
	logger Logger,
) (string, error) {
	log := OrLogger(logger)
	available := GetTextGenerationModelTasks(tasks, false)
	if len(available) == 0 {
		return "", ErrNoTextGenerationModel
	}
	normalizedPreferred := pyStrip(preferredTaskName)
	if normalizedPreferred != "" {
		if _, ok := available[normalizedPreferred]; ok {
			return normalizedPreferred, nil
		}
	}
	names := sortedTaskNames(available)
	for _, taskName := range names {
		if sameTaskConfig(available[taskName], modelConfig) {
			return taskName, nil
		}
	}
	requested := nonEmptyModelNames(modelConfig.ModelList)
	if len(requested) > 0 {
		for _, taskName := range names {
			if equalStringSlices(requested, nonEmptyModelNames(available[taskName].ModelList)) {
				return taskName, nil
			}
		}
		for _, requestedModel := range requested {
			if taskName, _, ok := FindTextGenerationTaskForModel(available, requestedModel); ok {
				log.Info(fmt.Sprintf("旧版文本生成 model_config 按模型 `%s` 近似映射到任务 `%s`", requestedModel, taskName))
				return taskName, nil
			}
		}
	}
	fallback, _, ok := PickTextGenerationTask(available, nil)
	if ok {
		if normalizedPreferred != "" {
			log.Warning(fmt.Sprintf("无法映射文本生成 model_config，回退默认任务: preferred=%s", normalizedPreferred))
		}
		return fallback, nil
	}
	return "", ErrNoTextGenerationModel
}

// sortedTaskNames 返回稳定顺序的任务名
func sortedTaskNames(available map[string]TextGenerationTaskConfig) []string {
	names := make([]string, 0, len(available))
	for taskName := range available {
		names = append(names, taskName)
	}
	sort.Strings(names)
	return names
}

// sameTaskConfig 判断两个任务配置是否指向同一对象语义（模型列表一致）
func sameTaskConfig(left TextGenerationTaskConfig, right TextGenerationTaskConfig) bool {
	return equalStringSlices(nonEmptyModelNames(left.ModelList), nonEmptyModelNames(right.ModelList))
}

// nonEmptyModelNames 去空白并保留顺序
func nonEmptyModelNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, item := range values {
		token := pyStrip(item)
		if token != "" {
			out = append(out, token)
		}
	}
	return out
}

// equalStringSlices 按顺序比较字符串切片
func equalStringSlices(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
