package webuiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (d Deps) handleReasoningReplay(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	request := ReasoningReplayRequest{ItemSchemaVersion: 1}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeReasoningFailure(w, http.StatusUnprocessableEntity, "请求体不是合法 JSON")
			return
		}
	}
	request.ModelName = strings.TrimSpace(request.ModelName)
	if request.ModelName == "" {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "model_name 不能为空")
		return
	}
	if request.ItemSchemaVersion != 1 {
		writeReasoningFailure(w, http.StatusBadRequest, "重放 Item schema 版本不匹配")
		return
	}
	if len(request.RequestItems) == 0 {
		writeReasoningFailure(w, http.StatusBadRequest, "重放 Items 不能为空")
		return
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "temperature 必须在 0 到 2 之间")
		return
	}
	if request.MaxTokens != nil && *request.MaxTokens < 1 {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "max_tokens 必须大于 0")
		return
	}
	if request.ToolDefinitions == nil && strings.TrimSpace(request.SourcePath) != "" {
		definitions, err := d.loadReplayToolDefinitions(request.SourcePath)
		if err != nil {
			status := http.StatusBadRequest
			if err.Error() == "文件不存在" {
				status = http.StatusNotFound
			}
			writeReasoningFailure(w, status, err.Error())
			return
		}
		request.ToolDefinitions = definitions
	}

	service := d.reasoningReplayService()
	if service == nil {
		writeReasoningFailure(w, http.StatusServiceUnavailable, "推理重放服务未接线：需要 agent LLM 路由器，或注入 webuiapi.Deps.ReasoningReplay")
		return
	}
	result, err := service.ReplayReasoning(r.Context(), request)
	if err != nil {
		var httpErr *ReasoningReplayHTTPError
		if errors.As(err, &httpErr) {
			writeReasoningFailure(w, httpErr.Status, httpErr.Message)
			return
		}
		writeReasoningFailure(w, http.StatusInternalServerError, "推理重放失败: "+err.Error())
		return
	}
	if result.OutputItems == nil {
		result.OutputItems = []map[string]any{}
	}
	if result.GenerationAttempts == nil {
		result.GenerationAttempts = []map[string]any{}
	}
	if result.SchemaVersion == 0 {
		result.SchemaVersion = 6
	}
	if result.ModelName == "" {
		result.ModelName = request.ModelName
	}
	var replayError any
	if result.Error != "" {
		replayError = result.Error
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":                  result.Success,
		"output_items":             result.OutputItems,
		"schema_version":           result.SchemaVersion,
		"generation_attempts":      result.GenerationAttempts,
		"model_name":               result.ModelName,
		"prompt_tokens":            result.PromptTokens,
		"completion_tokens":        result.CompletionTokens,
		"total_tokens":             result.TotalTokens,
		"prompt_cache_hit_tokens":  result.PromptCacheHit,
		"prompt_cache_miss_tokens": result.PromptCacheMiss,
		"duration_ms":              result.DurationMS,
		"error":                    replayError,
	})
}
