package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
	"MaiGo/internal/plugin_runtime/transport"
)

// RPCServerOptions Host 端 RPC 服务参数
type RPCServerOptions struct {
	HostVersion  string
	SessionToken string
	// SendQueueSize Runner RPC 发送队列容量，<=0 使用默认值 128
	SendQueueSize int
	// Group 该 RPC 服务所属 Runner 分组，决定 Hook 订阅的 source_rank（builtin=0，其余=1）
	Group RunnerGroup
}

// RPCServer Host 端 RPC 服务器骨架，对齐 plugin_runtime.host.rpc_server.RPCServer
type RPCServer struct {
	host         *Host
	codec        protocol.Codec
	hostVersion  string
	sessionToken string
	runnerGroup  RunnerGroup

	mu          sync.Mutex
	listener    net.Listener
	activeConn  *transport.FramedConn
	running     bool
	stopCh      chan struct{}
	stopOnce    sync.Once
	handshakeMu sync.Mutex
	// lastHandshakeReason 最近一次握手拒绝原因；由 handshakeMu 保护
	lastHandshakeReason string
	wg                  sync.WaitGroup

	sendQueueSize int
	sendMu        sync.Mutex
	sendQueue     chan *rpcSendJob
	sendClosed    bool

	pendingMu sync.Mutex
	pending   map[int64]*rpcPendingRequest
}

// rpcPendingRequest Host 等待 Runner 响应的请求与诊断元数据
type rpcPendingRequest struct {
	ch             chan protocol.Envelope
	method         string
	pluginID       string
	timeoutMS      int64
	startedAt      time.Time
	payloadSummary map[string]any
}

// NewRPCServer 构造 RPC 服务器；sessionToken 为空时自动生成
func NewRPCServer(host *Host, opts RPCServerOptions) (*RPCServer, error) {
	if host == nil {
		return nil, fmt.Errorf("Host 不能为空")
	}
	token := strings.TrimSpace(opts.SessionToken)
	if token == "" {
		var err error
		token, err = newSessionToken()
		if err != nil {
			return nil, err
		}
	}
	hostVersion := strings.TrimSpace(opts.HostVersion)
	if hostVersion == "" {
		hostVersion = "dev"
	}
	group := opts.Group
	if group == "" {
		group = RunnerGroupThirdParty
	}
	return &RPCServer{
		host:          host,
		codec:         host.Codec(),
		hostVersion:   hostVersion,
		sessionToken:  token,
		runnerGroup:   group,
		stopCh:        make(chan struct{}),
		sendQueueSize: normalizeRPCSendQueueSize(opts.SendQueueSize),
		pending:       make(map[int64]*rpcPendingRequest),
	}, nil
}

// HasActiveConnection 是否已有完成 runner.hello 的 Runner 连接
func (s *RPCServer) HasActiveConnection() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeConn != nil
}

// SendRequest 向 Runner 发送 RPC 请求并等待响应
func (s *RPCServer) SendRequest(ctx context.Context, method, pluginID string, payload map[string]any, timeoutMS int64) (protocol.Envelope, error) {
	if s == nil || s.host == nil {
		return protocol.Envelope{}, fmt.Errorf("RPC Server 未初始化")
	}
	s.mu.Lock()
	conn := s.activeConn
	s.mu.Unlock()
	if conn == nil {
		return protocol.Envelope{}, fmt.Errorf("Runner 未连接")
	}
	if timeoutMS <= 0 {
		timeoutMS = 30000
	}
	reqID := s.host.NextRequestID()
	env := protocol.NewRequestEnvelope(reqID, method, pluginID, payload)
	env.TimeoutMS = timeoutMS

	ch := make(chan protocol.Envelope, 1)
	s.pendingMu.Lock()
	s.pending[reqID] = &rpcPendingRequest{
		ch:             ch,
		method:         method,
		pluginID:       pluginID,
		timeoutMS:      timeoutMS,
		startedAt:      time.Now(),
		payloadSummary: summarizeRPCPayload(payload),
	}
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, reqID)
		s.pendingMu.Unlock()
	}()

	if err := s.enqueueSend(ctx, conn, env); err != nil {
		return protocol.Envelope{}, err
	}

	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case resp := <-ch:
		// 错误 envelope 交给调用方按方法语义转换，避免这里提前包上 [CODE] 前缀
		return resp, nil
	case <-ctx.Done():
		return protocol.Envelope{}, ctx.Err()
	case <-timer.C:
		return protocol.Envelope{}, fmt.Errorf("请求 %s 超时 (%dms)", method, timeoutMS)
	}
}

