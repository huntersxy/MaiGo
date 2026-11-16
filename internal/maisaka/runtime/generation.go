package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"MaiGo/internal/agent"
	"MaiGo/internal/common/media"
	"MaiGo/internal/config"
	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/mcp_module"
	"MaiGo/internal/web_search"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

var ErrNoModel = fmt.Errorf("尚未配置模型")

// Generation 是一次可替换的模型、工具、MCP 和 ADK Agent 快照
//
// LLM 任务路由（对齐 MaiBot model_task_config，经 Router.NewClientForTaskWithFailover）：
//   - model / modelName：replyer，主聊天 groupAgent 与 privateAgent
//   - plannerModel / plannerModelName：planner，子代理单次调用（MaiBot run_sub_agent 默认任务）
//   - utilsModel / utilsModelName：utils，子代理 planner 不可用时的回退及后续 utils 子任务
//
// 压缩仍共用 replyer 模型；Planner/Replyer 分环未拆
type Generation struct {
	model            model.AgenticModel
	modelName        string
	contextWindow    int
	plannerModel     model.AgenticModel
	plannerModelName string
	utilsModel       model.AgenticModel
	utilsModelName   string
	groupAgent       *adk.TypedChatModelAgent[*schema.AgenticMessage]
	privateAgent     *adk.TypedChatModelAgent[*schema.AgenticMessage]
	groupTools       []tool.BaseTool
	privateTools     []tool.BaseTool
	mcp              *mcp.Manager
	media            *media.Manager

	// releaseMCP 释放进程级 MCP 服务租约，不关闭共享连接
	releaseMCP func()

	wg        sync.WaitGroup
	closeOnce sync.Once
}

func (g *Generation) close() {
	if g == nil {
		return
	}
	g.closeOnce.Do(func() {
		if g.releaseMCP != nil {
			g.releaseMCP()
			g.releaseMCP = nil
		}
	})
}

// buildGeneration 使用 LLM 任务路由（replyer/planner/utils 快照 + replyer 主聊天）或活跃档案构造完整运行态
func (a *Agent) buildGeneration() (*Generation, error) {
	cfg := config.Snapshot()
	chatModel, modelName, contextWindow, err := a.buildMainChatModel(a.ctx)
	if err != nil {
		return nil, err
	}
	plannerModel, plannerModelName := a.buildOptionalRoutedTaskModel(a.ctx, llm.TaskPlanner)
	utilsModel, utilsModelName := a.buildOptionalRoutedTaskModel(a.ctx, llm.TaskUtils)

	servers, clientCfg, err := a.mcpRuntimeConfig(a.ctx)
	if err != nil {
		return nil, err
	}
	// 进程级 MCP 共享服务：配置签名不变时复用连接，变化时原子替换
	mcpService := mcp.DefaultService()
	mcpService.SetClientConfig(clientCfg)
	// 先登记内置工具名，保证首次 EnsureInitialized 就按保留名避让
	mcpService.SetReservedToolNames(mcpBuiltinToolNames(a.ctx)...)
	mcpService.SetHostCallbacks(a.mcpHostCallbacks(
		chatModel, modelName,
		plannerModel, plannerModelName,
		utilsModel, utilsModelName,
	))
	if loadErr := mcpService.EnsureInitialized(a.ctx, servers); loadErr != nil {
		// 对齐 MaiBot allow_empty：部分服务器失败时不阻断 Agent，仅记录诊断
		zap.L().Warn("部分 MCP 服务器未能加载", zap.Error(loadErr))
	}
	websearch.SetMCPToolInvoker(mcpService)
	mcpManager, releaseMCP := mcpService.Lease()

	boot := config.Boot()
	mediaManager := media.New(media.Options{
		Dir:        boot.MediaDir,
		MaxBytesMB: cfg.Media.MaxBytesMB,
		Quality:    float32(cfg.Media.ImageQuality),
		NarrowEdge: cfg.Media.NarrowEdge,
	})
	groupTools, privateTools, err := a.buildToolLists(mcpManager)
	if err != nil {
		releaseMCP()
		return nil, err
	}
	generation := &Generation{
		model:            chatModel,
		modelName:        modelName,
		contextWindow:    contextWindow,
		plannerModel:     plannerModel,
		plannerModelName: plannerModelName,
		utilsModel:       utilsModel,
		utilsModelName:   utilsModelName,
		groupTools:       groupTools,
		privateTools:     privateTools,
		mcp:              mcpManager,
		media:            mediaManager,
		releaseMCP:       releaseMCP,
	}
	if plannerModelName != "" || utilsModelName != "" {
		zap.L().Debug("generation 任务模型快照",
			zap.String("replyer", modelName),
			zap.String("planner", plannerModelName),
			zap.String("utils", utilsModelName))
	}
	generation.groupAgent, err = a.newChatAgent(generation, groupTools, a.passthroughModelInput)
	if err != nil {
		releaseMCP()
		return nil, err
	}
	generation.privateAgent, err = a.newChatAgent(generation, privateTools, a.passthroughModelInput)
	if err != nil {
		releaseMCP()
		return nil, err
	}
	a.wireLearnersEmbedding(a.ctx)
	a.wireNativeImageEmbedding()
	a.wireLearnersJargonRouter()
	a.wireLearnersExpressionRouter()
	a.wirePersonFactRouter()
	return generation, nil
}

