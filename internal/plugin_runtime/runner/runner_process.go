package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// child 已拉起的 Python Runner 子进程句柄
type child struct {
	cmd *exec.Cmd
	wg  sync.WaitGroup

	mu       sync.Mutex
	done     chan struct{}
	waitOnce sync.Once
	exited   bool
	exitErr  error
}

// startWait 异步回收进程；stderr 使用独立管道，Wait 不会截断异常堆栈
func (c *child) startWait() {
	if c == nil || c.cmd == nil {
		return
	}
	c.waitOnce.Do(func() {
		go func() {
			err := c.cmd.Wait()
			c.mu.Lock()
			c.exited = true
			c.exitErr = err
			c.mu.Unlock()
			if c.done != nil {
				close(c.done)
			}
		}()
	})
}

// hasExited 返回子进程是否已退出并完成回收
func (c *child) hasExited() bool {
	if c == nil {
		return true
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *child) exitError() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitErr
}

func buildRunnerEnv(state *State, opts StartOptions, extra map[string]string) ([]string, error) {
	base := os.Environ()
	kv := make(map[string]string, len(base)+16)
	for _, entry := range base {
		if i := strings.IndexByte(entry, '='); i > 0 {
			kv[entry[:i]] = entry[i+1:]
		}
	}

	put := func(key, val string) {
		if val != "" {
			kv[key] = val
		}
	}

	put(EnvIPCAddress, state.IPCAddress)
	put(EnvSessionToken, state.SessionToken)
	put(EnvRunnerGroup, string(opts.Group))
	put(EnvHostVersion, strings.TrimSpace(opts.HostVersion))
	put(EnvHostPID, strconv.Itoa(os.Getpid()))
	put(EnvPluginDirs, strings.Join(opts.PluginDirs, string(os.PathListSeparator)))
	put(EnvTrustedPluginDirs, strings.Join(opts.TrustedPluginDirs, string(os.PathListSeparator)))
	put(EnvPluginTypeFilter, strings.TrimSpace(opts.PluginTypeFilter))
	put(EnvLocalPluginSDKPath, strings.TrimSpace(opts.LocalSDKPath))
	put(EnvForcePluginCompatibility, "0")
	put(EnvExternalPluginIDs, "{}")
	put(EnvBlockedPluginReasons, "{}")

	for k, v := range extra {
		if k = strings.TrimSpace(k); k != "" {
			kv[k] = v
		}
	}
	for k, v := range opts.ExtraEnv {
		if k = strings.TrimSpace(k); k != "" {
			kv[k] = v
		}
	}

	if pythonPath, enabled, err := buildPythonPathWithLocalSDK(kv); err != nil {
		return nil, err
	} else if enabled {
		kv["PYTHONPATH"] = pythonPath
		zap.L().Info("Runner 将优先使用本地插件 SDK",
			zap.String("path", kv[EnvLocalPluginSDKPath]))
	}

	out := make([]string, 0, len(kv))
	for k, v := range kv {
		out = append(out, k+"="+v)
	}
	return out, nil
}

