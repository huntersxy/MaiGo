package llm

// 失败请求快照：首次失败保存请求 Items、工具定义、请求参数、模型/Provider 快照与
// 错误信息，并把快照路径挂到错误链上；按保留数清理旧快照。
//
// schema_version=6 与 MaiBot v6 只读兼容；replay 字段指向 Go 重放工具，不伪造
// Python replay_llm_request.py。同一逻辑请求的后续失败追加 .events.jsonl。
import (
	"MaiGo/internal/llm_models/contract"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"

	"MaiGo/internal/common/logger"
)

const (
	// defaultLLMRequestSnapshotLimit 对齐 MaiBot DEFAULT_LLM_REQUEST_SNAPSHOT_LIMIT
	defaultLLMRequestSnapshotLimit = 128
	// goRequestSnapshotVersion 对齐 MaiBot 当前失败快照 schema 版本（字段只读兼容，重放入口见 replay/main.go）
	goRequestSnapshotVersion = 6
)

var (
	snapshotConfigMu sync.Mutex
	snapshotConfig   struct {
		dir   string
		limit int
	}
	snapshotFilePattern = regexp.MustCompile("[^A-Za-z0-9._-]+")
	snapshotTrimMu      sync.Mutex
)

// ConfigureRequestSnapshots 配置快照根目录与保留数；limit <= 0 时使用 128
func ConfigureRequestSnapshots(dir string, limit int) {
	snapshotConfigMu.Lock()
	defer snapshotConfigMu.Unlock()
	snapshotConfig.dir = strings.TrimSpace(dir)
	snapshotConfig.limit = limit
}

// RequestSnapshotDirectory 返回当前快照根目录
func RequestSnapshotDirectory() string {
	snapshotConfigMu.Lock()
	dir := snapshotConfig.dir
	limit := snapshotConfig.limit
	snapshotConfigMu.Unlock()
	if dir != "" {
		return dir
	}
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	if limit <= 0 {
		limit = defaultLLMRequestSnapshotLimit
	}
	return filepath.Join(root, "logs", "maisaka_prompt", "llm_error")
}

func requestSnapshotEnabled() bool {
	snapshotConfigMu.Lock()
	configured := strings.TrimSpace(snapshotConfig.dir) != ""
	snapshotConfigMu.Unlock()
	return configured || strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")) != ""
}

func requestSnapshotLimit() int {
	snapshotConfigMu.Lock()
	limit := snapshotConfig.limit
	snapshotConfigMu.Unlock()
	if limit > 0 {
		return limit
	}
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_LLM_REQUEST_SNAPSHOT_LIMIT")); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultLLMRequestSnapshotLimit
}

// 尝试记录器定义在 contract 包。
//
// 该类型被「客户端层」（失败时读路径追加事件）与「快照层」（写入时记录路径）
// 同时使用，留在任一侧都会循环。对齐 Python base_client.py 把
// RequestSnapshot/attempt 放在共用的基类模块。
type snapshotAttemptRecorder = contract.AttemptRecorder

var (
	newSnapshotAttemptRecorder         = contract.NewAttemptRecorder
	snapshotAttemptRecorderFromContext = contract.AttemptRecorderFromContext
	ensureSnapshotAttemptRecorder      = contract.EnsureAttemptRecorder
)

// snapshotLogicalTurnID 从尝试记录器取本轮固定标识；无记录器时为空
func snapshotLogicalTurnID(recorder *snapshotAttemptRecorder) string {
	return contract.SnapshotLogicalTurnID(recorder)
}

// newRequestSnapshotSpec 用模型配置填充快照元数据
func newRequestSnapshotSpec(cfg ModelConfig, operation, requestKind string, input []*schema.AgenticMessage, options *model.Options, providerRequest, internalRequest map[string]any) requestSnapshotSpec {
	return contract.NewRequestSnapshotSpec(cfg, operation, requestKind, input, options, providerRequest, internalRequest)
}

// requestSnapshotSpec 一次失败请求的可诊断信息。
//
// 结构位于 contract 包：客户端实现（model_client 子包）也要填充同一份
// 描述，留在父包会让子包反向依赖。
type requestSnapshotSpec = contract.RequestSnapshotSpec

