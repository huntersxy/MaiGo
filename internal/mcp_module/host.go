package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// SamplingHandler 处理服务端发起的 MCP Sampling 请求，对应 MCPHostLLMBridge.handle_sampling_request
type SamplingHandler interface {
	CreateMessage(ctx context.Context, request mcpprotocol.CreateMessageRequest) (*mcpprotocol.CreateMessageResult, error)
}

// structuredSamplingHandler 可选扩展：接收 mcp-go 内容块未保留的 tool_result structuredContent
type structuredSamplingHandler interface {
	CreateMessageWithStructured(ctx context.Context, request mcpprotocol.CreateMessageRequest, structured map[string]any) (*mcpprotocol.CreateMessageResult, error)
}

// samplingTemperatureHandler 可选扩展：保留 temperature 是否出现的原始信息
// Python 缺省 None 使用模型默认温度，显式 0 传 0；mcp-go 的 float64 无法区分两者
type samplingTemperatureHandler interface {
	CreateMessageWithTemperature(ctx context.Context, request mcpprotocol.CreateMessageRequest, structured map[string]any, temperature *float64) (*mcpprotocol.CreateMessageResult, error)
}

// ElicitationHandler 处理服务端发起的 MCP Elicitation 请求
type ElicitationHandler interface {
	Elicit(ctx context.Context, request mcpprotocol.ElicitationRequest) (*mcpprotocol.ElicitationResult, error)
}

// LoggingHandler 消费服务端日志通知
type LoggingHandler func(ctx context.Context, params mcpprotocol.LoggingMessageNotificationParams)

// MessageHandler 消费未归类的服务端通知
type MessageHandler func(ctx context.Context, notification mcpprotocol.JSONRPCNotification)

// HostCallbacks 宿主侧可选能力集合，对应 MaiBot MCPHostCallbacks
//
// 与 Python 一致：对应开关打开但回调为空时不声明该能力
type HostCallbacks struct {
	Sampling    SamplingHandler
	Elicitation ElicitationHandler
	Logging     LoggingHandler
	Message     MessageHandler
}

// BuildClientCapabilities 按配置与已注入回调生成客户端能力声明
func (h HostCallbacks) BuildClientCapabilities(cfg ClientConfig) mcpprotocol.ClientCapabilities {
	cfg = cfg.Normalize()
	caps := mcpprotocol.ClientCapabilities{}
	if cfg.EnableRoots {
		caps.Roots = &struct {
			ListChanged bool `json:"listChanged,omitempty"`
		}{ListChanged: true}
	}
	if cfg.EnableSampling && h.Sampling != nil {
		sampling := &mcpprotocol.SamplingCapability{}
		if cfg.SamplingToolSupport {
			sampling.Tools = &struct{}{}
		}
		caps.Sampling = sampling
	}
	if cfg.EnableElicitation && h.Elicitation != nil && (cfg.ElicitationAllowForm || cfg.ElicitationAllowURL) {
		elicitation := &mcpprotocol.ElicitationCapability{}
		if cfg.ElicitationAllowForm {
			elicitation.Form = &struct{}{}
		}
		if cfg.ElicitationAllowURL {
			elicitation.URL = &struct{}{}
		}
		caps.Elicitation = elicitation
	}
	return caps
}

// installHostRequestHandler 安装服务端反向请求处理，覆盖 mcp-go 客户端默认能力限制
//
// mcp-go v0.54.1 在 Client.Initialize 中会用空能力覆盖 WithSamplingHandler 的声明，
// 因此这里不注册 client 级 handler，而是在 Start 之后直接替换 transport 的请求处理器，
// 以便按 MaiBot 语义完整声明 sampling.tools / elicitation.form / elicitation.url
func installHostRequestHandler(
	cli *client.Client,
	cfg ClientConfig,
	host HostCallbacks,
) {
	if cli == nil {
		return
	}
	bidirectional, ok := cli.GetTransport().(transport.BidirectionalInterface)
	if !ok {
		return
	}
	bidirectional.SetRequestHandler(func(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
		return handleHostRequest(ctx, cfg, host, request)
	})
}

