package webuiapi

// 推理记录关联查看，对齐 reasoning_process.py 的 /related-records

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"MaiGo/internal/maisaka/display"
)

var (
	reasoningTraceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reasoningCallIDPattern  = regexp.MustCompile(`调用\s*ID[:：]\s*([\w-]+)`)
)

func loadReasoningJSONPayload(path string) (map[string]any, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func isNumericReasoningStem(stem string) bool {
	if stem == "" {
		return false
	}
	for _, char := range stem {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// handleReasoningRelatedRecords 读取同轮次的小型关联索引；旧记录沿用工具调用关联
func (d Deps) handleReasoningRelatedRecords(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	stage := strings.TrimSpace(r.URL.Query().Get("stage"))
	stem := strings.TrimSpace(r.URL.Query().Get("stem"))
	if !isSafeReasoningName(session) || !isSafeReasoningName(stage) || !isNumericReasoningStem(stem) {
		writeReasoningFailure(w, http.StatusBadRequest, "推理记录标识不合法")
		return
	}
	payload, ok := loadReasoningJSONPayload(filepath.Join(root, stage, session, stem+".json"))
	if !ok {
		writeReasoningFailure(w, http.StatusBadRequest, "推理记录标识不合法")
		return
	}
	records := []display.ReasoningStep{{Stage: stage, Session: session, Stem: stem}}
	metadata, _ := payload["metadata"].(map[string]any)
	traceID, _ := metadata["reasoning_trace_id"].(string)
	if reasoningTraceIDPattern.MatchString(strings.TrimSpace(traceID)) {
		for _, step := range display.FindReasoningSteps(root, strings.TrimSpace(traceID), session) {
			if !isSafeReasoningName(step.Stage) || step.Session != session || !isNumericReasoningStem(step.Stem) {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, step.Stage, session, step.Stem+".json")); err != nil {
				continue
			}
			records = append(records, step)
		}
	}
	if len(records) == 1 {
		records = append(records, relatedReasoningFallback(root, session, stage, stem, payload)...)
	}
	unique := map[string]display.ReasoningStep{}
	for _, step := range records {
		unique[step.Stage+"\x00"+step.Session+"\x00"+step.Stem] = step
	}
	ordered := make([]display.ReasoningStep, 0, len(unique))
	for _, step := range unique {
		ordered = append(ordered, step)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftErr := strconv.ParseInt(ordered[i].Stem, 10, 64)
		right, rightErr := strconv.ParseInt(ordered[j].Stem, 10, 64)
		if leftErr == nil && rightErr == nil && left != right {
			return left < right
		}
		return ordered[i].Stage < ordered[j].Stage
	})
	items := make([]map[string]any, 0, len(ordered))
	for _, step := range ordered {
		items = append(items, reasoningToolCallRecord("", step.Stage, step.Session, step.Stem))
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": items})
}

// parseReasoningSourceURI 解析记录中保存的 Planner 预览地址
func parseReasoningSourceURI(root, session, raw string) (display.ReasoningStep, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return display.ReasoningStep{}, false
	}
	paths := parsed.Query()["path"]
	if len(paths) != 1 {
		return display.ReasoningStep{}, false
	}
	parts := strings.Split(filepath.ToSlash(path.Clean(strings.TrimSpace(paths[0]))), "/")
	if len(parts) != 3 || parts[0] != "planner" || parts[1] != session || filepath.Ext(parts[2]) != ".json" {
		return display.ReasoningStep{}, false
	}
	stem := strings.TrimSuffix(parts[2], ".json")
	if !isNumericReasoningStem(stem) {
		return display.ReasoningStep{}, false
	}
	if _, err := os.Stat(filepath.Join(root, "planner", session, stem+".json")); err != nil {
		return display.ReasoningStep{}, false
	}
	return display.ReasoningStep{Stage: "planner", Session: session, Stem: stem}, true
}

// relatedReasoningFallback 旧记录按选择原因的调用 ID 与输出中的调用 ID 反查
func relatedReasoningFallback(root, session, stage, stem string, payload map[string]any) []display.ReasoningStep {
	out := []display.ReasoningStep{}
	if request, ok := payload["request"].(map[string]any); ok {
		if match := reasoningCallIDPattern.FindStringSubmatch(stringValue(request["selection_reason"])); len(match) == 2 {
			if source, found := reasoningToolCallSource(root, session, stage, stem, match[1]); found {
				out = append(out, source)
			}
		}
	}
	callIDs := map[string]bool{}
	if items, ok := payload["output_items"].([]any); ok {
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item == nil || stringValue(item["item_type"]) != "FunctionCallItem" {
				continue
			}
			toolCall, _ := item["tool_call"].(map[string]any)
			if callID := strings.TrimSpace(stringValue(toolCall["call_id"])); callID != "" {
				callIDs[callID] = true
			}
		}
	}
	if len(callIDs) == 0 {
		return out
	}
	sourceTimestamp, err := strconv.ParseInt(stem, 10, 64)
	if err != nil {
		return out
	}
	for _, candidateStage := range listReasoningStageNames(root) {
		if candidateStage == stage || len(callIDs) == 0 {
			continue
		}
		sessionDir := filepath.Join(root, candidateStage, session)
		if info, statErr := os.Stat(sessionDir); statErr != nil || !info.IsDir() {
			continue
		}
		for callID, foundStem := range findReasoningToolCallStems(sessionDir, sourceTimestamp, callIDs) {
			out = append(out, display.ReasoningStep{Stage: candidateStage, Session: session, Stem: foundStem})
			delete(callIDs, callID)
		}
	}
	return out
}

// reasoningToolCallSource 复用工具来源定位逻辑
func reasoningToolCallSource(root, session, toolStage, stem, callID string) (display.ReasoningStep, bool) {
	sourceTimestamp, err := strconv.ParseInt(stem, 10, 64)
	if err != nil {
		return display.ReasoningStep{}, false
	}
	stageNames := listReasoningStageNames(root)
	sort.SliceStable(stageNames, func(i, j int) bool { return stageNames[i] == "planner" && stageNames[j] != "planner" })
	for _, stage := range stageNames {
		if stage == toolStage {
			continue
		}
		sessionDir := filepath.Join(root, stage, session)
		if info, statErr := os.Stat(sessionDir); statErr != nil || !info.IsDir() {
			continue
		}
		if sourceStem := findReasoningToolCallSourceStem(sessionDir, sourceTimestamp, callID); sourceStem != "" {
			return display.ReasoningStep{Stage: stage, Session: session, Stem: sourceStem}, true
		}
	}
	return display.ReasoningStep{}, false
}
