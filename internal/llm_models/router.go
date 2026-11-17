package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/llm_models/contract"

	"github.com/cloudwego/eino/components/model"
)

// RouteSource 描述模型档案从何而来
type RouteSource string

const (
	RouteBinding     RouteSource = "binding"
	RouteModelConfig RouteSource = "model_config"
	RouteActive      RouteSource = "active"
)

// ResolvedRoute 一次任务路由结果
type ResolvedRoute struct {
	Task          TaskName
	EffectiveTask TaskName
	Source        RouteSource
	MaiBotModel   string // model_config 中的 models[].name，可能为空
	Profile       modelprofiles.Profile
	Config        ModelConfig
	TaskConfig    maibot.TaskConfig
}

var (
	errUnknownTask = contract.ErrUnknownTask
)

// modelNotFoundError 保留 ErrNotFound 的 errors.Is 语义，同时输出面向用户的模型名
type modelNotFoundError struct {
	name string
}

func (e *modelNotFoundError) Error() string {
	return fmt.Sprintf("未找到名为 '%s' 的模型", e.name)
}

func (e *modelNotFoundError) Is(target error) bool {
	return target == modelprofiles.ErrNotFound
}

func newModelNotFoundError(name string) error {
	return &modelNotFoundError{name: name}
}

// Router 按 MaiBot 任务名选择模型档案；未命中时回退到数据库当前活跃模型
type Router struct {
	profiles modelprofiles.Source
	file     *maibot.ModelConfigFile
	// bindings 任务名 -> 模型档案 Name（PG model_profiles.name）
	bindings map[TaskName]string

	modelUsageMu sync.Mutex
	modelUsage   map[string]ModelUsage // 对齐 MaiBot：(total_tokens, penalty, usage_penalty)
}

// NewRouter 构造任务路由器；file 可为 nil（仅 bindings + 活跃模型）
func NewRouter(profiles modelprofiles.Source, file *maibot.ModelConfigFile) *Router {
	return &Router{
		profiles: profiles,
		file:     file,
		bindings: make(map[TaskName]string),
	}
}

// SetModelConfigFile 热更新 model_config 快照
func (r *Router) SetModelConfigFile(file *maibot.ModelConfigFile) {
	r.file = file
}

// BindProfile 将任务固定到指定档案名（model_profiles.name）
func (r *Router) BindProfile(task TaskName, profileName string) {
	if r.bindings == nil {
		r.bindings = make(map[TaskName]string)
	}
	profileName = trimTask(profileName)
	if profileName == "" {
		delete(r.bindings, task)
		return
	}
	r.bindings[task] = profileName
}

// Resolve 解析任务对应的模型档案与客户端配置
func (r *Router) Resolve(ctx context.Context, task TaskName) (ResolvedRoute, error) {
	if r == nil || r.profiles == nil {
		return ResolvedRoute{}, fmt.Errorf("模型服务未初始化")
	}
	task, err := NormalizeTaskName(string(task))
	if err != nil {
		return ResolvedRoute{}, err
	}

	if name := r.bindings[task]; name != "" {
		profile, err := r.profileByName(ctx, name)
		if err != nil {
			return ResolvedRoute{}, err
		}
		cfg, err := r.modelConfigFromProfile(profile)
		if err != nil {
			return ResolvedRoute{}, err
		}
		taskCfg := r.taskConfigFor(task)
		cfg = withTaskEffectiveParametersFor(task, cfg, taskCfg)
		return ResolvedRoute{
			Task:       task,
			Source:     RouteBinding,
			Profile:    profile,
			Config:     cfg,
			TaskConfig: taskCfg,
		}, nil
	}

	taskCfg := r.taskConfigFor(task)
	effective := task
	if r.file != nil && task != TaskWebSearch {
		if resolved, err := ResolveTaskConfig(*r.file, task); err == nil {
			taskCfg = resolved.Config
			effective = resolved.Effective
			if modelName, ok := SelectModelName(resolved.Config); ok {
				if route, err := r.resolveMaiBotModel(ctx, task, effective, taskCfg, modelName); err == nil {
					return route, nil
				}
			}
		}
	}

	profile, err := r.profiles.Active(ctx)
	if err != nil {
		return ResolvedRoute{}, err
	}
	cfg, err := r.modelConfigFromProfile(profile)
	if err != nil {
		return ResolvedRoute{}, err
	}
	cfg = withTaskEffectiveParametersFor(task, cfg, taskCfg)
	return ResolvedRoute{
		Task:          task,
		EffectiveTask: effective,
		Source:        RouteActive,
		Profile:       profile,
		Config:        cfg,
		TaskConfig:    taskCfg,
	}, nil
}

// taskConfigFor 返回任务的 model_task_config；缺失时返回零值，由默认档补 0.7/4096
func (r *Router) taskConfigFor(task TaskName) maibot.TaskConfig {
	if r == nil || r.file == nil || task == TaskWebSearch {
		return maibot.TaskConfig{}
	}
	resolved, err := ResolveTaskConfig(*r.file, task)
	if err != nil {
		return maibot.TaskConfig{}
	}
	return resolved.Config
}

// NewEmbeddingClientForTask 按任务路由并创建嵌入客户端（embedding / image_embedding）
// 多候选时返回请求级 failover 包装器；单候选也由 failover 统一累计 token/penalty
func (r *Router) NewEmbeddingClientForTask(ctx context.Context, task TaskName) (ResolvedRoute, EmbeddingClient, error) {
	return r.newEmbeddingClientForTask(ctx, task, embeddingRequestMetadata{
		taskName:    task,
		requestType: string(task),
	})
}

