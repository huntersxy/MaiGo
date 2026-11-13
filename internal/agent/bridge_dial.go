package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// DialStdio 启动 ACP 子进程并通过 stdin/stdout 交换 NDJSON
func (b *Bridge) DialStdio(ctx context.Context, command string, args []string, cwd string, env map[string]string) error {
	_ = ctx
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return fmt.Errorf("acpagent: 连接已打开")
	}
	b.mu.Unlock()

	processEnv := os.Environ()
	kv := make(map[string]string, len(processEnv)+len(env))
	for _, entry := range processEnv {
		if i := strings.IndexByte(entry, '='); i > 0 {
			kv[entry[:i]] = entry[i+1:]
		}
	}
	for k, v := range env {
		kv[k] = v
	}

	var executable string
	var err error
	if customPath, ok := env["PATH"]; ok && customPath != "" {
		executable, err = lookPathIn(command, customPath)
	} else {
		executable, err = exec.LookPath(command)
	}
	if err != nil {
		return fmt.Errorf("找不到 ACP 启动程序：%s", command)
	}

	envSlice := make([]string, 0, len(kv))
	for k, v := range kv {
		envSlice = append(envSlice, k+"="+v)
	}

	cmd := exec.Command(executable, args...)
	cmd.Env = envSlice
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.SysProcAttr = bridgeSysProcAttr()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acpagent: stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("acpagent: stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("acpagent: stderr 管道失败: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("acpagent: 启动 ACP 子进程失败: %w", err)
	}

	go drainBridgeStderr(stderr, func(line string) {
		b.mu.Lock()
		b.appendStderr(line)
		b.mu.Unlock()
	})

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.stdout = stdout
	b.failure = nil
	b.startReadLocked()
	b.mu.Unlock()
	return nil
}

// DialTCP 连接 ACP TCP 桥；token 非空时发送 dsh Desktop 认证首帧
func (b *Bridge) DialTCP(ctx context.Context, addr string, token string, timeoutSec float64) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return fmt.Errorf("acpagent: 连接已打开")
	}
	b.mu.Unlock()

	dialTimeout := time.Duration(timeoutSec * float64(time.Second))
	if dialTimeout <= 0 {
		dialTimeout = 60 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("acpagent: TCP 连接失败: %w", err)
	}

	if token != "" {
		if err := writeDesktopAuth(conn, token, dialTimeout); err != nil {
			_ = conn.Close()
			return err
		}
	}

	b.mu.Lock()
	b.conn = conn
	b.stdin = conn
	b.stdout = ioReadCloserConn{Conn: conn}
	b.failure = nil
	b.startReadLocked()
	b.mu.Unlock()
	return nil
}

type ioReadCloserConn struct {
	net.Conn
}

func (c ioReadCloserConn) Close() error {
	return nil
}

func writeDesktopAuth(conn net.Conn, token string, timeout time.Duration) error {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	if err := WriteFrame(conn, map[string]any{"token": token}); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("dsh Desktop 桥接认证失败: %w", err)
	}
	var resp map[string]any
	if err := sonic.ConfigStd.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("dsh Desktop 桥接认证响应无效: %w", err)
	}
	if resp["authenticated"] != true {
		return fmt.Errorf("dsh Desktop 桥接认证失败")
	}
	return nil
}

func lookPathIn(name, pathList string) (string, error) {
	if filepath.IsAbs(name) {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("not found")
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		pathext := os.Getenv("PATHEXT")
		if strings.TrimSpace(pathext) == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		for _, ext := range strings.Split(pathext, ";") {
			ext = strings.TrimSpace(ext)
			if ext == "" {
				continue
			}
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			exts = append(exts, ext)
		}
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("not found")
}
