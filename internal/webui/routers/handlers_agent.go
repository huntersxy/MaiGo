package webuiapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"

	"MaiGo/internal/agent"
)

// AgentControlService 外接 ACP Agent 的可注入控制面，便于 main 接线与测试替身
type AgentControlService interface {
	Status(computerStatus map[string]any) agent.Status
	Control(ctx context.Context, operation string) (agent.Status, error)
	Enqueue(text string, images []string, kind string, replyTo string) (agent.HistoryRecord, error)
	Agent() *agent.Agent
}

// pythonAgentComputer 对齐 Python computer.service.status() 的九字段结构
func pythonAgentComputer(raw map[string]any) map[string]any {
	computer := map[string]any{
		"enabled":         false,
		"paused":          false,
		"takeover":        false,
		"error":           "",
		"activity":        map[string]any{},
		"pending_request": nil,
		"connected":       false,
		"sandbox_id":      nil,
		"sandbox_running": false,
	}
	for key := range computer {
		if value, ok := raw[key]; ok {
			computer[key] = value
		}
	}
	return computer
}

// pythonAgentStatus 对齐 Python agent.service.status() 的字段与空值语义
func pythonAgentStatus(status agent.Status) map[string]any {
	var sessionID any
	if status.ExternalSessionID != "" {
		sessionID = status.ExternalSessionID
	}
	return map[string]any{
		"enabled":             status.Enabled,
		"name":                status.Name,
		"agent_id":            status.AgentID,
		"backend":             "acp",
		"desktop_enabled":     status.DesktopEnabled,
		"external_session_id": sessionID,
		"paused":              status.Paused,
		"error":               status.Error,
		"agent_state":         status.AgentState,
		"computer":            pythonAgentComputer(status.Computer),
	}
}

// disabledAgentStatus 对齐 Agent 未启动时 AgentService.status() 的形状
func disabledAgentStatus() map[string]any {
	return pythonAgentStatus(agent.Status{
		Name:       "麦麦的 Agent",
		AgentID:    "__maibot_agent__",
		AgentState: "stopped",
	})
}

func (d Deps) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if d.ACPAgent == nil {
		writeJSON(w, http.StatusOK, disabledAgentStatus())
		return
	}
	writeJSON(w, http.StatusOK, pythonAgentStatus(d.ACPAgent.Status(nil)))
}

func (d Deps) handleAgentControl(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if d.ACPAgent == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "Agent 未启动，请先启用 agent.enabled。"})
		return
	}
	var request struct {
		Operation string `json:"operation"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeValidationError(w, "operation 只接受 pause/resume")
		return
	}
	operation := strings.TrimSpace(request.Operation)
	if operation != "pause" && operation != "resume" {
		writeValidationError(w, "operation 只接受 pause/resume")
		return
	}
	status, err := d.ACPAgent.Control(r.Context(), operation)
	if err != nil {
		if errors.Is(err, agent.ErrDisabled) {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "Agent 未启动，请先启用 agent.enabled。"})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pythonAgentStatus(status))
}

// agentHistoryEvents 把历史记录转换为前端事件索引，对齐 Python history.events
func agentHistoryEvents(records []agent.HistoryRecord, after int) []map[string]any {
	if after < 0 {
		after = 0
	}
	if after > len(records) {
		after = len(records)
	}
	events := make([]map[string]any, 0, len(records)-after)
	for index, record := range records[after:] {
		events = append(events, map[string]any{
			"index": after + index,
			"id":    record.ID,
			"time":  record.Time,
			"kind":  record.Kind,
			"items": record.Items,
		})
	}
	return events
}

func (d Deps) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	after := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeValidationError(w, "after 不能为负数")
			return
		}
		after = parsed
	}
	if d.ACPAgent == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"events":          []map[string]any{},
			"queued":          []map[string]any{},
			"state":           "disabled",
			"error":           "",
			"pending":         false,
			"backend":         "acp",
			"desktop_enabled": false,
		})
		return
	}
	agent := d.ACPAgent.Agent()
	status := d.ACPAgent.Status(nil)
	if agent == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"events":          []map[string]any{},
			"queued":          []map[string]any{},
			"state":           "disabled",
			"error":           status.Error,
			"pending":         false,
			"backend":         "acp",
			"desktop_enabled": status.DesktopEnabled,
		})
		return
	}
	records, err := agent.History().Load()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "读取 Agent 历史失败: "+err.Error())
		return
	}
	state := agent.State()
	if status.Paused {
		state = "paused"
	}
	// 对齐 agent.py：先取 inbox 再读历史，消费竞态只会造成重复不会漏
	queued := make([]map[string]any, 0, agent.InboxSize())
	for _, record := range agent.InboxSnapshot() {
		queued = append(queued, map[string]any{
			"index":    -1,
			"id":       record.ID,
			"time":     record.Time,
			"kind":     record.Kind,
			"reply_to": record.ReplyTo,
			"items":    record.Items,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":          agentHistoryEvents(records, after),
		"queued":          queued,
		"state":           state,
		"error":           status.Error,
		"pending":         agent.HasPending(),
		"backend":         "acp",
		"desktop_enabled": status.DesktopEnabled,
	})
}

func (d Deps) handleAgentMessage(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var request struct {
		Text string `json:"text"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeValidationError(w, "消息不能为空")
		return
	}
	text := strings.TrimSpace(request.Text)
	if text == "" {
		writeValidationError(w, "消息不能为空")
		return
	}
	if d.ACPAgent == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "Agent 未启动"})
		return
	}
	record, err := d.ACPAgent.Enqueue(text, nil, "user", "")
	if err != nil {
		if errors.Is(err, agent.ErrDisabled) {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "Agent 未启动"})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": record.ID, "backend": "acp"})
}
