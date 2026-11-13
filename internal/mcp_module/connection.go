package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mcptool "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client"
	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// serverConnection 单个 MCP 服务器的连接与能力快照，对齐 MaiBot MCPConnection
type serverConnection struct {
	cfg               ServerConfig
	clientCfg         ClientConfig
	cli               *client.Client
	protocolVersion   string
	capabilities      mcpprotocol.ServerCapabilities
	tools             []tool.BaseTool
	toolDefs          []ToolDefinition
	prompts           []mcpprotocol.Prompt
	resources         []mcpprotocol.Resource
	resourceTemplates []mcpprotocol.ResourceTemplate
}

// connectServer 建立连接、完成握手并按服务端声明发现能力
func connectServer(
	ctx context.Context,
	cfg ServerConfig,
	clientCfg ClientConfig,
	host HostCallbacks,
	discoverExtended bool,
) (*serverConnection, error) {
	transportType := cfg.TransportType()
	if transportType == "unknown" {
		return nil, newError(ErrorKindConnection, cfg.Name, "connect",
			fmt.Sprintf("MCP 服务器 %q 使用了未知传输类型: %s", cfg.Name, cfg.Type), nil)
	}
	cli, protocolVersion, capabilities, err := startMCPClient(ctx, cfg, clientCfg, host)
	if err != nil {
		return nil, err
	}
	conn := &serverConnection{
		cfg:             cfg,
		clientCfg:       clientCfg,
		cli:             cli,
		protocolVersion: protocolVersion,
		capabilities:    capabilities,
	}
	if err := conn.discover(ctx, discoverExtended); err != nil {
		_ = conn.close()
		return nil, err
	}
	return conn, nil
}

func (c *serverConnection) discover(ctx context.Context, discoverExtended bool) error {
	if serverSupportsTools(c.capabilities) {
		toolDefs, err := listAllTools(ctx, c.cli)
		if err != nil {
			return newError(ErrorKindOf(err), c.cfg.Name, "tools/list",
				fmt.Sprintf("获取 MCP 工具失败: %v", err), err)
		}
		c.toolDefs = filterToolDefinitions(toolDefs, c.cfg.ToolNameList)
		baseTools, err := mcptool.GetTools(ctx, &mcptool.Config{
			Cli:          c.cli,
			ToolNameList: c.cfg.ToolNameList,
		})
		if err != nil {
			return newError(ErrorKindOf(err), c.cfg.Name, "tools/list",
				fmt.Sprintf("获取 MCP 工具失败: %v", err), err)
		}
		c.tools = wrapToolTimeout(baseTools, c.cfg.ReadTimeout())
	}
	if !discoverExtended {
		return nil
	}
	if serverSupportsPrompts(c.capabilities) {
		prompts, err := listAllPrompts(ctx, c.cli)
		if err != nil {
			return newError(ErrorKindOf(err), c.cfg.Name, "prompts/list",
				fmt.Sprintf("获取 MCP Prompt 失败: %v", err), err)
		}
		c.prompts = prompts
	}
	if serverSupportsResources(c.capabilities) {
		resources, err := listAllResources(ctx, c.cli)
		if err != nil {
			return newError(ErrorKindOf(err), c.cfg.Name, "resources/list",
				fmt.Sprintf("获取 MCP Resource 失败: %v", err), err)
		}
		c.resources = resources
		templates, err := listAllResourceTemplates(ctx, c.cli)
		if err != nil {
			if !errors.Is(err, mcpprotocol.ErrMethodNotFound) {
				return newError(ErrorKindOf(err), c.cfg.Name, "resources/templates/list",
					fmt.Sprintf("获取 MCP Resource Template 失败: %v", err), err)
			}
			zap.L().Warn("MCP 服务器未实现 list_resource_templates，按空集合处理",
				zap.String("server", c.cfg.Name))
			templates = nil
		}
		c.resourceTemplates = templates
	}
	return nil
}

