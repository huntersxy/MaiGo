package htmlrender

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

// browserInstance 一个 Chrome/Chromium 浏览器进程或外部 CDP 端点
type browserInstance struct {
	client   *cdpClient
	cmd      *exec.Cmd
	waitCh   chan error
	exitedCh chan struct{}
	version  string

	launched    bool
	execPath    string
	userDataDir string

	closeOnce sync.Once
}

func (b *browserInstance) alive() bool {
	if b == nil || b.client == nil || !b.client.alive() {
		return false
	}
	if b.exitedCh != nil {
		select {
		case <-b.exitedCh:
			return false
		default:
		}
	}
	return true
}

func (b *browserInstance) close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		if b.client != nil {
			b.client.close()
		}
		if b.cmd != nil && b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
			if b.exitedCh != nil {
				select {
				case <-b.exitedCh:
				case <-time.After(3 * time.Second):
				}
			}
		}
		if b.userDataDir != "" {
			_ = os.RemoveAll(b.userDataDir)
		}
	})
}

type browserVersionInfo struct {
	WebSocketDebuggerURL string
	Browser              string
	ProtocolVersion      string
	UserAgent            string
}

func resolveBrowserWebSocket(ctx context.Context, endpoint string, timeout time.Duration) (string, browserVersionInfo, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", browserVersionInfo{}, fmt.Errorf("未配置浏览器 CDP 端点")
	}
	if strings.HasPrefix(endpoint, "ws://") || strings.HasPrefix(endpoint, "wss://") {
		return endpoint, browserVersionInfo{}, nil
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	versionURL := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(versionURL, "/json/version") {
		versionURL += "/json/version"
	}
	client := &http.Client{}
	if timeout > 0 {
		client.Timeout = timeout
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return "", browserVersionInfo{}, fmt.Errorf("构建浏览器版本请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", browserVersionInfo{}, fmt.Errorf("读取浏览器 CDP 版本失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", browserVersionInfo{}, fmt.Errorf("读取浏览器 CDP 版本响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", browserVersionInfo{}, fmt.Errorf("浏览器 CDP 版本接口返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload map[string]any
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return "", browserVersionInfo{}, fmt.Errorf("解析浏览器 CDP 版本失败: %w", err)
	}
	wsURL, _ := payload["webSocketDebuggerUrl"].(string)
	if strings.TrimSpace(wsURL) == "" {
		return "", browserVersionInfo{}, fmt.Errorf("浏览器 CDP 版本缺少 webSocketDebuggerUrl")
	}
	info := browserVersionInfo{
		WebSocketDebuggerURL: wsURL,
		Browser:              asString(payload["Browser"]),
		ProtocolVersion:      asString(payload["Protocol-Version"]),
		UserAgent:            asString(payload["User-Agent"]),
	}
	return wsURL, info, nil
}

func connectBrowser(ctx context.Context, wsURL string, info browserVersionInfo) (*browserInstance, error) {
	client, err := dialCDP(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	version := info.Browser
	if version == "" {
		version, _, err = client.browserVersion(ctx)
		if err != nil {
			client.close()
			return nil, fmt.Errorf("读取浏览器版本失败: %w", err)
		}
	}
	return &browserInstance{client: client, version: version}, nil
}

func launchLocalBrowser(ctx context.Context, cfg Config) (*browserInstance, error) {
	execPath, err := findBrowserExecutable(cfg)
	if err != nil {
		return nil, err
	}
	port, err := freeTCPPort()
	if err != nil {
		return nil, fmt.Errorf("分配浏览器调试端口失败: %w", err)
	}
	userDataDir, err := os.MkdirTemp("", "maibot-htmlrender-*")
	if err != nil {
		return nil, fmt.Errorf("创建浏览器临时目录失败: %w", err)
	}
	args := browserLaunchArgs(cfg, port, userDataDir, execPath)
	cmd := exec.Command(execPath, args...)
	cmd.Stdout = io.Discard
	stderr := &tailBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_BROWSERS_PATH="+resolveBrowserInstallRoot(cfg))
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(userDataDir)
		return nil, fmt.Errorf("启动本地浏览器失败: %w", err)
	}
	waitCh := make(chan error, 1)
	exitedCh := make(chan struct{})
	go func() {
		waitCh <- cmd.Wait()
		close(exitedCh)
	}()

	versionURL := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	deadline := time.Now().Add(cfg.StartupTimeout)
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(userDataDir)
			return nil, fmt.Errorf("启动本地浏览器取消: %w", ctx.Err())
		default:
		}
		select {
		case err := <-waitCh:
			_ = os.RemoveAll(userDataDir)
			return nil, fmt.Errorf("启动本地浏览器失败: 进程提前退出: %v (%s)", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(userDataDir)
			return nil, fmt.Errorf("启动本地浏览器超时（%s）: %s", cfg.StartupTimeout, stderr.String())
		}
		wsURL, _, err := resolveBrowserWebSocket(ctx, versionURL, 2*time.Second)
		if err == nil {
			client, dialErr := dialCDP(ctx, wsURL)
			if dialErr == nil {
				product, _, versionErr := client.browserVersion(ctx)
				if versionErr != nil {
					client.close()
					_ = cmd.Process.Kill()
					_ = os.RemoveAll(userDataDir)
					return nil, fmt.Errorf("读取本地浏览器版本失败: %w", versionErr)
				}
				return &browserInstance{
					client:      client,
					cmd:         cmd,
					waitCh:      waitCh,
					exitedCh:    exitedCh,
					version:     product,
					launched:    true,
					execPath:    execPath,
					userDataDir: userDataDir,
				}, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func browserLaunchArgs(cfg Config, port int, userDataDir, execPath string) []string {
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		"--remote-allow-origins=*",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-sync",
		"--disable-extensions",
		"--disable-component-update",
		"--disable-default-apps",
		"--mute-audio",
		"--hide-scrollbars",
		"--no-service-autorun",
		"--password-store=basic",
		"--disable-dev-shm-usage",
		"--disable-setuid-sandbox",
		"--no-sandbox",
		"--no-zygote",
		"--disable-gpu",
		"--window-size=900,500",
	}
	if cfg.Headless && !strings.Contains(strings.ToLower(filepath.Base(execPath)), "headless") {
		args = append(args, "--headless=new")
	}
	args = append(args, cfg.LaunchArgs...)
	args = append(args, "about:blank")
	return args
}

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("无法读取临时端口")
	}
	return addr.Port, nil
}

func findBrowserExecutable(cfg Config) (string, error) {
	if configured := resolveBrowserPath(cfg, cfg.BrowserPath); configured != "" {
		if fileExists(configured) {
			return configured, nil
		}
	}
	for _, candidate := range defaultBrowserCandidates() {
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	if path := findPlaywrightBrowser(resolveBrowserInstallRoot(cfg)); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("未找到可用的 Chrome/Chromium/Edge 浏览器；请设置 plugin_runtime.render.executable_path 或 MAIBOT_HTMLRENDER_BROWSER_PATH")
}

func defaultBrowserCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if strings.TrimSpace(root) == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			)
		}
		return out
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		var out []string
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "msedge"} {
			if path, err := exec.LookPath(name); err == nil {
				out = append(out, path)
			}
		}
		return out
	}
}

