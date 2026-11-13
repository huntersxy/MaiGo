package utils

// Episode 语义切分服务，对齐 A_memorix core/utils/episode_segmentation_service.py
// LLM 网关通过 model_routing 的 TextGenerationGateway 注入

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EpisodeSegmentationVersion 分段实现版本
const EpisodeSegmentationVersion = "episode_indices_v2"

// EpisodeSegmentationService 基于 LLM 的 Episode 语义切分服务
type EpisodeSegmentationService struct {
	PluginConfig map[string]any
	Gateway      TextGenerationGateway
	Tasks        map[string]TextGenerationTaskConfig
	Logger       Logger
}

// NewEpisodeSegmentationService 构造分段服务
func NewEpisodeSegmentationService(
	pluginConfig map[string]any,
	gateway TextGenerationGateway,
	tasks map[string]TextGenerationTaskConfig,
	logger Logger,
) *EpisodeSegmentationService {
	if pluginConfig == nil {
		pluginConfig = map[string]any{}
	}
	if tasks == nil {
		tasks = map[string]TextGenerationTaskConfig{}
	}
	return &EpisodeSegmentationService{
		PluginConfig: pluginConfig,
		Gateway:      gateway,
		Tasks:        tasks,
		Logger:       logger,
	}
}

// Cfg 读取点分配置，缺失时返回默认值
func (s *EpisodeSegmentationService) Cfg(key string, defaultValue any) any {
	return episodeConfigValue(s.PluginConfig, key, defaultValue)
}

func episodeConfigValue(config map[string]any, key string, defaultValue any) any {
	var current any = config
	for _, part := range strings.Split(key, ".") {
		next, ok := current.(map[string]any)
		if !ok {
			return defaultValue
		}
		value, exists := next[part]
		if !exists {
			return defaultValue
		}
		current = value
	}
	return current
}

// resolveModelConfig 解析分段使用的模型与展示标签
func (s *EpisodeSegmentationService) resolveModelConfig() (*ResolvedLLMModel, string) {
	available := GetTextGenerationModelTasks(s.Tasks, false)
	if len(available) == 0 {
		return nil, "unavailable"
	}
	selector := pyStrip(pyStrOrEmpty(s.Cfg("episode.segmentation_model", "auto")))
	if selector != "" && strings.ToLower(selector) != "auto" {
		taskName, taskConfig, selectedModelName, ok := ResolveTextGenerationModelSelector(available, selector)
		if ok && taskName != "" {
			return &ResolvedLLMModel{
				TaskName:          taskName,
				TaskConfig:        taskConfig,
				SelectedModelName: selectedModelName,
			}, selector
		}
		OrLogger(s.Logger).Warning(fmt.Sprintf("episode.segmentation_model='%s' 不可用，回退 auto", selector))
	}
	// 对齐 Python：先确认 preferred 集里有可用任务，再按完整优先级挑选；
	// 仅有 preferred 之外的任务（如 learner）时 Python 返回 unavailable
	if _, _, ok := PickTextGenerationTask(available, []string{"memory", "utils", "replyer", "planner", "tool_use"}); ok {
		if taskName, taskConfig, ok := PickTextGenerationTask(available, nil); ok {
			return &ResolvedLLMModel{TaskName: taskName, TaskConfig: taskConfig}, "auto"
		}
	}
	return nil, "unavailable"
}

// GenerationSignature 返回会影响分段输出的实现与模型配置签名
func (s *EpisodeSegmentationService) GenerationSignature() map[string]any {
	model, selector := s.resolveModelConfig()
	if model == nil {
		return map[string]any{
			"segmentation_version": EpisodeSegmentationVersion,
			"selector":             selector,
			"mode":                 "unavailable",
		}
	}
	modelList := make([]string, 0, len(model.TaskConfig.ModelList))
	for _, item := range model.TaskConfig.ModelList {
		if token := pyStrip(item); token != "" {
			modelList = append(modelList, token)
		}
	}
	return map[string]any{
		"segmentation_version": EpisodeSegmentationVersion,
		"selector":             selector,
		"mode":                 "llm",
		"task_name":            model.TaskName,
		"selected_model_name":  model.SelectedModelName,
		"model_list":           modelList,
		"temperature":          model.TaskConfig.Temperature,
		"max_tokens":           model.TaskConfig.MaxTokens,
	}
}

