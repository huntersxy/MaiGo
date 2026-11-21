package hostports

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/plugin_runtime"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// LLM 插件 LLM 端口，复用主程序任务路由
type LLM struct {
	router func() *llm.Router
}

// NewLLM 构造 LLM 端口；router 延迟取 Agent 的任务路由器
func NewLLM(router func() *llm.Router) *LLM {
	return &LLM{router: router}
}

func (p *LLM) taskRouter() (*llm.Router, error) {
	if p == nil || p.router == nil {
		return nil, fmt.Errorf("LLM 路由器未接入")
	}
	router := p.router()
	if router == nil {
		return nil, fmt.Errorf("LLM 路由器未接入")
	}
	return router, nil
}

type pluginLLMRoute struct {
	task      llm.TaskName
	modelName string
}

// resolvePluginRoute 对齐 _resolve_llm_capability_route 的任务/模型选择规则
func resolvePluginRoute(req pluginruntime.PluginLLMRequest) (pluginLLMRoute, error) {
	taskName := strings.TrimSpace(req.TaskName)
	modelAlias := strings.TrimSpace(req.Model)
	modelName := strings.TrimSpace(req.ModelName)
	if modelAlias != "" && modelName != "" && modelAlias != modelName {
		return pluginLLMRoute{}, fmt.Errorf("model 与 model_name 不能指定不同的模型")
	}
	route := pluginLLMRoute{task: llm.TaskUtils}
	if req.TaskNameProvided {
		if taskName != "" {
			task, err := normalizePluginTaskName(taskName)
			if err != nil {
				return pluginLLMRoute{}, fmt.Errorf("未找到名为 %q 的模型配置: %w", taskName, err)
			}
			route.task = task
		}
		route.modelName = firstNonEmpty(modelName, modelAlias)
		return route, nil
	}
	if modelName != "" {
		route.modelName = modelName
		return route, nil
	}
	if modelAlias != "" {
		if task, err := normalizePluginTaskName(modelAlias); err == nil {
			route.task = task
			return route, nil
		}
		route.modelName = modelAlias
	}
	return route, nil
}

func normalizePluginTaskName(name string) (llm.TaskName, error) {
	if name == "emoji" {
		return llm.TaskPlanner, nil
	}
	return llm.NormalizeTaskName(name)
}

// Generate 执行一次插件 LLM 生成
// 对齐 Python _cap_llm_generate(_with_tools)：任务名 + pluginID 进入编排，
// 显式 model/model_name 仍由路由器执行重试、硬超时、413 压缩、快照与 usage 记录。
func (p *LLM) Generate(ctx context.Context, pluginID string, req pluginruntime.PluginLLMRequest) (map[string]any, error) {
	router, err := p.taskRouter()
	if err != nil {
		return nil, err
	}
	route, err := resolvePluginRoute(req)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestType := "plugin." + strings.TrimSpace(pluginID)
	ctx = llm.WithCacheStatsMetadata(ctx, string(route.task), requestType, "")
	ctx = llm.WithModelUsageMetadata(ctx, string(route.task), requestType, "")
	var clientRoute llm.ResolvedRoute
	var client model.AgenticModel
	if route.modelName == "" {
		clientRoute, client, err = router.NewClientForTaskWithFailover(ctx, route.task)
	} else {
		clientRoute, client, err = router.NewClientForTaskWithFailoverAndModel(ctx, route.task, route.modelName)
	}
	if err != nil {
		return nil, err
	}
	tools, err := parsePluginToolOptions(req.Tools)
	if err != nil {
		return nil, err
	}
	options := make([]model.Option, 0, 3)
	if req.Temperature != nil {
		options = append(options, model.WithTemperature(float32(*req.Temperature)))
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		options = append(options, model.WithMaxTokens(*req.MaxTokens))
	}
	if len(tools) > 0 {
		options = append(options, model.WithTools(tools))
	}
	message, err := client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(req.Prompt)}, options...)
	if err != nil {
		return nil, err
	}
	modelName := firstNonEmpty(clientRoute.MaiBotModel, clientRoute.Config.Model, clientRoute.Profile.ModelName)
	return pluginLLMPayload(message, modelName), nil
}