// GetPrompt 读取指定 Prompt，对齐 MCPConnection.get_prompt
func (c *serverConnection) GetPrompt(ctx context.Context, promptName string, arguments map[string]string) (*PromptResult, error) {
	if c.cli == nil {
		return nil, newError(ErrorKindConnection, c.cfg.Name, "prompts/get",
			fmt.Sprintf("MCP 服务器 %q 未连接", c.cfg.Name), nil)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.ReadTimeout())
	defer cancel()
	request := mcpprotocol.GetPromptRequest{}
	request.Params.Name = promptName
	request.Params.Arguments = arguments
	result, err := c.cli.GetPrompt(callCtx, request)
	if err != nil {
		return nil, newError(ErrorKindOf(err), c.cfg.Name, "prompts/get",
			fmt.Sprintf("MCP Prompt %q 读取失败: %v", promptName, err), err)
	}
	if result == nil {
		return nil, newError(ErrorKindNotFound, c.cfg.Name, "prompts/get",
			fmt.Sprintf("MCP Prompt %q 读取结果为空", promptName), nil)
	}
	converted := buildPromptResult(*result, promptName, c.cfg.Name)
	return &converted, nil
}

// ReadResource 读取指定 Resource，对齐 MCPConnection.read_resource
func (c *serverConnection) ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	if c.cli == nil {
		return nil, newError(ErrorKindConnection, c.cfg.Name, "resources/read",
			fmt.Sprintf("MCP 服务器 %q 未连接", c.cfg.Name), nil)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.ReadTimeout())
	defer cancel()
	request := mcpprotocol.ReadResourceRequest{}
	request.Params.URI = uri
	result, err := c.cli.ReadResource(callCtx, request)
	if err != nil {
		return nil, newError(ErrorKindOf(err), c.cfg.Name, "resources/read",
			fmt.Sprintf("MCP Resource %q 读取失败: %v", uri, err), err)
	}
	if result == nil {
		return nil, newError(ErrorKindNotFound, c.cfg.Name, "resources/read",
			fmt.Sprintf("MCP Resource %q 读取结果为空", uri), nil)
	}
	converted := buildResourceReadResult(*result, uri, c.cfg.Name)
	return &converted, nil
}

// callTool 直接调用 MCP 工具并保留结构化结果，对齐 MCPConnection.call_tool
func (c *serverConnection) callTool(ctx context.Context, toolName string, arguments map[string]any) (ToolExecutionResult, error) {
	if c == nil || c.cli == nil {
		return ToolExecutionResult{
			ToolName:     toolName,
			ServerName:   c.serverName(),
			ErrorMessage: "MCP 服务器未连接",
			ContentItems: []ToolContentItem{},
		}, newError(ErrorKindConnection, c.serverName(), "tools/call",
			"MCP 服务器未连接", nil)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.ReadTimeout())
	defer cancel()
	request := mcpprotocol.CallToolRequest{}
	request.Params.Name = toolName
	request.Params.Arguments = arguments
	result, err := c.cli.CallTool(callCtx, request)
	if err != nil {
		return ToolExecutionResult{
			ToolName:     toolName,
			ServerName:   c.cfg.Name,
			ErrorMessage: fmt.Sprintf("MCP 工具 %q 执行失败: %v", toolName, err),
			ContentItems: []ToolContentItem{},
		}, newError(ErrorKindOf(err), c.cfg.Name, "tools/call",
			fmt.Sprintf("MCP 工具 %q 执行失败: %v", toolName, err), err)
	}
	if result == nil {
		return ToolExecutionResult{
			ToolName:     toolName,
			ServerName:   c.cfg.Name,
			ErrorMessage: "MCP 工具返回为空",
			ContentItems: []ToolContentItem{},
		}, newError(ErrorKindNotFound, c.cfg.Name, "tools/call",
			fmt.Sprintf("MCP 工具 %q 执行结果为空", toolName), nil)
	}
	converted := buildToolExecutionResult(*result, toolName, c.cfg.Name, c.protocolVersion, c.sessionID())
	return converted, nil
}

