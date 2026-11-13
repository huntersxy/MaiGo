package computer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	envLocalMode        = "MAIBOT_COMPUTER_LOCAL"
	envLocalAllowlist   = "MAIBOT_COMPUTER_LOCAL_ALLOWLIST"
	envLocalTimeout     = "MAIBOT_COMPUTER_LOCAL_TIMEOUT_SECONDS"
	defaultLocalTimeout = 60
	maxCommandOutput    = 32 * 1024
)

// LocalModeEnabled 是否显式开启本机命令执行（默认关闭，极不安全）
func LocalModeEnabled() bool {
	raw := strings.TrimSpace(os.Getenv(envLocalMode))
	if raw == "" {
		return false
	}
	ok, err := strconv.ParseBool(raw)
	return err == nil && ok
}

func localAllowlist() []string {
	raw := strings.TrimSpace(os.Getenv(envLocalAllowlist))
	if raw == "" {
		return nil
	}
	var list []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			list = append(list, part)
		}
	}
	return list
}

func localExecTimeout() time.Duration {
	sec := defaultLocalTimeout
	if raw := strings.TrimSpace(os.Getenv(envLocalTimeout)); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			sec = v
		}
	}
	return time.Duration(sec) * time.Second
}

// CommandResult 对齐沙盒 shell 动作的 stdout/stderr/exit_code
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func truncateOutput(raw string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	b := []byte(raw)
	if len(b) <= maxBytes {
		return raw
	}
	return string(b[:maxBytes]) + "...(truncated)"
}

func commandAllowed(command string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	lower := strings.ToLower(cmd)
	for _, entry := range allowed {
		a := strings.ToLower(strings.TrimSpace(entry))
		if a == "" {
			continue
		}
		if lower == a || strings.HasPrefix(lower, a+" ") || strings.HasPrefix(lower, a+"\t") {
			return true
		}
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	first := strings.ToLower(fields[0])
	for _, entry := range allowed {
		if first == strings.ToLower(strings.TrimSpace(entry)) {
			return true
		}
	}
	return false
}

func runLocalShell(ctx context.Context, command string) (CommandResult, error) {
	if !LocalModeEnabled() {
		return CommandResult{}, ErrNotImplemented
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return CommandResult{}, fmt.Errorf("computer: 命令不能为空")
	}
	if !commandAllowed(command, localAllowlist()) {
		return CommandResult{}, ErrCommandNotAllowed
	}

	timeout := localExecTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 与 MaiBot 沙盒内 shell 一致使用 PowerShell；仅在 MAIBOT_COMPUTER_LOCAL=1 时于宿主机执行
	cmd := exec.CommandContext(runCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{
		Stdout: truncateOutput(stdout.String(), maxCommandOutput),
		Stderr: truncateOutput(stderr.String(), maxCommandOutput),
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("computer: 本机命令超时（%s）", timeout)
	}
	return result, err
}
