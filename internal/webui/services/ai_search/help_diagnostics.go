package search

// WebUI 答疑 Agent 的有界运行诊断，对齐 src/webui/services/ai_help_diagnostics.py

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/common/logger"
)

const (
	diagnosticRecentLogsTool      = "read_recent_logs"
	diagnosticRecentReasoningTool = "list_recent_reasoning"
	diagnosticReadReasoningTool   = "read_reasoning_record"
	diagnosticReasoningScanLimit  = 48
	diagnosticReasoningMaxBytes   = 256 * 1024
)

var diagnosticToolNames = map[string]struct{}{
	diagnosticRecentLogsTool: {}, diagnosticRecentReasoningTool: {}, diagnosticReadReasoningTool: {},
}

var (
	diagnosticPrivateLogText  = regexp.MustCompile(`(?i)(?:authorization|api[\s_-]*key|password|passwd|cookie|secret|(?:access|refresh|auth)[\s_-]*token|(?:bearer|basic)\s+\S+|(?:sk|rk|pk)-[a-z0-9_-]{8,}|gh[pousr]_[a-z0-9]{12,}|AKIA[A-Z0-9]{16}|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.|(?:prompt|request[\s_-]*body|(?:user|chat)[\s_-]*message|token|messages|request_items|output_items|reasoning_content)['"]?\s*[:=])|<(?:message|prompt)\b|密钥|密码|口令|令牌|凭据|消息正文|消息内容|请求正文`)
	diagnosticURL             = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	diagnosticHTTPStatus      = regexp.MustCompile(`(?i)\b(?:HTTP(?:/[0-9.]+)?\s+|status(?:_code)?\s*[:=]\s*)([1-5]\d{2})\b`)
	diagnosticIdentifier      = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,96}$`)
	diagnosticTimestamp       = regexp.MustCompile(`^[0-9TtZz:+., -]{1,40}$`)
	diagnosticReasoningStatus = map[string]struct{}{
		"failed": {}, "final_failed": {}, "retrying": {}, "switching_model": {},
		"succeeded": {}, "succeeded_after_retry": {}, "cancelled": {},
	}
)

func buildDiagnosticToolInfos() []*schema.ToolInfo {
	return []*schema.ToolInfo{
		buildToolInfo(diagnosticRecentLogsTool, "Read bounded and redacted recent warnings/errors from this MaiBot instance.",
			map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 12, "description": "最多返回条数"},
				"level": map[string]any{"type": "string", "enum": []string{"WARNING", "ERROR", "CRITICAL"}, "description": "Minimum log level"},
			}, nil),
		buildToolInfo(diagnosticRecentReasoningTool, "List recent MaiBot reasoning records with opaque IDs and stages.",
			map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "description": "最多返回条数"},
			}, nil),
		buildToolInfo(diagnosticReadReasoningTool,
			"Inspect the model, timing, tokens, tool names and outcome of one reasoning record. "+
				"Use an ID from the recent list or an exact relative .json path supplied by the user.",
			map[string]any{
				"record_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{24}$", "description": "list_recent_reasoning 返回的 ID"},
				"record_path": map[string]any{
					"type": "string", "minLength": 1, "maxLength": 512,
					"description": "用户在本轮明确给出的精确相对 .json 路径",
				},
			}, nil),
	}
}

// ExecuteDiagnosticTool 执行诊断工具
func ExecuteDiagnosticTool(instanceDir, name string, arguments map[string]any) map[string]any {
	switch name {
	case diagnosticRecentLogsTool:
		return readRecentLogs(arguments)
	case diagnosticRecentReasoningTool:
		return listRecentReasoning(instanceDir, arguments)
	case diagnosticReadReasoningTool:
		return readReasoningRecord(instanceDir, arguments)
	default:
		return map[string]any{"error": "未知答疑诊断工具"}
	}
}

func readRecentLogs(arguments map[string]any) map[string]any {
	limit := intValue(arguments["limit"], 8)
	if limit < 1 {
		limit = 1
	}
	if limit > 12 {
		limit = 12
	}
	level := strings.ToUpper(stringValue(arguments["level"]))
	if level != "ERROR" && level != "CRITICAL" {
		level = "WARNING"
	}
	minimum := map[string]int{"WARNING": 1, "ERROR": 2, "CRITICAL": 3}[level]
	entries := logger.RecentWebUIEntries(500)
	logs := make([]map[string]any, 0, limit)
	for index := len(entries) - 1; index >= 0 && len(logs) < limit; index-- {
		entry := entries[index]
		rank := map[string]int{"WARNING": 1, "WARN": 1, "ERROR": 2, "CRITICAL": 3}[strings.ToUpper(entry.Level)]
		if rank < minimum {
			continue
		}
		item := map[string]any{"level": strings.ToUpper(entry.Level), "message": redactLogMessage(entry.Message)}
		if entry.Timestamp != "" && diagnosticTimestamp.MatchString(entry.Timestamp) {
			item["timestamp"] = entry.Timestamp
		}
		if module := safeDiagnosticIdentifier(entry.Module); module != "" {
			item["module"] = module
		}
		if match := diagnosticHTTPStatus.FindStringSubmatch(entry.Message); len(match) == 2 {
			if code, err := strconv.Atoi(match[1]); err == nil {
				item["http_status"] = code
			}
		}
		logs = append(logs, item)
	}
	// 日志按时间旧→新返回，便于阅读
	for left, right := 0, len(logs)-1; left < right; left, right = left+1, right-1 {
		logs[left], logs[right] = logs[right], logs[left]
	}
	return map[string]any{"logs": logs}
}

func redactLogMessage(message string) string {
	message = diagnosticURL.ReplaceAllStringFunc(message, func(raw string) string {
		index := strings.Index(raw, "://")
		if index < 0 {
			return "[链接已隐藏]"
		}
		rest := raw[index+3:]
		host := rest
		if slash := strings.IndexAny(host, "/?#"); slash >= 0 {
			host = host[:slash]
		}
		if host == "" || strings.Contains(host, "@") {
			return "[链接已隐藏]"
		}
		return raw[:index+3] + host
	})
	if diagnosticPrivateLogText.MatchString(message) {
		return "[涉及凭据或消息正文的日志内容已隐藏]"
	}
	message = strings.TrimSpace(message)
	if lineEnd := strings.IndexByte(message, '\n'); lineEnd >= 0 {
		message = message[:lineEnd]
	}
	if runes := []rune(message); len(runes) > 400 {
		message = string(runes[:400])
	}
	return strings.TrimSpace(message)
}

func safeDiagnosticIdentifier(value any) string {
	text, _ := value.(string)
	if diagnosticIdentifier.MatchString(text) && !diagnosticPrivateLogText.MatchString(text) {
		return text
	}
	return ""
}

type diagnosticReasoningRow struct {
	ID        string
	Stage     string
	Session   string
	Stem      string
	Path      string
	UpdatedAt time.Time
}

func recentReasoningRows(instanceDir string) []diagnosticReasoningRow {
	root := filepath.Join(filepath.Clean(instanceDir), "logs", "maisaka_prompt")
	rows := []diagnosticReasoningRow{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".json") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) != 3 {
			return nil
		}
		stage, session, stem := parts[0], parts[1], strings.TrimSuffix(parts[2], filepath.Ext(parts[2]))
		if safeDiagnosticIdentifier(stage) == "" || !safeDiagnosticPathPart(session) || !safeDiagnosticPathPart(stem) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rows = append(rows, diagnosticReasoningRow{
			ID: recordID(relative), Stage: stage, Session: session, Stem: stem, Path: path, UpdatedAt: info.ModTime(),
		})
		return nil
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].UpdatedAt.After(rows[j].UpdatedAt) })
	if len(rows) > diagnosticReasoningScanLimit {
		rows = rows[:diagnosticReasoningScanLimit]
	}
	return rows
}

func safeDiagnosticPathPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, "/\\:")
}

func recordID(relative string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(relative)))
	return hex.EncodeToString(sum[:])[:24]
}

func listRecentReasoning(instanceDir string, arguments map[string]any) map[string]any {
	limit := intValue(arguments["limit"], 6)
	if limit < 1 {
		limit = 1
	}
	if limit > 10 {
		limit = 10
	}
	rows := recentReasoningRows(instanceDir)
	records := make([]map[string]any, 0, limit)
	for _, row := range rows {
		records = append(records, map[string]any{
			"id": row.ID, "stage": row.Stage, "timestamp": float64(row.UpdatedAt.UnixNano()) / 1e9,
		})
		if len(records) == limit {
			break
		}
	}
	return map[string]any{"records": records}
}

func readReasoningRecord(instanceDir string, arguments map[string]any) map[string]any {
	recordIDValue := strings.TrimSpace(stringValue(arguments["record_id"]))
	recordPath := strings.TrimSpace(stringValue(arguments["record_path"]))
	if (recordIDValue == "") == (recordPath == "") {
		return map[string]any{"error": "必须提供且只提供一种推理记录标识"}
	}
	root := filepath.Join(filepath.Clean(instanceDir), "logs", "maisaka_prompt")
	if recordPath != "" {
		if filepath.IsAbs(recordPath) || strings.Contains(recordPath, "..") || !strings.EqualFold(filepath.Ext(recordPath), ".json") {
			return map[string]any{"error": "推理记录路径无效"}
		}
		full := filepath.Join(root, filepath.FromSlash(recordPath))
		relative, err := filepath.Rel(root, full)
		if err != nil || strings.HasPrefix(relative, "..") {
			return map[string]any{"error": "推理记录路径无效"}
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) != 3 {
			return map[string]any{"error": "推理记录路径必须包含类型、会话和 JSON 文件名"}
		}
		return projectReasoningRecord(recordID(relative), safeDiagnosticIdentifier(parts[0]), full)
	}
	for _, row := range recentReasoningRows(instanceDir) {
		if row.ID == recordIDValue {
			return projectReasoningRecord(row.ID, row.Stage, row.Path)
		}
	}
	return map[string]any{"error": "推理记录未找到或已移出最近记录范围"}
}

func projectReasoningRecord(id, stage, path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{"error": "暂时无法读取诊断记录"}
	}
	if len(raw) > diagnosticReasoningMaxBytes {
		raw = raw[:diagnosticReasoningMaxBytes]
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return map[string]any{"error": "推理记录格式错误"}
	}
	record := map[string]any{"id": id, "stage": stage}
	projectReasoningSnapshot(snapshot, record)
	return map[string]any{"record": record}
}

func projectReasoningSnapshot(snapshot map[string]any, result map[string]any) {
	if request, ok := snapshot["request"].(map[string]any); ok {
		if kind := safeDiagnosticIdentifier(request["kind"]); kind != "" {
			result["request_kind"] = kind
		}
	}
	if metadata, ok := snapshot["metadata"].(map[string]any); ok {
		model := safeDiagnosticIdentifier(metadata["model_name"])
		if model == "" {
			model = safeDiagnosticIdentifier(metadata["model"])
		}
		if model != "" {
			result["model"] = model
		}
		if status, ok := metadata["status"].(string); ok {
			if _, valid := diagnosticReasoningStatus[status]; valid {
				result["status"] = status
			}
		}
		if duration, ok := finiteNumber(metadata["duration_ms"]); ok && duration >= 0 && duration < 86400000 {
			result["duration_ms"] = duration
		}
		for _, name := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
			if value, ok := finiteNumber(metadata[name]); ok && value >= 0 && value <= 1_000_000_000 {
				result[name] = int64(value)
			}
		}
		if source, ok := metadata["request_source"].(string); ok && (source == "context_injection" || source == "planner_snapshot") {
			result["source"] = source
		}
	}
	outputItems, _ := snapshot["output_items"].([]any)
	tools := map[string]map[string]any{}
	for _, rawItem := range outputItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if item["item_type"] == "FunctionCallItem" {
			if call, ok := item["tool_call"].(map[string]any); ok {
				if name := safeDiagnosticIdentifier(call["func_name"]); name != "" {
					if _, exists := tools[name]; !exists {
						tools[name] = map[string]any{"name": name}
					}
				}
			}
		}
		if item["item_type"] == "FunctionCallOutputItem" {
			name := safeDiagnosticIdentifier(item["tool_name"])
			if name != "" {
				if success, ok := item["success"].(bool); ok {
					tools[name] = map[string]any{"name": name, "success": success}
				}
			}
		}
	}
	if len(tools) > 0 {
		names := make([]string, 0, len(tools))
		for name := range tools {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) > 6 {
			names = names[:6]
		}
		items := make([]any, 0, len(names))
		for _, name := range names {
			items = append(items, tools[name])
		}
		result["tools"] = items
	}
	if attempts, ok := snapshot["generation_attempts"].([]any); ok && len(attempts) > 0 {
		result["attempt_count"] = len(attempts)
		if latest, ok := attempts[len(attempts)-1].(map[string]any); ok {
			if model := safeDiagnosticIdentifier(latest["model"]); model != "" {
				if _, exists := result["model"]; !exists {
					result["model"] = model
				}
			}
		}
		for index := len(attempts) - 1; index >= 0; index-- {
			attempt, _ := attempts[index].(map[string]any)
			errorInfo, _ := attempt["error"].(map[string]any)
			if errorInfo == nil {
				continue
			}
			outcome := map[string]any{}
			if errorType := safeDiagnosticIdentifier(errorInfo["type"]); errorType != "" {
				outcome["type"] = errorType
			}
			if code, ok := finiteNumber(errorInfo["status_code"]); ok && code >= 100 && code <= 599 {
				outcome["http_status"] = int(code)
			}
			if len(outcome) > 0 {
				result["last_error"] = outcome
				break
			}
		}
	}
}

func finiteNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// DiagnosticToolNames 返回诊断工具名集合，供工具调度使用
func DiagnosticToolNames() map[string]struct{} { return diagnosticToolNames }

// FormatDiagnosticToolError 统一工具参数错误文案
func FormatDiagnosticToolError(name string, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("诊断工具参数无效: %v", err)}
	}
	return map[string]any{"error": "未知答疑诊断工具"}
}