func (s *RPCServer) deliverResponse(env protocol.Envelope) {
	s.pendingMu.Lock()
	pending := s.pending[env.RequestID]
	s.pendingMu.Unlock()
	if pending == nil {
		return
	}
	select {
	case pending.ch <- env:
	default:
	}
}

// PendingRequestSnapshot 返回当前等待 Runner 响应的请求快照，对齐 get_pending_request_snapshot
func (s *RPCServer) PendingRequestSnapshot(minDurationMS int64) []map[string]any {
	if s == nil {
		return nil
	}
	now := time.Now()
	s.pendingMu.Lock()
	out := make([]map[string]any, 0, len(s.pending))
	for id, pending := range s.pending {
		durationMS := now.Sub(pending.startedAt).Milliseconds()
		if durationMS < minDurationMS {
			continue
		}
		item := map[string]any{
			"request_id":       id,
			"method":           pending.method,
			"plugin_id":        pending.pluginID,
			"timeout_ms":       pending.timeoutMS,
			"started_at_epoch": float64(pending.startedAt.UnixNano()) / float64(time.Second),
			"duration_ms":      durationMS,
			"payload_summary":  pending.payloadSummary,
		}
		out = append(out, item)
	}
	s.pendingMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		left, _ := out[i]["duration_ms"].(int64)
		right, _ := out[j]["duration_ms"].(int64)
		return left > right
	})
	return out
}

// AbortPendingRequests 中止全部等待 Runner 响应的请求，对齐 abort_pending_requests
func (s *RPCServer) AbortPendingRequests(message string) int {
	if s == nil {
		return 0
	}
	if strings.TrimSpace(message) == "" {
		message = "服务器正在关闭"
	}
	return s.abortPendingRequests(protocol.ErrorCodeShuttingDown, message)
}

func (s *RPCServer) abortPendingRequests(code protocol.ErrorCode, message string) int {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	count := 0
	for id, pending := range s.pending {
		resp := protocol.Envelope{
			RequestID:   id,
			MessageType: protocol.MessageTypeResponse,
			Error: map[string]any{
				"code":    string(code),
				"message": message,
			},
		}
		select {
		case pending.ch <- resp:
			count++
		default:
		}
		delete(s.pending, id)
	}
	return count
}