func pluginLLMPayload(message *schema.AgenticMessage, modelName string) map[string]any {
	text, reasoning, toolCalls := agenticContent(message)
	payload := map[string]any{
		"success":                  true,
		"response":                 text,
		"reasoning":                reasoning,
		"model_name":               modelName,
		"prompt_tokens":            0,
		"completion_tokens":        0,
		"total_tokens":             0,
		"prompt_cache_hit_tokens":  0,
		"prompt_cache_miss_tokens": 0,
	}
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		usage := message.ResponseMeta.TokenUsage
		hit := usage.PromptTokenDetails.CachedTokens
		miss := usage.PromptTokens - hit
		if miss < 0 {
			miss = 0
		}
		payload["prompt_tokens"] = usage.PromptTokens
		payload["completion_tokens"] = usage.CompletionTokens
		payload["total_tokens"] = usage.TotalTokens
		payload["prompt_cache_hit_tokens"] = hit
		payload["prompt_cache_miss_tokens"] = miss
	}
	if len(toolCalls) > 0 {
		payload["tool_calls"] = toolCalls
	}
	return payload
}

func agenticContent(message *schema.AgenticMessage) (string, string, []map[string]any) {
	if message == nil {
		return "", "", nil
	}
	var text strings.Builder
	var reasoning strings.Builder
	toolCalls := make([]map[string]any, 0)
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				text.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				text.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning != nil {
				reasoning.WriteString(block.Reasoning.Text)
			}
		case schema.ContentBlockTypeFunctionToolCall:
			call := block.FunctionToolCall
			if call == nil {
				continue
			}
			arguments := map[string]any{}
			if strings.TrimSpace(call.Arguments) != "" {
				if err := sonic.UnmarshalString(call.Arguments, &arguments); err != nil {
					arguments = map[string]any{"_raw": call.Arguments}
				}
			}
			entry := map[string]any{
				"id": call.CallID,
				"function": map[string]any{
					"name":      call.Name,
					"arguments": arguments,
				},
			}
			if len(block.Extra) > 0 {
				entry["extra_content"] = block.Extra
			}
			toolCalls = append(toolCalls, entry)
		}
	}
	return strings.TrimSpace(text.String()), strings.TrimSpace(reasoning.String()), toolCalls
}

// Embed 执行文本嵌入
func (p *LLM) Embed(ctx context.Context, _ string, req pluginruntime.PluginEmbedRequest) (map[string]any, error) {
	router, err := p.taskRouter()
	if err != nil {
		return nil, err
	}
	route, client, err := router.NewEmbeddingClientForTask(ctx, llm.TaskEmbedding)
	if err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, len(req.Texts))
	for _, text := range req.Texts {
		result, err := client.EmbedText(ctx, text)
		if err != nil {
			return nil, err
		}
		results = append(results, embeddingPayload(result, route))
	}
	if len(results) == 1 {
		return map[string]any{"success": true, "embedding": results[0]["embedding"], "model_name": results[0]["model_name"]}, nil
	}
	return map[string]any{"success": true, "results": results}, nil
}

func embeddingPayload(result llm.EmbeddingResult, route llm.ResolvedRoute) map[string]any {
	return map[string]any{
		"embedding":  result.Embedding,
		"model_name": firstNonEmpty(result.ModelIdentifier, result.Model, route.Config.Model),
	}
}

// Transcribe 执行语音转写
func (p *LLM) Transcribe(ctx context.Context, _ string, audioBase64 string) (map[string]any, error) {
	router, err := p.taskRouter()
	if err != nil {
		return nil, err
	}
	route, err := router.Resolve(ctx, llm.TaskVoice)
	if err != nil {
		return nil, err
	}
	transcriber, err := llm.NewAudioTranscriber(route.Config)
	if err != nil {
		return nil, err
	}
	result, err := transcriber.Transcribe(ctx, llm.AudioTranscriptionRequest{AudioBase64: audioBase64})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(result.Text)
	return map[string]any{"success": text != "", "text": text, "content": text}, nil
}

// AvailableModels 返回可用任务名
func (p *LLM) AvailableModels(context.Context) ([]string, error) {
	names := make([]string, 0, len(llm.AllTaskNames()))
	for _, task := range llm.AllTaskNames() {
		names = append(names, string(task))
	}
	return names, nil
}
