package memorix

// A_memorix 宿主服务的依赖契约与注入选项
// Python 对照：src/A_memorix/host_service.py 中直接引用的全局单例
// （config_manager、SDKMemoryKernel、runtime_registry、AMemorixConfigUtils）
// 在 Go 侧全部改为接口注入，避免 memorix 包与 core/runtime 产生 import 环

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/config/maibot"

	"github.com/google/uuid"
)

// HostKernel 是宿主服务持有的长期记忆内核契约
// 对照 host_service.py 里调用的 SDKMemoryKernel 公开方法；
// 时间字段保留 any 原值，由适配器按内核要求转换
type HostKernel interface {
	Initialize(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Close()

	SearchMemory(ctx context.Context, req HostKernelSearchRequest) (map[string]any, error)
	IngestSummary(ctx context.Context, params HostIngestSummaryParams) (map[string]any, error)
	IngestText(ctx context.Context, params HostIngestTextParams) (map[string]any, error)
	GetPersonProfile(ctx context.Context, personID, chatID string, limit int) (map[string]any, error)
	MaintainMemory(ctx context.Context, action, target string, hours any, reason string, limit int) (map[string]any, error)
	MemoryStats() (map[string]any, error)
	ImageMemory(ctx context.Context, action string, payload map[string]any) (map[string]any, error)
	EnqueueFeedbackTask(ctx context.Context, payload map[string]any) (any, error)

	RuntimeCapabilityStatus() map[string]any

	IsAdminComponent(component string) bool
	ParseAdminCommand(component string, payload any) (action string, err error)
	DispatchAdmin(ctx context.Context, component string, payload map[string]any) (any, error)
}

// HostKernelStatusReporter 可选能力：内核上报降级原因与缺失服务
// 未实现的内核按无降级信息处理，不影响既有 HostKernel 契约
type HostKernelStatusReporter interface {
	HostStatusReport() map[string]any
}

// HostKernelSearchRequest 对照 core/runtime.KernelSearchRequest
// 不在本包直接引用 runtime 类型，避免 import 环
type HostKernelSearchRequest struct {
	Query         string
	Limit         int
	Mode          string
	ChatID        string
	SharedChatIDs []string
	PersonID      string
	TimeStart     any
	TimeEnd       any
	RespectFilter bool
	UserID        string
	GroupID       string
}

// HostIngestSummaryParams 对照 core/runtime.IngestSummaryParams
type HostIngestSummaryParams struct {
	ExternalID    string
	ChatID        string
	Text          string
	Participants  []string
	TimeStart     any
	TimeEnd       any
	Tags          []string
	Metadata      map[string]any
	RespectFilter bool
	UserID        string
	GroupID       string
}

// HostIngestTextParams 对照 core/runtime.IngestTextParams
type HostIngestTextParams struct {
	ExternalID    string
	SourceType    string
	Text          string
	ChatID        string
	PersonIDs     []string
	Participants  []string
	Timestamp     any
	TimeStart     any
	TimeEnd       any
	Tags          []string
	Metadata      map[string]any
	Entities      []string
	Relations     []map[string]any
	RespectFilter bool
	UserID        string
	GroupID       string
}

// HostConfigSnapshot 是一次配置读取的结果
// Raw 为 bot_config.toml 中 [a_memorix] 原表；nil 表示文件中没有该表
type HostConfigSnapshot struct {
	Model maibot.AMemorixConfig
	Raw   map[string]any
}

// HostConfigStore 对齐 host_service.py 使用的 config_manager 能力子集
type HostConfigStore interface {
	BotConfigPath() string
	AMemorixSnapshot() (HostConfigSnapshot, error)
	Reload(ctx context.Context) error
	RegisterReloadCallback(callback func(scopes []string))
}

// KernelFactory 构造内核；对齐 SDKMemoryKernel(plugin_root=..., config=...)
type KernelFactory func(ctx context.Context, pluginRoot string, config map[string]any) (HostKernel, error)

// HostFileOps 配置文件的底层读写注入点，便于测试替换
type HostFileOps interface {
	ReadFile(path string) ([]byte, error)
	WriteFileExclusive(path string, data []byte, perm os.FileMode) error
	ReplaceFile(src, dst string) error
	Remove(path string) error
	MkdirAll(path string, perm os.FileMode) error
	Stat(path string) (os.FileInfo, error)
	Chmod(path string, mode os.FileMode) error
}

// HostServiceOptions 宿主服务依赖
// 默认值按 Python 的模块级单例尽量补齐；未注入的项会在报告中标注差异
type HostServiceOptions struct {
	ConfigStore           HostConfigStore
	KernelFactory         KernelFactory
	FileOps               HostFileOps
	DefaultConfig         func() maibot.AMemorixConfig
	SharedSessionIDs      func(sessionID string) []string
	RegisterRuntimeKernel func(kernel HostKernel)
	PluginRoot            func() string
	SchemaPath            func() string
}

type osHostFileOps struct{}

func (osHostFileOps) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osHostFileOps) WriteFileExclusive(path string, data []byte, perm os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (osHostFileOps) ReplaceFile(src, dst string) error {
	return os.Rename(src, dst)
}

func (osHostFileOps) Remove(path string) error {
	return os.Remove(path)
}

func (osHostFileOps) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (osHostFileOps) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (osHostFileOps) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// defaultAMemorixConfig 对齐 AMemorixConfig() 中宿主服务实际读取的字段默认值
// 说明：Go 配置结构体由生成器产出且无 pydantic 默认值，这里只补 plugin.enabled
// 与 storage.data_dir 两个直接决定宿主行为的默认值，其余字段交给内核 Cfg 回退
func defaultAMemorixConfig() maibot.AMemorixConfig {
	return maibot.AMemorixConfig{
		Plugin:  maibot.AMemorixPluginConfig{Enabled: false},
		Storage: maibot.AMemorixStorageConfig{DataDir: "data/a-memorix"},
	}
}

// HostToolParam 工具参数声明，对照 plugin.py 的 @Tool schema
type HostToolParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// HostToolSpec 工具声明，对照 plugin.py 的 @Tool
type HostToolSpec struct {
	Name        string
	Description string
	Parameters  []HostToolParam
}

// helper: 读取 map 字符串
func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	return anyToString(payload[key])
}

