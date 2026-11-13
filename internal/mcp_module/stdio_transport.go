package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

const stdioTerminationGrace = 2 * time.Second

// tolerantStdioTransport MCP stdio 传输，使用容错 stdout 过滤并自管子进程生命周期
//
// 对齐 MaiBot stdio_filter.tolerant_stdio_client：协议处理完全复用 mcp-go 的 Stdio，
// 差异只在 stdout 先经过 tolerantReader 过滤
type tolerantStdioTransport struct {
	cfg    ServerConfig
	inner  *transport.Stdio
	cmd    *exec.Cmd
	mu     sync.Mutex
	closed bool

	pendingNotification func(notification mcpprotocol.JSONRPCNotification)
	pendingRequest      transport.RequestHandler
}

func newTolerantStdioTransport(cfg ServerConfig) *tolerantStdioTransport {
	return &tolerantStdioTransport{cfg: cfg}
}

func (t *tolerantStdioTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.inner != nil {
		t.mu.Unlock()
		return nil
	}
	if err := t.startProcess(ctx); err != nil {
		t.mu.Unlock()
		return err
	}
	inner := t.inner
	notification := t.pendingNotification
	request := t.pendingRequest
	t.mu.Unlock()

	if notification != nil {
		inner.SetNotificationHandler(notification)
	}
	if request != nil {
		inner.SetRequestHandler(request)
	}
	return inner.Start(ctx)
}

func (t *tolerantStdioTransport) startProcess(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	command := strings.TrimSpace(t.cfg.Command)
	if command == "" {
		return fmt.Errorf("stdio 服务器 %q 缺少 command 配置", t.cfg.Name)
	}
	cmd := exec.CommandContext(ctx, command, t.cfg.Args...)
	cmd.Env = mergedEnv(os.Environ(), t.cfg.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建 MCP stdio stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("创建 MCP stdio stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("创建 MCP stdio stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("启动 MCP stdio 服务器 %q 失败: %w", t.cfg.Name, err)
	}
	t.cmd = cmd
	t.inner = transport.NewIO(newTolerantReader(stdout), stdin, stderr)
	return nil
}

func (t *tolerantStdioTransport) SendRequest(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	inner := t.innerTransport()
	if inner == nil {
		return nil, fmt.Errorf("MCP stdio 传输尚未启动")
	}
	return inner.SendRequest(ctx, request)
}

func (t *tolerantStdioTransport) SendNotification(ctx context.Context, notification mcpprotocol.JSONRPCNotification) error {
	inner := t.innerTransport()
	if inner == nil {
		return fmt.Errorf("MCP stdio 传输尚未启动")
	}
	return inner.SendNotification(ctx, notification)
}

func (t *tolerantStdioTransport) SetNotificationHandler(handler func(notification mcpprotocol.JSONRPCNotification)) {
	t.mu.Lock()
	inner := t.inner
	if inner == nil {
		t.pendingNotification = handler
	}
	t.mu.Unlock()
	if inner != nil {
		inner.SetNotificationHandler(handler)
	}
}

func (t *tolerantStdioTransport) SetRequestHandler(handler transport.RequestHandler) {
	t.mu.Lock()
	inner := t.inner
	if inner == nil {
		t.pendingRequest = handler
	}
	t.mu.Unlock()
	if inner != nil {
		inner.SetRequestHandler(handler)
	}
}

func (t *tolerantStdioTransport) GetSessionId() string { return "" }

func (t *tolerantStdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	inner := t.inner
	cmd := t.cmd
	t.mu.Unlock()

	var closeErr error
	if inner != nil {
		closeErr = inner.Close()
	}
	if cmd != nil && cmd.Process != nil {
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()
		select {
		case err := <-waitCh:
			if err != nil && closeErr == nil {
				closeErr = err
			}
		case <-time.After(stdioTerminationGrace):
			if err := cmd.Process.Kill(); err != nil {
				zap.L().Warn("终止 MCP stdio 服务器失败", zap.String("server", t.cfg.Name), zap.Error(err))
			}
			<-waitCh
		}
	}
	return closeErr
}

func (t *tolerantStdioTransport) innerTransport() *transport.Stdio {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inner
}

// mergedEnv 把默认环境与 KEY=VALUE 覆盖项合并，后者覆盖前者
func mergedEnv(base []string, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	add := func(item string) {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, item := range base {
		add(item)
	}
	for _, item := range overrides {
		add(item)
	}
	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}