func spawnRunnerProcess(ctx context.Context, opts StartOptions, state *State) (*child, error) {
	pythonBin := strings.TrimSpace(opts.PythonBin)
	if pythonBin == "" {
		return nil, fmt.Errorf("未指定 Python 解释器")
	}

	resolved, err := lookPathPython(pythonBin)
	if err != nil {
		return nil, err
	}

	args := append([]string{resolved}, runnerExecArgs(opts.ScriptPath)...)
	workDir := runnerWorkingDir(opts.ProjectRoot, opts.ScriptPath)
	if workDir != "" {
		if st, statErr := os.Stat(workDir); statErr != nil || !st.IsDir() {
			return nil, fmt.Errorf("Runner 工作目录无效: %q", workDir)
		}
	}

	// 脚本为相对路径时相对于项目根解析
	if len(args) > 1 && strings.HasSuffix(strings.ToLower(args[1]), ".py") && !filepath.IsAbs(args[1]) && opts.ProjectRoot != "" {
		args[1] = filepath.Join(opts.ProjectRoot, args[1])
	}

	env, err := buildRunnerEnv(state, opts, nil)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, resolved, args[1:]...)
	cmd.Env = env
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.SysProcAttr = runnerSysProcAttr()

	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("创建 Runner stderr 管道失败: %w", err)
	}
	cmd.Stderr = stderrWriter
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return nil, fmt.Errorf("拉起 Runner 子进程失败: %w", err)
	}
	_ = stderrWriter.Close()

	child := &child{cmd: cmd, done: make(chan struct{})}
	child.wg.Add(1)
	go func() {
		defer child.wg.Done()
		drainRunnerStderr(stderrReader, opts.Group, child.cmd.Process.Pid)
	}()
	child.startWait()

	zap.L().Info("Python Runner 子进程已拉起",
		zap.String("group", string(opts.Group)),
		zap.Int("pid", cmd.Process.Pid),
		zap.String("python", resolved),
		zap.Strings("args", args[1:]),
		zap.String("workdir", workDir),
		zap.String("ipc", state.IPCAddress))

	return child, nil
}

func lookPathPython(bin string) (string, error) {
	if filepath.IsAbs(bin) {
		if st, err := os.Stat(bin); err != nil || st.IsDir() {
			return "", fmt.Errorf("Python 解释器不存在: %q（请设置 %s 或安装 Python）", bin, EnvPluginPython)
		}
		return bin, nil
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("找不到 Python 可执行文件 %q: %w（请设置 %s 或将 Python 加入 PATH）", bin, err, EnvPluginPython)
	}
	return path, nil
}

func drainRunnerStderr(r io.ReadCloser, group Group, pid int) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		zap.L().Warn("Runner stderr",
			zap.String("group", string(group)),
			zap.Int("pid", pid),
			zap.String("line", line))
	}
}

func stopRunnerChild(child *child, group Group, timings ShutdownTimings) {
	if child == nil || child.cmd == nil || child.cmd.Process == nil {
		return
	}
	proc := child.cmd.Process
	pid := proc.Pid
	child.startWait()

	if !waitRunnerExit(child, timings.naturalExitWait) {
		if err := terminateRunnerProcess(proc); err != nil {
			zap.L().Warn("Runner 进程 terminate 失败",
				zap.String("group", string(group)),
				zap.Int("pid", pid),
				zap.Error(err))
		}
		if !waitRunnerExit(child, timings.terminateWait) {
			if err := proc.Kill(); err != nil {
				zap.L().Warn("Runner 进程 kill 失败",
					zap.String("group", string(group)),
					zap.Int("pid", pid),
					zap.Error(err))
			}
			if !waitRunnerExit(child, timings.killWait) {
				zap.L().Warn("Runner 进程 kill 后仍未退出",
					zap.String("group", string(group)),
					zap.Int("pid", pid))
			}
		}
	}

	stderrDone := make(chan struct{})
	go func() {
		child.wg.Wait()
		close(stderrDone)
	}()
	select {
	case <-stderrDone:
	case <-time.After(timings.stderrWait):
		zap.L().Warn("Runner 已退出，但等待 stderr 排空超时",
			zap.String("group", string(group)),
			zap.Int("pid", pid))
	}

	fields := []zap.Field{
		zap.String("group", string(group)),
		zap.Int("pid", pid),
	}
	if child.cmd.ProcessState != nil {
		fields = append(fields, zap.Int("exit_code", child.cmd.ProcessState.ExitCode()))
	}
	if err := child.exitError(); err != nil {
		fields = append(fields, zap.Error(err))
	}
	zap.L().Info("Python Runner 子进程已停止", fields...)
}

func waitRunnerExit(child *child, timeout time.Duration) bool {
	select {
	case <-child.done:
		return true
	case <-time.After(timeout):
		return false
	}
}
