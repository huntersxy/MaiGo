package computer

// 沙盒生命周期与文件协议；宿主只与 wsb.exe、交换目录和来宾代理通信

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	exchangeRoot       = `C:\MaiExchange`
	bootstrapRoot      = `C:\MaiBootstrap`
	workspaceRoot      = `C:\MaiWorkspace`
	guestProtocol      = 2
	heartbeatStale     = 30 * time.Second
	sandboxPollPeriod  = 2 * time.Second
	stateFileName      = "session.json"
	requestTimeoutCode = "沙盒请求仍未返回，未重放操作；使用 computer_poll(operation_id) 查询原操作结果。"
)

var operationIDPattern = regexp.MustCompile("^[0-9a-f]{32}$")

// ConnectionError 沙盒连接/生命周期错误；服务层据此暂停电脑
type ConnectionError struct{ Message string }

func (e *ConnectionError) Error() string { return e.Message }

func connectionErrorf(format string, args ...any) error {
	return &ConnectionError{Message: fmt.Sprintf(format, args...)}
}

// TimeoutError 请求等待超时；操作仍在来宾侧执行
type TimeoutError struct{ Message string }

func (e *TimeoutError) Error() string { return e.Message }

func isConnectionError(err error) bool {
	var target *ConnectionError
	return errors.As(err, &target)
}

func isTimeoutError(err error) bool {
	var target *TimeoutError
	return errors.As(err, &target)
}

// SandboxBridge 对齐 bridge.SandboxBridge
type SandboxBridge struct {
	runtimeDir string
	workspace  string
	cli        string
	stateFile  string

	cfgMu sync.RWMutex
	cfg   Config

	startMu sync.Mutex
	opMu    sync.Mutex

	mu              sync.RWMutex
	sandboxID       string
	exchange        string
	pending         string
	lastOperation   string
	lastFrame       map[string]any
	connected       bool
	observedRunning bool
	closing         bool
	viewer          *exec.Cmd
}

// NewSandboxBridge 创建桥；runtimeDir 为实例 temp/computer，workspace 为 data/computer/workspace
func NewSandboxBridge(runtimeDir, workspace string, cfg Config) *SandboxBridge {
	return newSandboxBridgeWithCLI(runtimeDir, workspace, cfg, defaultWSBPath())
}

func newSandboxBridgeWithCLI(runtimeDir, workspace string, cfg Config, cli string) *SandboxBridge {
	return &SandboxBridge{
		runtimeDir: filepath.Clean(runtimeDir),
		workspace:  filepath.Clean(workspace),
		cli:        cli,
		stateFile:  filepath.Join(filepath.Clean(runtimeDir), stateFileName),
		cfg:        cfg,
	}
}

func defaultWSBPath() string {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		return ""
	}
	return filepath.Join(localAppData, "Microsoft", "WindowsApps", "wsb.exe")
}

// Workspace 返回宿主侧电脑工作目录（映射到来宾 C:\MaiWorkspace）
func (b *SandboxBridge) Workspace() string {
	if b == nil {
		return ""
	}
	return b.workspace
}

func (b *SandboxBridge) setConfig(cfg Config) {
	b.cfgMu.Lock()
	b.cfg = cfg
	b.cfgMu.Unlock()
}

func (b *SandboxBridge) config() Config {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.cfg
}