func handleHostRequest(
	ctx context.Context,
	cfg ClientConfig,
	host HostCallbacks,
	request transport.JSONRPCRequest,
) (*transport.JSONRPCResponse, error) {
	cfg = cfg.Normalize()
	switch request.Method {
	case string(mcpprotocol.MethodPing):
		return jsonRPCResult(request.ID, mcpprotocol.EmptyResult{})
	case string(mcpprotocol.MethodListRoots):
		if !cfg.EnableRoots || len(cfg.Roots) == 0 {
			return nil, fmt.Errorf("roots 能力未启用")
		}
		roots := make([]mcpprotocol.Root, 0, len(cfg.Roots))
		for _, root := range cfg.Roots {
			roots = append(roots, mcpprotocol.Root{URI: root.URI, Name: root.Name})
		}
		return jsonRPCResult(request.ID, mcpprotocol.ListRootsResult{Roots: roots})
	case string(mcpprotocol.MethodSamplingCreateMessage):
		if !cfg.EnableSampling || host.Sampling == nil {
			return nil, fmt.Errorf("sampling 能力未启用")
		}
		params, structured, err := decodeSamplingInvocation(request.Params)
		if err != nil {
			return nil, fmt.Errorf("解析 sampling 请求失败: %w", err)
		}
		samplingRequest := mcpprotocol.CreateMessageRequest{
			Request:             mcpprotocol.Request{Method: string(mcpprotocol.MethodSamplingCreateMessage)},
			CreateMessageParams: params,
		}
		var result *mcpprotocol.CreateMessageResult
		temperature := decodeOptionalTemperature(request.Params)
		switch handler := host.Sampling.(type) {
		case samplingTemperatureHandler:
			result, err = handler.CreateMessageWithTemperature(ctx, samplingRequest, structured, temperature)
		case structuredSamplingHandler:
			result, err = handler.CreateMessageWithStructured(ctx, samplingRequest, structured)
		default:
			result, err = host.Sampling.CreateMessage(ctx, samplingRequest)
		}
		if err != nil {
			return nil, err
		}
		return jsonRPCResult(request.ID, result)
	case string(mcpprotocol.MethodElicitationCreate):
		if !cfg.EnableElicitation || host.Elicitation == nil {
			return nil, fmt.Errorf("elicitation 能力未启用")
		}
		params, err := decodeElicitationParams(request.Params)
		if err != nil {
			return nil, fmt.Errorf("解析 elicitation 请求失败: %w", err)
		}
		mode := params.Mode
		if mode == "" {
			mode = mcpprotocol.ElicitationModeForm
		}
		if mode == mcpprotocol.ElicitationModeForm && !cfg.ElicitationAllowForm {
			return nil, fmt.Errorf("elicitation form 模式未允许")
		}
		if mode == mcpprotocol.ElicitationModeURL && !cfg.ElicitationAllowURL {
			return nil, fmt.Errorf("elicitation url 模式未允许")
		}
		elicitationRequest := mcpprotocol.ElicitationRequest{
			Request: mcpprotocol.Request{Method: string(mcpprotocol.MethodElicitationCreate)},
			Params:  params,
		}
		// 能力声明阶段已按 h.Elicitation != nil 过滤，此处再防御一次避免 nil 解引用
		if host.Elicitation == nil {
			return nil, fmt.Errorf("MCP Elicitation 处理器未接线")
		}
		result, err := host.Elicitation.Elicit(ctx, elicitationRequest)
		if err != nil {
			return nil, err
		}
		return jsonRPCResult(request.ID, result)
	default:
		return nil, fmt.Errorf("不支持的 MCP 服务端请求: %s", request.Method)
	}
}

func jsonRPCResult(id mcpprotocol.RequestId, result any) (*transport.JSONRPCResponse, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return transport.NewJSONRPCResultResponse(id, encoded), nil
}

func decodeSamplingParams(raw any) (mcpprotocol.CreateMessageParams, error) {
	params, _, err := decodeSamplingInvocation(raw)
	return params, err
}

// decodeSamplingInvocation 解析 Sampling 参数，并额外提取 tool_result 的 structuredContent
func decodeSamplingInvocation(raw any) (mcpprotocol.CreateMessageParams, map[string]any, error) {
	var params mcpprotocol.CreateMessageParams
	if raw == nil {
		return params, nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return params, nil, err
	}
	if err := json.Unmarshal(encoded, &params); err != nil {
		return params, nil, err
	}
	// 部分传输（HTTP）会把 content 反序列化成 map，这里统一还原为具体内容类型
	for index := range params.Messages {
		contentMap, ok := params.Messages[index].Content.(map[string]any)
		if !ok {
			continue
		}
		content, parseErr := mcpprotocol.ParseContent(contentMap)
		if parseErr != nil {
			return params, nil, fmt.Errorf("解析第 %d 条 sampling 消息失败: %w", index, parseErr)
		}
		params.Messages[index].Content = content
	}
	return params, samplingStructuredResults(encoded), nil
}

