package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ToolResult 工具执行结果；ImageData 为 base64，MIMEType 形如 image/png
type ToolResult struct {
	Text       string
	Structured any
	Images     []ToolImage
	IsError    bool
}

// ToolImage 工具返回的图片内容
type ToolImage struct {
	Data     string
	MIMEType string
}

// ToolDefinition 对外暴露的 MCP 工具；Handler 由提供者实现
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, arguments map[string]any) (ToolResult, error)
}

// ToolProvider 对齐 Python ToolProvider
type ToolProvider interface {
	ProviderName() string
	Tools() []ToolDefinition
}

// AgentToolHost 供 agent_* 工具回调宿主；未接线时工具返回真实错误
type AgentToolHost interface {
	TellAgent(ctx context.Context, text string, target string, images []string) (map[string]any, error)
	ListAgentChats(ctx context.Context) ([]map[string]any, error)
	RecentAgentMessages(ctx context.Context, target string, limit int) (map[string]any, error)
}

// ToolMCPServer 对齐 mcp_server.ToolMCPServer：本机 Streamable HTTP + Bearer 认证
type ToolMCPServer struct {
	server     *mcpserver.MCPServer
	httpServer *mcpserver.StreamableHTTPServer
	listener   net.Listener
	token      string
	path       string
	mu         sync.Mutex
	closed     bool
}

// NewToolMCPServer 注册工具；空列表也允许，便于真实报告未接线的能力
func NewToolMCPServer(providers []ToolProvider) *ToolMCPServer {
	s := mcpserver.NewMCPServer("maibot-agent", "1.0.0")
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		for _, tool := range provider.Tools() {
			def := tool
			s.AddTool(mcp.Tool{
				Name:        def.Name,
				Description: def.Description,
				InputSchema: toolInputSchema(def.InputSchema),
			}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				result, err := def.Handler(ctx, request.GetArguments())
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return toolResultToMCP(result)
			})
		}
	}
	return &ToolMCPServer{server: s, path: "/mcp"}
}

func toolInputSchema(raw map[string]any) mcp.ToolInputSchema {
	schema := mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}}
	if raw == nil {
		return schema
	}
	if value, ok := raw["type"].(string); ok && value != "" {
		schema.Type = value
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		schema.Properties = props
	}
	if required, ok := raw["required"].([]any); ok {
		for _, item := range required {
			if name, ok := item.(string); ok {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	if required, ok := raw["required"].([]string); ok {
		schema.Required = append(schema.Required, required...)
	}
	if extra, ok := raw["additionalProperties"].(bool); ok && !extra {
		schema.AdditionalProperties = false
	}
	return schema
}

func toolResultToMCP(result ToolResult) (*mcp.CallToolResult, error) {
	text := result.Text
	if text == "" && result.Structured != nil {
		raw, err := sonic.ConfigStd.Marshal(result.Structured)
		if err != nil {
			return nil, err
		}
		text = string(raw)
	}
	out := mcp.NewToolResultText(text)
	if result.Structured != nil {
		out.StructuredContent = result.Structured
	}
	for _, image := range result.Images {
		out.Content = append(out.Content, mcp.NewImageContent(image.Data, image.MIMEType))
	}
	if result.IsError {
		out.IsError = true
	}
	return out, nil
}

// Start 监听 127.0.0.1；返回可供 mount 的描述
func (s *ToolMCPServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("acpagent: MCP 工具服务已启动")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("ACP 工具服务启动失败: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return err
	}
	s.token = token
	s.listener = listener
	s.closed = false
	s.httpServer = mcpserver.NewStreamableHTTPServer(s.server,
		mcpserver.WithStateLess(true),
		mcpserver.WithEndpointPath(s.path),
	)
	mux := http.NewServeMux()
	mux.Handle(s.path, s)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"maibot-agent","transport":"streamable-http","endpoint":"/mcp"}`))
	})
	go func() {
		_ = http.Serve(listener, mux)
	}()
	return nil
}

// Port 返回实际监听端口
func (s *ToolMCPServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return 0
	}
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Mount 返回 ACP session/new 的 mcpServers 配置
func (s *ToolMCPServer) Mount() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	port := 0
	if s.listener != nil {
		port = s.listener.Addr().(*net.TCPAddr).Port
	}
	return map[string]any{
		"type": "http",
		"name": "maibot-agent",
		"url":  fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		"headers": []any{
			map[string]any{"name": "Authorization", "value": "Bearer " + s.token},
		},
	}
}

// Close 关闭监听；对齐 Python close 的 should_exit + 有限等待
func (s *ToolMCPServer) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	listener := s.listener
	httpServer := s.httpServer
	s.listener = nil
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if httpServer != nil {
		wait, cancel := context.WithTimeout(ctx, time.Second)
		_ = httpServer.Shutdown(wait)
		cancel()
	}
	s.mu.Lock()
	if s.httpServer == httpServer {
		s.httpServer = nil
	}
	s.mu.Unlock()
	if listener != nil {
		return listener.Close()
	}
	return nil
}

func (s *ToolMCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="maibot-agent"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.httpServer == nil {
		http.Error(w, "MCP 工具服务未启动", http.StatusServiceUnavailable)
		return
	}
	s.httpServer.ServeHTTP(w, r)
}

func (s *ToolMCPServer) authorized(r *http.Request) bool {
	s.mu.Lock()
	expected := s.token
	s.mu.Unlock()
	if expected == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	given := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("ACP 工具服务认证令牌生成失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func randomRecordID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
