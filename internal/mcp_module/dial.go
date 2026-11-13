package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
)

// newMCPClientForConfig 按配置创建 MCP 客户端，测试可替换为进程内 transport
var newMCPClientForConfig = newMCPClient

// newMCPClient 按配置创建 MCP 客户端，调用方负责 Start、Initialize 与 Close
func newMCPClient(cfg ServerConfig) (*client.Client, error) {
	headers := cfg.BuildHTTPHeaders()
	switch cfg.TransportType() {
	case "sse":
		options := []transport.ClientOption{
			transport.WithHeaders(headers),
			transport.WithHTTPClient(newHTTPClient(cfg, 0)),
			transport.WithEndpointTimeout(cfg.HTTPTimeout()),
			transport.WithResponseTimeout(cfg.ReadTimeout()),
		}
		sseTransport, err := transport.NewSSE(cfg.URL, options...)
		if err != nil {
			return nil, fmt.Errorf("创建 SSE 客户端失败: %w", err)
		}
		return client.NewClient(sseTransport), nil
	case "streamable_http":
		options := []transport.StreamableHTTPCOption{
			transport.WithHTTPHeaders(headers),
			transport.WithHTTPBasicClient(newHTTPClient(cfg, cfg.ReadTimeout())),
		}
		httpTransport, err := transport.NewStreamableHTTP(cfg.URL, options...)
		if err != nil {
			return nil, fmt.Errorf("创建 Streamable HTTP 客户端失败: %w", err)
		}
		return client.NewClient(httpTransport), nil
	case "stdio":
		return client.NewClient(newTolerantStdioTransport(cfg)), nil
	default:
		return nil, fmt.Errorf("不支持的 MCP 服务器类型: %s", cfg.Type)
	}
}

// newHTTPClient 构建带连接/读超时的 HTTP 客户端
//
// overall 为 0 时表示长连接（SSE 流），读超时由传输层的 endpoint/response 超时控制
func newHTTPClient(cfg ServerConfig, overall time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: cfg.HTTPTimeout(), KeepAlive: 30 * time.Second}
	httpTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   cfg.HTTPTimeout(),
		ResponseHeaderTimeout: cfg.HTTPTimeout(),
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          32,
	}
	// 对齐 Python src/mcp_module/connection.py:330 的 httpx.AsyncClient 默认 follow_redirects=False
	return &http.Client{
		Transport:     httpTransport,
		Timeout:       overall,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// startMCPClient 创建、启动并完成 MCP 握手
func startMCPClient(
	ctx context.Context,
	cfg ServerConfig,
	clientCfg ClientConfig,
	host HostCallbacks,
) (*client.Client, string, mcpprotocol.ServerCapabilities, error) {
	clientCfg = clientCfg.Normalize()
	cli, err := newMCPClientForConfig(cfg)
	if err != nil {
		return nil, "", mcpprotocol.ServerCapabilities{}, newError(
			ErrorKindOf(err), cfg.Name, "connect", err.Error(), err)
	}
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, "", mcpprotocol.ServerCapabilities{}, newError(
			ErrorKindOf(err), cfg.Name, "connect",
			fmt.Sprintf("启动 MCP 客户端失败: %v", err), err)
	}

	// 必须在 Start 之后、Initialize 之前接管反向请求，才能按配置完整声明宿主能力
	installHostRequestHandler(cli, clientCfg, host)
	installNotificationHandler(cli, host)
	logHostCapabilityGaps(clientCfg, host)

	initRequest := mcpprotocol.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcpprotocol.LATEST_PROTOCOL_VERSION
	initRequest.Params.Capabilities = host.BuildClientCapabilities(clientCfg)
	initRequest.Params.ClientInfo = mcpprotocol.Implementation{
		Name:    clientCfg.ClientName,
		Version: clientCfg.ClientVersion,
	}
	initResult, err := cli.Initialize(ctx, initRequest)
	if err != nil {
		_ = cli.Close()
		return nil, "", mcpprotocol.ServerCapabilities{}, newError(
			ErrorKindOf(err), cfg.Name, "initialize",
			fmt.Sprintf("初始化 MCP 连接失败: %v", err), err)
	}
	protocolVersion := ""
	capabilities := mcpprotocol.ServerCapabilities{}
	if initResult != nil {
		protocolVersion = initResult.ProtocolVersion
		capabilities = initResult.Capabilities
	}
	return cli, protocolVersion, capabilities, nil
}

// normalizeTransport 归一化传输类型别名
func normalizeTransport(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "stdio":
		return "stdio"
	case "sse":
		return "sse"
	case "streamable_http", "streamable-http", "streamablehttp", "http":
		return "streamable_http"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}
