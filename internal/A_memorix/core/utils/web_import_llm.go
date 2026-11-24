package utils

// Web 导入 LLM 模型选择、调用与时间语义抽取，对齐 web_import_manager.py

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// selectModel 对齐 _select_model
func (m *WebImportManager) selectModel() (ResolvedLLMModel, error) {
	if m.deps.LLM == nil {
		return ResolvedLLMModel{}, fmt.Errorf("没有可用 LLM 模型")
	}
	models := GetTextGenerationModelTasks(m.deps.LLM.GetAvailableModels(), false)
	if len(models) == 0 {
		return ResolvedLLMModel{}, fmt.Errorf("没有可用 LLM 模型")
	}
	configModel := strings.TrimSpace(importValueText(m.cfg("advanced.extraction_model", "auto")))
	if configModel == "" {
		configModel = "auto"
	}
	if strings.ToLower(configModel) != "auto" {
		taskName, taskConfig, selected, ok := ResolveTextGenerationModelSelector(models, configModel)
		if ok && taskName != "" {
			return ResolvedLLMModel{TaskName: taskName, TaskConfig: taskConfig.Copy(), SelectedModelName: selected}, nil
		}
		m.deps.Logger.Warning(fmt.Sprintf("advanced.extraction_model=%q 不可用于文本生成，已回退自动选择", configModel))
	}
	preferred := []string{"memory", "utils", "lpmm_entity_extract", "lpmm_rdf_build", "replyer", "planner", "tool_use"}
	taskName, taskConfig, ok := PickTextGenerationTask(models, preferred)
	if ok && taskName != "" {
		return ResolvedLLMModel{TaskName: taskName, TaskConfig: taskConfig.Copy()}, nil
	}
	return ResolvedLLMModel{}, fmt.Errorf("没有可用 LLM 模型")
}

// llmCall 对齐 _llm_call
func (m *WebImportManager) llmCall(ctx context.Context, prompt string, model ResolvedLLMModel) (map[string]any, error) {
	config := m.llmRetryConfig()
	retries := int(config["retries"])
	timeout := config["llm_call_seconds"]
	var lastError error
	temperature := model.TaskConfig.Temperature
	maxTokens := model.TaskConfig.MaxTokens
	for attempt := 0; attempt <= retries; attempt++ {
		var result TextGenerationResult
		callCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
		}
		result = GenerateWithResolvedModel(callCtx, m.deps.LLM, model, "A_Memorix.WebImport", prompt, &temperature, &maxTokens, m.deps.Logger)
		cancel()
		text := strings.TrimSpace(result.Completion)
		if result.Success && text != "" {
			parsed, err := webImportParseLLMResponse(text)
			if err == nil {
				return parsed, nil
			}
			lastError = err
		} else {
			lastError = fmt.Errorf("LLM 生成失败")
			if result.Error != "" {
				lastError = fmt.Errorf("LLM 生成失败: %s", result.Error)
			}
		}
		if attempt >= retries {
			break
		}
		delay := config["max_wait"]
		if candidate := config["min_wait"] * powFloat(config["multiplier"], float64(attempt)); candidate < delay {
			delay = candidate
		}
		if delay > 0 {
			if err := sleepContext(ctx, time.Duration(delay*float64(time.Second))); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("LLM 抽取失败: %v", lastError)
}

// webImportParseLLMResponse 解析 LLM 返回的 JSON 对象，兼容代码围栏与前后噪声
func webImportParseLLMResponse(text string) (map[string]any, error) {
	txt := strings.TrimSpace(text)
	if strings.Contains(txt, "```") {
		parts := strings.Split(txt, "```json")
		txt = parts[len(parts)-1]
		fences := strings.Split(txt, "```")
		txt = strings.TrimSpace(fences[0])
		if strings.HasPrefix(txt, "json") {
			txt = strings.TrimSpace(txt[4:])
		}
	}
	var value any
	if err := json.Unmarshal([]byte(txt), &value); err == nil {
		return webImportCoerceImportDataDict(value, "LLM 抽取结果")
	}
	start := strings.Index(txt, "{")
	end := strings.LastIndex(txt, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(txt[start:end+1]), &value); err == nil {
			return webImportCoerceImportDataDict(value, "LLM 抽取结果")
		}
	}
	return nil, fmt.Errorf("LLM 返回内容不是合法 JSON")
}

// parseReferenceTime 对齐 _parse_reference_time
func parseReferenceTime(value string) time.Time {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Now()
	}
	formats := []string{"2006/01/02 15:04:05", "2006/01/02 15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006/01/02", "2006-01-02"}
	for _, format := range formats {
		if parsed, err := time.ParseInLocation(format, text, time.Local); err == nil {
			return parsed
		}
	}
	return time.Now()
}

// extractChatTimeMetaWithLLM 对齐 _extract_chat_time_meta_with_llm
func (m *WebImportManager) extractChatTimeMetaWithLLM(ctx context.Context, text string, model ResolvedLLMModel, referenceTime string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	referenceNow := parseReferenceTime(referenceTime).Format("2006/01/02 15:04")
	lines := []string{
		"You are a time extraction engine for chat logs.",
		"Extract temporal information from the following chat paragraph.",
		"",
		"Rules:",
		"1. Use semantic understanding, not regex matching.",
		"2. Convert relative expressions to absolute local datetime using reference_now.",
		"3. If a time span exists, return event_time_start/event_time_end.",
		"4. If only one point in time exists, return event_time.",
		"5. If no reliable time info exists, keep all event_time fields null.",
		"6. Return JSON only.",
		"",
		"reference_now: " + referenceNow,
		"text:",
		text,
		"",
		"JSON schema:",
		"{",
		"  \"event_time\": null,",
		"  \"event_time_start\": null,",
		"  \"event_time_end\": null,",
		"  \"time_range\": null,",
		"  \"time_granularity\": null,",
		"  \"time_confidence\": 0.0",
		"}",
	}
	prompt := strings.Join(lines, "\n")
	// 时间字段抽取独立使用快速模型，不继承完整知识抽取的模型选择。
	timeModel, err := ResolveFastTextGenerationModel(m.deps.LLM.GetAvailableModels())
	if err != nil {
		m.deps.Logger.Warning(fmt.Sprintf("chat_log 时间语义抽取失败: %v", err))
		return nil, nil
	}
	result, err := m.llmCall(ctx, prompt, timeModel)
	if err != nil {
		m.deps.Logger.Warning(fmt.Sprintf("chat_log 时间语义抽取失败: %v", err))
		return nil, nil
	}
	rawTimeMeta := map[string]any{
		"event_time":       result["event_time"],
		"event_time_start": result["event_time_start"],
		"event_time_end":   result["event_time_end"],
		"time_range":       result["time_range"],
		"time_granularity": result["time_granularity"],
		"time_confidence":  result["time_confidence"],
	}
	normalized, err := NormalizeTimeMeta(rawTimeMeta)
	if err != nil {
		return nil, nil
	}
	for _, key := range []string{"event_time", "event_time_start", "event_time_end"} {
		if _, ok := normalized[key]; ok {
			return normalized, nil
		}
	}
	return nil, nil
}

// powFloat 计算浮点幂
func powFloat(base float64, exponent float64) float64 {
	result := 1.0
	for index := 0; index < int(exponent); index++ {
		result *= base
	}
	return result
}

// sleepContext 等待指定时长，可被上下文取消
func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