// captureRequestSnapshot 保存失败快照并把路径挂到错误链；保存失败只告警，不改变原错误
func captureRequestSnapshot(spec requestSnapshotSpec) error {
	if spec.Error == nil {
		return nil
	}
	// Go 侧默认不落盘：只有显式 ConfigureRequestSnapshots 或设置了实例目录才保存，
	// 避免单元测试与无实例运行污染工作目录（MaiBot 默认写 PROJECT_ROOT/logs）
	if !requestSnapshotEnabled() {
		return spec.Error
	}
	// 同一逻辑请求的后续尝试只追加事件，不再生成第二个快照
	if recorder := spec.Recorder; recorder != nil {
		if existing := recorder.SnapshotPath(); existing != "" {
			if _, statErr := os.Stat(existing); statErr == nil {
				if eventErr := appendRequestSnapshotEvent(existing, spec); eventErr != nil {
					zap.L().Warn("追加 LLM 失败请求事件失败", zap.Error(eventErr))
				}
				return attachRequestSnapshot(spec.Error, existing)
			}
		}
	}
	path, err := saveRequestSnapshot(spec)
	if err != nil {
		zap.L().Warn("保存 LLM 失败请求快照失败", zap.Error(err))
		return spec.Error
	}
	return attachRequestSnapshot(spec.Error, path)
}

type requestSnapshotCarrier interface {
	error
	RequestSnapshotPath() string
	RequestSnapshotURI() string
}

type requestSnapshotError struct {
	cause error
	path  string
}

func (e *requestSnapshotError) Error() string               { return e.cause.Error() }
func (e *requestSnapshotError) Unwrap() error               { return e.cause }
func (e *requestSnapshotError) RequestSnapshotPath() string { return e.path }
func (e *requestSnapshotError) RequestSnapshotURI() string {
	if e.path == "" {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(e.path)}).String()
}

// attachRequestSnapshot 包装错误并暴露快照路径；已有快照时保持原错误
func attachRequestSnapshot(err error, path string) error {
	if err == nil || strings.TrimSpace(path) == "" {
		return err
	}
	var existing requestSnapshotCarrier
	if errors.As(err, &existing) && existing.RequestSnapshotPath() != "" {
		return err
	}
	return &requestSnapshotError{cause: err, path: path}
}

// RequestSnapshotInfo 从错误链提取快照路径与 URI
func RequestSnapshotInfo(err error) (path, uri string, ok bool) {
	var carrier requestSnapshotCarrier
	if !errors.As(err, &carrier) {
		return "", "", false
	}
	path = carrier.RequestSnapshotPath()
	if path == "" {
		return "", "", false
	}
	return path, carrier.RequestSnapshotURI(), true
}

func saveRequestSnapshot(spec requestSnapshotSpec) (string, error) {
	root := RequestSnapshotDirectory()
	sessionDir := filepath.Join(root, snapshotSessionDirName(spec.SessionID))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("创建快照目录失败: %w", err)
	}
	now := time.Now()
	requestID := uuid.NewString()
	if spec.Recorder != nil && spec.Recorder.RequestID != "" {
		requestID = spec.Recorder.RequestID
	}
	fileName := now.Format("20060102_150405.000000") + "_" + requestID + ".json"
	path := filepath.Join(sessionDir, fileName)

	assets := &snapshotAssetCollector{dir: filepath.Join(sessionDir, "assets")}
	payload := buildRequestSnapshotPayload(spec, requestID, now, path, assets)
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化失败请求快照失败: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", fmt.Errorf("写入失败请求快照失败: %w", err)
	}
	if spec.Recorder != nil {
		spec.Recorder.SetSnapshotPath(path)
	}
	trimRequestSnapshots(root, requestSnapshotLimit(), path)
	return path, nil
}

func snapshotAttemptPayload(spec requestSnapshotSpec, started time.Time, ordinal int) map[string]any {
	now := time.Now()
	if started.IsZero() {
		started = now
	}
	duration := now.Sub(started)
	if duration < 0 {
		duration = 0
	}
	if ordinal < 1 {
		ordinal = 1
	}
	return map[string]any{
		"attempt_id":       strings.ReplaceAll(uuid.NewString(), "-", ""),
		"workflow_purpose": strings.TrimSpace(spec.TaskName),
		"workflow_attempt": ordinal,
		"provider_attempt": ordinal,
		"model_attempt":    ordinal,
		"status":           "failed",
		"started_at":       started.Format(time.RFC3339Nano),
		"duration_ms":      duration.Milliseconds(),
		"provider":         strings.TrimSpace(spec.ProviderName),
		"endpoint":         sanitizeDiagnosticURL(strings.TrimSpace(spec.BaseURL)),
		"model":            strings.TrimSpace(spec.Model),
		"client_type":      strings.TrimSpace(spec.ClientType),
		"operation":        strings.TrimSpace(spec.Operation),
		"wire_protocol":    strings.TrimSpace(spec.Operation),
		"error":            snapshotErrorPayload(spec.Error),
	}
}

