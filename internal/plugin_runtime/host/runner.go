package host

import (
	"MaiGo/internal/plugin_runtime/runner"
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// RunnerGroup Runner 运行时分组，对齐 integration.py 的 builtin / third_party 双 Supervisor
// RunnerState 单个 Runner 组的运行状态
//
// 内嵌 runner.State 以提升进程字段（Group/Started/IPCAddress/PluginDirs 等），
// 字段访问方式不变；这里只额外持有 RPCServer——它属于宿主传输层，不放进子包。
type RunnerState struct {
	runner.State
	rpc *RPCServer
}

// runnerState 返回内嵌的子包状态指针
func (s *RunnerState) runnerState() *runner.State {
	if s == nil {
		return nil
	}
	return &s.State
}

// StartRunnerOptionsFromSpawnConfig 用环境解析结果填充 spawn 相关字段
func StartRunnerOptionsFromSpawnConfig(cfg SpawnConfig, base StartRunnerOptions) StartRunnerOptions {
	base.SpawnPython = cfg.Enabled
	base.PythonBin = cfg.PythonBin
	base.ScriptPath = cfg.ScriptPath
	if base.ProjectRoot == "" {
		base.ProjectRoot = cfg.ProjectRoot
	}
	if base.SpawnPython {
		base.StartIPCListener = true
	}
	return applyDefaultPluginDirs(base)
}

// StartRunner 启动指定分组的 Runner（可选 IPC 监听与 Python 子进程）
func (h *Host) StartRunner(ctx context.Context, opts StartRunnerOptions) (*RunnerState, error) {
	if opts.Group == "" {
		opts.Group = RunnerGroupThirdParty
	}
	h.rememberStartOptions(opts)
	usageStarted := false
	if opts.SpawnPython {
		started, usageErr := h.ensureUsageTracking()
		if usageErr != nil {
			h.failRunnerStart(opts.Group, usageErr)
			return nil, usageErr
		}
		usageStarted = started
	}
	completed := false
	defer func() {
		if completed || !usageStarted {
			return
		}
		if stopErr := h.StopUsageTracking(); stopErr != nil {
			zap.L().Warn("回滚插件使用时长记录失败", zap.Error(stopErr))
		}
	}()
	h.beginRunnerStart(opts.Group)
	if opts.SpawnPython {
		opts.StartIPCListener = true
	}
	if opts.StartIPCListener && strings.TrimSpace(opts.IPCAddress) == "" {
		opts.IPCAddress = h.configuredRunnerIPCAddress(opts.Group)
	}

	state := &RunnerState{
		State: runner.State{
			Group:             opts.Group,
			Started:           false,
			SpawnPython:       opts.SpawnPython,
			IPCAddress:        strings.TrimSpace(opts.IPCAddress),
			PluginDirs:        append([]string(nil), opts.PluginDirs...),
			TrustedPluginDirs: append([]string(nil), opts.TrustedPluginDirs...),
		},
	}

	needIPC := opts.StartIPCListener || state.IPCAddress != "" || opts.SpawnPython
	if needIPC {
		ipcReq, err := ResolveIPCListenRequest(state.IPCAddress)
		if err != nil {
			h.failRunnerStart(opts.Group, err)
			return nil, err
		}
		srv, err := NewRPCServer(h, RPCServerOptions{
			HostVersion:  opts.HostVersion,
			Group:        opts.Group,
			SessionToken: "",
		})
		if err != nil {
			h.failRunnerStart(opts.Group, err)
			return nil, err
		}
		addr, err := srv.ListenRequest(ctx, ipcReq)
		if err != nil {
			srv.Stop()
			h.failRunnerStart(opts.Group, err)
			return nil, fmt.Errorf("启动 Runner IPC 监听失败: %w", err)
		}
		state.IPCAddress = addr
		state.SessionToken = srv.SessionToken()
		state.rpc = srv
		state.Started = true
	}

	if opts.SpawnPython {
		opts.ExtraEnv = h.runnerHostEnv(opts.ExtraEnv)
		if state.rpc == nil {
			err := fmt.Errorf("SpawnPython 需要先启动 IPC 监听")
			h.failRunnerStart(opts.Group, err)
			return nil, err
		}
		child, err := spawnRunnerProcess(ctx, opts, state)
		if err != nil {
			state.rpc.Stop()
			h.failRunnerStart(opts.Group, err)
			return nil, err
		}
		state.SetChild(child)
	}

	h.mu.Lock()
	h.runners[opts.Group] = state
	h.mu.Unlock()
	h.finishRunnerStart(opts.Group, state, opts.SpawnPython)
	h.loadPluginManagementAliases()
	completed = true
	return state, nil
}

// PythonSpawned 是否已通过 exec 拉起 Python Runner
func (s *RunnerState) PythonSpawned() bool {
	return s != nil && s.Child() != nil
}

// StopRunner 停止指定分组的 Runner 子进程与 RPC 监听
func (h *Host) StopRunner(group RunnerGroup) {
	h.markRunnerStopping(group)
	h.mu.Lock()
	state := h.runners[group]
	delete(h.runners, group)
	h.mu.Unlock()
	h.ReconcileUsage()

	if state == nil {
		h.markRunnerStopped(group)
		return
	}
	// 先按 MaiBot 语义通知 Runner 排空并退出，再终止子进程；无连接时只记录，不阻塞停止流程。
	timings := runnerShutdownTimingsFor(h.shutdownRequestedNow())
	h.shutdownRunnerRPC(state, "normal", timings.DrainTimeoutMS())
	if state.Child() != nil {
		stopRunnerChild(state.Child(), state.Group, timings)
	}
	if state.rpc != nil {
		state.rpc.Stop()
		state.rpc = nil
	}
	state.Started = false
	h.markRunnerStopped(group)
	h.clearStartupState(group)
}

// StopAllRunners 按启动逆序停止全部 Runner 组（第三方先于内置）
func (h *Host) StopAllRunners() {
	h.mu.RLock()
	groups := make([]RunnerGroup, 0, len(h.runners))
	for g := range h.runners {
		groups = append(groups, g)
	}
	h.mu.RUnlock()
	h.StopRunnerGroupsReverse(groups)
}

// LogSpawnSkipped 在启用开关但无法拉起时输出明确日志
func LogSpawnSkipped(cfg SpawnConfig, reason error) {
	fields := []zap.Field{
		zap.Bool("spawn_requested", cfg.Enabled),
		zap.String("python", cfg.PythonBin),
		zap.String("script", cfg.ScriptPath),
		zap.String("project_root", cfg.ProjectRoot),
	}
	if reason != nil {
		fields = append(fields, zap.Error(reason))
	}
	zap.L().Warn("未拉起 Python Runner 子进程", fields...)
}
