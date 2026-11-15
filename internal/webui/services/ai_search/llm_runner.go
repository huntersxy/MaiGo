package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka/builtin_tool"
)

const (
	searchIndexToolName = "search_webui_index"
	searchIndexMaxLimit = 10
	searchIndexDefault  = 6
	searchMaxRounds     = 4
)

// LLMRunner 使用 utils 任务模型对 WebUI 候选目录做真实检索与选择
type LLMRunner struct {
	router            func() *llm.Router
	documentStore     *tools.AISearchDocumentStore
	localConfigReader *tools.LocalConfigReader
}

// NewLLMRunner 构造真实模型执行器；router 在每次执行时读取，支持热更新
func NewLLMRunner(router func() *llm.Router) *LLMRunner {
	return NewLLMRunnerWithOptions(router, LLMRunnerOptions{})
}

// Run 执行有限轮次工具调用并解析最终 JSON
func (r *LLMRunner) Run(ctx context.Context, request Request, emit Emit) (Response, error) {
	if r == nil || r.router == nil {
		return Response{}, &UnavailableError{Message: "AI 搜索服务未接线：需要可注入的 SearchRunner 或 LLM 路由器"}
	}
	router := r.router()
	if router == nil {
		return Response{}, &UnavailableError{Message: "AI 搜索服务未接线：LLM 路由器为空"}
	}
	route, client, err := router.NewClientForTask(ctx, llm.TaskUtils)
	if err != nil {
		return Response{}, &UnavailableError{Message: "AI 搜索模型不可用: " + err.Error()}
	}
	if emit == nil {
		emit = func(ProgressEvent) {}
	}
	r.officialDocsStore().PrewarmOfficialDocs()
	emit(NewProgressEvent("start"))
	toolInfos := buildSearchToolInfos()
	// 对齐 Python：单条 user 提示词由 webui_ai_search 模板渲染
	messages := []*schema.AgenticMessage{
		schema.UserAgenticMessage(buildSearchUserPrompt(request)),
	}
	totalTokens := 0
	groundingParts := []string{}
	readSourceIDs := make(map[string]struct{})
	usedLocalConfig := false
	var answer string
	for round := 1; round <= searchMaxRounds; round++ {
		roundValue := round
		event := NewProgressEvent("planning")
		event.Round = &roundValue
		emit(event)
		response, err := client.Generate(ctx, messages, model.WithTools(toolInfos), model.WithMaxTokens(2048), model.WithTemperature(0.2))
		if err != nil {
			return Response{}, fmt.Errorf("AI 搜索调用失败: %w", err)
		}
		totalTokens += tokenUsage(response)
		answer = MessageText(response)
		calls := toolCalls(response)
		if len(calls) == 0 {
			break
		}
		if len(calls) > MaxToolCallsPerRound {
			calls = calls[:MaxToolCallsPerRound]
		}
		messages = append(messages, assistantToolMessage(calls))
		for _, call := range calls {
			started := NewProgressEvent("tool")
			started.Status = "started"
			started.Tool = call.Name
			started.Query = call.Query
			started.Targets = searchToolTargets(call.Paths)
			emit(started)
			execution := r.executeSearchTool(ctx, call, request.Candidates)
			if execution.Fatal != nil {
				return Response{}, execution.Fatal
			}
			encoded, _ := json.Marshal(execution.Payload)
			if execution.Error == "" {
				groundingParts = append(groundingParts, string(encoded))
			}
			messages = append(messages, toolResultMessage(call, string(encoded)))
			for _, sourceID := range execution.SourceIDs {
				readSourceIDs[sourceID] = struct{}{}
			}
			usedLocalConfig = usedLocalConfig || call.Name == readLocalConfigToolName
			completed := NewProgressEvent("tool")
			completed.Status = "completed"
			completed.Tool = call.Name
			completed.Query = call.Query
			completed.Targets = execution.Targets
			if execution.Error != "" {
				completed.Status = "failed"
				completed.Error = execution.Error
			}
			if execution.HasCount {
				count := execution.Count
				completed.Count = &count
			}
			completed.Titles = append(completed.Titles, execution.Titles...)
			emit(completed)
		}
	}
	output, err := parseModelOutput(answer)
	// 对齐 Python：定稿与纠错请求都不带工具，改用证据消息承载已读资料
	evidenceMessages := buildEvidenceMessages(messages, NormalizeLanguage(request.Language))
	if err != nil {
		emit(NewProgressEvent("finalizing"))
		finalMessages := append(append([]*schema.AgenticMessage{}, evidenceMessages...),
			schema.UserAgenticMessage(aiSearchPromptText("webui_ai_search_final", NormalizeLanguage(request.Language), nil)))
		response, finalErr := client.Generate(ctx, finalMessages, model.WithMaxTokens(2048), model.WithTemperature(0.2))
		if finalErr != nil {
			return Response{}, fmt.Errorf("AI 搜索调用失败: %w", finalErr)
		}
		totalTokens += tokenUsage(response)
		output, err = parseModelOutput(MessageText(response))
		if err != nil {
			return Response{}, err
		}
	}
	normalized := NormalizeOutput(output, request.Candidates)
	groundingError := ""
	evidence := strings.Join(append([]string{request.Query}, groundingParts...), "\n")
	if groundingErr := ValidateOutputEvidence(normalized, evidence); groundingErr != nil {
		started := NewProgressEvent("correcting")
		started.Status = "started"
		started.Error = groundingErr.Error()
		emit(started)
		correctionMessages := append(append([]*schema.AgenticMessage{}, evidenceMessages...),
			schema.UserAgenticMessage(aiSearchPromptText("webui_ai_search_correction", NormalizeLanguage(request.Language),
				map[string]string{"error": groundingErr.Error()})))
		correctionResponse, correctionErr := client.Generate(ctx, correctionMessages, model.WithMaxTokens(2048), model.WithTemperature(0.2))
		if correctionErr != nil {
			return Response{}, fmt.Errorf("AI 搜索调用失败: %w", correctionErr)
		}
		totalTokens += tokenUsage(correctionResponse)
		corrected, correctionParseErr := parseModelOutput(MessageText(correctionResponse))
		if correctionParseErr != nil {
			return Response{}, correctionParseErr
		}
		normalized = NormalizeOutput(corrected, request.Candidates)
		if secondErr := ValidateOutputEvidence(normalized, evidence); secondErr != nil {
			groundingError = "AI 回答证据校验失败: " + secondErr.Error()
			failed := NewProgressEvent("correcting")
			failed.Status = "failed"
			failed.Error = secondErr.Error()
			emit(failed)
			normalized.Answer = ""
			normalized.Suggestions = []string{}
			normalized.SourceIDs = []string{}
		}
	}
	modelName := route.MaiBotModel
	if strings.TrimSpace(modelName) == "" {
		modelName = route.Profile.Name
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = route.Config.Model
	}
	normalized.SourceIDs = filterSourceIDs(normalized.SourceIDs, readSourceIDs)
	return Response{
		ModelName: modelName, Answer: normalized.Answer, Suggestions: normalized.Suggestions,
		Sources: r.responseSources(normalized.SourceIDs), ExpandedTerms: normalized.ExpandedTerms, Results: normalized.Results,
		TotalTokens: totalTokens, GroundingError: groundingError, UsedLocalConfig: usedLocalConfig,
	}, nil
}