// decodeOptionalTemperature 从原始 JSON-RPC 参数判断 temperature 是否出现
// 缺省或 null 返回 nil；显式 0 返回指向 0 的指针，与 Python 的 None / 0 区分一致
func decodeOptionalTemperature(raw any) *float64 {
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil
	}
	value, ok := root["temperature"]
	if !ok || string(value) == "null" {
		return nil
	}
	var temperature float64
	if err := json.Unmarshal(value, &temperature); err != nil {
		return nil
	}
	return &temperature
}

// samplingStructuredResults 从原始 JSON 中提取 tool_result.structuredContent
// mcp-go 的 ToolResultContent 没有该字段，反序列化后会丢失
func samplingStructuredResults(encoded []byte) map[string]any {
	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil
	}
	messages, _ := root["messages"].([]any)
	results := make(map[string]any)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		for _, item := range structuredContentItems(message["content"]) {
			itemType, _ := item["type"].(string)
			if itemType != "tool_result" {
				continue
			}
			toolUseID := samplingString(item["toolUseId"])
			if toolUseID == "" {
				toolUseID = samplingString(item["tool_use_id"])
			}
			structured := item["structuredContent"]
			if structured == nil {
				structured = item["structured_content"]
			}
			if toolUseID == "" || structured == nil || isEmptyStructuredContent(structured) {
				continue
			}
			results[toolUseID] = structured
		}
	}
	if len(results) == 0 {
		return nil
	}
	return results
}

func structuredContentItems(raw any) []map[string]any {
	values, ok := raw.([]any)
	if !ok {
		if single, singleOK := raw.(map[string]any); singleOK {
			return []map[string]any{single}
		}
		return nil
	}
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, itemOK := value.(map[string]any); itemOK {
			items = append(items, item)
		}
	}
	return items
}

func samplingString(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func isEmptyStructuredContent(raw any) bool {
	switch value := raw.(type) {
	case nil:
		return true
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	default:
		return false
	}
}

func decodeElicitationParams(raw any) (mcpprotocol.ElicitationParams, error) {
	var params mcpprotocol.ElicitationParams
	if raw == nil {
		return params, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return params, err
	}
	if err := json.Unmarshal(encoded, &params); err != nil {
		return params, err
	}
	if err := params.Validate(); err != nil {
		return params, err
	}
	return params, nil
}

// installNotificationHandler 把服务端日志与自定义通知转发给宿主回调
func installNotificationHandler(cli *client.Client, host HostCallbacks) {
	if cli == nil || (host.Logging == nil && host.Message == nil) {
		return
	}
	cli.OnNotification(func(notification mcpprotocol.JSONRPCNotification) {
		ctx := context.Background()
		if notification.Method == string(mcpprotocol.MethodNotificationMessage) && host.Logging != nil {
			params := decodeLoggingParams(notification.Params)
			host.Logging(ctx, params)
			return
		}
		if host.Message != nil {
			host.Message(ctx, notification)
		}
	})
}

func decodeLoggingParams(raw mcpprotocol.NotificationParams) mcpprotocol.LoggingMessageNotificationParams {
	params := mcpprotocol.LoggingMessageNotificationParams{}
	if raw.AdditionalFields != nil {
		if level, ok := raw.AdditionalFields["level"].(string); ok {
			params.Level = mcpprotocol.LoggingLevel(level)
		}
		if logger, ok := raw.AdditionalFields["logger"].(string); ok {
			params.Logger = logger
		}
		params.Data = raw.AdditionalFields["data"]
	}
	return params
}

func logHostCapabilityGaps(cfg ClientConfig, host HostCallbacks) {
	cfg = cfg.Normalize()
	if cfg.EnableSampling && host.Sampling == nil {
		zap.L().Warn("MCP 已启用 sampling 配置，但宿主未提供 sampling 回调，当前不会声明该能力")
	}
	if cfg.EnableElicitation && host.Elicitation == nil {
		zap.L().Warn("MCP 已启用 elicitation 配置，但宿主未提供 elicitation 回调，当前不会声明该能力")
	}
	if strings.TrimSpace(cfg.ClientName) == "" {
		zap.L().Warn("MCP 客户端名称为空，使用默认值")
	}
}
