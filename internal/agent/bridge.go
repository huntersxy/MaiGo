package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const stderrMaxLines = 20

// UpdateHandler 对齐 ACPConnection 的 session/update 回调
type UpdateHandler func(ctx context.Context, params map[string]any)

// RequestError 对端返回 JSON-RPC error
type RequestError struct {
	Body map[string]any
}

func (e *RequestError) Error() string {
	return "ACP: " + fmt.Sprint(e.Body)
}

type pendingReply struct {
	ch chan replyResult
}

type replyResult struct {
	result any
	err    error
}

// Bridge 对齐 acp.ACPConnection：NDJSON stdio 或 TCP
type Bridge struct {
	onUpdate UpdateHandler

	mu       sync.Mutex
	writeMu  sync.Mutex
	running  bool
	failure  error
	sequence int
	pending  map[int]pendingReply

	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
	conn   io.Closer

	stderrLines []string

	readCancel context.CancelFunc
	readDone   chan struct{}
	recvCh     chan map[string]any
}

// NewBridge 创建未连接的桥
func NewBridge() *Bridge {
	return &Bridge{
		pending: make(map[int]pendingReply),
		recvCh:  make(chan map[string]any, 8),
	}
}

// SetUpdateHandler 注册 session/update 回调
func (b *Bridge) SetUpdateHandler(h UpdateHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onUpdate = h
}

// Connected 读写泵仍在运行且未记录致命失败
func (b *Bridge) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running && b.failure == nil
}

// Failure 返回读循环记录的致命错误
func (b *Bridge) Failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failure
}

// Start 在 DialStdio/DialTCP 之后显式启动读循环；Dial* 已自动 Start
func (b *Bridge) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return nil
	}
	if b.stdout == nil {
		return ErrNotConnected
	}
	b.startReadLocked()
	return nil
}

// Stop 关闭连接并等待子进程退出
func (b *Bridge) Stop(ctx context.Context, timeoutSec float64) error {
	b.mu.Lock()
	if !b.running && b.stdin == nil && b.cmd == nil && b.conn == nil {
		b.mu.Unlock()
		return nil
	}
	stdin := b.stdin
	stdout := b.stdout
	cmd := b.cmd
	conn := b.conn
	readCancel := b.readCancel
	readDone := b.readDone
	b.stdin = nil
	b.stdout = nil
	b.cmd = nil
	b.conn = nil
	b.running = false
	b.readCancel = nil
	b.readDone = nil
	b.mu.Unlock()

	// 先关闭 IO，避免读循环阻塞在 ReadFrame 导致无法退出
	if conn != nil {
		_ = conn.Close()
	} else {
		if stdin != nil {
			_ = stdin.Close()
		}
		if stdout != nil {
			_ = stdout.Close()
		}
	}
	if readCancel != nil {
		readCancel()
	}
	if readDone != nil {
		<-readDone
	}

	waitCtx := ctx
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec*float64(time.Second)))
		defer cancel()
	}

	if cmd != nil && cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-waitCtx.Done():
			_ = terminateBridgeProcess(cmd)
		case <-done:
		}
	}
	b.mu.Lock()
	b.failPendingLocked(errors.New("ACP 连接已关闭"))
	b.mu.Unlock()
	return nil
}

// Send 写入一条 JSON-RPC 帧
func (b *Bridge) Send(ctx context.Context, message map[string]any) error {
	_ = ctx
	b.mu.Lock()
	fail := b.failure
	stdin := b.stdin
	b.mu.Unlock()
	if fail != nil {
		return fail
	}
	if stdin == nil {
		return ErrNotConnected
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return WriteFrame(stdin, message)
}

// Receive 等待读循环投递的非响应帧（通知或未识别的消息），带 ctx 超时
func (b *Bridge) Receive(ctx context.Context) (map[string]any, error) {
	select {
	case msg := <-b.recvCh:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Request 对齐 request(method, params, timeout)
func (b *Bridge) Request(ctx context.Context, method string, params map[string]any, timeoutSec float64) (any, error) {
	b.mu.Lock()
	if b.failure != nil {
		err := b.failure
		b.mu.Unlock()
		return nil, err
	}
	b.sequence++
	id := b.sequence
	ch := make(chan replyResult, 1)
	b.pending[id] = pendingReply{ch: ch}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	if params == nil {
		params = map[string]any{}
	}
	if err := b.Send(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}

	waitCtx := ctx
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec*float64(time.Second)))
		defer cancel()
	}

	select {
	case rep := <-ch:
		if rep.err != nil {
			return nil, rep.err
		}
		return rep.result, nil
	case <-waitCtx.Done():
		details := b.StderrTail()
		if details != "" {
			return nil, fmt.Errorf("ACP %s 超时（%.g 秒），请求未重试\n外部进程 stderr：\n%s", method, timeoutSec, details)
		}
		return nil, fmt.Errorf("ACP %s 超时（%.g 秒），请求未重试", method, timeoutSec)
	}
}