type searchToolCall struct {
	CallID    string
	Name      string
	Query     string
	Limit     int
	LimitSet  bool
	Paths     []string
	Offset    int
	Arguments string
}

type searchDocument struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Content  string `json:"content"`

	// 内部字段不进入工具结果，保留供调试/后续扩展
	Description string `json:"-"`
	Document    string `json:"-"`
}

type searchToolPayload struct {
	Query     string           `json:"query"`
	Documents []searchDocument `json:"documents"`
}

// buildSearchUserPrompt 渲染 webui_ai_search 模板，对齐 _build_ai_search_prompt
func buildSearchUserPrompt(request Request) string {
	locale := NormalizeLanguage(request.Language)
	queryJSON, err := json.Marshal(strings.TrimSpace(request.Query))
	if err != nil {
		queryJSON = []byte(`""`)
	}
	return aiSearchPromptText("webui_ai_search", locale, map[string]string{
		"query_json":      string(queryJSON),
		"candidate_count": strconv.Itoa(len(request.Candidates)),
	})
}

// buildEvidenceMessages 把工具结果转换为普通证据消息，对齐 _build_evidence_messages
//
// 工具轮次（含 FunctionCall）整体丢弃，只保留工具结果渲染出的证据文本与原始对话消息。
func buildEvidenceMessages(messages []*schema.AgenticMessage, locale string) []*schema.AgenticMessage {
	toolTurn := make(map[string]struct{})
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, block := range message.ContentBlocks {
			if block == nil || block.Type != schema.ContentBlockTypeFunctionToolCall || block.FunctionToolCall == nil {
				continue
			}
			if callID := strings.TrimSpace(block.FunctionToolCall.CallID); callID != "" {
				toolTurn[callID] = struct{}{}
			}
		}
	}
	evidence := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		if message.Role == schema.AgenticRoleTypeAssistant && len(toolTurn) > 0 {
			hasToolCall := false
			for _, block := range message.ContentBlocks {
				if block != nil && block.Type == schema.ContentBlockTypeFunctionToolCall {
					hasToolCall = true
					break
				}
			}
			if hasToolCall {
				continue
			}
		}
		if message.Role == schema.AgenticRoleTypeUser {
			converted := make([]*schema.ContentBlock, 0, len(message.ContentBlocks))
			for _, block := range message.ContentBlocks {
				if block == nil || block.Type != schema.ContentBlockTypeFunctionToolResult || block.FunctionToolResult == nil {
					converted = append(converted, block)
					continue
				}
				text := functionToolResultText(block.FunctionToolResult)
				converted = append(converted, schema.NewContentBlock(&schema.UserInputText{Text: aiSearchPromptText(
					"webui_ai_search_evidence", locale, map[string]string{
						"tool_name":   block.FunctionToolResult.Name,
						"tool_output": text,
					})}))
			}
			evidence = append(evidence, &schema.AgenticMessage{Role: message.Role, ContentBlocks: converted})
			continue
		}
		evidence = append(evidence, message)
	}
	return evidence
}