func summarizeRPCPayload(payload map[string]any) map[string]any {
	summary := map[string]any{}
	if len(payload) == 0 {
		return summary
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summary["payload_keys"] = keys
	if componentName := strings.TrimSpace(stringPayloadValue(payload["component_name"])); componentName != "" {
		summary["component_name"] = componentName
	}
	if args, ok := payload["args"].(map[string]any); ok {
		argKeys := make([]string, 0, len(args))
		for key := range args {
			argKeys = append(argKeys, key)
		}
		sort.Strings(argKeys)
		summary["args_keys"] = argKeys
		summary["args_count"] = len(args)
	}
	if clientType := strings.TrimSpace(stringPayloadValue(payload["client_type"])); clientType != "" {
		summary["client_type"] = clientType
	}
	if operation := strings.TrimSpace(stringPayloadValue(payload["operation"])); operation != "" {
		summary["operation"] = operation
	}
	return summary
}

func stringPayloadValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

// RunnerGroup 返回该 RPC 服务所属分组，缺省 third_party
func (s *RPCServer) RunnerGroup() RunnerGroup {
	if s == nil || s.runnerGroup == "" {
		return RunnerGroupThirdParty
	}
	return s.runnerGroup
}

// SessionToken 返回握手用会话令牌
func (s *RPCServer) SessionToken() string {
	return s.sessionToken
}

// LastHandshakeRejectionReason 返回最近一次握手拒绝原因
func (s *RPCServer) LastHandshakeRejectionReason() string {
	if s == nil {
		return ""
	}
	s.handshakeMu.Lock()
	defer s.handshakeMu.Unlock()
	return s.lastHandshakeReason
}

// ClearHandshakeState 清空握手拒绝原因
func (s *RPCServer) ClearHandshakeState() {
	if s == nil {
		return
	}
	s.handshakeMu.Lock()
	s.lastHandshakeReason = ""
	s.handshakeMu.Unlock()
}

// ListenAddress 根据地址格式选择 TCP、UDS 或 Named Pipe 并启动 accept 循环
//
// 地址规则对齐 transport.factory.create_transport_client：
//   - 以 \\.\pipe\ 开头 -> Named Pipe（Windows）
//   - 含 ':' -> TCP
//   - 含 '/' 或以 .sock 结尾 -> UDS（仅 Unix）
//   - 空字符串 -> TCP 127.0.0.1:0
func (s *RPCServer) ListenAddress(ctx context.Context, address string) (string, error) {
	req, err := ResolveIPCListenRequest(address)
	if err != nil {
		return "", err
	}
	return s.ListenRequest(ctx, req)
}

// ListenRequest 按 ResolveIPCListenRequest 结果启动监听
func (s *RPCServer) ListenRequest(ctx context.Context, req IPCListenRequest) (string, error) {
	address := strings.TrimSpace(req.Address)
	var (
		ln   net.Listener
		addr string
		err  error
	)
	switch {
	case transport.IsNamedPipeAddress(address) || req.NamedPipe:
		ln, addr, err = transport.ListenNamedPipe(address)
	case address == "" || strings.Contains(address, ":"):
		ln, addr, err = transport.ListenTCP(address)
	case strings.Contains(address, "/") || strings.HasSuffix(address, ".sock"):
		ln, addr, err = transport.ListenUDS(address)
	default:
		return "", fmt.Errorf("无法识别的传输地址格式: %q", address)
	}
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		_ = ln.Close()
		return "", fmt.Errorf("RPC Server 已在运行")
	}
	s.listener = ln
	s.running = true
	s.mu.Unlock()

	s.startSendWorker()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(ctx, ln)
	}()
	return addr, nil
}

// Stop 关闭监听与当前 Runner 连接
func (s *RPCServer) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.stopOnce.Do(func() { close(s.stopCh) })
	ln := s.listener
	conn := s.activeConn
	s.listener = nil
	s.activeConn = nil
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	// 先中止 pending 与排队发送，再断连接，避免调用方白等发送队列
	s.AbortPendingRequests("服务器正在关闭")
	s.failQueuedSends(protocol.ErrorCodeShuttingDown, "服务器正在关闭")
	if conn != nil {
		_ = conn.Close()
	}
	s.wg.Wait()
}

func (s *RPCServer) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
			}
			if s.isStopped() {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *RPCServer) isStopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *RPCServer) serveConn(ctx context.Context, raw net.Conn) {
	framed := transport.NewFramedConn(raw)
	defer func() { _ = raw.Close() }()

	s.handshakeMu.Lock()
	if err := s.handleHandshake(framed); err != nil {
		s.handshakeMu.Unlock()
		return
	}
	s.handshakeMu.Unlock()

	// 连接级 ctx 传给所有异步 handler；连接退出时取消，让在途 SendRequest 与响应发送退出
	connCtx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup

	// 退出顺序对齐 Python _handle_connection：先停止接收并让在途请求可退出，
	// 再清理 activeConn / pending，最后等待 handler 全部结束
	defer func() {
		cancel()
		s.mu.Lock()
		wasActive := s.activeConn == framed
		if wasActive {
			s.activeConn = nil
		}
		s.mu.Unlock()
		if wasActive {
			// 连接断开后立即失败等待中的请求，对齐 rpc_server 的 _fail_pending_requests
			s.abortPendingRequests(protocol.ErrorCodePluginCrashed, "Runner 连接已断开")
		}
		// handler 的发送都带 connCtx，取消后 enqueueSend 会经 ctx.Done 返回，不会永久阻塞
		handlers.Wait()
	}()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		data, err := framed.RecvFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !s.isStopped() {
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					// 连接断开，静默退出
				}
			}
			return
		}

		env, err := s.codec.DecodeEnvelope(data)
		if err != nil {
			continue
		}

		switch {
		case env.IsResponse():
			// response 在接收循环内直接投递，保证 pending 唤醒不被 handler 阻塞
			s.deliverResponse(env)
		case env.IsRequest():
			// request 异步处理，接收循环继续读帧；同一连接上的嵌套 SendRequest 才能收到响应
			handlers.Add(1)
			go func(env protocol.Envelope) {
				defer handlers.Done()
				s.dispatchAsyncRequest(connCtx, framed, env)
			}(env)
		case env.IsBroadcast():
			handlers.Add(1)
			go func(env protocol.Envelope) {
				defer handlers.Done()
				s.dispatchAsyncBroadcast(connCtx, env)
			}(env)
		}
	}
}

