package webuiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/common/self_update"
	"MaiGo/internal/runtime"

	"github.com/bytedance/sonic"
)

// 本文件对齐 ../MaiBot/src/webui/routers/self_update.py 的状态、检查、流式检查与登记更新

type selfUpdateSourceRequest struct {
	SourceID     string `json:"source_id"`
	CustomPrefix string `json:"custom_prefix"`
}

type selfUpdateCheckRequest struct {
	SourceID     string `json:"source_id"`
	CustomPrefix string `json:"custom_prefix"`
	Force        *bool  `json:"force"`
}

type selfUpdateApplyRequest struct {
	SourceID       string `json:"source_id"`
	CustomPrefix   string `json:"custom_prefix"`
	ExpectedCommit string `json:"expected_commit"`
	TargetCommit   string `json:"target_commit"`
}

func (d Deps) selfUpdateOptions() selfupdate.CheckOptions {
	return selfupdate.CheckOptions{
		Root:                  d.InstanceDir,
		CurrentVersion:        d.buildVersion(),
		EnforcePublicOutbound: d.selfUpdateEnforcePublicOutbound(),
	}
}

func (d Deps) selfUpdateEnforcePublicOutbound() *bool {
	value := d.enforcePublicOutbound()
	return &value
}

func (d Deps) handleSelfUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	status, err := selfupdate.CurrentStatus(r.Context(), d.selfUpdateOptions())
	if err != nil {
		writeSelfUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (d Deps) handleSelfUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var body selfUpdateCheckRequest
	if r.Body != nil {
		_ = sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body)
	}
	options := d.selfUpdateOptions()
	options.SourceID = body.SourceID
	options.CustomPrefix = body.CustomPrefix
	if body.Force == nil {
		options.Force = true
	} else {
		options.Force = *body.Force
	}
	status, err := selfupdate.CheckStatus(r.Context(), options)
	if err != nil {
		writeSelfUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (d Deps) handleSelfUpdateCheckStream(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var body selfUpdateCheckRequest
	if r.Body != nil {
		_ = sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body)
	}
	options := d.selfUpdateOptions()
	options.SourceID = body.SourceID
	options.CustomPrefix = body.CustomPrefix
	if body.Force == nil {
		options.Force = true
	} else {
		options.Force = *body.Force
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	var mu sync.Mutex
	writeEvent := func(event map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(event)
		if flusher != nil {
			flusher.Flush()
		}
	}
	options.OnProgress = func(message string) {
		writeEvent(map[string]any{"type": "progress", "message": message})
	}

	done := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	go func() {
		defer close(heartbeatStopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				writeEvent(map[string]any{"type": "heartbeat"})
			}
		}
	}()

	status, err := selfupdate.CheckStatus(r.Context(), options)
	close(done)
	<-heartbeatStopped
	if err != nil {
		writeEvent(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	writeEvent(map[string]any{"type": "result", "status": status})
}

func (d Deps) handleSelfUpdateApply(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var body selfUpdateApplyRequest
	if r.Body != nil {
		_ = sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body)
	}
	expected := strings.TrimSpace(body.ExpectedCommit)
	if expected == "" {
		expected = strings.TrimSpace(body.TargetCommit)
	}
	if expected == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "缺少更新目标版本或提交"})
		return
	}
	options := selfupdate.RequestOptions{
		Root:                  d.InstanceDir,
		CurrentVersion:        d.buildVersion(),
		SourceID:              body.SourceID,
		CustomPrefix:          body.CustomPrefix,
		EnforcePublicOutbound: d.selfUpdateEnforcePublicOutbound(),
	}
	if _, err := selfupdate.RequestUpdate(r.Context(), expected, options); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	if err := runtime.ScheduleWorkerRestart(""); err != nil {
		_ = selfupdate.ClearPendingUpdate(d.InstanceDir)
		status := http.StatusInternalServerError
		if errors.Is(err, runtime.ErrRestartRequiresWorker) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "麦麦正在更新并重启..."})
}

func writeSelfUpdateError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch selfupdate.ErrorCode(err) {
	case "fetch_failed":
		status = http.StatusBadGateway
	case "invalid_source":
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{"detail": err.Error()})
}