// Notify 发送 JSON-RPC 通知
func (b *Bridge) Notify(ctx context.Context, method string, params map[string]any) error {
	if params == nil {
		params = map[string]any{}
	}
	return b.Send(ctx, map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// Initialize 对齐 initialize 握手；需已 Dial 或通过内部 dialFromConfig
func (b *Bridge) Initialize(ctx context.Context, cfg Config, cwd string, clientName string, mcpPort *int) (map[string]any, error) {
	if !b.Connected() {
		if err := b.dialFromConfig(ctx, cfg, cwd, mcpPort); err != nil {
			return nil, err
		}
	}
	raw, err := b.Request(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo":      map[string]any{"name": clientName, "version": "1"},
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}, cfg.ACPTimeoutSec)
	if err != nil {
		return nil, err
	}
	initialized, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ACP initialize 返回类型无效")
	}
	if pv, _ := initialized["protocolVersion"].(float64); int(pv) != 1 {
		return nil, fmt.Errorf("外部 Agent 未协商 ACP v1")
	}
	capabilities, _ := initialized["agentCapabilities"].(map[string]any)
	mcpCaps, _ := capabilities["mcpCapabilities"].(map[string]any)
	if http, _ := mcpCaps["http"].(bool); !http {
		return nil, fmt.Errorf("外部 Agent 未声明 HTTP MCP 能力，无法挂载麦麦工具")
	}
	return capabilities, nil
}

// OpenSession 对齐 session/new 或 session/resume
func (b *Bridge) OpenSession(ctx context.Context, cwd string, mount map[string]any, sessionID string, timeoutSec float64) (map[string]any, error) {
	method := "session/new"
	params := map[string]any{"cwd": cwd, "mcpServers": []any{mount}}
	if sessionID != "" {
		method = "session/resume"
		params["sessionId"] = sessionID
	}
	raw, err := b.Request(ctx, method, params, timeoutSec)
	if err != nil {
		return nil, err
	}
	out, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ACP %s 返回类型无效", method)
	}
	return out, nil
}

// Close 兼容旧 API，委托 Stop
func (b *Bridge) Close(ctx context.Context) error {
	return b.Stop(ctx, 0)
}

// StderrTail 最近 stderr 行，用于超时诊断
func (b *Bridge) StderrTail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.stderrLines) == 0 {
		return ""
	}
	return stringsJoinLines(b.stderrLines)
}

func (b *Bridge) startReadLocked() {
	if b.running {
		return
	}
	readCtx, cancel := context.WithCancel(context.Background())
	b.readCancel = cancel
	b.readDone = make(chan struct{})
	b.running = true
	go b.readLoop(readCtx)
}

func (b *Bridge) readLoop(ctx context.Context) {
	defer close(b.readDone)
	reader := bufio.NewReader(b.stdout)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		message, err := ReadFrame(reader)
		if err != nil {
			b.mu.Lock()
			if b.failure == nil {
				if errors.Is(err, io.EOF) {
					b.failure = fmt.Errorf("ACP 连接已关闭\n%s", stringsJoinLines(b.stderrLines))
				} else {
					b.failure = err
				}
				b.failPendingLocked(b.failure)
			}
			b.mu.Unlock()
			return
		}
		b.dispatch(ctx, message)
	}
}