// NewEmbeddingClientForTaskRequest 按任务路由嵌入客户端并指定统计用的 request_type/session_id
func (r *Router) NewEmbeddingClientForTaskRequest(
	ctx context.Context, task TaskName, requestType, sessionID string,
) (ResolvedRoute, EmbeddingClient, error) {
	return r.newEmbeddingClientForTask(ctx, task, embeddingRequestMetadata{
		taskName:    task,
		requestType: requestType,
		sessionID:   sessionID,
	})
}

// applyRouteTaskMetadata 把任务名写入配置，供失败快照与缓存统计使用
func applyRouteTaskMetadata(resolved *ResolvedRoute, task TaskName) {
	if resolved == nil {
		return
	}
	resolved.Config.TaskName = string(task)
	resolved.Config.RequestType = string(task)
}

// NewClientForTask 按任务路由并创建 eino Responses 客户端（主聊天等生成任务）
func (r *Router) NewClientForTask(ctx context.Context, task TaskName) (ResolvedRoute, model.AgenticModel, error) {
	resolved, err := r.Resolve(ctx, task)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	applyRouteTaskMetadata(&resolved, task)
	client, err := NewClient(resolved.Config)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	return resolved, withUsageTracking(r, usageModelName(resolved), resolved.Config, client, attemptForRoute(resolved)), nil
}

func (r *Router) modelConfigFromProfile(profile modelprofiles.Profile) (ModelConfig, error) {
	key, err := r.profiles.DecryptKey(profile)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("解密模型密钥失败: %w", err)
	}
	clientType := normalizeClientType(profile.Provider)
	provider, hasProvider := r.providerForProfile(profile)
	// 档案 provider 可能是 api_providers[].name；未知类型时用 Provider 的 client_type
	if hasProvider && (clientType == "" || (!supportedMaiBotClientTypes[clientType] && !pluginProviderAvailable(clientType))) {
		if providerType := normalizeClientType(provider.ClientType); providerType != "" {
			clientType = providerType
		}
	}
	if clientType == "" {
		clientType = "openai_responses"
	}
	cfg := ModelConfig{
		ClientType:       clientType,
		APIKey:           key,
		BaseURL:          profile.BaseURL,
		Model:            profile.ModelName,
		ModelDisplayName: strings.TrimSpace(profile.Name),
		ThinkingType:     profile.ThinkingType,
		ReasoningEffort:  profile.ReasoningEffort,
		ExtraParams:      nil,
		// 数据库档案没有图片限制字段；按 Python ModelInfo 默认值处理
		ImageMaxSide: 8000,
		ImageMinSide: 1,
		// 数据库档案没有 send_temperature 开关；按 Python ModelInfo 默认 true 处理
		SendTemperature:    true,
		SendTemperatureSet: true,
	}
	if hasProvider {
		cfg = applyProfileProviderDefaults(cfg, provider)
	}
	return cfg, nil
}

func (r *Router) profileByName(ctx context.Context, name string) (modelprofiles.Profile, error) {
	list, err := r.profiles.List(ctx)
	if err != nil {
		return modelprofiles.Profile{}, err
	}
	for _, row := range list {
		if row.Name == name {
			return row, nil
		}
	}
	return modelprofiles.Profile{}, newModelNotFoundError(name)
}

func (r *Router) adjustModelUsage(modelName string, delta ModelUsage) {
	if r == nil || modelName == "" || delta == (ModelUsage{}) {
		return
	}
	r.modelUsageMu.Lock()
	defer r.modelUsageMu.Unlock()
	if r.modelUsage == nil {
		r.modelUsage = make(map[string]ModelUsage)
	}
	current := r.modelUsage[modelName]
	current.TotalTokens += delta.TotalTokens
	current.Penalty += delta.Penalty
	current.UsagePenalty += delta.UsagePenalty
	r.modelUsage[modelName] = current
}

func (r *Router) modelUsageSnapshot() map[string]ModelUsage {
	if r == nil {
		return nil
	}
	r.modelUsageMu.Lock()
	defer r.modelUsageMu.Unlock()
	if len(r.modelUsage) == 0 {
		return nil
	}
	out := make(map[string]ModelUsage, len(r.modelUsage))
	for k, v := range r.modelUsage {
		out[k] = v
	}
	return out
}

// profileByMaiBotModelName 按 model_config 模型名选择档案：
// 先精确匹配档案 Name（Python 模型 name 唯一），未命中再看 ModelName 唯一命中；
// 多个档案共享同一 ModelName 时报歧义，避免静默选错档案
func (r *Router) profileByMaiBotModelName(ctx context.Context, maiBotModelName string) (modelprofiles.Profile, error) {
	list, err := r.profiles.List(ctx)
	if err != nil {
		return modelprofiles.Profile{}, err
	}
	var matched []modelprofiles.Profile
	for _, row := range list {
		if row.Name == maiBotModelName {
			return row, nil
		}
		if row.ModelName == maiBotModelName {
			matched = append(matched, row)
		}
	}
	switch len(matched) {
	case 0:
		return modelprofiles.Profile{}, newModelNotFoundError(maiBotModelName)
	case 1:
		return matched[0], nil
	default:
		names := make([]string, 0, len(matched))
		for _, row := range matched {
			names = append(names, row.Name)
		}
		return modelprofiles.Profile{}, fmt.Errorf(
			"模型标识 '%s' 对应多个模型档案（%s），请在任务配置中改用模型档案名称",
			maiBotModelName, strings.Join(names, "、"))
	}
}
