package webuiapi

// 聊天数据迁移 API；执行任务独立于浏览器连接，对齐 routers/chat_migration.py。

import (
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/services"
)

type chatMigrationPreviewRequest struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type chatMigrationExecuteRequest struct {
	Decisions     map[string]string `json:"decisions"`
	ConfirmMemory bool              `json:"confirm_memory"`
}

func (d Deps) handleChatMigrationPreview(w http.ResponseWriter, r *http.Request) {
	var request chatMigrationPreviewRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	source := strings.TrimSpace(request.Source)
	target := strings.TrimSpace(request.Target)
	if source == "" || target == "" || len(source) > 255 || len(target) > 255 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "source 和 target 必须是 1 到 255 字符"})
		return
	}
	job, err := services.PreviewChatMigration(r.Context(), source, target)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (d Deps) handleChatMigrationList(w http.ResponseWriter, r *http.Request) {
	items, err := services.ListChatMigrationJobs(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (d Deps) handleChatMigrationGet(w http.ResponseWriter, r *http.Request) {
	job, err := services.GetChatMigrationJob(r.Context(), chi.URLParam(r, "job_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (d Deps) handleChatMigrationExecute(w http.ResponseWriter, r *http.Request) {
	var request chatMigrationExecuteRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	if request.Decisions == nil {
		request.Decisions = map[string]string{}
	}
	result, err := services.StartChatMigration(r.Context(), chi.URLParam(r, "job_id"), request.Decisions, request.ConfirmMemory)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d Deps) handleChatMigrationRecover(w http.ResponseWriter, r *http.Request) {
	result, err := services.RecoverChatMigration(r.Context(), chi.URLParam(r, "job_id"))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
