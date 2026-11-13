package llm

import (
	"context"
	"errors"
	"fmt"

	"maibot-go/internal/config/maibot"
	"maibot-go/internal/config/model_profiles"

	"github.com/cloudwego/eino/components/model"
)

// NewClientForTaskWithFailover 按任务 model_list 与 selection_strategy 创建客户端；
// 多候选时返回请求级 failover 包装器：客户端创建失败跳过，请求失败按策略切换下一个模型
// 单候选也使用包装器以累计 token / penalty / usage_penalty；binding / 活跃档案回退路径只试一次，
// 但仍包一层 413/data URI 压缩重试
func (r *Router) NewClientForTaskWithFailover(ctx context.Context, task TaskName) (ResolvedRoute, model.AgenticModel, error) {
	if r == nil || r.profiles == nil {
		return ResolvedRoute{}, nil, fmt.Errorf("LLM 路由器未初始化")
	}
	task, err := NormalizeTaskName(string(task))
	if err != nil {
		return ResolvedRoute{}, nil, err
	}

	if name := r.bindings[task]; name != "" {
		return r.fallbackClientForTask(ctx, task)
	}

	if r.file == nil || task == TaskWebSearch {
		return r.fallbackClientForTask(ctx, task)
	}

	taskRes, models, err := TaskModelList(*r.file, task)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	if len(models) == 0 {
		return r.fallbackClientForTask(ctx, task)
	}

	strategy := NormalizeSelectionStrategy(task, taskRes.Config.SelectionStrategy)
	hardTimeout := hardTimeoutForTask(task, taskRes.Config.HardTimeout)
	clients := make(map[string]model.AgenticModel, len(models))
	routes := make(map[string]ResolvedRoute, len(models))
	attempts := make(map[string]modelAttempt, len(models))
	configs := make(map[string]ModelConfig, len(models))
	var lastErr error
	for _, modelName := range models {
		resolved, err := r.resolveMaiBotModel(ctx, task, taskRes.Effective, taskRes.Config, modelName)
		if err != nil {
			lastErr = err
			continue
		}
		applyRouteTaskMetadata(&resolved, task)
		client, err := NewClient(resolved.Config)
		if err != nil {
			lastErr = err
			continue
		}
		clients[modelName] = client
		routes[modelName] = resolved
		configs[modelName] = resolved.Config
		attempts[modelName] = modelAttempt{
			Policy:      retryPolicyFromConfig(resolved.Config),
			HardTimeout: hardTimeout,
		}
	}
	if len(clients) == 0 {
		if lastErr != nil {
			return ResolvedRoute{}, nil, fmt.Errorf("任务 %q 全部候选模型创建客户端失败: %w", task, lastErr)
		}
		return ResolvedRoute{}, nil, fmt.Errorf("任务 %q 无可用模型", task)
	}

	available := make([]string, 0, len(models))
	for _, modelName := range models {
		if _, ok := clients[modelName]; ok {
			available = append(available, modelName)
		}
	}
	usage := r.modelUsageSnapshot()
	initial, ok := PickNextModel(ModelSelectInput{
		Task:     task,
		Models:   available,
		Strategy: strategy,
		Score: func(candidate string) int {
			return usage[candidate].BalanceScore()
		},
	})
	if !ok {
		return ResolvedRoute{}, nil, fmt.Errorf("任务 %q 无可用模型", task)
	}
	return routes[initial], newFailoverModel(r, task, models, strategy, clients, attempts, configs), nil
}

