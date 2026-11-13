package scripts

// 脚本侧 LLM 网关，对齐 process_knowledge.py 的 _select_model 与 _llm_call
// 复用 llm.Router + modelprofiles.StaticStore，不重写模型客户端内核

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/pelletier/go-toml/v2"

	"maibot-go/internal/A_memorix"
	"maibot-go/internal/A_memorix/core/utils"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/config/model_profiles"
	"maibot-go/internal/llm_models"
)

// scriptLLMGateway 脚本生成入口，按任务路由并复用重试与 JSON 解析
type scriptLLMGateway struct {
	router      *llm.Router
	client      model.AgenticModel
	taskName    llm.TaskName
	taskConfig  utils.TextGenerationTaskConfig
	temperature *float64
	maxTokens   *int
	logger      utils.Logger
}

// newScriptLLMGateway 构造脚本 LLM 网关，无可路由模型时返回 No LLM models
func newScriptLLMGateway(ctx context.Context, pluginConfig map[string]any, logger utils.Logger) (*scriptLLMGateway, error) {
	logger = utils.OrLogger(logger)
	file, err := loadScriptModelConfig()
	if err != nil {
		return nil, errors.New("No LLM models")
	}
	store, ok := scriptStaticStore(*file)
	if !ok {
		return nil, errors.New("No LLM models")
	}
	router := llm.NewRouter(store, file)
	available := scriptTextTasks(file)
	selector := strings.TrimSpace(pyString(NestedGet(pluginConfig, "advanced.extraction_model", "auto")))
	if selector == "" {
		selector = "auto"
	}
	taskName := ""
	var taskConfig utils.TextGenerationTaskConfig
	if selector != "auto" {
		if name, config, _, found := utils.ResolveTextGenerationModelSelector(available, selector); found {
			taskName = name
			taskConfig = config
		} else {
			logger.Warning(fmt.Sprintf("advanced.extraction_model=%q 不可用于文本生成，已回退自动选择", selector))
		}
	}
	if taskName == "" {
		name, config, found := utils.PickTextGenerationTask(available, utils.AMemorixTextTaskPriority())
		if !found {
			return nil, errors.New("No LLM models")
		}
		taskName = name
		taskConfig = config
	}

	resolved, client, err := router.NewClientForTaskWithFailover(ctx, llm.TaskName(taskName))
	if err != nil {
		return nil, err
	}
	gateway := &scriptLLMGateway{
		router:     router,
		client:     client,
		taskName:   resolved.Task,
		taskConfig: taskConfig,
		logger:     logger,
	}
	if taskConfig.Temperature > 0 {
		value := taskConfig.Temperature
		gateway.temperature = &value
	}
	if taskConfig.MaxTokens > 0 {
		value := taskConfig.MaxTokens
		gateway.maxTokens = &value
	}
	return gateway, nil
}

// completion 对齐 _llm_call：最多 3 次重试并解析 JSON 响应
func (g *scriptLLMGateway) completion(ctx context.Context, prompt string) (map[string]any, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := g.callOnce(ctx, prompt)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt >= 3 {
			break
		}
		sleep := time.Duration(1<<uint(attempt)) * time.Second
		if sleep > 10*time.Second {
			sleep = 10 * time.Second
		}
		g.logger.Warning(fmt.Sprintf("LLM 调用即将重试: attempt=%d next_sleep=%s error=%v", attempt, sleep, err))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
	}
	return nil, lastErr
}

// callOnce 单次生成并解析 JSON
func (g *scriptLLMGateway) callOnce(ctx context.Context, prompt string) (map[string]any, error) {
	message, err := g.client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(prompt)})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(agenticPlainText(message))
	if text == "" {
		return nil, errors.New("LLM generation failed")
	}
	if strings.Contains(text, "```") {
		if index := strings.LastIndex(text, "```json"); index >= 0 {
			text = strings.SplitN(text[index+len("```json"):], "```", 2)[0]
		} else {
			text = strings.SplitN(text, "```", 3)[1]
		}
		text = strings.TrimSpace(text)
	}
	var parsed map[string]any
	if err := UnmarshalJSON([]byte(text), &parsed); err == nil {
		return parsed, nil
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, errors.New("LLM response is not valid JSON")
	}
	if err := UnmarshalJSON([]byte(text[start:end+1]), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// loadScriptModelConfig 加载实例 model_config.toml
func loadScriptModelConfig() (*maibotcfg.ModelConfigFile, error) {
	candidates := make([]string, 0, 3)
	if path := strings.TrimSpace(os.Getenv("MAIBOT_MODEL_CONFIG_PATH")); path != "" {
		candidates = append(candidates, ExpandUser(path))
	}
	instanceRoot := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if instanceRoot == "" {
		instanceRoot = memorix.RepoRoot()
	}
	candidates = append(candidates, filepath.Join(instanceRoot, "config", "model_config.toml"))
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var file maibotcfg.ModelConfigFile
		if err := toml.Unmarshal(raw, &file); err != nil {
			continue
		}
		if len(file.ApiProviders) == 0 {
			continue
		}
		return &file, nil
	}
	return nil, errors.New("model_config.toml not found")
}

// scriptStaticStore 从首个可用 provider 构造静态档案，对齐 tryMaisakaLLM 的回退逻辑
func scriptStaticStore(file maibotcfg.ModelConfigFile) (modelprofiles.Source, bool) {
	for _, provider := range file.ApiProviders {
		key := strings.TrimSpace(provider.ApiKey)
		base := strings.TrimSpace(provider.BaseUrl)
		if key == "" || base == "" {
			continue
		}
		clientType := strings.TrimSpace(provider.ClientType)
		if clientType == "" {
			clientType = "openai_responses"
		}
		modelID := ""
		for _, model := range file.Models {
			if strings.TrimSpace(model.ApiProvider) == strings.TrimSpace(provider.Name) {
				if id := strings.TrimSpace(model.ModelIdentifier); id != "" {
					modelID = id
					break
				}
			}
		}
		if modelID == "" {
			continue
		}
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			name = "script-model"
		}
		store, err := modelprofiles.NewStaticStore(name, clientType, base, modelID, key)
		if err != nil {
			continue
		}
		return store, true
	}
	return nil, false
}

// scriptTextTasks 收集 model_task_config 中的文本生成任务
func scriptTextTasks(file *maibotcfg.ModelConfigFile) map[string]utils.TextGenerationTaskConfig {
	tasks := map[string]utils.TextGenerationTaskConfig{}
	add := func(name string, config maibotcfg.TaskConfig) {
		tasks[name] = utils.TextGenerationTaskConfig{
			ModelList:         append([]string{}, config.ModelList...),
			MaxTokens:         config.MaxTokens,
			Temperature:       config.Temperature,
			SelectionStrategy: config.SelectionStrategy,
			HardTimeout:       config.HardTimeout,
		}
	}
	add("replyer", file.ModelTaskConfig.Replyer)
	add("planner", file.ModelTaskConfig.Planner)
	add("memory", file.ModelTaskConfig.Memory)
	add("utils", file.ModelTaskConfig.Utils)
	add("learner", file.ModelTaskConfig.Learner)
	add("fast_model", file.ModelTaskConfig.FastModel)
	add("emoji", file.ModelTaskConfig.Emoji)
	return utils.GetTextGenerationModelTasks(tasks, false)
}

// agenticPlainText 提取 AgenticMessage 文本
func agenticPlainText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				builder.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				builder.WriteString(block.UserInputText.Text)
			}
		}
	}
	return strings.TrimSpace(builder.String())
}
