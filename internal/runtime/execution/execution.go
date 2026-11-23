// Package execution 定义 Go 平台执行模式与功能开关。
//
// 与 internal/runtime.Mode（runner/worker 进程角色）不同，本包管理的是
// compatibility / native 两种执行策略，以及各项 Go 平台特有优化开关。
// 所有配置存储在实例目录的 config/runtime.toml 中，与 Python 共享的
// bot_config.toml 互不影响。
package execution

// Mode 执行模式：兼容模式对齐 Python 行为，原生模式启用 Go 优化
type Mode string

const (
	ModeCompatibility Mode = "compatibility"
	ModeNative        Mode = "native"
)

// NativeModeImplemented 表示原生模式是否已经接入实际调度层。
//
// 阶段 2 的会话并发调度与入站提交定序已接入；单个引擎是否生效仍由 [features] 与 mode 共同决定。
const NativeModeImplemented = true

// NativeModeActive 判断当前配置下原生模式是否有实际生效的引擎。
//
// native 是总开关，[features] 是细粒度开关；已接入的引擎在实现处参与这里的 OR 判定。
func NativeModeActive(profile Profile, toggles FeatureToggles) bool {
	if profile.Mode != ModeNative {
		return false
	}
	for _, feature := range toggles.ToInfoList() {
		if feature.Implemented && feature.Enabled {
			return true
		}
	}
	return false
}

func NativeFeatureActive(profile Profile, toggles FeatureToggles, key string) bool {
	return profile.Mode == ModeNative && toggles.Enabled(key) && IsFeatureImplemented(key)
}

// validModes 用于校验用户输入
var validModes = map[Mode]struct{}{
	ModeCompatibility: {},
	ModeNative:        {},
}

// IsValidMode 检查给定模式是否合法
func IsValidMode(m Mode) bool {
	_, ok := validModes[m]
	return ok
}

// Profile 运行时执行配置
type Profile struct {
	// Mode 当前执行模式
	Mode Mode `json:"mode" toml:"mode"`
	// WorkerPoolSize 工作池大小；仅 native 模式生效，0 表示按 CPU 核心数自动
	WorkerPoolSize int `json:"worker_pool_size" toml:"worker_pool_size"`
}

// DefaultProfile 返回默认配置：兼容模式、自动工作池
func DefaultProfile() Profile {
	return Profile{
		Mode:           ModeCompatibility,
		WorkerPoolSize: 0,
	}
}

// runtimeConfigFileName 运行时配置文件名
const runtimeConfigFileName = "runtime.toml"