func findPlaywrightBrowser(root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasPrefix(name, "chromium-") && !strings.HasPrefix(name, "chrome-") && !strings.HasPrefix(name, "chromium_headless_shell-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		for _, relative := range []string{
			"chrome-win/chrome.exe",
			"chrome-win64/chrome.exe",
			"chrome-win/headless_shell.exe",
			"chrome-headless-shell-win64/chrome-headless-shell.exe",
			"chrome-mac/Chromium.app/Contents/MacOS/Chromium",
			"chrome-mac/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			"chrome-linux/chrome",
			"chrome-linux64/chrome",
			"chrome-headless-shell-linux64/chrome-headless-shell",
		} {
			candidate := filepath.Join(dir, filepath.FromSlash(relative))
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func resolveBrowserInstallRoot(cfg Config) string {
	root := strings.TrimSpace(cfg.BrowserInstallRoot)
	if root == "" {
		root = "data/playwright-browsers"
	}
	if filepath.IsAbs(root) {
		return root
	}
	base := strings.TrimSpace(cfg.InstanceDir)
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, root)
}

func resolveBrowserPath(cfg Config, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	base := strings.TrimSpace(cfg.InstanceDir)
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, path)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if b.limit > 0 && len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(bytes.TrimSpace(b.data)))
}
