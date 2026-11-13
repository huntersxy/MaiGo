package maisaka

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// terminalPlannerPanelMu 保证多会话同时输出终端面板时块不交错
var terminalPlannerPanelMu sync.Mutex

// emitPlannerTerminalPanel 在开启 show_maisaka_thinking 时把本轮 Planner 结果打到终端
// payload 与 Monitor 快照共用同一份构造结果，展示层不改变监控数据
func (r *Runtime) emitPlannerTerminalPanel(payload map[string]any, state *CycleRuntimeState, detail CycleDetail) {
	if r == nil || r.opts.ShowThinking == nil || !r.opts.ShowThinking() {
		return
	}
	writer := r.opts.TerminalOutput
	if writer == nil {
		writer = os.Stdout
	}
	renderPlannerTerminalPanel(writer, r.sessionID, r.sessionName, r.opts.ReplyFrequency, payload, state, detail)
}

// renderPlannerTerminalPanel 输出纯文本 Planner 面板，对齐 Python runtime_mixin 的信息范围
// Python 用 Rich Panel 着色；Go 只保留字段与顺序，颜色/边框不追求像素一致
func renderPlannerTerminalPanel(writer io.Writer, sessionID, sessionName string, replyFrequency func() float64, payload map[string]any, state *CycleRuntimeState, detail CycleDetail) {
	if writer == nil {
		return
	}
	planner := payloadMap(payload, "planner")
	finalState := payloadMap(payload, "final_state")
	var b strings.Builder
	title := "MaiSaka 循环"
	if cycleID := payloadInt(payload, "cycle_id"); cycleID > 0 {
		title = fmt.Sprintf("MaiSaka 循环 [%d]", cycleID)
	}
	fmt.Fprintf(&b, "\n=== %s ===\n", title)
	fmt.Fprintf(&b, "聊天流名称：%s\n", fallbackText(sessionName, "未命名"))
	fmt.Fprintf(&b, "聊天流ID：%s\n", fallbackText(sessionID, "未记录"))
	if replyFrequency != nil {
		fmt.Fprintf(&b, "当前回复频率：%.0f%%\n", replyFrequency()*100)
	}
	if round := strings.TrimSpace(detail.RoundText); round != "" {
		fmt.Fprintf(&b, "循环轮次：%s\n", round)
	}
	if logicalTurn := strings.TrimSpace(detail.LogicalTurn); logicalTurn != "" {
		fmt.Fprintf(&b, "逻辑轮次：%s\n", logicalTurn)
	}
	if agentState := payloadString(finalState, "agent_state"); agentState != "" {
		fmt.Fprintf(&b, "Agent 状态：%s\n", agentState)
	}
	modelName := payloadString(planner, "model_name")
	if modelName == "" && state != nil {
		modelName = strings.TrimSpace(state.Response.ModelName)
	}
	fmt.Fprintf(&b, "请求模型：%s\n", fallbackText(modelName, "未记录"))
	fmt.Fprintf(&b, "本次请求token消耗：输入 %d / 输出 %d\n",
		payloadInt(planner, "prompt_tokens"), payloadInt(planner, "completion_tokens"))
	fmt.Fprintf(&b, "Planner 耗时：%.2f s\n", payloadFloat(planner, "duration_ms")/1000)
	if interrupted, _ := payload["interrupted"].(bool); interrupted {
		b.WriteString("Planner 已打断\n")
	}
	tools := payloadTools(payload)
	fmt.Fprintf(&b, "工具执行：%d 次\n", len(tools))
	for _, tool := range tools {
		status := "成功"
		if success, ok := tool["success"].(bool); ok && !success {
			status = "失败"
		}
		fmt.Fprintf(&b, "  - %s（%s，%d ms）：%s\n",
			fallbackText(payloadString(tool, "tool_name"), "未命名工具"),
			status,
			payloadInt64(tool, "duration_ms"),
			fallbackText(payloadString(tool, "summary"), "无摘要"),
		)
	}
	content := payloadString(planner, "content")
	fmt.Fprintf(&b, "Maisaka 返回：%s\n", fallbackText(content, "（空）"))
	if reason := payloadString(finalState, "end_reason"); reason != "" {
		if endDetail := payloadString(finalState, "end_detail"); endDetail != "" {
			fmt.Fprintf(&b, "结束原因：%s（%s）\n", reason, endDetail)
		} else {
			fmt.Fprintf(&b, "结束原因：%s\n", reason)
		}
	}
	if state != nil {
		for _, line := range state.PlannerExtraLines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			fmt.Fprintf(&b, "· %s\n", line)
		}
	}
	b.WriteString(strings.Repeat("─", 40))
	b.WriteString("\n")
	terminalPlannerPanelMu.Lock()
	defer terminalPlannerPanelMu.Unlock()
	_, _ = io.WriteString(writer, b.String())
}

func payloadMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	mapped, _ := values[key].(map[string]any)
	return mapped
}

func payloadString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}

func payloadInt(values map[string]any, key string) int {
	return int(payloadInt64(values, key))
}

func payloadInt64(values map[string]any, key string) int64 {
	if values == nil {
		return 0
	}
	switch typed := values[key].(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func payloadFloat(values map[string]any, key string) float64 {
	if values == nil {
		return 0
	}
	switch typed := values[key].(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func payloadTools(values map[string]any) []map[string]any {
	if values == nil {
		return nil
	}
	switch typed := values["tools"].(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	default:
		return nil
	}
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