func (b *SandboxBridge) sessionStateLocked() map[string]any {
	var exchange any
	if b.exchange != "" {
		exchange = resolvePath(b.exchange)
	}
	return map[string]any{
		"sandbox_id":     b.sandboxID,
		"exchange":       exchange,
		"pending":        nullableString(b.pending),
		"last_operation": nullableString(b.lastOperation),
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (b *SandboxBridge) save() error {
	b.mu.RLock()
	if b.sandboxID == "" || b.exchange == "" {
		b.mu.RUnlock()
		return nil
	}
	state := b.sessionStateLocked()
	b.mu.RUnlock()
	return writeJSONAtomic(b.stateFile, state)
}

func (b *SandboxBridge) publish(request map[string]any, channel string) error {
	b.mu.RLock()
	exchange := b.exchange
	if exchange == "" {
		b.mu.RUnlock()
		return connectionErrorf("沙盒交换目录未初始化")
	}
	id, _ := request["id"].(string)
	state := b.sessionStateLocked()
	b.mu.RUnlock()

	if err := writeJSONAtomic(filepath.Join(exchange, id+".submitted.json"), request); err != nil {
		return err
	}
	if channel == "request" {
		if err := writeJSONAtomic(b.stateFile, state); err != nil {
			return err
		}
	}
	return writeJSONAtomic(filepath.Join(exchange, id+"."+channel+".json"), request)
}

// restore 仅恢复明确记录为本体创建的实例，不扫描或接管其他沙盒
func (b *SandboxBridge) restore() (bool, error) {
	if !fileExists(b.stateFile) {
		return false, nil
	}
	state, err := readJSONMap(b.stateFile)
	if err != nil {
		return false, err
	}
	sandboxID, _ := state["sandbox_id"].(string)
	exchange, _ := state["exchange"].(string)
	pending, _ := state["pending"].(string)
	lastOperation, _ := state["last_operation"].(string)
	if exchange == "" {
		return false, fmt.Errorf("记录的沙盒通信目录为空")
	}
	absoluteExchange := resolvePath(exchange)
	if !pathWithin(b.runtimeDir, absoluteExchange) {
		return false, fmt.Errorf("记录的沙盒通信目录不属于本实例")
	}
	b.mu.Lock()
	b.sandboxID = sandboxID
	b.exchange = absoluteExchange
	b.pending = pending
	b.lastOperation = lastOperation
	b.mu.Unlock()

	environments, err := b.command("list")
	if err != nil {
		return false, err
	}
	running, err := sandboxIDInList(environments, sandboxID)
	if err != nil {
		return false, err
	}
	if !running {
		b.mu.Lock()
		b.connected = false
		b.observedRunning = false
		b.mu.Unlock()
		return false, connectionErrorf("记录的沙盒已关闭；在 WebUI 重建沙盒 创建新桌面，工作目录仍保留。")
	}
	ready, err := b.waitJSON(filepath.Join(absoluteExchange, "ready.json"), b.config().StartupTimeout)
	if err != nil {
		return false, err
	}
	if protocol, _ := numberValue(ready["protocol"]); int(protocol) != guestProtocol {
		return false, connectionErrorf("原沙盒代理版本不支持恢复，请在 WebUI 重建沙盒 更新代理。")
	}
	if pending != "" {
		claimed := false
		for _, suffix := range []string{"request", "running", "result"} {
			if fileExists(filepath.Join(absoluteExchange, pending+"."+suffix+".json")) {
				claimed = true
				break
			}
		}
		if !claimed {
			submitted, err := readJSONMap(filepath.Join(absoluteExchange, pending+".submitted.json"))
			if err != nil {
				return false, err
			}
			if err := writeJSONAtomic(filepath.Join(absoluteExchange, pending+".request.json"), submitted); err != nil {
				return false, err
			}
		}
	}
	if err := b.connect(); err != nil {
		return false, err
	}
	var lastFrame map[string]any
	if lastOperation != "" {
		frame, err := readJSONMap(filepath.Join(absoluteExchange, lastOperation+".result.json"))
		if err != nil {
			return false, err
		}
		lastFrame = frame
	}
	b.mu.Lock()
	b.connected = true
	b.observedRunning = true
	if lastFrame != nil {
		b.lastFrame = lastFrame
	}
	b.mu.Unlock()
	return true, nil
}

func (b *SandboxBridge) connect() error {
	b.mu.RLock()
	sandboxID := b.sandboxID
	viewer := b.viewer
	b.mu.RUnlock()
	if sandboxID == "" {
		return connectionErrorf("没有可连接的沙盒实例")
	}
	instance := strings.ReplaceAll(sandboxID, "'", "''")
	script := "@(Get-CimInstance Win32_Process -Filter \"Name='WindowsSandboxRemoteSession.exe'\" | " +
		fmt.Sprintf("Where-Object { $_.CommandLine.Contains('%s') }).Count", instance)
	probeCtx, cancel := context.WithTimeout(context.Background(), timeoutDuration(b.config().StartupTimeout))
	defer cancel()
	probe := exec.CommandContext(probeCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	hideWindow(probe)
	output, err := probe.Output()
	if err != nil {
		if probeCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("探测沙盒查看窗口超时")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return err
	}
	count, err := parseProcessCount(output)
	if err != nil {
		return err
	}
	if count == 0 {
		_ = viewer
		if b.cli == "" {
			return fmt.Errorf("没有可用的 wsb.exe，无法连接沙盒桌面")
		}
		connectCmd := exec.Command(b.cli, "connect", "--id", sandboxID)
		hideWindow(connectCmd)
		if err := connectCmd.Start(); err != nil {
			return fmt.Errorf("启动沙盒查看窗口失败: %v", err)
		}
		go func() { _ = connectCmd.Wait() }()
		b.mu.Lock()
		b.viewer = connectCmd
		b.mu.Unlock()
	}
	return nil
}

func parseProcessCount(output []byte) (int, error) {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return 0, nil
	}
	var count int
	if _, err := fmt.Sscanf(text, "%d", &count); err != nil {
		return 0, fmt.Errorf("解析沙盒查看窗口数量失败: %s", text)
	}
	return count, nil
}

func (b *SandboxBridge) running() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sandboxID != "" && b.observedRunning
}

// StatusSnapshot 返回服务层需要的桥状态
type BridgeSnapshot struct {
	SandboxID      string
	Pending        string
	Connected      bool
	SandboxRunning bool
}

func (b *SandboxBridge) snapshot() BridgeSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return BridgeSnapshot{
		SandboxID:      b.sandboxID,
		Pending:        b.pending,
		Connected:      b.connected,
		SandboxRunning: b.sandboxID != "" && b.observedRunning,
	}
}

