package webuiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/llm_models"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/pelletier/go-toml/v2"
)

const reasoningReplayTimeout = 180 * time.Second

type llmReasoningReplay struct {
	router      func() *llm.Router
	instanceDir string
}

func (d Deps) reasoningReplayService() ReasoningReplayService {
	if d.ReasoningReplay != nil {
		return d.ReasoningReplay
	}
	if d.Agent == nil || d.Agent.LLMRouter() == nil {
		return nil
	}
	agentRef := d.Agent
	return &llmReasoningReplay{
		router:      func() *llm.Router { return agentRef.LLMRouter() },
		instanceDir: d.InstanceDir,
	}
}

type replayTaskEntry struct {
	name      llm.TaskName
	modelList []string
}

func replayTaskEntries(config *maibotcfg.ModelConfigFile) []replayTaskEntry {
	tasks := config.ModelTaskConfig
	return []replayTaskEntry{
		{llm.TaskWebSearch, tasks.WebSearch.ModelList},
		{llm.TaskReplyer, tasks.Replyer.ModelList},
		{llm.TaskPlanner, tasks.Planner.ModelList},
		{llm.TaskMemory, tasks.Memory.ModelList},
		{llm.TaskUtils, tasks.Utils.ModelList},
		{llm.TaskLearner, tasks.Learner.ModelList},
		{llm.TaskFastModel, tasks.FastModel.ModelList},
		{llm.TaskEmoji, tasks.Emoji.ModelList},
		{llm.TaskVLM, tasks.Vlm.ModelList},
		{llm.TaskVoice, tasks.Voice.ModelList},
		{llm.TaskEmbedding, tasks.Embedding.ModelList},
		{llm.TaskImageEmbedding, tasks.ImageEmbedding.ModelList},
	}
}

func (r *llmReasoningReplay) loadModelConfig() (*maibotcfg.ModelConfigFile, error) {
	root := strings.TrimSpace(r.instanceDir)
	if root == "" {
		return nil, &ReasoningReplayHTTPError{Status: http.StatusServiceUnavailable, Message: "推理重放缺少实例目录，无法读取 model_config.toml"}
	}
	path := filepath.Join(root, "config", "model_config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ReasoningReplayHTTPError{Status: http.StatusInternalServerError, Message: "读取模型配置失败: " + err.Error()}
	}
	var config maibotcfg.ModelConfigFile
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, &ReasoningReplayHTTPError{Status: http.StatusInternalServerError, Message: "解析模型配置失败: " + err.Error()}
	}
	return &config, nil
}

func replayModelExists(config *maibotcfg.ModelConfigFile, modelName string) bool {
	for _, item := range config.Models {
		if strings.TrimSpace(item.Name) == modelName {
			return true
		}
	}
	return false
}

func replayTaskFor(config *maibotcfg.ModelConfigFile, stage, modelName string) (llm.TaskName, bool, error) {
	tasks := replayTaskEntries(config)
	stage = strings.TrimSpace(stage)

	var stageEntry *replayTaskEntry
	if stage != "" {
		if name, err := llm.NormalizeTaskName(stage); err == nil {
			for index := range tasks {
				if tasks[index].name == name {
					stageEntry = &tasks[index]
					break
				}
			}
		}
	}
	if stageEntry != nil && (len(stageEntry.modelList) == 0 || replayTaskContainsModel(stageEntry.modelList, modelName)) {
		return stageEntry.name, true, nil
	}
	for _, task := range tasks {
		if replayTaskContainsModel(task.modelList, modelName) {
			return task.name, true, nil
		}
	}
	if stageEntry != nil {
		return "", false, &ReasoningReplayHTTPError{
			Status:  http.StatusConflict,
			Message: fmt.Sprintf("重放模型与任务路由不一致：任务 %s 未配置模型 %s，且没有其他任务包含该模型", stageEntry.name, modelName),
		}
	}
	if len(tasks) == 0 {
		return "", false, &ReasoningReplayHTTPError{Status: http.StatusInternalServerError, Message: "没有可用的模型任务配置"}
	}
	return tasks[0].name, false, nil
}

func replayTaskContainsModel(models []string, modelName string) bool {
	for _, name := range models {
		if strings.TrimSpace(name) == modelName {
			return true
		}
	}
	return false
}

func parseReplayToolDefinitions(raws []json.RawMessage) ([]*schema.ToolInfo, error) {
	tools := make([]*schema.ToolInfo, 0, len(raws))
	for index, raw := range raws {
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("第 %d 个工具定义不是 JSON 对象", index+1)
		}
		info := &schema.ToolInfo{}
		info.Name = strings.TrimSpace(stringValue(payload["name"]))
		info.Desc = strings.TrimSpace(stringValue(payload["description"]))
		if nested, ok := payload["function"].(map[string]any); ok {
			if info.Name == "" {
				info.Name = strings.TrimSpace(stringValue(nested["name"]))
			}
			if info.Desc == "" {
				info.Desc = strings.TrimSpace(stringValue(nested["description"]))
			}
			if _, ok := payload["parameters"]; !ok {
				payload["parameters"] = nested["parameters"]
			}
		}
		if info.Name == "" {
			return nil, fmt.Errorf("工具定义缺少 name")
		}
		rawParams := payload["parameters"]
		if rawParams == nil {
			rawParams = payload["params"]
		}
		if rawParams == nil {
			rawParams = payload["input_schema"]
		}
		params, _ := rawParams.(map[string]any)
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("序列化工具 %s 参数失败: %w", info.Name, err)
		}
		var parameterSchema jsonschema.Schema
		if len(encoded) > 0 && string(encoded) != "null" {
			if err := json.Unmarshal(encoded, &parameterSchema); err != nil {
				return nil, fmt.Errorf("工具 %s 参数 schema 无效: %w", info.Name, err)
			}
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&parameterSchema)
		tools = append(tools, info)
	}
	return tools, nil
}

