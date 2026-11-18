package pythoncompat

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	defaultSidecarPython = "/app/pycompat/bin/python"
	defaultSidecarScript = "/app/pycompat_sidecar.py"
	defaultSidecarCWD    = "/app"
	defaultSourceRoot    = "/app/pycompat-src"
	defaultSidecarPort   = 8767
	defaultIdleSeconds   = 300
	healthProbeInterval  = 100 * time.Millisecond
	startLockName        = "maigo-pycompat.lock"
	startPidName         = "maigo-pycompat.pid"
)

// sidecarProc 本进程拉起的侧车子进程；多 Worker 共享 127.0.0.1 端口时，
// 先探测再抢锁启动，避免并发绑定失败后静默回落本地实现。
// 空闲退出由侧车自行处理；本结构按 lastActive 收割僵死进程。
type sidecarProc struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	waitDone   chan struct{}
	lastActive time.Time
	reapStop   chan struct{}
	idleSecs   int
}

var (
	managedSidecar = &sidecarProc{waitDone: make(chan struct{})}
	startFlight    singleflight.Group
	shutdownOnce   sync.Once
)

func init() {
	registerSidecarShutdown()
}

// ensureRunning 保证 loopback 侧车可用：先 /health 或 TCP，未就绪再独占拉起。
func (c *Client) ensureRunning(ctx context.Context) (func(), error) {
	if !c.Configured() {
		return func() {}, nil
	}
	if !isLoopbackBaseURL(c.baseURL) {
		return func() {}, nil
	}
	if err := ensureSidecarReady(ctx, c); err != nil {
		return nil, err
	}
	managedSidecar.touch()
	return func() {}, nil
}

func ensureSidecarReady(ctx context.Context, c *Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready, _ := probeSidecar(ctx, c); ready {
		return nil
	}
	_, err, _ := startFlight.Do(c.baseURL, func() (any, error) {
		if ready, _ := probeSidecar(ctx, c); ready {
			return nil, nil
		}
		return nil, startSidecarExclusive(ctx, c)
	})
	return err
}

func startSidecarExclusive(ctx context.Context, c *Client) error {
	lockPath := filepath.Join(os.TempDir(), startLockName)
	pidPath := filepath.Join(os.TempDir(), startPidName)
	lock, err := acquireStartLock(lockPath)
	if err != nil {
		return fmt.Errorf("pythoncompat: 获取侧车启动锁失败: %w", err)
	}
	defer releaseStartLock(lock)

	if ready, _ := probeSidecar(ctx, c); ready {
		return nil
	}
	if err := managedSidecar.spawn(ctx, c, pidPath); err != nil {
		return err
	}
	return waitUntilReady(ctx, c)
}

func (p *sidecarProc) spawn(ctx context.Context, c *Client, pidPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil && p.cmd.Process != nil {
		if ready, _ := probeSidecar(ctx, c); ready {
			return nil
		}
		select {
		case <-p.waitDone:
		default:
			_ = terminateProcess(p.cmd)
			<-p.waitDone
		}
		p.cmd = nil
		p.waitDone = make(chan struct{})
	}

	python, script, cwd := launchPaths()
	if _, err := os.Stat(python); err != nil {
		return fmt.Errorf("pythoncompat: 侧车解释器不可用 (%s): %w", python, err)
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("pythoncompat: 侧车脚本不可用 (%s): %w", script, err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "depends-data"), 0o755); err != nil {
		return fmt.Errorf("pythoncompat: 创建 depends-data 失败: %w", err)
	}

	port, err := portFromBaseURL(c.baseURL)
	if err != nil {
		return err
	}
	sourceRoot := strings.TrimSpace(os.Getenv("MAIBOT_SOURCE_ROOT"))
	if sourceRoot == "" {
		sourceRoot = defaultSourceRoot
	}
	instanceDir := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if instanceDir == "" {
		instanceDir = cwd
	}
	token := strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_SIDECAR_TOKEN"))
	if token == "" {
		token = c.token
	}
	idleSeconds := loadIdleSeconds()

	cmd := exec.Command(python, script)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PYTHONDONTWRITEBYTECODE=1",
		"MAIBOT_SOURCE_ROOT="+sourceRoot,
		"MAIBOT_INSTANCE_DIR="+instanceDir,
		"MAIBOT_COMPAT_SIDECAR_PORT="+strconv.Itoa(port),
		"MAIBOT_COMPAT_SIDECAR_TOKEN="+token,
		"MAIBOT_COMPAT_SIDECAR_HOST=127.0.0.1",
		"MAIBOT_COMPAT_SIDECAR_IDLE="+strconv.Itoa(idleSeconds),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pythoncompat: 拉起侧车失败: %w", err)
	}
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	p.cmd = cmd
	p.lastActive = time.Now()
	p.idleSecs = idleSeconds
	waitDone := p.waitDone
	p.startReaperLocked(idleSeconds)
	go func() {
		_ = cmd.Wait()
		_ = os.Remove(pidPath)
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
			if p.reapStop != nil {
				close(p.reapStop)
				p.reapStop = nil
			}
			p.waitDone = make(chan struct{})
		}
		p.mu.Unlock()
		close(waitDone)
	}()
	return nil
}