func (b *SandboxBridge) lastFrameSnapshot() map[string]any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.lastFrame == nil {
		return nil
	}
	out := make(map[string]any, len(b.lastFrame))
	for key, value := range b.lastFrame {
		out[key] = value
	}
	return out
}

func (b *SandboxBridge) command(arguments ...string) (map[string]any, error) {
	if b.cli == "" {
		return nil, connectionErrorf("需要支持 wsb.exe 实例管理的新版 Windows Sandbox；请更新 Windows 并启用沙盒功能。")
	}
	timeout := timeoutDuration(b.config().StartupTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append(append([]string{}, arguments...), "--raw")
	cmd := exec.CommandContext(ctx, b.cli, args...)
	hideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("wsb.exe %s 超时（%.1fs）", strings.Join(arguments, " "), timeout.Seconds())
		}
		text := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		if text == "" {
			text = err.Error()
		}
		return nil, fmt.Errorf("%s", text)
	}
	output := bytes.TrimSpace(stdout.Bytes())
	output = bytes.TrimPrefix(output, []byte{0xEF, 0xBB, 0xBF})
	if len(output) == 0 {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(output, &decoded); err != nil {
		return nil, fmt.Errorf("解析 wsb.exe 输出失败: %w", err)
	}
	return decoded, nil
}

func (b *SandboxBridge) list() (map[string]any, error) {
	return b.command("list")
}

func sandboxIDInList(environments map[string]any, sandboxID string) (bool, error) {
	raw, ok := environments["WindowsSandboxEnvironments"].([]any)
	if !ok {
		return false, fmt.Errorf("解析 wsb.exe 输出失败: 缺少 WindowsSandboxEnvironments")
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return false, fmt.Errorf("解析 wsb.exe 输出失败: 沙盒条目格式无效")
		}
		id, ok := entry["Id"].(string)
		if !ok {
			return false, fmt.Errorf("解析 wsb.exe 输出失败: 沙盒条目缺少 Id")
		}
		if id == sandboxID {
			return true, nil
		}
	}
	return false, nil
}

