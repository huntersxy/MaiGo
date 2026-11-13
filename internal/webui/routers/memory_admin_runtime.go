package webuiapi

// WebUI memory 管理 API 公共设施：鉴权、admin 调用、请求解析与响应归一化
// 对齐 ../MaiBot/src/webui/routers/memory.py 的错误语义；缺服务时返回真实 503，
// 不伪造空成功

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"maibot-go/internal/A_memorix"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
)

const memoryAdminMaxBodyBytes = 32 << 20

// memoryAdminAuth 对齐 Python require_auth 依赖：未认证直接 401，不再进入处理器
func memoryAdminAuth(d Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !d.requireAuth(w, r) {
				return
			}
			if !validateRequestBodyForRoute(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// runtimeKernel 返回当前进程注册的 A_memorix 内核；Bridge/Remote 模式下可能为 nil
func runtimeKernel() *memruntime.Kernel {
	return memruntime.GetRuntimeKernel()
}

// memoryMetadataStore 返回 metadata SQLite 查询面；未就绪时为 nil
func memoryMetadataStore() interface {
	Query(string, ...any) ([]map[string]any, error)
} {
	kernel := runtimeKernel()
	if kernel == nil {
		return nil
	}
	store := kernel.MetadataStore()
	if store == nil {
		return nil
	}
	return store
}

// invokeMemoryAdmin 统一走宿主服务的 Invoke，继承 disabled/initializing/queued 语义；
// 宿主不可用时回落到内核 DispatchAdminCommand，仍缺服务则返回 503
func (d Deps) invokeMemoryAdmin(w http.ResponseWriter, r *http.Request, component, action string, payload map[string]any) bool {
	return d.invokeMemoryAdminTimeout(w, r, component, action, payload, nil)
}

func (d Deps) invokeMemoryAdminTimeout(w http.ResponseWriter, r *http.Request, component, action string, payload map[string]any, timeoutMs *int) bool {
	args := cloneMemoryPayload(payload)
	if args == nil {
		args = map[string]any{}
	}
	if action != "" {
		args["action"] = action
	}

	host := memorix.DefaultRegistry().Host()
	switch typed := host.(type) {
	case *memorix.AMemorixHostService:
		result, err := typed.Invoke(r.Context(), component, args, timeoutMs)
		return d.writeMemoryAdminInvokeResult(w, r, component, action, result, err)
	case memorix.AdminInvoker:
		result, err := typed.InvokeAdmin(r.Context(), component, args, timeoutMs)
		if err != nil {
			var unsupported *memorix.AdminUnsupportedError
			if errors.As(err, &unsupported) {
				writeJSON(w, http.StatusOK, unsupported.ToResponse())
				return false
			}
			if errors.Is(err, context.DeadlineExceeded) {
				writeAPIError(w, http.StatusGatewayTimeout, "A_Memorix 调用超时: component="+component)
				return false
			}
			writeAPIError(w, http.StatusBadGateway, "A_Memorix 远端管理调用失败: "+err.Error())
			return false
		}
		return d.writeMemoryAdminInvokeResult(w, r, component, action, result, nil)
	case memorix.DisabledHost:
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false, "enabled": false, "disabled": true,
			"reason": "a_memorix_disabled",
			"error":  "A_Memorix 未启用，请在长期记忆配置中开启后再使用。",
		})
		return false
	}

	if host != nil {
		// Bridge 等宿主没有 admin 内核，明确返回能力边界而不是 501
		backend := memorix.BackendName(host)
		unsupported := memorix.NewAdminUnsupportedError(backend, component, action, backend+"_admin_not_supported")
		writeJSON(w, http.StatusOK, unsupported.ToResponse())
		return false
	}

	kernel := runtimeKernel()
	if kernel == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 运行时尚未初始化")
		return false
	}
	command, err := memruntime.ParseAdminCommand(component, args)
	if err != nil {
		var contractErr *memruntime.AdminContractError
		if errors.As(err, &contractErr) {
			writeJSON(w, http.StatusOK, contractErr.ToResponse())
			return false
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return false
	}
	ctx := r.Context()
	if timeoutMs != nil && *timeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, durationMilliseconds(*timeoutMs))
		defer cancel()
	}
	result, err := memruntime.DispatchAdminCommand(ctx, kernel, command)
	return d.writeMemoryAdminInvokeResult(w, r, component, action, result, err)
}

func (d Deps) writeMemoryAdminInvokeResult(w http.ResponseWriter, r *http.Request, component, action string, result any, err error) bool {
	if err != nil {
		var responder interface{ ToResponse() map[string]any }
		if errors.As(err, &responder) {
			writeJSON(w, http.StatusOK, responder.ToResponse())
			return false
		}
		if errors.Is(err, context.DeadlineExceeded) {
			writeAPIError(w, http.StatusGatewayTimeout, "A_Memorix 调用超时: component="+component)
			return false
		}
		writeInternalError(w, err)
		return false
	}
	if result == nil {
		writeAPIError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("A_Memorix 服务未接线: %s/%s（对应 runtime/services 服务尚未装配）", component, action))
		return false
	}
	writeJSON(w, http.StatusOK, result)
	return true
}

