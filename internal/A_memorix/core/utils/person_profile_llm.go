package utils

// A_memorix 人物画像证据分类的 LLM 端口实现
// 复用宿主 TextGenerationGateway 与模型任务选择，不直连具体模型客户端

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrPersonProfileClassificationUnavailable 表示没有可用的画像分类模型任务
// 调用方据此显式降级为规则分桶，而不是把空结果伪装成模型输出
var ErrPersonProfileClassificationUnavailable = errors.New("人物画像分类模型未配置")

// personProfileClassificationTaskPriority 对齐 Python 画像分类的 preferred 顺序
var personProfileClassificationTaskPriority = []string{
	"memory", "utils", "planner", "tool_use", "replyer",
}

// PersonProfileClassificationLLM 把宿主文本生成网关适配为画像分类端口
type PersonProfileClassificationLLM struct {
	gateway TextGenerationGateway
	tasks   map[string]TextGenerationTaskConfig
	logger  Logger
}

// NewPersonProfileClassificationLLM 创建画像分类模型端口
// gateway 为 nil 时仍返回端口，调用时给出明确的未配置错误
func NewPersonProfileClassificationLLM(
	gateway TextGenerationGateway,
	tasks map[string]TextGenerationTaskConfig,
	logger Logger,
) *PersonProfileClassificationLLM {
	return &PersonProfileClassificationLLM{
		gateway: gateway,
		tasks:   tasks,
		logger:  OrLogger(logger),
	}
}

// ResolveModel 按 Python 的 preferred 顺序解析真实模型任务
func (l *PersonProfileClassificationLLM) ResolveModel() (ResolvedLLMModel, error) {
	if l == nil || l.gateway == nil {
		return ResolvedLLMModel{}, fmt.Errorf("%w: LLM 网关未初始化", ErrPersonProfileClassificationUnavailable)
	}
	available := GetTextGenerationModelTasks(l.tasks, false)
	taskName, taskConfig, ok := PickTextGenerationTask(available, personProfileClassificationTaskPriority)
	if !ok {
		return ResolvedLLMModel{}, fmt.Errorf("%w: 没有可用的文本生成模型配置", ErrPersonProfileClassificationUnavailable)
	}
	return ResolvedLLMModel{TaskName: taskName, TaskConfig: taskConfig}, nil
}

// GeneratePersonProfileClassification 按真实任务调用宿主 LLM 编排
func (l *PersonProfileClassificationLLM) GeneratePersonProfileClassification(
	ctx context.Context, prompt string, temperature float64, maxTokens int,
) (string, error) {
	model, err := l.ResolveModel()
	if err != nil {
		return "", err
	}
	result := GenerateWithResolvedModel(
		ctx, l.gateway, model, PersonProfileClassificationRequestType,
		prompt, &temperature, &maxTokens, l.logger,
	)
	if !result.Success {
		if strings.TrimSpace(result.Error) != "" {
			return "", fmt.Errorf("人物画像证据分类模型调用失败: %s", strings.TrimSpace(result.Error))
		}
		return "", errors.New("人物画像证据分类模型未返回文本")
	}
	return result.Completion, nil
}

// ModelSignature 返回画像生成指纹使用的真实模型签名
// 没有可用任务时返回 nil，由画像服务回退到 rule_based 签名
func (l *PersonProfileClassificationLLM) ModelSignature() map[string]any {
	model, err := l.ResolveModel()
	if err != nil {
		return nil
	}
	models := make([]string, 0, len(model.TaskConfig.ModelList))
	for _, item := range model.TaskConfig.ModelList {
		if name := strings.TrimSpace(item); name != "" {
			models = append(models, name)
		}
	}
	sort.Strings(models)
	return map[string]any{
		"mode":                "llm",
		"task_name":           model.TaskName,
		"selected_model_name": model.SelectedModelName,
		"model_list":          models,
	}
}