func (b *Bridge) dispatch(ctx context.Context, message map[string]any) {
	if method, ok := message["method"].(string); ok {
		if id, hasID := message["id"]; hasID {
			b.handleServerRequest(ctx, message, method, id)
			return
		}
		if method == "session/update" {
			params, _ := message["params"].(map[string]any)
			b.mu.Lock()
			h := b.onUpdate
			b.mu.Unlock()
			if h != nil {
				h(ctx, params)
			}
			return
		}
		b.tryRecv(message)
		return
	}
	if idRaw, ok := message["id"]; ok {
		id := intFromJSON(idRaw)
		b.mu.Lock()
		p, ok := b.pending[id]
		b.mu.Unlock()
		if !ok {
			b.tryRecv(message)
			return
		}
		if errObj, ok := message["error"]; ok {
			errMap, _ := errObj.(map[string]any)
			p.ch <- replyResult{err: &RequestError{Body: errMap}}
			return
		}
		p.ch <- replyResult{result: message["result"]}
	}
}

func (b *Bridge) handleServerRequest(ctx context.Context, message map[string]any, method string, id any) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	switch method {
	case "session/request_permission":
		params, _ := message["params"].(map[string]any)
		options, _ := params["options"].([]any)
		var optionID string
		for _, opt := range options {
			m, _ := opt.(map[string]any)
			if m["kind"] == "allow_once" {
				optionID, _ = m["optionId"].(string)
				break
			}
		}
		if optionID != "" {
			resp["result"] = map[string]any{
				"outcome": map[string]any{"outcome": "selected", "optionId": optionID},
			}
		} else {
			resp["result"] = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
		}
	default:
		resp["error"] = map[string]any{
			"code":    -32601,
			"message": "客户端未提供此能力：" + method,
		}
	}
	_ = b.Send(ctx, resp)
}

func (b *Bridge) tryRecv(message map[string]any) {
	select {
	case b.recvCh <- message:
	default:
	}
}

func (b *Bridge) failPendingLocked(err error) {
	for id, p := range b.pending {
		select {
		case p.ch <- replyResult{err: err}:
		default:
		}
		delete(b.pending, id)
	}
}

func (b *Bridge) appendStderr(line string) {
	b.stderrLines = append(b.stderrLines, line)
	if len(b.stderrLines) > stderrMaxLines {
		b.stderrLines = b.stderrLines[len(b.stderrLines)-stderrMaxLines:]
	}
}

func (b *Bridge) dialFromConfig(ctx context.Context, cfg Config, cwd string, mcpPort *int) error {
	switch cfg.Connection {
	case ConnectionDSHDesktop:
		info, err := LoadDesktopBridgeInfo(DesktopManifestPath(cfg))
		if err != nil {
			return err
		}
		addr := fmt.Sprintf("%s:%d", info.Host, info.Port)
		return b.DialTCP(ctx, addr, info.Token, cfg.ACPTimeoutSec)
	default:
		args := cfg.ACPArgs
		if cfg.Connection == ConnectionDSH {
			args = []string{"--profile", "acp"}
		}
		needsPort := false
		for _, arg := range args {
			if strings.Contains(arg, "{mcp_port}") {
				needsPort = true
				break
			}
		}
		if needsPort {
			if mcpPort == nil {
				return fmt.Errorf("ACP 启动参数使用了 {mcp_port}，但工具服务尚未启动")
			}
			replaced := make([]string, len(args))
			portStr := fmt.Sprintf("%d", *mcpPort)
			for i, arg := range args {
				replaced[i] = strings.ReplaceAll(arg, "{mcp_port}", portStr)
			}
			args = replaced
		}
		return b.DialStdio(ctx, cfg.ACPCommand, args, cwd, cfg.ACPEnv)
	}
}

func intFromJSON(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func stringsJoinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for i := 1; i < len(lines); i++ {
		out += "\n" + lines[i]
	}
	return out
}

func drainBridgeStderr(r io.Reader, appendLine func(string)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		appendLine(line)
		zap.L().Info("ACP stderr", zap.String("line", line))
	}
}

func terminateBridgeProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := killBridgeProcessTree(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = cmd.Process.Wait()
	return nil
}
