package webuiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/webui/services/ai_search"
)

var defaultWebSearchCache = search.NewCache(search.DefaultCacheTTL, search.DefaultCacheMaxEntries)

func (d Deps) searchService() (*search.Service, error) {
	if d.SearchService != nil {
		return d.SearchService, nil
	}
	if d.SearchRunner != nil {
		return search.NewServiceWithCache(d.SearchRunner, defaultWebSearchCache), nil
	}
	if d.Agent == nil || d.Agent.LLMRouter() == nil {
		return nil, &search.UnavailableError{Message: "AI 搜索服务未接线：需要 LLM 路由器或注入 webuiapi.Deps.SearchRunner"}
	}
	runner := search.NewLLMRunnerForInstance(func() *llm.Router { return d.Agent.LLMRouter() }, d.InstanceDir)
	return search.NewServiceWithCache(runner, defaultWebSearchCache), nil
}

// handleSearchAIStream 对应 Python search.py 的 POST /search/ai/stream
func (d Deps) handleSearchAIStream(w http.ResponseWriter, r *http.Request) {
	var request search.Request
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	if err := search.ValidateRequest(request); err != nil {
		status := http.StatusUnprocessableEntity
		var validation *search.ValidationError
		if errors.As(err, &validation) && strings.Contains(validation.Message, "重复 ID") {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	service, err := d.searchService()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error(), "implemented": false})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeInternalError(w, fmt.Errorf("当前 HTTP 服务器不支持流式响应"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	writeEvent := func(payload any) error {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(encoded, '\n')); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	// 与 Python _stream_ai_search_events 对齐：进度事件逐条输出，最后输出 result
	var streamErr error
	emit := func(event search.ProgressEvent) {
		if streamErr != nil {
			return
		}
		if event.Type == "" {
			event.Type = "progress"
		}
		streamErr = writeEvent(event)
	}
	ctx, cancel := context.WithTimeout(r.Context(), search.Timeout)
	defer cancel()
	response, runErr := service.Execute(ctx, request, emit)
	if streamErr != nil {
		return
	}
	if runErr != nil {
		message := searchErrorMessage(runErr)
		failed := search.NewProgressEvent("failed")
		failed.Status = "failed"
		failed.Error = message
		_ = writeEvent(failed)
		_ = writeEvent(map[string]any{"type": "error", "message": message, "status": search.StatusCode(runErr)})
		return
	}
	completed := search.NewProgressEvent("completed")
	completed.Status = "completed"
	if writeEvent(completed) != nil {
		return
	}
	_ = writeEvent(map[string]any{"type": "result", "response": response})
}

func searchErrorMessage(err error) string {
	var output *search.OutputError
	if errors.As(err, &output) {
		return "AI 搜索结果解析失败: " + output.Message
	}
	var unavailable *search.UnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Message
	}
	return "AI 搜索调用失败: " + err.Error()
}
