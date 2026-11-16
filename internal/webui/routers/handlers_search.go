package webuiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"MaiGo/internal/llm_models"
	webuiplugin "MaiGo/internal/webui/routers/plugin"
	"MaiGo/internal/webui/services/ai_search"
)

var defaultWebSearchCache = search.NewCache(search.DefaultCacheTTL, search.DefaultCacheMaxEntries)

var activeHelpSessions sync.Map

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
	var pluginTools *search.PluginToolset
	if d.PluginMarket != nil {
		pluginTools = webuiplugin.NewAISearchPluginToolset(d.pluginDeps())
	}
	runner := search.NewLLMRunnerForInstance(func() *llm.Router { return d.Agent.LLMRouter() }, d.InstanceDir, pluginTools)
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
	var response search.Response
	var runErr error
	if request.HelpSessionID != nil {
		response, runErr = d.runHelpTurn(ctx, request, service, emit, writeEvent)
	} else {
		response, runErr = service.Execute(ctx, request, emit)
	}
	if streamErr != nil {
		return
	}
	if runErr != nil {
		message := searchErrorMessage(runErr)
		status := search.StatusCode(runErr)
		if strings.Contains(message, "答疑会话正在处理") {
			status = http.StatusConflict
		} else if strings.Contains(message, "答疑会话不存在") {
			status = http.StatusNotFound
		}
		failed := search.NewProgressEvent("failed")
		failed.Status = "failed"
		failed.Error = message
		_ = writeEvent(failed)
		_ = writeEvent(map[string]any{"type": "error", "message": message, "status": status})
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

func (d Deps) helpHistoryDB() *gorm.DB {
	if d.Memory == nil {
		return nil
	}
	return d.Memory.GetDB()
}

func (d Deps) handleHelpSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := search.ListHelpSessions(d.helpHistoryDB())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取答疑历史失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (d Deps) handleHelpSessionDetail(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "session_id"))
	if _, err := uuid.Parse(sessionID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "session_id 必须是 UUID"})
		return
	}
	detail, ok, err := search.GetHelpSession(d.helpHistoryDB(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取答疑会话失败: " + err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "答疑会话不存在"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (d Deps) handleHelpSessionDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "session_id"))
	if _, err := uuid.Parse(sessionID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "session_id 必须是 UUID"})
		return
	}
	if _, busy := activeHelpSessions.LoadOrStore(sessionID, struct{}{}); busy {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "该答疑会话正在处理，请稍后重试"})
		return
	}
	defer activeHelpSessions.Delete(sessionID)
	deleted, err := search.DeleteHelpSession(d.helpHistoryDB(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "删除答疑会话失败: " + err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "答疑会话不存在"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runHelpTurn 接管一轮答疑：登记问题、执行搜索并保存结果
func (d Deps) runHelpTurn(
	ctx context.Context, request search.Request, service *search.Service,
	emit search.Emit, writeEvent func(any) error,
) (search.Response, error) {
	newSession := request.HelpSessionID != nil && strings.TrimSpace(*request.HelpSessionID) == "new"
	sessionID := ""
	if newSession {
		sessionID = uuid.NewString()
	} else {
		sessionID = strings.TrimSpace(*request.HelpSessionID)
	}
	if _, busy := activeHelpSessions.LoadOrStore(sessionID, struct{}{}); busy {
		return search.Response{}, &search.ValidationError{Message: "该答疑会话正在处理，请稍后重试"}
	}
	defer activeHelpSessions.Delete(sessionID)

	db := d.helpHistoryDB()
	turnID, history, ok, err := search.BeginHelpTurn(db, sessionID, strings.TrimSpace(request.Query), newSession)
	if err != nil {
		return search.Response{}, err
	}
	if !ok {
		return search.Response{}, &search.ValidationError{Message: "答疑会话不存在"}
	}
	_ = writeEvent(map[string]any{"type": "session", "id": sessionID})
	request.History = history
	response, runErr := service.Execute(ctx, request, emit)
	if runErr != nil {
		_ = search.FinishHelpTurn(db, turnID, nil, searchErrorMessage(runErr))
		return search.Response{}, runErr
	}
	if err := search.FinishHelpTurn(db, turnID, &response, ""); err != nil {
		return response, fmt.Errorf("保存答疑记录失败: %w", err)
	}
	return response, nil
}
