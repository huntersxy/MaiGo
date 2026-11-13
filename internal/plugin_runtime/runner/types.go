// Package runner 是插件 Runner 子进程的生命周期管理。
//
// 对齐 MaiBot src/plugin_runtime/runner/。
//
// 本包只负责「怎么起进程、怎么等退出、怎么杀」，不持有 Host：
// Host 的编排方法（StartRunner/StopRunner/StopAllRunners）与 RPCServer 绑定
// 留在父包，通过参数把状态传进来，避免父子包循环引用。
package runner

import "time"

// Group Runner 分组
type Group string

const (
	// GroupBuiltin 内置插件分组
	GroupBuiltin Group = "builtin"
	// GroupThirdParty 第三方插件分组
	GroupThirdParty Group = "third_party"
)

// StartOptions 启动 Runner 子进程的参数
type StartOptions struct {
	Group             Group
	PluginDirs        []string
	TrustedPluginDirs []string
	PluginTypeFilter  string
	IPCAddress        string
	// StartIPCListener 为 true 时启动 RPC 监听（IPCAddress 为空则 TCP 127.0.0.1:0）
	StartIPCListener bool
	HostVersion      string
	LocalSDKPath     string

	// SpawnPython 为 true 时 exec Python Runner（会先确保 IPC 已监听）
	SpawnPython bool
	PythonBin   string
	ScriptPath  string
	ProjectRoot string
	ExtraEnv    map[string]string
}

// State 单个 Runner 组的运行状态
//
// 注意：RPCServer 不在这里。它属于父包的传输层，由父包另行持有，
// 本包只关心子进程本身。
type State struct {
	Group             Group
	Started           bool
	SpawnPython       bool
	IPCAddress        string
	SessionToken      string
	PluginDirs        []string
	TrustedPluginDirs []string

	child *child
}

// ShutdownTimings 单次 Runner 关停的等待时序，对齐 supervisor._shutdown_runner
type ShutdownTimings struct {
	drainTimeoutMS  int64
	naturalExitWait time.Duration
	terminateWait   time.Duration
	killWait        time.Duration
	stderrWait      time.Duration
}

// NewShutdownTimings 构造关停时序
//
// 字段保持私有（对齐 Python supervisor._shutdown_runner 的内部时序常量），
// 父包只需 DrainTimeoutMS 用于通知 Runner 排空，其余字段由本包内部消费。
func NewShutdownTimings(drainTimeoutMS int64, naturalExitWait, terminateWait, killWait, stderrWait time.Duration) ShutdownTimings {
	return ShutdownTimings{
		drainTimeoutMS:  drainTimeoutMS,
		naturalExitWait: naturalExitWait,
		terminateWait:   terminateWait,
		killWait:        killWait,
		stderrWait:      stderrWait,
	}
}

// DrainTimeoutMS 排空超时（毫秒），供父包构造 Runner 排空通知
func (t ShutdownTimings) DrainTimeoutMS() int64 { return t.drainTimeoutMS }

// NaturalExitWait 自然退出等待时长
func (t ShutdownTimings) NaturalExitWait() time.Duration { return t.naturalExitWait }

// TerminateWait 发送终止信号后的等待时长
func (t ShutdownTimings) TerminateWait() time.Duration { return t.terminateWait }

// KillWait 强杀后的等待时长
func (t ShutdownTimings) KillWait() time.Duration { return t.killWait }

// StderrWait stderr 排空等待时长
func (t ShutdownTimings) StderrWait() time.Duration { return t.stderrWait }
