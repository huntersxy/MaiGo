package runtime

// Replyer 生成循环的 Agent 侧 LLM 端口
// 客户端实现与选择策略在 chat/replyer；本文件只把 Agent 的模型路由与实例配置
// 适配成 replyer.ModelResolver 端口。

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/config/maibot"
	"MaiGo/internal/llm_models"

	"github.com/cloudwego/eino/components/model"
)

// replyerModelResolver 把 Agent 的模型路由适配为 replyer.ModelResolver
type replyerModelResolver struct {
	agent *Agent
	task  llm.TaskName
}

// ResolveModel 按请求名解析模型；显式模型同样走路由层的重试与失败快照
func (r replyerModelResolver) ResolveModel(ctx context.Context, requested string) (replyer.ResolvedModel, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || r.agent == nil {
		return replyer.ResolvedModel{}, fmt.Errorf("模型名不能为空")
	}
	router := r.agent.llmRouterSnapshot()
	if router == nil {
		return replyer.ResolvedModel{}, fmt.Errorf("%w: Replyer 指定模型 %q 无法选择", ErrModelRouterUnavailable, requested)
	}
	resolved, client, err := router.NewClientForTaskWithFailoverAndModel(ctx, r.task, requested)
	if err != nil {
		return replyer.ResolvedModel{}, err
	}
	info := r.agent.replyerModelInfo(requested)
	if strings.TrimSpace(info.Name) == "" {
		info.Name = requested
	}
	// ModelIdentifier 必须是真实模型标识，请求名只是选择键
	if strings.TrimSpace(info.ModelIdentifier) == "" || info.ModelIdentifier == requested {
		if modelIdentifier := strings.TrimSpace(resolved.Config.Model); modelIdentifier != "" {
			info.ModelIdentifier = modelIdentifier
		}
	}
	if strings.TrimSpace(info.ClientType) == "" {
		info.ClientType = resolved.Config.ClientType
	}
	if strings.TrimSpace(info.ClientType) == "" {
		info.ClientType = "openai"
	}
	if strings.TrimSpace(info.APIProvider) == "" {
		info.APIProvider = resolved.Config.ProviderName
	}
	return replyer.ResolvedModel{Model: client, Name: requested, Info: info}, nil
}

// ModelInfo 返回模型元信息
func (r replyerModelResolver) ModelInfo(modelName string) replyer.ModelInfo {
	if r.agent == nil {
		return replyer.ModelInfo{Name: strings.TrimSpace(modelName), ModelIdentifier: strings.TrimSpace(modelName)}
	}
	return r.agent.replyerModelInfo(modelName)
}

// ClientForTask 按任务名构造客户端
func (r replyerModelResolver) ClientForTask(ctx context.Context, taskName string) (replyer.LLMClient, error) {
	if r.agent == nil {
		return nil, fmt.Errorf("Agent 未初始化")
	}
	client, modelName, _, err := r.agent.buildRoutedTaskModel(ctx, llm.TaskName(strings.TrimSpace(taskName)))
	if err != nil {
		return nil, err
	}
	return newReplyerAgenticClient(r.agent, taskName, client, modelName), nil
}

func newReplyerAgenticClient(a *Agent, taskName string, active model.AgenticModel, modelName string) *replyer.AgenticLLMClient {
	return replyer.NewAgenticLLMClient(replyerModelResolver{agent: a, task: llm.TaskName(taskName)}, taskName, active, modelName)
}

// replyerModelInfo 从实例 model_config 读取模型视觉与 Provider 协议
func (a *Agent) replyerModelInfo(modelName string) replyer.ModelInfo {
	modelName = strings.TrimSpace(modelName)
	info := replyer.ModelInfo{Name: modelName, ModelIdentifier: modelName}
	if a == nil || modelName == "" {
		return info
	}
	instance := a.instanceConfig()
	if instance == nil {
		return info
	}
	matched := false
	for _, item := range instance.Model.Models {
		if strings.TrimSpace(item.Name) == modelName {
			matched = true
			info = replyerModelInfoFromConfig(item)
			break
		}
	}
	if !matched {
		for _, item := range instance.Model.Models {
			if strings.TrimSpace(item.ModelIdentifier) == modelName {
				info = replyerModelInfoFromConfig(item)
				info.Name = modelName
				break
			}
		}
	}
	info.ClientType = strings.TrimSpace(info.ClientType)
	if info.APIProvider != "" && info.ClientType == "" {
		info.ClientType = "openai"
	}
	return info
}

func replyerModelInfoFromConfig(item maibot.ModelInfo) replyer.ModelInfo {
	info := replyer.ModelInfo{
		Name:            strings.TrimSpace(item.Name),
		ModelIdentifier: strings.TrimSpace(item.ModelIdentifier),
		Visual:          item.Visual,
		APIProvider:     strings.TrimSpace(item.ApiProvider),
		MaxTokens:       replyerConfigInt(item.MaxTokens),
		Temperature:     replyerConfigFloat(item.Temperature),
	}
	if info.ModelIdentifier == "" {
		info.ModelIdentifier = info.Name
	}
	return info
}

func replyerConfigInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	}
	return 0
}

func replyerConfigFloat(value any) *float64 {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int:
		result = float64(typed)
	case int64:
		result = float64(typed)
	default:
		return nil
	}
	return &result
}

// replyerLLMFactory 按 Hook 指定的 task 构造任务模型客户端；默认 replyer 直接复用主客户端
func (a *Agent) replyerLLMFactory(ctx context.Context, base replyer.LLMClient) replyer.LLMClientFactory {
	return func(taskName, requestType, sessionID string) (replyer.LLMClient, error) {
		name := strings.TrimSpace(taskName)
		if name == "" || name == base.TaskName() {
			return base, nil
		}
		client, modelName, _, err := a.buildRoutedTaskModel(ctx, llm.TaskName(name))
		if err != nil {
			return nil, fmt.Errorf("Replyer 任务模型 %q 不可用: %w", name, err)
		}
		return newReplyerAgenticClient(a, name, client, modelName), nil
	}
}