func (r *llmReasoningReplay) ReplayReasoning(ctx context.Context, request ReasoningReplayRequest) (ReasoningReplayResult, error) {
	result := ReasoningReplayResult{ModelName: request.ModelName, SchemaVersion: 6, GenerationAttempts: []map[string]any{}}
	router := r.router()
	if router == nil {
		return result, &ReasoningReplayHTTPError{Status: http.StatusServiceUnavailable, Message: "推理重放需要已接线的 LLM 路由器"}
	}
	config, err := r.loadModelConfig()
	if err != nil {
		return result, err
	}
	if !replayModelExists(config, request.ModelName) {
		return result, &ReasoningReplayHTTPError{Status: http.StatusNotFound, Message: "未找到模型: " + request.ModelName}
	}
	task, _, err := replayTaskFor(config, request.Stage, request.ModelName)
	if err != nil {
		return result, err
	}
	items, err := deserializeReplayItems(request.RequestItems, r.instanceDir)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	messages, err := replayMessages(items)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if len(messages) == 0 {
		result.Error = "重放 Items 没有可发送给模型的内容"
		return result, nil
	}
	tools, err := parseReplayToolDefinitions(request.ToolDefinitions)
	if err != nil {
		result.Error = "工具定义无效: " + err.Error()
		return result, nil
	}
	resolved, client, err := router.NewClientForTask(ctx, task)
	if err != nil {
		return result, &ReasoningReplayHTTPError{Status: http.StatusInternalServerError, Message: "推理重放模型路由失败: " + err.Error()}
	}
	routeName := strings.TrimSpace(resolved.Profile.ModelName)
	if resolved.MaiBotModel != "" {
		routeName = resolved.MaiBotModel
	}
	if routeName == "" {
		routeName = strings.TrimSpace(resolved.Config.Model)
	}
	if routeName != "" && routeName != request.ModelName && strings.TrimSpace(resolved.Config.Model) != request.ModelName {
		return result, &ReasoningReplayHTTPError{
			Status:  http.StatusConflict,
			Message: fmt.Sprintf("重放模型与任务路由不一致：请求 %s，任务 %s 路由到 %s", request.ModelName, task, routeName),
		}
	}
	options := make([]model.Option, 0, 4)
	if len(tools) > 0 {
		options = append(options, model.WithTools(tools))
	}
	if request.Temperature != nil {
		options = append(options, model.WithTemperature(float32(*request.Temperature)))
	}
	if request.MaxTokens != nil {
		options = append(options, model.WithMaxTokens(*request.MaxTokens))
	}
	started := time.Now()
	generateCtx, cancel := context.WithTimeout(ctx, reasoningReplayTimeout)
	defer cancel()
	message, err := client.Generate(generateCtx, messages, options...)
	result.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		result.Error = "LLM 调用失败: " + err.Error()
		return result, nil
	}
	output, err := replayOutputItems(message, time.Now())
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	result.Success = true
	result.OutputItems = output
	result.ModelName = request.ModelName
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		usage := message.ResponseMeta.TokenUsage
		result.PromptTokens = usage.PromptTokens
		result.CompletionTokens = usage.CompletionTokens
		result.TotalTokens = usage.TotalTokens
		result.PromptCacheHit = usage.PromptTokenDetails.CachedTokens
		result.PromptCacheMiss = result.PromptTokens - result.PromptCacheHit
		if result.PromptCacheMiss < 0 {
			result.PromptCacheMiss = 0
		}
	}
	return result, nil
}

func (d Deps) loadReplayToolDefinitions(sourcePath string) ([]json.RawMessage, error) {
	root := strings.TrimSpace(d.InstanceDir)
	if root == "" {
		return nil, fmt.Errorf("实例目录未配置")
	}
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(sourcePath)))
	if filepath.IsAbs(cleaned) || cleaned == "." || strings.Contains(cleaned, "..") {
		return nil, fmt.Errorf("路径不合法")
	}
	if strings.ToLower(filepath.Ext(cleaned)) != ".json" {
		return nil, fmt.Errorf("不支持的文件类型")
	}
	base := filepath.Join(root, "logs", "maisaka_prompt")
	full := filepath.Join(base, cleaned)
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("路径不合法")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("文件不存在")
		}
		return nil, fmt.Errorf("读取推理日志失败: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("推理日志不是合法 JSON")
	}
	rawDefinitions, ok := payload["tool_definitions"].([]any)
	if !ok {
		return []json.RawMessage{}, nil
	}
	definitions := make([]json.RawMessage, 0, len(rawDefinitions))
	for _, raw := range rawDefinitions {
		if _, ok := raw.(map[string]any); !ok {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		definitions = append(definitions, encoded)
	}
	return definitions, nil
}