// buildToolExecutionResult 把 mcp-go 调用结果转换为 MaiBot ToolExecutionResult
func buildToolExecutionResult(
	result mcpprotocol.CallToolResult,
	toolName, serverName, protocolVersion, sessionID string,
) ToolExecutionResult {
	items := buildContentItems(result.Content)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(item.HistoryText()); text != "" {
			parts = append(parts, text)
		}
	}
	history := strings.TrimSpace(strings.Join(parts, "\n"))
	converted := ToolExecutionResult{
		ToolName:          toolName,
		ServerName:        serverName,
		Success:           !result.IsError,
		StructuredContent: result.StructuredContent,
		ContentItems:      items,
		Metadata: map[string]any{
			"server_name":      serverName,
			"protocol_version": protocolVersion,
			"session_id":       sessionID,
		},
	}
	if result.IsError {
		converted.ErrorMessage = history
	} else {
		converted.Content = history
	}
	return converted
}

// watchConnectionLost 注册传输层断连回调；mcp-go 目前仅 SSE 实现该钩子
// 回调带连接指针，便于 Manager 忽略已替换旧连接的迟到断连事件
func (c *serverConnection) watchConnectionLost(handler func(serverName string, conn *serverConnection, err error)) {
	if c == nil || c.cli == nil || handler == nil {
		return
	}
	c.cli.OnConnectionLost(func(err error) {
		handler(c.cfg.Name, c, err)
	})
}

func (c *serverConnection) serverName() string {
	if c == nil {
		return ""
	}
	return c.cfg.Name
}

// sessionID 返回当前传输协商得到的 MCP 会话标识
func (c *serverConnection) sessionID() string {
	if c.cli == nil {
		return ""
	}
	transport := c.cli.GetTransport()
	if transport == nil {
		return ""
	}
	return transport.GetSessionId()
}

func (c *serverConnection) close() error {
	if c.cli == nil {
		return nil
	}
	err := c.cli.Close()
	c.cli = nil
	return err
}

func filterToolDefinitions(defs []mcpprotocol.Tool, whitelist []string) []ToolDefinition {
	allowed := make(map[string]struct{}, len(whitelist))
	for _, name := range whitelist {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	out := make([]ToolDefinition, 0, len(defs))
	for _, raw := range defs {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		out = append(out, buildToolDefinition(raw))
	}
	return out
}

func buildToolDefinition(raw mcpprotocol.Tool) ToolDefinition {
	annotation := buildToolAnnotation(raw.Annotations)
	title := strings.TrimSpace(raw.Title)
	if title == "" && annotation != nil {
		title = annotation.Title
	}
	return ToolDefinition{
		Name:         raw.Name,
		Title:        title,
		Description:  raw.Description,
		InputSchema:  toolInputSchema(raw),
		OutputSchema: toolOutputSchema(raw),
		Annotation:   annotation,
		Icons:        buildIcons(raw.Icons),
		Metadata:     metadataFromMeta(raw.Meta),
	}
}

// timeoutInvokableTool 为 MCP 工具调用套上 read_timeout，对齐 MCPConnection.call_tool 的 read_timeout_seconds
type timeoutInvokableTool struct {
	inner   tool.InvokableTool
	timeout time.Duration
}

func (t *timeoutInvokableTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *timeoutInvokableTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}
	return t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
}

func wrapToolTimeout(items []tool.BaseTool, timeout time.Duration) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(items))
	for _, item := range items {
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			out = append(out, item)
			continue
		}
		out = append(out, &timeoutInvokableTool{inner: invokable, timeout: timeout})
	}
	return out
}

// marshalToolResult 把 MCP 工具结果文本还原为 JSON 对象，供 CallTool 兼容接口使用
func marshalToolResult(text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, NewError("MCP 工具返回为空")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return nil, NewError("MCP 工具返回了无效 JSON")
	}
	return data, nil
}