func (p *sidecarProc) touch() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastActive = time.Now()
}

func (p *sidecarProc) startReaperLocked(idleSeconds int) {
	if idleSeconds <= 0 {
		return
	}
	if p.reapStop != nil {
		close(p.reapStop)
	}
	stop := make(chan struct{})
	p.reapStop = stop
	grace := time.Duration(idleSeconds)*time.Second + 30*time.Second
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// 不打 /health：探活会刷新侧车 LAST_ACTIVE，反而拖死空闲退出
				if !p.shouldReap(grace) {
					continue
				}
				p.stop()
				return
			}
		}
	}()
}

func (p *sidecarProc) shouldReap(grace time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil || p.lastActive.IsZero() {
		return false
	}
	select {
	case <-p.waitDone:
		return false
	default:
	}
	return time.Since(p.lastActive) >= grace
}

func waitUntilReady(ctx context.Context, c *Client) error {
	for {
		if ready, err := probeSidecar(ctx, c); ready {
			return nil
		} else if err != nil && ctx.Err() != nil {
			return fmt.Errorf("pythoncompat: 等待侧车就绪超时: %w", ctx.Err())
		}
		select {
		case <-ctx.Done():
			managedSidecar.stop()
			return fmt.Errorf("pythoncompat: 等待侧车就绪超时: %w", ctx.Err())
		case <-time.After(healthProbeInterval):
		}
	}
}

func probeSidecar(ctx context.Context, c *Client) (bool, error) {
	if !c.Configured() {
		return false, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			return true, nil
		}
		return false, nil
	}
	hostPort, parseErr := hostPortFromBaseURL(c.baseURL)
	if parseErr != nil {
		return false, err
	}
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, dialErr := dialer.DialContext(ctx, "tcp", hostPort)
	if dialErr != nil {
		return false, err
	}
	_ = conn.Close()
	return false, nil
}

func (p *sidecarProc) stop() {
	p.mu.Lock()
	cmd := p.cmd
	waitDone := p.waitDone
	if p.reapStop != nil {
		close(p.reapStop)
		p.reapStop = nil
	}
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-waitDone:
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
			p.waitDone = make(chan struct{})
		}
		p.mu.Unlock()
		return
	default:
	}
	_ = terminateProcess(cmd)
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
	}
	p.mu.Lock()
	if p.cmd == cmd {
		p.cmd = nil
		p.waitDone = make(chan struct{})
	}
	p.mu.Unlock()
}

func stopManagedSidecar() {
	shutdownOnce.Do(func() {
		managedSidecar.stop()
	})
}

func loadIdleSeconds() int {
	raw := strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_SIDECAR_IDLE"))
	if raw == "" {
		return defaultIdleSeconds
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return defaultIdleSeconds
	}
	return seconds
}

func launchPaths() (python, script, cwd string) {
	python = strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_SIDECAR_PYTHON"))
	if python == "" {
		python = defaultSidecarPython
	}
	script = strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_SIDECAR_SCRIPT"))
	if script == "" {
		script = defaultSidecarScript
	}
	cwd = strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_SIDECAR_CWD"))
	if cwd == "" {
		cwd = defaultSidecarCWD
	}
	return python, script, cwd
}

func isLoopbackBaseURL(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func portFromBaseURL(baseURL string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return 0, fmt.Errorf("pythoncompat: 解析侧车地址失败: %w", err)
	}
	if u.Port() != "" {
		port, convErr := strconv.Atoi(u.Port())
		if convErr != nil {
			return 0, fmt.Errorf("pythoncompat: 侧车端口无效: %w", convErr)
		}
		return port, nil
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return 443, nil
	default:
		if raw := strings.TrimSpace(os.Getenv("MAIBOT_COMPAT_SIDECAR_PORT")); raw != "" {
			if port, convErr := strconv.Atoi(raw); convErr == nil && port > 0 {
				return port, nil
			}
		}
		return defaultSidecarPort, nil
	}
}

func hostPortFromBaseURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("pythoncompat: 侧车地址缺少主机")
	}
	port, err := portFromBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
