package scripts

// A_memorix 脚本公共引导层，对齐 src/A_memorix/scripts/_bootstrap.py
// Python 版通过 sys.path 注入源码目录后暴露默认路径；Go 版无需路径注入，
// 这里收敛路径解析、TOML 读取、脚本日志与 JSON 编解码等公共能力

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/pelletier/go-toml/v2"

	"maibot-go/internal/A_memorix"
)

// DefaultDBName 对齐 _bootstrap.DEFAULT_DB_PATH 中 data/MaiBot.db 的文件名
const DefaultDBName = "MaiBot.db"

// SelfCheckTempPrefix 对齐 tempfile.TemporaryDirectory(prefix="memorix-runtime-self-check-")
const SelfCheckTempPrefix = "memorix-runtime-self-check-"

// DefaultConfigPath 对齐 DEFAULT_CONFIG_PATH = config_path()
func DefaultConfigPath() string { return memorix.ConfigPath() }

// DefaultDataDir 对齐 DEFAULT_DATA_DIR = default_data_dir()
func DefaultDataDir() string { return memorix.DefaultDataDir() }

// DefaultDBPath 对齐 DEFAULT_DB_PATH = PROJECT_ROOT / "data" / "MaiBot.db"
func DefaultDBPath() string { return filepath.Join(memorix.RepoRoot(), "data", DefaultDBName) }

// ResolveRepoPath 对齐 _bootstrap.resolve_repo_path
// 空路径回退 fallback，绝对路径原样解析，相对路径按实例根目录展开
func ResolveRepoPath(rawPath string, fallback string) string {
	return memorix.ResolveRepoPath(rawPath, fallback)
}

// ExpandUser 对齐 Path.expanduser() 的常见用法：仅展开开头的 ~ 与 ~/
func ExpandUser(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// IsSubPath 对齐 Path.relative_to 的包含判断：target 等于 base 或在 base 之下
func IsSubPath(base string, target string) bool {
	baseAbs := resolveAbsoluteForCompare(base)
	targetAbs := resolveAbsoluteForCompare(target)
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveAbsoluteForCompare 归一用于包含判断的绝对路径
func resolveAbsoluteForCompare(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// LoadTOML 读取 TOML 为嵌套 map，对齐 tomlkit.load + dict(raw)
func LoadTOML(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	if err := toml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	return config, nil
}

// NestedLookup 按点分路径读取配置，返回取值与是否存在
func NestedLookup(config map[string]any, key string) (any, bool) {
	var current any = config
	for _, part := range strings.Split(key, ".") {
		table, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, exists := table[part]
		if !exists {
			return nil, false
		}
		current = value
	}
	return current, true
}

// NestedGet 对齐 _nested_get：逐层取字典值，缺失返回默认值
func NestedGet(config map[string]any, key string, defaultValue any) any {
	if value, ok := NestedLookup(config, key); ok {
		return value
	}
	return defaultValue
}

// SetNested 写入点分配置，缺失的中间层自动创建
func SetNested(config map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	current := config
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

// ConfigGetter 返回对齐 plugin.get_config 的读取回调
func ConfigGetter(config map[string]any) func(key string, defaultValue any) any {
	return func(key string, defaultValue any) any {
		return NestedGet(config, key, defaultValue)
	}
}

// ConfigBool 读取布尔配置，键缺失或值为空时返回默认值
func ConfigBool(config map[string]any, key string, defaultValue bool) bool {
	value, ok := NestedLookup(config, key)
	if !ok || value == nil {
		return defaultValue
	}
	return isTruthy(value)
}

// isTruthy 对齐 Python bool(value) 的常用分支
func isTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
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

// ScriptLogger 脚本控制台日志，对齐 get_logger(name) 的输出习惯
// 差异说明：Python 走项目 rich 日志格式，这里用固定 "时间 | 级别 | 名称 | 内容" 写 stderr
type ScriptLogger struct {
	name string
	out  io.Writer
	mu   sync.Mutex
}

// NewScriptLogger 创建写 stderr 的脚本日志器
func NewScriptLogger(name string) *ScriptLogger {
	return &ScriptLogger{name: name, out: os.Stderr}
}

// NewScriptLoggerTo 创建写指定输出目标的脚本日志器
func NewScriptLoggerTo(name string, out io.Writer) *ScriptLogger {
	if out == nil {
		out = os.Stderr
	}
	return &ScriptLogger{name: name, out: out}
}

// Debug 输出调试日志
func (l *ScriptLogger) Debug(message string) { l.log("DEBUG", message) }

// Info 输出信息日志
func (l *ScriptLogger) Info(message string) { l.log("INFO", message) }

// Warning 输出告警日志
func (l *ScriptLogger) Warning(message string) { l.log("WARNING", message) }

// Error 输出错误日志
func (l *ScriptLogger) Error(message string) { l.log("ERROR", message) }

// Debugf 格式化输出调试日志
func (l *ScriptLogger) Debugf(format string, args ...any) { l.Debug(fmt.Sprintf(format, args...)) }

// Infof 格式化输出信息日志
func (l *ScriptLogger) Infof(format string, args ...any) { l.Info(fmt.Sprintf(format, args...)) }

// Warningf 格式化输出告警日志
func (l *ScriptLogger) Warningf(format string, args ...any) { l.Warning(fmt.Sprintf(format, args...)) }

// Errorf 格式化输出错误日志
func (l *ScriptLogger) Errorf(format string, args ...any) { l.Error(fmt.Sprintf(format, args...)) }

// log 输出一行日志
func (l *ScriptLogger) log(level string, message string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.out
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "%s | %s | %s | %s\n", time.Now().Format("2006-01-02 15:04:05"), level, l.name, message)
}

// scriptJSON 对齐 json.dumps 的 ensure_ascii=False 语义（不转义 HTML 字符）
var scriptJSON = sonic.Config{EscapeHTML: false}.Froze()

// MarshalJSONIndent 对齐 json.dumps(value, ensure_ascii=False, indent=2)
func MarshalJSONIndent(value any) (string, error) {
	raw, err := scriptJSON.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// UnmarshalJSON 对齐 json.load
func UnmarshalJSON(raw []byte, dest any) error {
	return scriptJSON.Unmarshal(raw, dest)
}

// pyString 对齐 str(value or "") 的常用语义
func pyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(typed)
	}
}

// truncateText 对齐 Python 字符串切片的截断显示（按字符计）
func truncateText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
