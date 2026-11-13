package runtime

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	workerShutdownTimeout = 60 * time.Second
	runnerShutdownTimeout = workerShutdownTimeout + 15*time.Second
	restartCooldown       = time.Second
)

// RunRunner 作为 Runner 监督当前可执行文件的 Worker 子进程，直至收到停止信号或非重启退出码
func RunRunner() int {
	ClearPendingUpdate()

	execPath, err := os.Executable()
	if err != nil {
		zap.L().Error("解析可执行文件路径失败", zap.Error(err))
		return 1
	}

	args := append([]string(nil), os.Args...)
	baseEnv := os.Environ()

	zap.L().Info("Runner 已启动，将监督 Worker 子进程",
		zap.String("executable", execPath),
		zap.Strings("args", args[1:]))

	return runRunnerLoop(execPath, args[1:], baseEnv)
}

func runRunnerLoop(execPath string, args, baseEnv []string) int {
	var updateApplied atomic.Bool
	workerEnv := workerChildEnv(baseEnv)
	code := superviseProcess(execPath, args, workerEnv, superviseOptions{
		restartCode:     RestartExitCode,
		shutdownTimeout: workerShutdownTimeout,
		childName:       "Worker",
		beforeRestart: func() *int {
			handoff, applied := runBeforeRestart()
			updateApplied.Store(applied)
			return handoff
		},
	})
	if code != RunnerRelaunchExitCode {
		return normalizeExitCode(code)
	}
	if !updateApplied.Load() {
		// Worker 自己以 43 退出不代表 Runner 需要交接，不能被上级误判
		return 1
	}
	if runnerSupervised(baseEnv) {
		return RunnerRelaunchExitCode
	}
	zap.L().Info("自更新完成，由新版本的 Runner 接手；当前进程只负责守护它")
	return normalizeExitCode(superviseProcess(execPath, args, runnerChildEnv(baseEnv), superviseOptions{
		restartCode:     RunnerRelaunchExitCode,
		shutdownTimeout: runnerShutdownTimeout,
		childName:       "Runner",
	}))
}

func workerChildEnv(base []string) []string {
	return childEnv(base, modeWorker, false)
}

func runnerChildEnv(base []string) []string {
	return childEnv(base, modeRunner, true)
}

func childEnv(base []string, mode Mode, supervised bool) []string {
	out := make([]string, 0, len(base)+2)
	drop := map[string]struct{}{
		EnvRuntimeMode:   {},
		EnvWorkerProcess: {},
	}
	if supervised {
		drop[EnvRunnerSupervised] = struct{}{}
	}
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		if _, skip := drop[key]; skip {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, EnvRuntimeMode+"="+string(mode))
	if mode == modeWorker {
		out = append(out, EnvWorkerProcess+"=1")
	}
	if supervised {
		out = append(out, EnvRunnerSupervised+"=1")
	}
	return out
}

type superviseOptions struct {
	restartCode     int
	shutdownTimeout time.Duration
	childName       string
	beforeRestart   func() *int
}

func superviseProcess(execPath string, args, env []string, opts superviseOptions) int {
	stop := make(chan os.Signal, 1)
	notifyStop(stop)
	defer signal.Stop(stop)

	for {
		select {
		case <-stop:
			return 0
		default:
		}

		cmd := exec.Command(execPath, args...)
		cmd.Env = env
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		setChildProcessGroup(cmd)

		if err := cmd.Start(); err != nil {
			zap.L().Error("启动 "+opts.childName+" 失败", zap.Error(err))
			return 1
		}

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		var stopRequested bool
		var deadline time.Time
		restartChild := false

	waitChild:
		for {
			select {
			case sig := <-stop:
				stopRequested = true
				if deadline.IsZero() {
					deadline = time.Now().Add(opts.shutdownTimeout)
					zap.L().Info("收到停止信号，等待 "+opts.childName+" 优雅退出",
						zap.String("signal", sig.String()),
						zap.Duration("timeout", opts.shutdownTimeout))
					if err := requestWorkerStop(cmd.Process); err != nil {
						zap.L().Warn("向 "+opts.childName+" 发送停止信号失败", zap.Error(err))
					}
				}
			case err := <-done:
				code := exitCodeFromWait(err)
				if stopRequested {
					return normalizeExitCode(code)
				}
				if code == opts.restartCode {
					zap.L().Info(opts.childName+" 请求重启", zap.Int("exit_code", opts.restartCode))
					if opts.beforeRestart != nil {
						if handoff := opts.beforeRestart(); handoff != nil {
							select {
							case <-stop:
								return 0
							default:
							}
							return normalizeExitCode(*handoff)
						}
					}
					if !sleepOrStop(stop, restartCooldown) {
						return 0
					}
					restartChild = true
					break waitChild
				}
				return normalizeExitCode(code)
			default:
				if stopRequested && !deadline.IsZero() && time.Now().After(deadline) {
					zap.L().Error(opts.childName + " 优雅关闭超时，强制终止；应用关闭未完成")
					_ = cmd.Process.Kill()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						zap.L().Error(opts.childName + " 强制终止后仍无法回收；应用关闭未完成")
					}
					return 1
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		if restartChild {
			continue
		}
	}
}

func runBeforeRestart() (*int, bool) {
	applied, err := ApplyPendingUpdate()
	if err != nil {
		// 对齐 Python：更新失败时保留旧版本继续启动 Worker，错误已写入 last_result.json
		zap.L().Error("自更新失败，继续以旧版本启动 Worker", zap.Error(err))
		return nil, false
	}
	if applied {
		zap.L().Info("自更新已完成，将交给新版本 Runner 接手")
		code := RunnerRelaunchExitCode
		return &code, true
	}
	return nil, false
}

func sleepOrStop(stop chan os.Signal, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return false
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	return true
}

func exitCodeFromWait(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return 128 + int(status.Signal())
			}
			return status.ExitStatus()
		}
	}
	return 1
}

func normalizeExitCode(code int) int {
	if code < 0 {
		return 1
	}
	return code
}

func notifyStop(ch chan os.Signal) {
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
}