// recordSnapshotAttempt 把本次失败追加到请求级记录器，并把全部尝试挂到 spec
func recordSnapshotAttempt(recorder *snapshotAttemptRecorder, spec *requestSnapshotSpec, started time.Time) {
	if spec == nil {
		return
	}
	if recorder == nil {
		recorder = newSnapshotAttemptRecorder()
	}
	attempts := recorder.AttemptsSnapshot()
	recorder.Append(snapshotAttemptPayload(*spec, started, len(attempts)+1))
	spec.Recorder = recorder
	spec.Attempts = recorder.AttemptsSnapshot()
}

func appendRequestSnapshotEvent(snapshotPath string, spec requestSnapshotSpec) error {
	eventPath := strings.TrimSuffix(snapshotPath, filepath.Ext(snapshotPath)) + ".events.jsonl"
	event := map[string]any{
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"status":    "failed",
		"error":     snapshotErrorPayload(spec.Error),
	}
	if logicalTurnID := strings.TrimSpace(spec.LogicalTurnID); logicalTurnID != "" {
		event["logical_turn_id"] = logicalTurnID
	}
	if len(spec.Attempts) > 0 {
		event["attempt"] = sanitizeGenerationDiagnostic(spec.Attempts[len(spec.Attempts)-1])
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(eventPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func snapshotReplayInfo(snapshotPath string) map[string]any {
	scriptPath := snapshotReplayScriptPath()
	command := ""
	if scriptPath != "" {
		command = fmt.Sprintf("go run \"%s\" \"%s\"", scriptPath, snapshotPath)
	}
	note := "Go 重放工具支持 chat/responses/gemini 生成请求的请求体重建；在仓库根目录运行，并设置 MAIBOT_REPLAY_API_KEY"
	if command == "" {
		note = "未找到 cmd/llm_replay，快照仅用于只读诊断"
	}
	return map[string]any{
		"command":     command,
		"file_uri":    (&url.URL{Scheme: "file", Path: filepath.ToSlash(snapshotPath)}).String(),
		"script_path": scriptPath,
		"note":        note,
	}
}

// snapshotReplayScriptPath 定位重放工具源码
//
// 重放工具位于 cmd/llm_replay/main.go，对应 Python 的 scripts/replay_llm_request.py。
// 找不到时 replay.command 为空，note 显示「未找到」。
func snapshotReplayScriptPath() string {
	candidates := []string{filepath.Join("cmd", "llm_replay", "main.go")}
	if _, file, _, ok := runtime.Caller(0); ok {
		root := filepath.Dir(file)
		// 从 internal/llm_models 上溯到仓库根
		for i := 0; i < 4; i++ {
			for _, rel := range candidates {
				candidate := filepath.Join(root, rel)
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
			root = filepath.Dir(root)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for _, rel := range candidates {
			candidate := filepath.Join(cwd, rel)
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate
			}
		}
	}
	return ""
}

func snapshotSessionDirName(sessionID string) string {
	normalized := snapshotFilePattern.ReplaceAllString(strings.TrimSpace(sessionID), "-")
	normalized = strings.Trim(normalized, "-._")
	if normalized == "" {
		return "system"
	}
	return normalized
}

func buildRequestSnapshotPayload(spec requestSnapshotSpec, requestID string, now time.Time, path string, assets *snapshotAssetCollector) map[string]any {
	createdAt := now.Format(time.RFC3339Nano)
	requestKind := strings.TrimSpace(spec.RequestKind)
	if requestKind == "" {
		requestKind = "response"
	}
	requestType := ""
	if spec.InternalRequest != nil {
		requestType = strings.TrimSpace(stringFromAny(spec.InternalRequest["request_type"]))
	}
	if requestType == "" {
		requestType = requestKind
	}
	errorPayload := snapshotErrorPayload(spec.Error)
	attempts := spec.Attempts
	if len(attempts) == 0 {
		attempts = []map[string]any{snapshotAttemptPayload(spec, time.Now(), 1)}
	}
	generationAttempts := make([]any, 0, len(attempts))
	for _, item := range attempts {
		generationAttempts = append(generationAttempts, sanitizeGenerationDiagnostic(item))
	}
	return map[string]any{
		"schema_version": goRequestSnapshotVersion,
		"generator":      "MaiGo",
		"request": map[string]any{
			"kind":         requestKind,
			"operation":    strings.TrimSpace(spec.Operation),
			"request_type": requestType,
			"task_name":    strings.TrimSpace(spec.TaskName),
		},
		"metadata": map[string]any{
			"client_type":   strings.TrimSpace(spec.ClientType),
			"created_at":    createdAt,
			"model_name":    strings.TrimSpace(spec.Model),
			"provider_name": strings.TrimSpace(spec.ProviderName),
			"request_id":    requestID,
			"session_id":    strings.TrimSpace(spec.SessionID),
			"status":        "failed",
			"updated_at":    createdAt,
		},
		"presentation":        map[string]any{"output_title": "输出 Items"},
		"snapshot_path":       path,
		"request_items":       serializeSnapshotRequestItemsForSpec(spec, assets),
		"output_items":        []any{},
		"tool_definitions":    serializeSnapshotToolDefinitions(spec.Options),
		"request_parameters":  buildSnapshotRequestParameters(spec),
		"provider_request":    sanitizeSnapshotValue(spec.ProviderRequest, true),
		"internal_request":    sanitizeSnapshotValue(spec.InternalRequest, true),
		"model_info":          sanitizeSnapshotValue(snapshotModelInfo(spec), false),
		"api_provider":        sanitizeSnapshotValue(snapshotAPIProviderDetails(spec.Config), false),
		"generation_attempts": generationAttempts,
		"error":               errorPayload,
		"replay":              snapshotReplayInfo(path),
	}
}

// serializeSnapshotRequestItemsForSpec 优先使用显式 ContextItems，其次从 eino 消息投影
func serializeSnapshotRequestItemsForSpec(spec requestSnapshotSpec, assets *snapshotAssetCollector) []any {
	if len(spec.ContextItems) > 0 {
		out := make([]any, 0, len(spec.ContextItems))
		for _, item := range spec.ContextItems {
			out = append(out, snapshotContextItem(item, assets, spec.LogicalTurnID))
		}
		return out
	}
	return serializeSnapshotRequestItems(spec.Input, spec.Config, assets, spec.LogicalTurnID)
}

func snapshotModelInfo(spec requestSnapshotSpec) map[string]any {
	cfg := spec.Config
	displayName := trimTask(cfg.ModelDisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(spec.Model)
	}
	maxImages, imageMaxSide, imageMinSide := cfg.EffectiveImageLimits()
	info := map[string]any{
		// Python ModelInfo.api_provider 是 Provider 名称字符串，不是对象
		"api_provider":      trimTask(cfg.ProviderName),
		"extra_params":      cloneAnyMap(cfg.ExtraParams),
		"force_stream_mode": cfg.ForceStreamMode,
		"max_images":        maxImages,
		"image_max_side":    imageMaxSide,
		"image_min_side":    imageMinSide,
		"max_tokens":        nil,
		"model_identifier":  strings.TrimSpace(spec.Model),
		"model_type":        strings.TrimSpace(cfg.ModelType),
		"name":              displayName,
		"send_temperature":  cfg.SendTemperature,
		"temperature":       nil,
		"visual":            cfg.Visual,
	}
	if value, ok := intFromAny(cfg.ModelMaxTokens); ok {
		info["max_tokens"] = value
	}
	if cfg.ModelTemperature != nil {
		if value, ok := floatFromAny(cfg.ModelTemperature); ok {
			info["temperature"] = value
		}
	}
	return info
}

func snapshotAPIProviderDetails(cfg ModelConfig) map[string]any {
	name := trimTask(cfg.ProviderName)
	if name == "" {
		name = cfg.ClientType
	}
	return map[string]any{
		"auth_header_name":         providerAuthHeaderName(cfg.AuthHeaderName),
		"auth_header_prefix":       providerAuthHeaderPrefixWithSet(cfg.AuthHeaderPrefix, cfg.AuthHeaderPrefixSet),
		"auth_query_name":          providerAuthQueryName(cfg.AuthQueryName),
		"auth_type":                providerAuthType(cfg.AuthType),
		"base_url":                 cfg.BaseURL,
		"client_type":              cfg.ClientType,
		"default_headers":          providerStringMapOrEmpty(cfg.DefaultHeaders),
		"default_query":            providerStringMapOrEmpty(cfg.DefaultQuery),
		"max_retry":                providerMaxRetry(cfg),
		"model_list_endpoint":      providerModelListEndpoint(cfg.ModelListEndpoint),
		"name":                     name,
		"organization":             providerOptionalString(cfg.Organization),
		"project":                  providerOptionalString(cfg.Project),
		"reasoning_parse_mode":     providerParseMode(cfg.ReasoningParseMode),
		"retry_interval":           providerDurationSeconds(cfg.RetryInterval),
		"timeout":                  providerDurationSeconds(cfg.Timeout),
		"tool_argument_parse_mode": providerParseMode(cfg.ToolArgumentParseMode),
	}
}

func snapshotErrorPayload(err error) map[string]any {
	if err == nil {
		return nil
	}
	payload := map[string]any{
		"type":    fmt.Sprintf("%T", err),
		"message": err.Error(),
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		payload["status_code"] = statusErr.StatusCode
	}
	var apiErr *openai.Error
	if _, exists := payload["status_code"]; !exists && errors.As(err, &apiErr) && apiErr != nil {
		payload["status_code"] = apiErr.StatusCode
	}
	if sanitized, ok := sanitizeGenerationDiagnostic(payload).(map[string]any); ok {
		return sanitized
	}
	return payload
}

func serializeSnapshotToolDefinitions(options *model.Options) []any {
	if options == nil || len(options.Tools) == 0 {
		return []any{}
	}
	definitions, err := buildChatToolDefinitions(options.Tools)
	if err != nil {
		out := make([]any, 0, len(options.Tools))
		for _, toolInfo := range options.Tools {
			if toolInfo == nil {
				continue
			}
			out = append(out, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        toolInfo.Name,
					"description": toolInfo.Desc,
				},
			})
		}
		return out
	}
	out := make([]any, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition)
	}
	return out
}

func buildSnapshotRequestParameters(spec requestSnapshotSpec) map[string]any {
	params := map[string]any{}
	if options := spec.Options; options != nil {
		if options.Temperature != nil {
			params["temperature"] = *options.Temperature
		}
		if options.MaxTokens != nil {
			params["max_tokens"] = *options.MaxTokens
		}
		if options.TopP != nil {
			params["top_p"] = *options.TopP
		}
		if len(options.Stop) > 0 {
			params["stop"] = append([]string(nil), options.Stop...)
		}
		if choice := chatToolChoiceValue(options); choice != "" {
			params["tool_choice"] = choice
		}
	}
	for key, value := range spec.InternalRequest {
		switch key {
		case "context_items", "model_info", "tool_options", "request_kind", "embedding_input", "audio_base64":
			continue
		}
		params[key] = value
	}
	sanitized, ok := sanitizeSnapshotValue(params, true).(map[string]any)
	if !ok {
		return params
	}
	return sanitized
}

// sanitizeSnapshotValue 递归脱敏；providerRequest=true 时额外隐藏正文与媒体
func sanitizeSnapshotValue(value any, providerRequest bool) any {
	return sanitizeSnapshotDiagnosticValue(value, "", providerRequest)
}

// sanitizeSnapshotDiagnosticValue 在生成诊断脱敏之上保留快照的正文/媒体占位语义
func sanitizeSnapshotDiagnosticValue(value any, key string, providerRequest bool) any {
	normalizedKey := normalizeDiagnosticKey(key)
	if isSnapshotCredentialKey(normalizedKey) {
		return snapshotSensitivePlaceholder
	}
	if isDiagnosticPrivateKey(normalizedKey) {
		return diagnosticPrivatePlaceholder
	}
	if providerRequest {
		switch normalizedKey {
		case "messages", "contents":
			return "[见 request_items]"
		case "input":
			// Responses 的 input 与顶层 request_items 重复，重放统一从 request_items 重建
			switch value.(type) {
			case []any, []map[string]any:
				return "[见 request_items]"
			}
		case "audio_base64", "image_base64", "base64":
			return "[见媒体引用]"
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for itemKey, item := range typed {
			out[itemKey] = sanitizeSnapshotDiagnosticValue(item, itemKey, providerRequest)
		}
		return out
	case map[string]string:
		// 值也可能命中大 base64/URL 规则，因此不再保持 map[string]string
		out := make(map[string]any, len(typed))
		for itemKey, item := range typed {
			out[itemKey] = sanitizeSnapshotDiagnosticValue(item, itemKey, providerRequest)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeSnapshotDiagnosticValue(item, normalizedKey, providerRequest)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeSnapshotDiagnosticValue(item, normalizedKey, providerRequest)
		}
		return out
	case []string:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeSnapshotDiagnosticValue(item, normalizedKey, providerRequest)
		}
		return out
	default:
		if providerRequest {
			if text, ok := value.(string); ok && isSnapshotDataURI(text) {
				return "[见媒体引用]"
			}
		}
		return sanitizeGenerationDiagnostic(value, normalizedKey)
	}
}

func isSnapshotCredentialKey(key string) bool {
	if isDiagnosticSensitiveKey(key) {
		return true
	}
	switch key {
	case "client_secret", "credential", "credentials":
		return true
	}
	return false
}

func isSnapshotDataURI(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "data:audio/") ||
		strings.HasPrefix(lower, "data:video/")
}

