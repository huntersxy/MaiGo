package llm

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// usageModel 为 binding / 活跃档案等单模型路径记录 token 与失败惩罚
// 这些路径不参与 model_list 的 balance 选择，因此只累计实际成功或失败，不做名额占用
// 请求级重试与 hard_timeout 复用 attemptRunner，与多候选路径语义一致
type usageModel struct {
	router  *Router
	model   string
	cfg     ModelConfig
	attempt attemptRunner
	inner   model.AgenticModel
}

func withUsageTracking(router *Router, modelName string, cfg ModelConfig, inner model.AgenticModel, attempt attemptRunner) model.AgenticModel {
	if router == nil || inner == nil || modelName == "" {
		return inner
	}
	return &usageModel{router: router, model: modelName, cfg: cfg, attempt: attempt, inner: inner}
}

// runner 为本次逻辑请求补充快照来源
func (m *usageModel) runner(input []*schema.AgenticMessage, opts ...model.Option) attemptRunner {
	runner := m.attempt
	runner.Snapshot = responseAttemptSnapshot(m.cfg, input, opts)
	return runner
}

func (m *usageModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	var message *schema.AgenticMessage
	err := m.runner(input, opts...).run(ctx, func(runCtx context.Context) error {
		var callErr error
		message, callErr = m.inner.Generate(runCtx, input, opts...)
		return callErr
	})
	if err != nil {
		if !failoverStopped(ctx, err) {
			m.router.adjustModelUsage(m.model, ModelUsage{Penalty: 1})
		}
		return nil, err
	}
	m.router.adjustModelUsage(m.model, ModelUsage{TotalTokens: agenticMessageTokens(message)})
	return message, nil
}

// Stream 先缓冲整条流再交付，读流中途的可重试错误整请求重放，与 failover 层语义一致
func (m *usageModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	var buffered []*schema.AgenticMessage
	err := m.runner(input, opts...).run(ctx, func(runCtx context.Context) error {
		reader, callErr := m.inner.Stream(runCtx, input, opts...)
		if callErr != nil {
			return callErr
		}
		messages, readErr := drainAgenticStream(reader)
		if readErr != nil {
			return readErr
		}
		if len(messages) == 0 {
			return &EmptyResponseError{Message: "流式响应内容为空"}
		}
		buffered = messages
		return nil
	})
	if err != nil {
		if !failoverStopped(ctx, err) {
			m.router.adjustModelUsage(m.model, ModelUsage{Penalty: 1})
		}
		return nil, err
	}
	return trackStreamUsage(m.router, m.model, replayAgenticStream(buffered)), nil
}

func usageModelName(resolved ResolvedRoute) string {
	if name := trimTask(resolved.MaiBotModel); name != "" {
		return name
	}
	if name := trimTask(resolved.Profile.ModelName); name != "" {
		return name
	}
	return trimTask(resolved.Config.Model)
}

// attemptForRoute 按解析结果构造单模型请求的重试与硬超时设置
func attemptForRoute(resolved ResolvedRoute) attemptRunner {
	return attemptRunner{
		Task:    resolved.Task,
		Model:   usageModelName(resolved),
		Policy:  retryPolicyFromConfig(resolved.Config),
		Timeout: hardTimeoutForTask(resolved.Task, resolved.TaskConfig.HardTimeout),
	}
}