func functionToolResultText(result *schema.FunctionToolResult) string {
	if result == nil {
		return ""
	}
	var builder strings.Builder
	for _, item := range result.Content {
		if item == nil {
			continue
		}
		if item.Text != nil && item.Text.Text != "" {
			builder.WriteString(item.Text.Text)
		}
	}
	return builder.String()
}

func buildSearchIndexTool() *schema.ToolInfo {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "一个或多个简短检索词，用空格分隔"},
			"limit": map[string]any{"type": "integer", "description": fmt.Sprintf("返回数量，范围 1 到 %d", searchIndexMaxLimit)},
		},
		"required": []any{"query"},
	}
	encoded, _ := json.Marshal(parameters)
	var parameterSchema jsonschema.Schema
	_ = json.Unmarshal(encoded, &parameterSchema)
	return &schema.ToolInfo{
		Name:        searchIndexToolName,
		Desc:        "搜索当前 WebUI 中可导航的页面和配置项，返回候选 ID、标题、说明与配置字段信息。",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameterSchema),
	}
}

func toolCalls(message *schema.AgenticMessage) []searchToolCall {
	if message == nil {
		return nil
	}
	calls := []searchToolCall{}
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolCall == nil {
			continue
		}
		call := block.FunctionToolCall
		arguments := parseSearchToolArguments(call.Arguments)
		limit, limitSet := searchToolLimit(arguments)
		calls = append(calls, searchToolCall{
			CallID: call.CallID, Name: call.Name,
			Query: resolveSearchToolQuery(arguments["query"]), Limit: limit, LimitSet: limitSet,
			Paths: searchToolPaths(arguments), Offset: searchToolOffset(arguments),
			Arguments: call.Arguments,
		})
	}
	return calls
}

func assistantToolMessage(calls []searchToolCall) *schema.AgenticMessage {
	blocks := make([]*schema.ContentBlock, 0, len(calls))
	for _, call := range calls {
		arguments := call.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{CallID: call.CallID, Name: call.Name, Arguments: arguments}))
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks}
}

func toolResultMessage(call searchToolCall, content string) *schema.AgenticMessage {
	blocks := []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
		CallID: call.CallID, Name: call.Name,
		Content: []*schema.FunctionToolResultContentBlock{{
			Type: schema.FunctionToolResultContentBlockTypeText,
			Text: &schema.UserInputText{Text: content},
		}},
	})}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}
}

func executeSearchIndexTool(call searchToolCall, candidates []Candidate) searchToolPayload {
	payload := searchToolPayload{Query: call.Query, Documents: []searchDocument{}}
	if call.Name != searchIndexToolName {
		return payload
	}
	payload.Documents = searchCandidates(call.Query, candidates, call.Limit)
	return payload
}

func toSearchDocument(candidate Candidate) searchDocument {
	content := candidate.Document
	if content == "" {
		content = candidate.Description
	}
	return searchDocument{
		ID: candidate.ID, Title: candidate.Title, Category: candidate.Category, Content: content,
		Description: candidate.Description, Document: candidate.Document,
	}
}

func parseModelOutput(raw string) (ModelOutput, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ModelOutput{}, &OutputError{Message: "模型没有返回可解析的 JSON 对象"}
	}
	if output, err := decodeModelOutput(trimmed); err == nil {
		return output, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if strings.HasPrefix(trimmed, "{") && !strings.HasSuffix(trimmed, "}") {
		return ModelOutput{}, &OutputError{Message: "模型返回的 JSON 不完整，可能因 max_token 限制被截断"}
	}
	if start < 0 || end <= start {
		return ModelOutput{}, &OutputError{Message: "模型没有返回可解析的 JSON 对象"}
	}
	output, err := decodeModelOutput(trimmed[start : end+1])
	if err != nil {
		return ModelOutput{}, &OutputError{Message: "模型返回的 AI 搜索结果结构无效"}
	}
	return output, nil
}

func decodeModelOutput(raw string) (ModelOutput, error) {
	var output ModelOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return ModelOutput{}, err
	}
	if output.Results == nil {
		output.Results = []ModelResult{}
	}
	if output.Suggestions == nil {
		output.Suggestions = []string{}
	}
	if output.SourceIDs == nil {
		output.SourceIDs = []string{}
	}
	if output.ExpandedTerms == nil {
		output.ExpandedTerms = []string{}
	}
	return output, nil
}

func tokenUsage(message *schema.AgenticMessage) int {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
		return 0
	}
	usage := message.ResponseMeta.TokenUsage
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.PromptTokens + usage.CompletionTokens
}