type snapshotAssetCollector struct {
	dir string
}

// writeImage 把图片写入快照 assets 目录，返回相对路径；失败返回错误文本
func (c *snapshotAssetCollector) writeImage(data, mimeType string) (string, string, error) {
	decoded, err := decodeSnapshotBase64(data)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(decoded)
	extension := snapshotImageExtension(mimeType)
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", "", err
	}
	name := hex.EncodeToString(sum[:]) + "." + extension
	path := filepath.Join(c.dir, name)
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if writeErr := os.WriteFile(path, decoded, 0o600); writeErr != nil {
			return "", "", writeErr
		}
	}
	return filepath.ToSlash(filepath.Join("assets", name)), hex.EncodeToString(sum[:]), nil
}

func decodeSnapshotBase64(data string) ([]byte, error) {
	trimmed := strings.TrimSpace(data)
	trimmed = strings.TrimPrefix(trimmed, "base64,")
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
	}
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func snapshotImageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	default:
		return "png"
	}
}

func trimRequestSnapshots(root string, limit int, keep string) {
	if limit <= 0 || strings.TrimSpace(root) == "" {
		return
	}
	// 对齐 Python request_snapshot.py:806-815 的 timed_operation("snapshot.trim_llm_request_snapshots")
	timing := logger.StartOperation(logger.OperationTimingOptions{
		Operation: "snapshot.trim_llm_request_snapshots",
		WarnSlow:  true,
		Details:   []zap.Field{zap.Int("limit", limit)},
	})
	defer timing.Finish("completed")
	snapshotTrimMu.Lock()
	defer snapshotTrimMu.Unlock()
	type entry struct {
		path    string
		modTime time.Time
	}
	var entries []entry
	_ = filepath.WalkDir(root, func(path string, dirEntry os.DirEntry, err error) error {
		if err != nil || dirEntry == nil || dirEntry.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".json") {
			return nil
		}
		info, infoErr := dirEntry.Info()
		if infoErr != nil {
			return nil
		}
		entries = append(entries, entry{path: path, modTime: info.ModTime()})
		return nil
	})
	if len(entries) <= limit {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.Before(entries[j].modTime)
	})
	remove := len(entries) - limit
	for _, old := range entries {
		if remove <= 0 {
			break
		}
		if old.path == keep {
			continue
		}
		_ = os.Remove(old.path)
		remove--
	}
}

// wrapAgenticStreamSnapshot 包装已建立流的错误：中途失败补失败快照，EOF 与主动中断不落盘
func wrapAgenticStreamSnapshot(reader *schema.StreamReader[*schema.AgenticMessage], wrap func(error) error) *schema.StreamReader[*schema.AgenticMessage] {
	if reader == nil || wrap == nil {
		return reader
	}
	return schema.StreamReaderWithConvert(reader, func(message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return message, nil
	}, schema.WithErrWrapper(func(err error) error {
		if err == nil || errors.Is(err, io.EOF) {
			return err
		}
		var abortErr *AbortError
		if errors.As(err, &abortErr) {
			return err
		}
		return wrap(err)
	}))
}