func sandboxListEmpty(environments map[string]any) (bool, bool) {
	raw, ok := environments["WindowsSandboxEnvironments"].([]any)
	if !ok {
		return false, false
	}
	return len(raw) == 0, true
}

func (b *SandboxBridge) waitJSON(path string, timeoutSeconds float64) (map[string]any, error) {
	var deadline time.Time
	timeout := timeoutDuration(timeoutSeconds)
	if timeoutSeconds > 0 {
		deadline = time.Now().Add(timeout)
	}
	nextProbe := time.Time{}
	agentError := filepath.Join(filepath.Dir(path), "agent-error.txt")
	heartbeat := filepath.Join(filepath.Dir(path), "heartbeat")
	for timeoutSeconds == 0 || time.Now().Before(deadline) {
		b.mu.RLock()
		closing := b.closing
		sandboxID := b.sandboxID
		b.mu.RUnlock()
		if closing {
			return nil, connectionErrorf("沙盒正在关闭")
		}
		if fileExists(path) {
			return readJSONMap(path)
		}
		if sandboxID != "" && (nextProbe.IsZero() || !time.Now().Before(nextProbe)) {
			environments, err := b.list()
			if err != nil {
				return nil, err
			}
			running, err := sandboxIDInList(environments, sandboxID)
			if err != nil {
				return nil, err
			}
			if !running {
				b.mu.Lock()
				b.observedRunning = false
				b.connected = false
				b.mu.Unlock()
				return nil, connectionErrorf("Windows Sandbox 实例已关闭")
			}
			b.mu.Lock()
			b.observedRunning = true
			b.mu.Unlock()
			nextProbe = time.Now().Add(sandboxPollPeriod)
		}
		if fileExists(agentError) {
			raw, err := os.ReadFile(agentError)
			if err != nil {
				return nil, err
			}
			return nil, connectionErrorf("%s", strings.TrimSpace(strings.TrimPrefix(string(raw), "\uFEFF")))
		}
		if info, err := os.Stat(heartbeat); err == nil && time.Since(info.ModTime()) > heartbeatStale {
			b.mu.Lock()
			b.connected = false
			b.mu.Unlock()
			return nil, connectionErrorf("沙盒实例仍存在，但来宾代理心跳已中断；原请求未重放，请检查桌面或重建代理。")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, &TimeoutError{Message: requestTimeoutCode}
}

func (b *SandboxBridge) start(ctx context.Context) error {
	b.startMu.Lock()
	defer b.startMu.Unlock()
	return b.startOwned(ctx)
}

func (b *SandboxBridge) startOwned(ctx context.Context) error {
	b.mu.RLock()
	closing := b.closing
	sandboxID := b.sandboxID
	connected := b.connected
	b.mu.RUnlock()
	if closing {
		return connectionErrorf("沙盒正在关闭")
	}
	if sandboxID != "" {
		if !connected {
			if _, err := b.restore(); err != nil {
				return err
			}
		}
		return nil
	}
	restored, err := b.restore()
	if err != nil {
		return err
	}
	if restored {
		return nil
	}
	if runtime.GOOS != "windows" || b.cli == "" || !fileExists(b.cli) {
		return connectionErrorf("需要支持 wsb.exe 实例管理的新版 Windows Sandbox；请更新 Windows 并启用沙盒功能。")
	}
	environments, err := b.list()
	if err != nil {
		return err
	}
	empty, ok := sandboxListEmpty(environments)
	if !ok {
		return fmt.Errorf("解析 wsb.exe 输出失败: 缺少 WindowsSandboxEnvironments")
	}
	if !empty {
		return connectionErrorf("已有其他 Windows Sandbox 在运行，请先关闭；不会接管其他桌面。")
	}
	session := filepath.Join(b.runtimeDir, randomID())
	bootstrap := filepath.Join(session, "bootstrap")
	exchange := filepath.Join(session, "exchange")
	if err := prepareSession(bootstrap, exchange, b.workspace, b.config()); err != nil {
		return err
	}
	launchPath := filepath.Join(session, "maimai.wsb")
	launchXML := sandboxXML(bootstrap, exchange, b.workspace, b.config())
	if err := os.WriteFile(launchPath, []byte(launchXML), 0o600); err != nil {
		return err
	}
	sandboxID = randomUUID()
	b.mu.Lock()
	b.exchange = exchange
	b.sandboxID = sandboxID
	b.mu.Unlock()
	if err := b.save(); err != nil {
		return err
	}
	if _, err := b.command("start", "--id", sandboxID, "--config", launchXML); err != nil {
		_ = b.closeOwned()
		return err
	}
	if err := b.connect(); err != nil {
		_ = b.closeOwned()
		return err
	}
	ready, err := b.waitJSON(filepath.Join(exchange, "ready.json"), b.config().StartupTimeout)
	if err != nil {
		_ = b.closeOwned()
		return err
	}
	success, _ := ready["success"].(bool)
	if !success {
		message, _ := ready["error"].(string)
		_ = b.closeOwned()
		return connectionErrorf("%s", message)
	}
	b.mu.Lock()
	b.connected = true
	b.observedRunning = true
	b.mu.Unlock()
	return nil
}

func prepareSession(bootstrap, exchange, workspace string, cfg Config) error {
	if err := os.MkdirAll(bootstrap, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(exchange, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(bootstrap, "guest.ps1"), guestScript, 0o600); err != nil {
		return err
	}
	settings, err := json.Marshal(map[string]any{"screenshot_max_edge": cfg.ScreenshotMaxEdge})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bootstrap, "settings.json"), settings, 0o600)
}

// sandboxXML 对齐 bridge.sandbox_xml
type sandboxXMLConfig struct {
	XMLName            xml.Name `xml:"Configuration"`
	Networking         string   `xml:"Networking"`
	ClipboardRedirect  string   `xml:"ClipboardRedirection"`
	AudioInput         string   `xml:"AudioInput"`
	VideoInput         string   `xml:"VideoInput"`
	PrinterRedirection string   `xml:"PrinterRedirection"`
	MemoryInMB         string   `xml:"MemoryInMB"`
	MappedFolders      struct {
		MappedFolder []struct {
			HostFolder    string `xml:"HostFolder"`
			SandboxFolder string `xml:"SandboxFolder"`
			ReadOnly      string `xml:"ReadOnly"`
		} `xml:"MappedFolder"`
	} `xml:"MappedFolders"`
	LogonCommand struct {
		Command string `xml:"Command"`
	} `xml:"LogonCommand"`
}

func sandboxXML(bootstrap, exchange, workspace string, cfg Config) string {
	config := sandboxXMLConfig{
		Networking:         enabledWord(cfg.Networking),
		ClipboardRedirect:  "Disable",
		AudioInput:         "Disable",
		VideoInput:         "Disable",
		PrinterRedirection: "Disable",
		MemoryInMB:         fmt.Sprintf("%d", cfg.MemoryMB),
	}
	for _, item := range []struct {
		source      string
		destination string
		readonly    bool
	}{
		{bootstrap, bootstrapRoot, true},
		{exchange, exchangeRoot, false},
		{workspace, workspaceRoot, false},
	} {
		absolute, err := filepath.Abs(item.source)
		if err != nil {
			absolute = item.source
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		config.MappedFolders.MappedFolder = append(config.MappedFolders.MappedFolder, struct {
			HostFolder    string `xml:"HostFolder"`
			SandboxFolder string `xml:"SandboxFolder"`
			ReadOnly      string `xml:"ReadOnly"`
		}{HostFolder: absolute, SandboxFolder: item.destination, ReadOnly: fmt.Sprintf("%t", item.readonly)})
	}
	config.LogonCommand.Command = `powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "C:\MaiBootstrap\guest.ps1"`
	encoded, err := xml.Marshal(config)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func enabledWord(value bool) string {
	if value {
		return "Enable"
	}
	return "Disable"
}

// perform 对齐 bridge.perform；opMu 串行化输入批次，observe/poll/cancel 不占该锁
func (b *SandboxBridge) perform(ctx context.Context, actions []map[string]any, timeout *float64, beforeSubmit func() error) (map[string]any, error) {
	if len(actions) == 0 {
		return nil, fmt.Errorf("actions 不能为空；观察桌面请使用 computer_observe，查询操作请使用 computer_poll。")
	}
	if err := ValidateActions(actions); err != nil {
		return nil, err
	}
	b.opMu.Lock()
	defer b.opMu.Unlock()
	if beforeSubmit != nil {
		if err := beforeSubmit(); err != nil {
			return nil, err
		}
	}
	if err := b.start(ctx); err != nil {
		return nil, err
	}
	if beforeSubmit != nil {
		if err := beforeSubmit(); err != nil {
			return nil, err
		}
	}
	b.mu.RLock()
	exchange := b.exchange
	pending := b.pending
	b.mu.RUnlock()
	if exchange == "" {
		return nil, connectionErrorf("沙盒交换目录未初始化")
	}
	if pending != "" {
		return nil, fmt.Errorf("上一条操作尚未领取结果；请调用 computer_poll(operation_id=%s)。", pending)
	}
	operationID := randomID()
	b.mu.Lock()
	b.pending = operationID
	b.mu.Unlock()
	request := map[string]any{"id": operationID, "actions": actions}
	if err := b.publish(request, "request"); err != nil {
		return nil, err
	}
	return b.collect(ctx, operationID, timeout)
}

func (b *SandboxBridge) collect(ctx context.Context, operationID string, timeout *float64) (map[string]any, error) {
	b.mu.RLock()
	exchange := b.exchange
	b.mu.RUnlock()
	if exchange == "" {
		return nil, connectionErrorf("沙盒交换目录未初始化")
	}
	waitSeconds := b.config().RequestTimeout
	if timeout != nil {
		waitSeconds = *timeout
	}
	result, err := b.waitJSON(filepath.Join(exchange, operationID+".result.json"), waitSeconds)
	if err != nil {
		if isTimeoutError(err) {
			return map[string]any{
				"success":      true,
				"status":       "running",
				"operation_id": operationID,
				"message":      "原操作仍在执行，使用 computer_poll 查询；没有重放输入。",
			}, nil
		}
		return nil, err
	}
	id, _ := result["id"].(string)
	if id != operationID {
		return nil, connectionErrorf("沙盒响应 ID 不匹配")
	}
	b.mu.Lock()
	clearPending := b.pending == operationID
	if clearPending {
		b.pending = ""
	}
	_, hasImage := result["image"]
	if hasImage {
		b.lastFrame = cloneAnyMap(result)
		b.lastOperation = operationID
	}
	b.mu.Unlock()
	if clearPending || hasImage {
		if err := b.save(); err != nil {
			return nil, err
		}
	}
	result["operation_id"] = operationID
	if _, ok := result["status"]; !ok {
		success, _ := result["success"].(bool)
		if success {
			result["status"] = "completed"
		} else {
			result["status"] = "failed"
		}
	}
	return result, nil
}

func (b *SandboxBridge) poll(ctx context.Context, operationID string, timeout *float64) (map[string]any, error) {
	if !operationIDPattern.MatchString(operationID) {
		return nil, fmt.Errorf("无效的操作 ID")
	}
	if err := b.start(ctx); err != nil {
		return nil, err
	}
	b.mu.RLock()
	exchange := b.exchange
	b.mu.RUnlock()
	if exchange == "" {
		return nil, connectionErrorf("沙盒交换目录未初始化")
	}
	if !fileExists(filepath.Join(exchange, operationID+".submitted.json")) {
		return nil, fmt.Errorf("该操作不属于当前沙盒实例")
	}
	return b.collect(ctx, operationID, timeout)
}

func (b *SandboxBridge) observe(ctx context.Context, timeout *float64) (map[string]any, error) {
	if err := b.start(ctx); err != nil {
		return nil, err
	}
	b.mu.RLock()
	exchange := b.exchange
	b.mu.RUnlock()
	if exchange == "" {
		return nil, connectionErrorf("沙盒交换目录未初始化")
	}
	operationID := randomID()
	request := map[string]any{"id": operationID, "actions": []map[string]any{{"action": "observe"}}}
	if err := b.publish(request, "observe"); err != nil {
		return nil, err
	}
	return b.collect(ctx, operationID, timeout)
}

func (b *SandboxBridge) cancel(ctx context.Context, operationID string) (map[string]any, error) {
	b.mu.RLock()
	pending := b.pending
	b.mu.RUnlock()
	if operationID != pending {
		quick := 0.01
		return b.poll(ctx, operationID, &quick)
	}
	b.mu.RLock()
	exchange := b.exchange
	b.mu.RUnlock()
	if exchange == "" {
		return nil, connectionErrorf("沙盒交换目录未初始化")
	}
	if err := os.WriteFile(filepath.Join(exchange, operationID+".cancel"), nil, 0o600); err != nil {
		return nil, err
	}
	wait := 5.0
	return b.poll(ctx, operationID, &wait)
}

// detach 麦麦退出仅断开执行器，保留桌面、后台程序和原请求
func (b *SandboxBridge) detach() error {
	b.mu.Lock()
	b.closing = true
	b.mu.Unlock()
	b.opMu.Lock()
	defer b.opMu.Unlock()
	defer func() {
		b.mu.Lock()
		b.closing = false
		b.mu.Unlock()
	}()
	if err := b.save(); err != nil {
		return err
	}
	b.mu.Lock()
	b.connected = false
	b.viewer = nil
	b.mu.Unlock()
	return nil
}

// close 先阻止新请求，再关闭自己拥有的实例
func (b *SandboxBridge) close() error {
	b.mu.Lock()
	b.closing = true
	b.mu.Unlock()
	b.opMu.Lock()
	err := b.closeOwned()
	b.opMu.Unlock()
	b.mu.Lock()
	b.closing = false
	b.mu.Unlock()
	return err
}

func (b *SandboxBridge) closeOwned() error {
	b.mu.RLock()
	sandboxID := b.sandboxID
	b.mu.RUnlock()
	if sandboxID == "" && fileExists(b.stateFile) {
		state, err := readJSONMap(b.stateFile)
		if err != nil {
			return err
		}
		sandboxID, _ = state["sandbox_id"].(string)
	}
	if sandboxID != "" {
		environments, err := b.list()
		if err != nil {
			return err
		}
		running, err := sandboxIDInList(environments, sandboxID)
		if err != nil {
			return err
		}
		if running {
			if _, err := b.command("stop", "--id", sandboxID); err != nil {
				return err
			}
		}
	}
	b.mu.Lock()
	viewer := b.viewer
	b.mu.Unlock()
	if viewer != nil && viewer.Process != nil {
		_ = viewer.Process.Kill()
	}
	b.mu.Lock()
	b.sandboxID = ""
	b.exchange = ""
	b.pending = ""
	b.lastFrame = nil
	b.lastOperation = ""
	b.connected = false
	b.observedRunning = false
	b.viewer = nil
	b.mu.Unlock()
	if err := os.Remove(b.stateFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d%08x", time.Now().UnixNano(), time.Now().Nanosecond())
	}
	return fmt.Sprintf("%x", raw)
}

// randomUUID 对齐 Python str(uuid4())，沙盒实例 ID 使用带连字符的 UUID
func randomUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		now := time.Now()
		return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x",
			now.UnixNano()&0xffffffff, now.Nanosecond()&0xffff,
			now.UnixNano()>>16&0xfff, now.Nanosecond()>>12&0xfff, now.UnixNano()>>32&0xffffffffffff)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func timeoutDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(seconds * float64(time.Second))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func resolvePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return absolute
}

func pathWithin(root, target string) bool {
	absoluteRoot := resolvePath(root)
	relative, err := filepath.Rel(absoluteRoot, resolvePath(target))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func readJSONMap(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func writeJSONAtomic(path string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		if err := os.Rename(name, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if runtime.GOOS != "windows" {
			break
		}
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
	}
	return fmt.Errorf("原子提交状态文件失败: %s -> %s: %w", name, path, lastErr)
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