func (s *RPCServer) handleHandshake(conn *transport.FramedConn) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	data, err := conn.RecvFrame()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return err
	}

	env, err := s.codec.DecodeEnvelope(data)
	if err != nil {
		s.lastHandshakeReason = "runner.hello 解码失败: " + err.Error()
		return err
	}
	if env.Method != protocol.MethodRunnerHello {
		s.lastHandshakeReason = "首条消息必须为 runner.hello"
		resp := env.MakeErrorResponse(string(protocol.ErrorCodeProtocolMismatch), "首条消息必须为 runner.hello", nil)
		_ = s.sendEnvelope(conn, resp)
		return fmt.Errorf("期望 runner.hello，收到 %q", env.Method)
	}

	hello, err := decodePayload[protocol.HelloPayload](env.Payload)
	if err != nil {
		s.lastHandshakeReason = "runner.hello payload 非法"
		resp := env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "runner.hello payload 非法", nil)
		_ = s.sendEnvelope(conn, resp)
		return err
	}
	if hello.SessionToken != s.sessionToken {
		s.lastHandshakeReason = "会话令牌无效"
		resp := env.MakeResponse(map[string]any{
			"accepted":     false,
			"host_version": "",
			"reason":       "会话令牌无效",
		}, nil)
		_ = s.sendEnvelope(conn, resp)
		return fmt.Errorf("会话令牌无效")
	}

	s.mu.Lock()
	activeConn := s.activeConn
	s.mu.Unlock()
	if activeConn != nil {
		s.lastHandshakeReason = "已有活跃 Runner 连接，拒绝新的握手"
		resp := env.MakeResponse(map[string]any{
			"accepted":     false,
			"host_version": "",
			"reason":       "已有活跃 Runner 连接，拒绝新的握手",
		}, nil)
		_ = s.sendEnvelope(conn, resp)
		return fmt.Errorf("已有活跃连接")
	}

	if !isSupportedSDKVersion(hello.SDKVersion) {
		zap.L().Warn("SDK 版本不在支持范围，已放行",
			zap.String("sdk_version", hello.SDKVersion),
			zap.String("min_sdk_version", protocol.MinSDKVersion),
			zap.String("max_sdk_version", protocol.MaxSDKVersion))
	}

	resp := env.MakeResponse(map[string]any{
		"accepted":     true,
		"host_version": s.hostVersion,
		"reason":       "",
	}, nil)
	s.lastHandshakeReason = ""
	// 先登记连接再回握手响应：Runner 收到 accepted 后会立刻发请求，
	// 反之 Host 侧并发调用可能看到 activeConn 仍为空而误报“Runner 未连接”。
	s.mu.Lock()
	s.activeConn = conn
	s.mu.Unlock()
	if err := s.sendEnvelope(conn, resp); err != nil {
		s.mu.Lock()
		if s.activeConn == conn {
			s.activeConn = nil
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *RPCServer) sendEnvelope(conn *transport.FramedConn, env protocol.Envelope) error {
	data, err := s.codec.EncodeEnvelope(env)
	if err != nil {
		return err
	}
	return conn.SendFrame(data)
}

// isSupportedSDKVersion 检查插件 SDK 版本是否在支持范围内
//
// 对齐 Python rpc_server._check_sdk_version：用三段元组比较，
// 任一版本号解析失败即视为不支持（Python 捕获 ValueError 后返回 False）。
func isSupportedSDKVersion(version string) bool {
	sdk, ok := parseVersionTuple(version)
	if !ok {
		return false
	}
	minimum, ok := parseVersionTuple(protocol.MinSDKVersion)
	if !ok {
		return false
	}
	maximum, ok := parseVersionTuple(protocol.MaxSDKVersion)
	if !ok {
		return false
	}
	return compareVersionTuple(minimum, sdk) <= 0 && compareVersionTuple(sdk, maximum) <= 0
}

func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 session token 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
