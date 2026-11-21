package host

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"MaiGo/internal/person_info/identity"
	"MaiGo/internal/services"
	httputil2 "MaiGo/internal/webui/httputil"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const personInjectionPath = "/api/host/bots/{bot_id}/proxy/api/webui/person-migrations/user-injection"

type personMigrationPreview struct {
	ID     string                        `json:"preview_id"`
	Kind   string                        `json:"kind"`
	Status string                        `json:"status"`
	Record string                        `json:"record,omitempty"`
	Plan   *services.PersonMigrationPlan `json:"plan,omitempty"`
}

type personMigrationRequest struct {
	PreviewID string            `json:"preview_id"`
	Source    string            `json:"source"`
	Target    string            `json:"target"`
	Decisions map[string]string `json:"decisions"`
}

func (w *WebUI) mountPersonMigration(router chi.Router) {
	router.Get(personInjectionPath, w.handlePersonMigration("get"))
	for _, operation := range []string{"preview", "execute", "cancel"} {
		router.Post(personInjectionPath+"/"+operation, w.handlePersonMigration(operation))
	}
}

func (state personMigrationPreview) public() map[string]any {
	if state.ID == "" {
		return map[string]any{}
	}
	if state.Status == "preview" && state.Kind == "injection" && (state.Plan == nil || state.Plan.Version != 3) {
		return map[string]any{"kind": "injection", "status": "invalid", "error": "预览已失效，请重新预览"}
	}
	result := map[string]any{"preview_id": state.ID, "kind": state.Kind, "status": state.Status}
	if state.Record != "" {
		result["record"] = state.Record
	}
	if state.Plan != nil {
		move := state.Plan.Identity
		type publicConflict struct {
			services.PersonMigrationConflict
			SourceID string `json:"source_id"`
			TargetID string `json:"target_id"`
		}
		conflicts := make([]publicConflict, 0, len(state.Plan.Conflicts))
		for _, conflict := range state.Plan.Conflicts {
			conflicts = append(conflicts, publicConflict{PersonMigrationConflict: conflict, SourceID: move.Source, TargetID: move.Target})
		}
		result["conflicts"] = conflicts
		result["decisions"] = state.Plan.Decisions
		result["ready"] = state.Plan.Ready
		result["relations"] = []map[string]string{{"source": move.Source, "target": move.Target}}
		result["counts"] = state.Plan.Counts()
	}
	return result
}

func (w *WebUI) handlePersonMigration(operation string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		writeError := func(status int, message string) {
			httputil2.WriteJSON(rw, status, map[string]any{"detail": message})
		}
		if !w.requireSession(r) {
			writeError(http.StatusUnauthorized, "请登录 WebUI")
			return
		}
		bot, err := w.supervisor.workspace.Registry.GetBot(chi.URLParam(r, "bot_id"))
		if err != nil {
			writeError(http.StatusNotFound, "未找到 Bot")
			return
		}
		if operation != "get" {
			release, err := w.maintenance.tryLock()
			if err != nil {
				writeError(http.StatusConflict, err.Error())
				return
			}
			defer release()
		}
		path := filepath.Join(bot.Directory, "temp", "person-migration-preview.json")
		var state personMigrationPreview
		if err := services.ReadPersonMigrationJSON(path, &state); err != nil && !errors.Is(err, os.ErrNotExist) {
			writeError(http.StatusConflict, err.Error())
			return
		}
		if operation == "get" {
			httputil2.WriteJSON(rw, http.StatusOK, map[string]any{"preview": state.public()})
			return
		}
		var request personMigrationRequest
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<20)).Decode(&request); err != nil {
			writeError(http.StatusUnprocessableEntity, "请求体格式错误")
			return
		}
		ctx := context.WithoutCancel(r.Context())
		if operation != "preview" {
			if _, err := hex.DecodeString(request.PreviewID); err != nil || len(request.PreviewID) != 32 {
				writeError(http.StatusUnprocessableEntity, "无效的 preview_id")
				return
			}
			if state.ID != request.PreviewID || state.Status != "preview" {
				writeError(http.StatusConflict, "预览不存在、已执行或已取消，请刷新")
				return
			}
		}
		switch operation {
		case "preview":
			if !identity.ValidID(request.Source) || !identity.ValidID(request.Target) || request.Source == request.Target ||
				utf8.RuneCountInString(request.Source) > 255 || utf8.RuneCountInString(request.Target) > 255 {
				writeError(http.StatusUnprocessableEntity, "来源和目标须为不同的用户 ID，且不能包含首尾空白")
				return
			}
			if state.Status == "preview" && state.Kind != "injection" {
				writeError(http.StatusConflict, "请先取消旧迁移预览")
				return
			}
			plan, err := services.PreviewPersonMigration(ctx, bot.Directory, "qq", request.Source, request.Target, request.Decisions)
			if err != nil {
				writeError(http.StatusConflict, err.Error())
				return
			}
			state = newPersonMigrationPreview(plan)
		case "execute":
			if state.Kind != "injection" {
				writeError(http.StatusGone, "Union 身份迁移已停用，请取消旧预览")
				return
			}
			if state.Plan == nil || !state.Plan.Ready {
				writeError(http.StatusConflict, "请先完成预览和冲突选择")
				return
			}
			job, err := services.ExecutePersonMigration(ctx, bot.Directory, *state.Plan)
			if err != nil && job == "" {
				if errors.Is(err, services.ErrPersonMigrationStale) {
					move := state.Plan.Identity
					plan, refreshErr := services.PreviewPersonMigration(ctx, bot.Directory, move.Platform, move.Source, move.Target, nil)
					if refreshErr == nil {
						err = errors.Join(err, writePersonMigrationPreview(path, newPersonMigrationPreview(plan)))
					}
				}
				writeError(http.StatusConflict, err.Error())
				return
			}
			state.Status, state.Record, state.Plan = "completed", job, nil
			if err != nil {
				writeError(http.StatusConflict, errors.Join(err, writePersonMigrationPreview(path, state)).Error())
				return
			}
		case "cancel":
			state.Status, state.Plan = "cancelled", nil
		}
		if err := writePersonMigrationPreview(path, state); err != nil {
			writeError(http.StatusConflict, err.Error())
			return
		}
		httputil2.WriteJSON(rw, http.StatusOK, state.public())
	}
}

func newPersonMigrationPreview(plan services.PersonMigrationPlan) personMigrationPreview {
	return personMigrationPreview{ID: strings.ReplaceAll(uuid.NewString(), "-", ""), Kind: "injection", Status: "preview", Plan: &plan}
}

func writePersonMigrationPreview(path string, state personMigrationPreview) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return services.WritePersonMigrationJSON(path, state)
}