// NewClientForTaskWithFailoverAndModel 按显式模型名创建客户端，等价 Python
// LLMOrchestrator 收到 model_name 时只走一次模型选择但仍执行重试/硬超时/413 压缩。
// 模型先按活跃档案名称/模型标识匹配，未命中时再回退 model_config.toml；
// 仅活跃档案部署（model_config 未加载）也能解析显式模型。
func (r *Router) NewClientForTaskWithFailoverAndModel(ctx context.Context, task TaskName, modelName string) (ResolvedRoute, model.AgenticModel, error) {
	if r == nil {
		return ResolvedRoute{}, nil, fmt.Errorf("LLM 路由器未初始化")
	}
	task, err := NormalizeTaskName(string(task))
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	modelName = trimTask(modelName)
	if modelName == "" {
		return r.NewClientForTaskWithFailover(ctx, task)
	}
	if r.file == nil && r.profiles == nil {
		return ResolvedRoute{}, nil, fmt.Errorf("LLM 路由器未初始化")
	}

	taskCfg := maibot.TaskConfig{}
	effective := task
	if r.file != nil && task != TaskWebSearch {
		taskRes, err := ResolveTaskConfig(*r.file, task)
		if err != nil {
			return ResolvedRoute{}, nil, err
		}
		taskCfg = taskRes.Config
		effective = taskRes.Effective
	}
	resolved, err := r.resolveMaiBotModel(ctx, task, effective, taskCfg, modelName)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	applyRouteTaskMetadata(&resolved, task)
	client, err := NewClient(resolved.Config)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	models := []string{modelName}
	attempts := map[string]modelAttempt{
		modelName: {
			Policy:      retryPolicyFromConfig(resolved.Config),
			HardTimeout: hardTimeoutForTask(task, resolved.TaskConfig.HardTimeout),
		},
	}
	configs := map[string]ModelConfig{modelName: resolved.Config}
	return resolved, newFailoverModel(r, task, models, StrategyFirst, map[string]model.AgenticModel{modelName: client}, attempts, configs), nil
}

// fallbackClientForTask 创建单模型回退客户端，并补上失败切换层提供的 413/data URI 图片压缩重试
func (r *Router) fallbackClientForTask(ctx context.Context, task TaskName) (ResolvedRoute, model.AgenticModel, error) {
	resolved, client, err := r.NewClientForTask(ctx, task)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	return resolved, newImageCompressionRetryModel(client), nil
}

func (r *Router) resolveMaiBotModel(
	ctx context.Context,
	task TaskName,
	effective TaskName,
	taskCfg maibot.TaskConfig,
	modelName string,
) (ResolvedRoute, error) {
	modelName = trimTask(modelName)
	if modelName == "" {
		return ResolvedRoute{}, fmt.Errorf("模型名为空")
	}
	if r.profiles != nil {
		profile, err := r.profileByMaiBotModelName(ctx, modelName)
		if err == nil {
			cfg, err := r.modelConfigFromProfile(profile)
			if err != nil {
				return ResolvedRoute{}, err
			}
			cfg = withTaskEffectiveParametersFor(task, cfg, taskCfg)
			return ResolvedRoute{
				Task:          task,
				EffectiveTask: effective,
				Source:        RouteModelConfig,
				MaiBotModel:   modelName,
				Profile:       profile,
				Config:        cfg,
				TaskConfig:    taskCfg,
			}, nil
		}
		// 无 model_config 时，档案查询失败不能伪装成“模型不存在”
		if r.file == nil && !errors.Is(err, modelprofiles.ErrNotFound) {
			return ResolvedRoute{}, fmt.Errorf("读取模型档案失败: %w", err)
		}
	}
	if r.file == nil {
		return ResolvedRoute{}, fmt.Errorf("未找到名为 %q 的模型（model_config 未加载且活跃模型档案未命中）: %w", modelName, modelprofiles.ErrNotFound)
	}
	cfg, err := ModelConfigFromMaiBotFile(*r.file, modelName)
	if err != nil {
		return ResolvedRoute{}, err
	}
	cfg = withTaskEffectiveParametersFor(task, cfg, taskCfg)
	return ResolvedRoute{
		Task:          task,
		EffectiveTask: effective,
		Source:        RouteModelConfig,
		MaiBotModel:   modelName,
		Profile: modelprofiles.Profile{
			Name:      modelName,
			ModelName: cfg.Model,
		},
		Config:     cfg,
		TaskConfig: taskCfg,
	}, nil
}