func episodeClampScore(value any, defaultValue float64) float64 {
	number, err := pyFloat64(value)
	if err != nil {
		number = defaultValue
	}
	if number < 0 {
		return 0
	}
	if number > 1 {
		return 1
	}
	return number
}

// SafeJSONLoads 解析 LLM 返回的 JSON 对象，兼容代码围栏与前后噪声
func (s *EpisodeSegmentationService) SafeJSONLoads(text string) (map[string]any, error) {
	raw := pyStrip(text)
	if raw == "" {
		return nil, fmt.Errorf("empty_response")
	}
	if strings.Contains(raw, "```") {
		raw = strings.ReplaceAll(raw, "```json", "```")
		raw = strings.ReplaceAll(raw, "```JSON", "```")
		parts := strings.Split(raw, "```")
		for _, part := range parts {
			part = pyStrip(part)
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				raw = part
				break
			}
		}
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		if value, ok := decoded.(map[string]any); ok {
			return value, nil
		}
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &decoded); err == nil {
			if value, ok := decoded.(map[string]any); ok {
				return value, nil
			}
		}
	}
	return nil, fmt.Errorf("invalid_json_response")
}

// BuildPrompt 对齐 _build_prompt 的英文提示词
func (s *EpisodeSegmentationService) BuildPrompt(
	source string,
	windowStart *float64,
	windowEnd *float64,
	paragraphs []map[string]any,
) string {
	rows := make([]string, 0, len(paragraphs))
	for index, item := range paragraphs {
		content := pyStrip(pyStrOrEmpty(item["content"]))
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = truncateRunes(content, 800)
		rows = append(rows, fmt.Sprintf(
			"[%d]\nevent_time=%s\nevent_time_start=%s\nevent_time_end=%s\ncontent=%s",
			index+1,
			episodePyRepr(item["event_time"]),
			episodePyRepr(item["event_time_start"]),
			episodePyRepr(item["event_time_end"]),
			content,
		))
	}
	sourceText := pyStrip(source)
	if sourceText == "" {
		sourceText = "unknown"
	}
	lines := []string{
		"You are an episode segmentation engine.",
		"Group meaningful events into coherent episodes. Skip paragraphs that do not describe an event.",
		"Return JSON ONLY. No markdown, no explanation.",
		"",
		"JSON schema:",
		"{",
		"  \"episodes\": [",
		"    {",
		"      \"title\": \"string\",",
		"      \"summary\": \"string\",",
		"      \"paragraph_indices\": [1, 2],",
		"      \"participants\": [\"person1\", \"person2\"],",
		"      \"keywords\": [\"kw1\", \"kw2\"],",
		"      \"time_confidence\": 0.0,",
		"      \"llm_confidence\": 0.0",
		"    }",
		"  ]",
		"}",
		"",
		"Rules:",
		"1) paragraph_indices are the 1-based numbers shown in the input.",
		"   You may omit irrelevant paragraphs and reuse a paragraph across related episodes.",
		"   Return {\"episodes\": []} if there are no meaningful events.",
		"2) title and summary must be non-empty.",
		"3) keep participants/keywords concise and deduplicated.",
		"4) if uncertain, still provide best effort confidence values.",
		"",
		"source=" + sourceText,
		"window_start=" + episodePyOptionalRepr(windowStart),
		"window_end=" + episodePyOptionalRepr(windowEnd),
		"paragraphs:",
	}
	return strings.Join(lines, "\n") + "\n" + strings.Join(rows, "\n\n")
}

func episodePyOptionalRepr(value *float64) string {
	if value == nil {
		return "None"
	}
	return pyFloatRepr(*value)
}

func episodePyRepr(value any) string {
	if value == nil {
		return "None"
	}
	return pyStr(value)
}