// decodeMemoryJSON 解析 JSON 请求体；空体视为空对象
func decodeMemoryJSON(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	reader := io.LimitReader(r.Body, memoryAdminMaxBodyBytes)
	data, err := io.ReadAll(reader)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "读取请求体失败: "+err.Error())
		return nil, false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, true
	}
	var payload map[string]any
	if err := sonic.Unmarshal(data, &payload); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体必须是 JSON 对象")
		return nil, false
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, true
}

func cloneMemoryPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}

// mqString 读取字符串查询参数并去空白
func mqString(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// mqRequiredString 必填字符串，缺省返回 422
func mqRequiredString(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	value := mqString(r, key)
	if value == "" {
		writeValidationError(w, key+" 不能为空")
		return "", false
	}
	return value, true
}

// mqInt 解析带上下界的整数查询参数，越界/非法按 FastAPI 422 返回
func mqInt(w http.ResponseWriter, r *http.Request, key string, fallback, min, max int) (int, bool) {
	raw := mqString(r, key)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeValidationError(w, key+" 必须是整数")
		return 0, false
	}
	if value < min || value > max {
		writeValidationError(w, fmt.Sprintf("%s 必须在 %d 到 %d 之间", key, min, max))
		return 0, false
	}
	return value, true
}

// mqFloat 解析带上下界的浮点查询参数
func mqFloat(w http.ResponseWriter, r *http.Request, key string, fallback, min, max float64) (float64, bool) {
	raw := mqString(r, key)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		writeValidationError(w, key+" 必须是数字")
		return 0, false
	}
	if value < min || value > max {
		writeValidationError(w, fmt.Sprintf("%s 必须在 %g 到 %g 之间", key, min, max))
		return 0, false
	}
	return value, true
}

// mqBool 解析布尔查询参数；未提供返回 fallback
func mqBool(w http.ResponseWriter, r *http.Request, key string, fallback bool) (bool, bool) {
	raw := mqString(r, key)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		writeValidationError(w, key+" 必须是布尔值")
		return false, false
	}
	return value, true
}

// mqCSV 拆分逗号分隔查询参数，去空白与空项
func mqCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, item := range parts {
		if token := strings.TrimSpace(item); token != "" {
			out = append(out, token)
		}
	}
	return out
}

// errMemoryMetadataNotReady metadata 后端未接线；调用方应映射为 503，与其余 memory 路由一致
var errMemoryMetadataNotReady = errors.New("长期记忆 metadata 数据库尚未就绪")

// queryMemoryRows 执行 metadata 查询；错误必须完整暴露
func queryMemoryRows(sql string, params ...any) ([]map[string]any, error) {
	store := memoryMetadataStore()
	if store == nil {
		return nil, errMemoryMetadataNotReady
	}
	return store.Query(sql, params...)
}

// queryMemoryRowsOptional 用于可选表（表不存在时按空结果处理）
func queryMemoryRowsOptional(sql string, params ...any) []map[string]any {
	rows, err := queryMemoryRows(sql, params...)
	if err != nil {
		return nil
	}
	return rows
}

// anyFloat 对齐 Python _safe_float：无法解析时返回 false
func anyFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	case []byte:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(string(typed)), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func anyInt(value any) int {
	if parsed, ok := anyFloat(value); ok {
		return int(parsed)
	}
	return 0
}

func anyTrimmed(value any) string {
	if value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func anyBoolInt(value any) bool {
	return anyInt(value) != 0
}

// memoryNotFound 判断服务返回是否属于“未找到”，供路由映射 404
func memoryNotFound(result map[string]any) bool {
	if result == nil {
		return false
	}
	if success, ok := result["success"].(bool); ok && success {
		return false
	}
	for _, key := range []string{"error", "detail", "message", "reason"} {
		text := strings.ToLower(anyTrimmed(result[key]))
		if strings.Contains(text, "not found") || strings.Contains(text, "未找到") || strings.Contains(text, "不存在") {
			return true
		}
	}
	return false
}

// memorixDefaultHost 返回当前注册的记忆宿主
func memorixDefaultHost() memorix.Host {
	return memorix.DefaultRegistry().Host()
}

// searchRequestForAggregate 构造 aggregate 检索请求
func searchRequestForAggregate(query string, limit int, payload map[string]any) memorix.SearchRequest {
	return memorix.SearchRequest{
		Query: query, Limit: limit, Mode: "aggregate",
		ChatID: anyTrimmed(payload["chat_id"]), PersonID: anyTrimmed(payload["person_id"]),
		TimeStart: payload["time_start"], TimeEnd: payload["time_end"],
		RespectFilter: false,
	}
}

func durationMilliseconds(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
}
