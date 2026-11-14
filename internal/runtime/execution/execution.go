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
