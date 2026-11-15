package memorix

// native 启动门判定：把 main 里“仅 MAIBOT_INSTANCE_DIR 非空才尝试 native”
// 扩展为按后端选择与 bot_config.toml 的 A_Memorix 启用状态判定
// main 只负责调用本函数并按结果创建实例持有者与宿主

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	// NativeBackendAuto 自动选择后端
	NativeBackendAuto = "auto"
	// NativeBackendNative 显式使用原生内核
	NativeBackendNative = "native"
)

// NativeStartupOptions 启动门输入
type NativeStartupOptions struct {
	// Backend 取值 auto/native，空值按 auto；历史 bridge/remote 后端已移除
	Backend string
	// Explicit 表示后端由用户显式指定
	Explicit bool
	// InstanceDir 实例根目录，空值时回退 RepoRoot()
	InstanceDir string
	// ConfigPath bot_config.toml 路径，空值时按实例目录推导
	ConfigPath string
	// ReadFile 测试注入点，nil 时读真实文件
	ReadFile func(path string) ([]byte, error)
}

// NativeStartupDecision 启动门结果
type NativeStartupDecision struct {
	Attempt       bool
	Reason        string
	Backend       string
	Explicit      bool
	InstanceDir   string
	ConfigPath    string
	ConfigFound   bool
	ConfigEnabled bool
}

// ResolveNativeStartup 判定是否尝试启动原生内核
// 显式 native 总是尝试；auto 只在 bot_config.toml 明确启用 A_Memorix 时尝试
func ResolveNativeStartup(opts NativeStartupOptions) NativeStartupDecision {
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" {
		backend = NativeBackendAuto
	}
	instanceDir := strings.TrimSpace(opts.InstanceDir)
	if instanceDir == "" {
		instanceDir = RepoRoot()
	}
	configPath := strings.TrimSpace(opts.ConfigPath)
	if configPath == "" {
		configPath = filepath.Join(instanceDir, "config", "bot_config.toml")
	}
	decision := NativeStartupDecision{
		Backend:     backend,
		Explicit:    opts.Explicit,
		InstanceDir: instanceDir,
		ConfigPath:  configPath,
	}
	switch backend {
	case NativeBackendNative:
		decision.Attempt = true
		decision.Reason = "显式选择 native 后端"
		return decision
	case NativeBackendAuto:
	default:
		decision.Reason = fmt.Sprintf("后端取值 %q 无效，按 auto 处理", backend)
		backend = NativeBackendAuto
		decision.Backend = backend
	}

	readFile := opts.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			decision.Reason = fmt.Sprintf("未找到 A_Memorix 配置 %s，auto 模式保持默认后端", configPath)
		} else {
			decision.Reason = fmt.Sprintf("读取 A_Memorix 配置失败: %v", err)
		}
		return decision
	}
	decision.ConfigFound = true
	enabled, err := aMemorixEnabledFromTOML(data)
	if err != nil {
		decision.Reason = fmt.Sprintf("解析 A_Memorix 配置失败: %v", err)
		return decision
	}
	decision.ConfigEnabled = enabled
	if !enabled {
		decision.Reason = "A_Memorix 未启用（[a_memorix.plugin].enabled 不是 true）"
		return decision
	}
	decision.Attempt = true
	decision.Reason = "auto 模式：bot_config.toml 已启用 A_Memorix"
	return decision
}

// aMemorixEnabledFromTOML 读取 [a_memorix.plugin].enabled，缺省按未启用处理
func aMemorixEnabledFromTOML(data []byte) (bool, error) {
	document := map[string]any{}
	if err := toml.Unmarshal(data, &document); err != nil {
		return false, err
	}
	section := anyToMap(document["a_memorix"])
	if section == nil {
		return false, nil
	}
	plugin := anyToMap(section["plugin"])
	if plugin == nil {
		return false, nil
	}
	raw, ok := plugin["enabled"]
	if !ok {
		return false, nil
	}
	enabled, ok := raw.(bool)
	if !ok {
		return false, nil
	}
	return enabled, nil
}