// helper: 读取并去空白字符串
func payloadTrimmed(payload map[string]any, key string) string {
	return strings.TrimSpace(payloadString(payload, key))
}

// helper: 读取整数，兼容 TOML/JSON 的多种数值类型
func payloadInt(payload map[string]any, key string, fallback int) int {
	if payload == nil {
		return fallback
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	case bool:
		if value {
			return 1
		}
		return 0
	}
	return fallback
}

// helper: 读取布尔，对齐 Python bool(payload.get(key, fallback))
// 键存在但值为 None 时按 False 处理；仅键缺失才使用 fallback
func payloadBool(payload map[string]any, key string, fallback bool) bool {
	if payload == nil {
		return fallback
	}
	raw, ok := payload[key]
	if !ok {
		return fallback
	}
	return pythonTruthy(raw)
}

func pythonTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// helper: 读取 map[string]any
func payloadMap(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return nil
	}
	return anyToMap(payload[key])
}

// helper: 读取字符串列表
func payloadStrings(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	return anyToStringSlice(payload[key])
}

// helper: 读取关系列表
func payloadRelations(payload map[string]any, key string) []map[string]any {
	if payload == nil {
		return nil
	}
	raw, ok := payload[key].([]any)
	if !ok {
		if direct, ok := payload[key].([]map[string]any); ok {
			return direct
		}
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if mapped := anyToMap(item); mapped != nil {
			out = append(out, mapped)
		}
	}
	return out
}

func anyToString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

func anyToStringSlice(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, anyToString(item))
		}
		return out
	default:
		return []string{anyToString(value)}
	}
}

func anyToMap(value any) map[string]any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[anyToString(key)] = item
		}
		return out
	default:
		return nil
	}
}

func toFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case int32:
		return float64(typed)
	case uint64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func recordTimeValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func nowUnixFloat() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

func newUUIDHex() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func trimLower(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func ingestFailureMessage(payload map[string]any) string {
	for _, key := range []string{"detail", "error"} {
		if value := strings.TrimSpace(anyToString(payload[key])); value != "" {
			return value
		}
	}
	return "A_Memorix 写入返回 success=false"
}
