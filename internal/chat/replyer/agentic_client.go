package replyer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/llm_models"

	"github.com/cloudwego/eino/components/model"
)

// ModelResolver 宿主侧模型解析端口
//
// 生成循环需要按名称选择模型，但路由、重试与失败快照属于宿主；
// 本包只消费解析结果，不感知 Router 实现。
type ModelResolver interface {
	// ResolveModel 按请求名返回可用模型；空名表示沿用当前任务模型
	ResolveModel(ctx context.Context, requested string) (ResolvedModel, error)
	// ModelInfo 返回模型元信息（Provider、协议、视觉能力）
	ModelInfo(modelName string) ModelInfo
	// ClientForTask 按任务名构造客户端，供 Hook 切换 task
	ClientForTask(ctx context.Context, taskName string) (LLMClient, error)
}

// ResolvedModel 模型解析结果
type ResolvedModel struct {
	Model     model.AgenticModel
	Name      string
	Info      ModelInfo
	Generated bool
}

// AgenticLLMClient 用 eino 模型实现 LLMClient
//
// 对齐 Python replyer 的 LLMServiceClient：把 Context Items 转成 eino 消息，
// 调用模型后还原为输出 Items，并记录 token 用量与单次尝试轨迹。
type AgenticLLMClient struct {
	resolver   ModelResolver
	taskName   string
	model      model.AgenticModel
	modelName  string
	info       ModelInfo
	generation any
}

// NewAgenticLLMClient 构造客户端；taskName 为空时按 replyer 处理
func NewAgenticLLMClient(resolver ModelResolver, taskName string, active model.AgenticModel, modelName string) *AgenticLLMClient {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = "replyer"
	}
	client := &AgenticLLMClient{
		resolver:  resolver,
		taskName:  name,
		model:     active,
		modelName: strings.TrimSpace(modelName),
	}
	if resolver != nil {
		client.info = resolver.ModelInfo(client.modelName)
	}
	return client
}

// TaskName 对齐 Python express_model.task_name
func (c *AgenticLLMClient) TaskName() string { return c.taskName }

// ClientType 供 generator 在响应未带协议类型时兜底
func (c *AgenticLLMClient) ClientType() string { return c.info.ClientType }

// GenerateResponseWithContext 按 options.ModelName 选择实际模型，再让 generator 重新装配请求
func (c *AgenticLLMClient) GenerateResponseWithContext(ctx context.Context, factory ContextFactory, options GenerationOptions) (GenerationResult, error) {
	if c == nil || c.model == nil {
		return GenerationResult{}, fmt.Errorf("Replyer 模型客户端未初始化")
	}
	if factory == nil {
		return GenerationResult{}, fmt.Errorf("Replyer 上下文工厂未配置")
	}
	activeModel, activeName, info, err := c.resolveModel(ctx, options.ModelName)
	if err != nil {
		return GenerationResult{}, err
	}
	requestItems, err := factory(ctx, &info)
	if err != nil {
		return GenerationResult{}, err
	}
	messages, err := ContextItemsToAgenticMessages(requestItems)
	if err != nil {
		return GenerationResult{}, err
	}
	started := time.Now()
	response, err := activeModel.Generate(ctx, messages)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("Replyer 模型调用失败: %w", err)
	}
	outputItems, err := OutputItemsFromAgenticMessage(response)
	if err != nil {
		return GenerationResult{}, err
	}

	result := GenerationResult{
		OutputItems:     outputItems,
		GenerationTrace: &GenerationTrace{Status: "completed"},
		ModelName:       activeName,
		ClientType:      info.ClientType,
		Response:        strings.TrimSpace(llm.GetContextResponseText(outputItems)),
		Reasoning:       strings.TrimSpace(llm.GetContextResponseReasoning(outputItems)),
		ToolCalls:       llm.GetContextResponseToolCalls(outputItems),
	}
	if response != nil && response.ResponseMeta != nil && response.ResponseMeta.TokenUsage != nil {
		usage := response.ResponseMeta.TokenUsage
		result.PromptTokens = usage.PromptTokens
		result.CompletionTokens = usage.CompletionTokens
		result.TotalTokens = usage.TotalTokens
		result.PromptCacheHitTokens = usage.PromptTokenDetails.CachedTokens
		result.PromptCacheMissTokens = usage.PromptTokens - usage.PromptTokenDetails.CachedTokens
	}
	if result.TotalTokens == 0 {
		result.TotalTokens = result.PromptTokens + result.CompletionTokens
	}
	result.GenerationAttempts = []GenerationAttempt{{
		AttemptID:       fmt.Sprintf("%s:%d", strings.TrimSpace(info.Name), 1),
		WorkflowPurpose: "replyer",
		WorkflowAttempt: 1,
		ProviderAttempt: 1,
		ModelAttempt:    1,
		Status:          "succeeded",
		StartedAt:       started,
		DurationMS:      time.Since(started).Milliseconds(),
		Provider:        info.APIProvider,
		Model:           info.ModelIdentifier,
		ClientType:      info.ClientType,
		Operation:       "response",
		WireProtocol:    info.ClientType,
	}}
	return result, nil
}

// resolveModel 显式模型名优先，否则沿用当前任务模型
func (c *AgenticLLMClient) resolveModel(ctx context.Context, requested string) (model.AgenticModel, string, ModelInfo, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == c.modelName {
		return c.model, c.modelName, c.info, nil
	}
	if c.resolver == nil {
		return nil, "", ModelInfo{}, fmt.Errorf("Replyer 指定模型 %q 无法选择: 未注入模型解析端口", requested)
	}
	resolved, err := c.resolver.ResolveModel(ctx, requested)
	if err != nil {
		return nil, "", ModelInfo{}, fmt.Errorf("Replyer 指定模型 %q 不可用: %w", requested, err)
	}
	if resolved.Model == nil {
		return nil, "", ModelInfo{}, fmt.Errorf("Replyer 指定模型 %q 未配置", requested)
	}
	info := resolved.Info
	if strings.TrimSpace(info.Name) == "" {
		info.Name = requested
	}
	if strings.TrimSpace(info.ModelIdentifier) == "" {
		info.ModelIdentifier = requested
	}
	return resolved.Model, resolved.Name, info, nil
}

// NewClientFactory 按 task 构造客户端；默认 replyer 直接复用主客户端
func (c *AgenticLLMClient) NewClientFactory() LLMClientFactory {
	return func(taskName, requestType, sessionID string) (LLMClient, error) {
		name := strings.TrimSpace(taskName)
		if name == "" || name == c.TaskName() {
			return c, nil
		}
		if c.resolver == nil {
			return nil, fmt.Errorf("Replyer 任务模型 %q 不可用: 未注入模型解析端口", name)
		}
		client, err := c.resolver.ClientForTask(context.Background(), name)
		if err != nil {
			return nil, fmt.Errorf("Replyer 任务模型 %q 不可用: %w", name, err)
		}
		return client, nil
	}
}

var _ LLMClient = (*AgenticLLMClient)(nil)
