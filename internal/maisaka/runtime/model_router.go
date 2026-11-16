package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/learners"
	"MaiGo/internal/llm_models"

	"github.com/cloudwego/eino/components/model"
	"go.uber.org/zap"
)

// ErrModelRouterUnavailable 模型路由器未配置；显式模型直选不允许静默回退直连
var ErrModelRouterUnavailable = errors.New("未配置模型路由器")

// SetInstanceHolder 绑定 MaiBot 实例 TOML；model_config 热重载时会刷新路由并重建 generation
func (a *Agent) SetInstanceHolder(holder *maibotcfg.InstanceHolder) {
	if a == nil || holder == nil {
		return
	}
	a.instanceMu.Lock()
	a.instanceHolder = holder
	a.syncModelConfigToRouterLocked()
	a.instanceMu.Unlock()

	if cfg := holder.Config(); cfg != nil {
		a.applyReplyTimingFromInstance(cfg)
	}
	a.wireLearnersEmbedding(context.Background())
	a.wireNativeImageEmbedding()
	a.wireLearnersJargonRouter()
	a.wireLearnersExpressionRouter()
	a.wirePersonFactRouter()
	holder.RegisterReloadCallback(a.onInstanceConfigReload)
}

func (a *Agent) onInstanceConfigReload(cfg *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
	if a == nil || cfg == nil {
		return
	}
	modelChanged := false
	botChanged := false
	for _, scope := range scopes {
		switch scope {
		case maibotcfg.ReloadScopeModel:
			modelChanged = true
		case maibotcfg.ReloadScopeBot:
			botChanged = true
		}
	}
	if botChanged {
		a.applyReplyTimingFromInstance(cfg)
		// bot_config 可能切换 use_vector_expression 或索引路径，需要重新启动或唤醒补建循环
		reg := learners.DefaultRegistry()
		// bot 作用域只关心学习配置签名；embedding 路由签名由 wireLearnersEmbedding 负责
		options := learners.ExpressionBackfillOptions{Settings: reg.VectorSettingsProvider()}
		if err := reg.StartExpressionVectorBackfill(a.ctx, options); err != nil {
			zap.L().Warn("表达向量历史补建热重载失败", zap.Error(err))
		}
	}
	if !modelChanged {
		return
	}
	a.instanceMu.Lock()
	if a.llmRouter != nil {
		a.llmRouter.SetModelConfigFile(&cfg.Model)
	}
	a.instanceMu.Unlock()
	a.wireLearnersEmbedding(context.Background())
	a.wireNativeImageEmbedding()
	a.wireLearnersJargonRouter()
	a.wireLearnersExpressionRouter()
	a.wirePersonFactRouter()
	if err := a.Reload(); err != nil {
		zap.L().Warn("model_config 热重载后重建 Agent generation 失败", zap.Error(err))
	}
}

func (a *Agent) syncModelConfigToRouterLocked() {
	if a == nil || a.llmRouter == nil {
		return
	}
	var file *maibotcfg.ModelConfigFile
	if a.instanceHolder != nil {
		if cfg := a.instanceHolder.Config(); cfg != nil {
			file = &cfg.Model
		}
	}
	a.llmRouter.SetModelConfigFile(file)
}

func (a *Agent) llmRouterSnapshot() *llm.Router {
	if a == nil {
		return nil
	}
	a.instanceMu.RLock()
	defer a.instanceMu.RUnlock()
	return a.llmRouter
}

// LLMRouter 返回任务路由器，供 emojisystem VLM 等子系统共用同一实例
func (a *Agent) LLMRouter() *llm.Router {
	return a.llmRouterSnapshot()
}

func generationModelMeta(resolved llm.ResolvedRoute) (modelName string, contextWindow int) {
	if resolved.Profile.ID != 0 || strings.TrimSpace(resolved.Profile.ModelName) != "" {
		return resolved.Profile.ModelName, resolved.Profile.ContextWindow
	}
	return strings.TrimSpace(resolved.Config.Model), 0
}

// buildRoutedTaskModel 按 model_task_config 任务名创建客户端（含 model_list 候选 failover）
func (a *Agent) buildRoutedTaskModel(ctx context.Context, task llm.TaskName) (model.AgenticModel, string, int, error) {
	router := a.llmRouterSnapshot()
	if router == nil {
		return nil, "", 0, fmt.Errorf("LLM 路由器未接入")
	}
	resolved, client, err := router.NewClientForTaskWithFailover(ctx, task)
	if err != nil {
		return nil, "", 0, err
	}
	name, window := generationModelMeta(resolved)
	if name == "" {
		return nil, "", 0, fmt.Errorf("任务 %q 路由结果缺少 model 名称", task)
	}
	zap.L().Debug("任务模型已路由",
		zap.String("task", string(resolved.Task)),
		zap.String("effective", string(resolved.EffectiveTask)),
		zap.String("source", string(resolved.Source)),
		zap.String("model", name))
	return client, name, window, nil
}

// buildOptionalRoutedTaskModel 非主路径任务；失败时返回 nil 客户端且不阻断 generation
func (a *Agent) buildOptionalRoutedTaskModel(ctx context.Context, task llm.TaskName) (model.AgenticModel, string) {
	client, name, _, err := a.buildRoutedTaskModel(ctx, task)
	if err != nil {
		if !errors.Is(err, modelprofiles.ErrNotFound) {
			zap.L().Debug("可选任务模型未就绪", zap.String("task", string(task)), zap.Error(err))
		}
		return nil, ""
	}
	return client, name
}

// buildMainChatModel 主聊天 ADK 使用 replyer 任务路由；无 model_config/bindings 时 Router 回退活跃档案
// 即使 llmRouter 尚未注入，也在这里惰性创建 Router，确保活跃档案单模型路径同样经过
// usageModel/attemptRunner，而不是绕过 Router 直连 NewClient
func (a *Agent) buildMainChatModel(ctx context.Context) (model.AgenticModel, string, int, error) {
	if a.llmRouterSnapshot() == nil {
		if a.modelStore == nil {
			return nil, "", 0, ErrNoModel
		}
		router := llm.NewRouter(a.modelStore, nil)
		a.instanceMu.Lock()
		if a.llmRouter == nil {
			a.llmRouter = router
			a.syncModelConfigToRouterLocked()
		} else {
			router = a.llmRouter
		}
		a.instanceMu.Unlock()
		if router == nil {
			return nil, "", 0, ErrNoModel
		}
	}
	client, name, window, err := a.buildRoutedTaskModel(ctx, llm.TaskReplyer)
	if err != nil {
		if errors.Is(err, modelprofiles.ErrNotFound) {
			return nil, "", 0, ErrNoModel
		}
		return nil, "", 0, err
	}
	return client, name, window, nil
}