// newChatAgent 两种会话共用步数、错误处理与执行约束，工具清单各自独立
func (a *Agent) newChatAgent(generation *Generation, chatTools []tool.BaseTool, genModelInput adk.TypedGenModelInput[*schema.AgenticMessage]) (*adk.TypedChatModelAgent[*schema.AgenticMessage], error) {
	maxStep := config.Snapshot().Agent.MaxStep
	if maxStep <= 0 {
		maxStep = 50
	}
	argumentsHandler, err := tools.NewToolArgumentsHandler(a.ctx, chatTools)
	if err != nil {
		return nil, err
	}
	return adk.NewTypedChatModelAgent[*schema.AgenticMessage](a.ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Model:         generation.model,
		GenModelInput: genModelInput,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:                chatTools,
				ExecuteSequentially:  true,
				UnknownToolsHandler:  tools.UnknownToolHandler,
				ToolArgumentsHandler: argumentsHandler,
				ToolCallMiddlewares: []compose.ToolMiddleware{
					{Invokable: tools.ToolErrorMiddleware()},
					tools.ToolTranscriptMiddleware(),
				},
			},
			ReturnDirectly: map[string]bool{"stayQuiet": true, "stop": true, "wait": true},
		},
		MaxIterations: maxStep,
		Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{
			&sessionInjector{TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{}, agent: a},
			&sessionPersister{TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{}},
		},
	})
}

func (a *Agent) buildToolLists(mcpManager *mcp.Manager) ([]tool.BaseTool, []tool.BaseTool, error) {
	agentFileBuilders := a.agentFileToolBuilders()
	groupTools, err := buildTools(mcpManager, append(groupToolBuilders(), agentFileBuilders...))
	if err != nil {
		return nil, nil, err
	}
	privateTools, err := buildTools(mcpManager, append(privateToolBuilders(), agentFileBuilders...))
	if err != nil {
		return nil, nil, err
	}
	seenTools := map[string]struct{}{}
	for _, list := range [][]tool.BaseTool{groupTools, privateTools} {
		for _, item := range list {
			info, infoErr := item.Info(a.ctx)
			if infoErr != nil || info == nil || strings.TrimSpace(info.Name) == "" {
				continue
			}
			seenTools[strings.TrimSpace(info.Name)] = struct{}{}
		}
	}
	if pluginTools := a.buildPluginTools(seenTools); len(pluginTools) > 0 {
		groupTools = append(groupTools, pluginTools...)
		privateTools = append(privateTools, pluginTools...)
	}
	return groupTools, privateTools, nil
}

func buildTools(mcpManager *mcp.Manager, builders []func() (tool.BaseTool, error)) ([]tool.BaseTool, error) {
	list := make([]tool.BaseTool, 0, len(builders)+8)
	for _, build := range builders {
		item, err := build()
		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	if mcpManager != nil {
		list = append(list, mcpManager.GetTools()...)
	}
	return list, nil
}

// agentFileToolBuilders 按 Agent 与文件能力开关注册 agent_*/files_* 工具
func (a *Agent) agentFileToolBuilders() []func() (tool.BaseTool, error) {
	instance := a.instanceBotConfig()
	if instance == nil {
		return nil
	}
	return tools.AgentFileToolBuilders(instance.Agent.Enabled, instance.Capabilities.Files, a.agentChatPolicy())
}

// agentChatPolicy 按实例配置构造外接 Agent 的聊天白名单
func (a *Agent) agentChatPolicy() tools.AgentChatPolicy {
	policy := tools.AgentChatPolicy{AgentID: agent.DefaultAgentID}
	if instance := a.instanceBotConfig(); instance != nil {
		policy.AllowUsers = instance.Agent.AllowUsers
		policy.AllowGroups = instance.Agent.AllowGroups
	}
	return policy
}

func appendMaisakaTools(builders []func() (tool.BaseTool, error)) []func() (tool.BaseTool, error) {
	builders = append(builders, tools.MaisakaCompatToolBuilders()...)
	return append(builders, tools.WebToolBuilders()...)
}

// groupToolBuilders 返回群聊可见工具，只包含 MaiBot 同名能力
func groupToolBuilders() []func() (tool.BaseTool, error) {
	return appendMaisakaTools(nil)
}

// privateToolBuilders 返回私聊可见工具；桌宠工具在执行时自行校验主人会话
func privateToolBuilders() []func() (tool.BaseTool, error) {
	builders := appendMaisakaTools(nil)
	return append(builders, tools.DesktopPetToolBuilders()...)
}

func generationFrom(ctx context.Context) *Generation {
	generation, _ := ctx.Value(generationKey{}).(*Generation)
	return generation
}

type generationKey struct{}

func withGeneration(ctx context.Context, generation *Generation) context.Context {
	return context.WithValue(ctx, generationKey{}, generation)
}

func (a *Agent) contextWindow() int {
	if generation := a.currentGeneration(); generation != nil {
		return generation.contextWindow
	}
	return 0
}