func truncateRunes(text string, limit int) string {
	if limit < 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// NormalizeEpisodes 对齐 _normalize_episodes，索引越界或字段缺失返回错误
func (s *EpisodeSegmentationService) NormalizeEpisodes(payload map[string]any, inputHashes []string) ([]map[string]any, error) {
	rawEpisodes, ok := episodeAnySlice(payload["episodes"])
	if !ok {
		return nil, fmt.Errorf("episodes_missing_or_not_list")
	}
	normalized := make([]map[string]any, 0, len(rawEpisodes))
	for _, rawItem := range rawEpisodes {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("episode_not_object")
		}
		title := pyStrip(pyStrOrEmpty(item["title"]))
		summary := pyStrip(pyStrOrEmpty(item["summary"]))
		if title == "" || summary == "" {
			return nil, fmt.Errorf("episode_title_or_summary_missing")
		}
		indices, ok := episodeAnySlice(item["paragraph_indices"])
		if !ok || len(indices) == 0 {
			return nil, fmt.Errorf("episode_paragraph_indices_missing")
		}
		dedupHashes := make([]string, 0, len(indices))
		seen := make(map[string]struct{}, len(indices))
		for _, rawIndex := range indices {
			index, ok := episodeStrictInt(rawIndex)
			if !ok || index < 1 || index > len(inputHashes) {
				return nil, fmt.Errorf("episode_paragraph_index_invalid: %v", rawIndex)
			}
			paragraphHash := inputHashes[index-1]
			if _, exists := seen[paragraphHash]; exists {
				continue
			}
			seen[paragraphHash] = struct{}{}
			dedupHashes = append(dedupHashes, paragraphHash)
		}
		participants := episodeStringList(item["participants"])
		if len(participants) > 16 {
			participants = participants[:16]
		}
		keywords := episodeStringList(item["keywords"])
		if len(keywords) > 20 {
			keywords = keywords[:20]
		}
		normalized = append(normalized, map[string]any{
			"title":            title,
			"summary":          summary,
			"paragraph_hashes": dedupHashes,
			"participants":     participants,
			"keywords":         keywords,
			"time_confidence":  episodeClampScore(item["time_confidence"], 1.0),
			"llm_confidence":   episodeClampScore(item["llm_confidence"], 0.5),
		})
	}
	return normalized, nil
}

func episodeAnySlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = item
		}
		return out, true
	case []string:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = item
		}
		return out, true
	default:
		return nil, false
	}
}

// episodeStrictInt 严格取整数，对齐 Python `type(index) is not int`
//
// JSON 的 1.0 在 Python 侧是 float，会被判非法；Go 同样拒绝浮点类型，
// 只接受整数族（含 json.Number 的纯整数形式）。
func episodeStrictInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case int32:
		return int(typed), true
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(number), true
	default:
		return 0, false
	}
}

func episodeStringList(value any) []string {
	items, ok := episodeAnySlice(value)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if token := pyStrip(pyStrOrEmpty(item)); token != "" {
			out = append(out, token)
		}
	}
	return out
}

func episodeIntOr(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if number, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return number
		}
	}
	return fallback
}

// Segment 调用 LLM 生成标准化 Episode 列表
func (s *EpisodeSegmentationService) Segment(
	ctx context.Context,
	source string,
	windowStart *float64,
	windowEnd *float64,
	paragraphs []map[string]any,
) (map[string]any, error) {
	if len(paragraphs) == 0 {
		return nil, fmt.Errorf("paragraphs_empty")
	}
	model, modelLabel := s.resolveModelConfig()
	if model == nil {
		return nil, fmt.Errorf("episode segmentation model unavailable")
	}
	prompt := s.BuildPrompt(source, windowStart, windowEnd, paragraphs)
	temperature := model.TaskConfig.Temperature
	maxTokens := model.TaskConfig.MaxTokens
	result := GenerateWithResolvedModel(
		ctx,
		s.Gateway,
		*model,
		"A_Memorix.EpisodeSegmentation",
		prompt,
		&temperature,
		episodeOptionalInt(maxTokens),
		s.Logger,
	)
	response := pyStrOrEmpty(result.Completion)
	// 对齐 Python `not response`：只判原始空串，纯空白交给 JSON 解析分支报错
	if !result.Success || response == "" {
		return nil, fmt.Errorf("llm_generate_failed")
	}
	payload, err := s.SafeJSONLoads(response)
	if err != nil {
		return nil, err
	}
	inputHashes := make([]string, len(paragraphs))
	for index, paragraph := range paragraphs {
		inputHashes[index] = pyStrip(pyStrOrEmpty(paragraph["hash"]))
	}
	episodes, err := s.NormalizeEpisodes(payload, inputHashes)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"episodes":             episodes,
		"segmentation_model":   modelLabel,
		"segmentation_version": EpisodeSegmentationVersion,
	}, nil
}

func episodeOptionalInt(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}
