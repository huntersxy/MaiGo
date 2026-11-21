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

	"MaiGo/internal/common/llmjson"
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
	instanceDir       string
	pluginTools       *PluginToolset
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
	route, client, err := router.NewClientForTask(ctx, llm.TaskPlanner)
	if err != nil {
		return Response{}, &UnavailableError{Message: "AI 搜索模型不可用: " + err.Error()}
	}
	if emit == nil {
		emit = func(ProgressEvent) {}
	}
	locale := NormalizeLanguage(request.Language)
	harness := NewHelpHarness(locale)
	artifacts := &HelpArtifactStore{}
	market := NewMarketStore(r.pluginTools)
	diagnosticsEnabled := request.HelpSessionID != nil
	emit(NewProgressEvent("start"))
	messages := buildInitialSearchMessages(request, locale)
	totalTokens := 0
	groundingParts := []string{}
	readSourceIDs := map[string]struct{}{}
	matchedCandidateIDs := map[string]struct{}{}
	usedLocalConfig := false
	directReferences := MatchHelpReferences(request.Query)
	if len(directReferences) > 0 {
		encoded, _ := json.Marshal(directReferences)
		groundingParts = append(groundingParts, string(encoded))
		for _, reference := range directReferences {
			readSourceIDs[reference["source_id"]] = struct{}{}
		}
		messages = append(messages, schema.UserAgenticMessage(aiSearchPromptText(
			"webui_ai_search_evidence", locale, map[string]string{
				"tool_name": "preloaded_help_references", "tool_output": string(encoded),
			})))
	}
	answer := ""
	for round := 1; round <= 6; round++ {
		roundValue := round
		event := NewProgressEvent("planning")
		event.Round = &roundValue
		emit(event)
		tools := r.toolInfos(request, harness, market, diagnosticsEnabled)
		response, err := client.Generate(ctx, messages, model.WithTools(tools), model.WithMaxTokens(2048), model.WithTemperature(0.2))
		if err != nil {
			return Response{}, fmt.Errorf("AI 搜索调用失败: %w", err)
		}
		totalTokens += tokenUsage(response)
		answer = MessageText(response)
		calls := toolCalls(response)
		if len(calls) == 0 {
			messages = append(messages, response)
			break
		}
		if len(calls) > MaxToolCallsPerRound {
			calls = calls[:MaxToolCallsPerRound]
		}
		messages = append(messages, assistantToolMessage(calls))
		for _, call := range calls {
			emitToolProgress(emit, call, "started", agentToolExecution{})
			execution := r.executeAgentTool(ctx, call, request, harness, artifacts, market, diagnosticsEnabled)
			if execution.Fatal != nil {
				return Response{}, execution.Fatal
			}
			if execution.Progress != nil {
				emit(*execution.Progress)
			}
			encoded, _ := json.Marshal(execution.Payload)
			messages = append(messages, toolResultMessage(call, string(encoded)))
			if execution.Error == "" {
				groundingParts = append(groundingParts, string(encoded))
				for _, sourceID := range execution.SourceIDs {
					readSourceIDs[sourceID] = struct{}{}
				}
				for _, candidateID := range execution.CandidateIDs {
					matchedCandidateIDs[candidateID] = struct{}{}
				}
			}
			usedLocalConfig = usedLocalConfig || call.Name == readLocalConfigToolName
			emitToolProgress(emit, call, completionStatus(execution), execution)
		}
	}
	output, err := parseModelOutput(answer)
	evidenceMessages := buildEvidenceMessages(messages, harness)
	if err != nil {
		emit(NewProgressEvent("finalizing"))
		finalMessages := append(append([]*schema.AgenticMessage{}, evidenceMessages...),
			schema.UserAgenticMessage(aiSearchPromptText("webui_ai_search_final", locale, nil)))
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
	filtered := make([]ModelResult, 0, len(normalized.Results))
	for _, result := range normalized.Results {
		if _, ok := matchedCandidateIDs[result.ID]; ok {
			filtered = append(filtered, result)
		}
	}
	normalized.Results = filtered
	groundingError := ""
	evidence := strings.Join(append([]string{request.Query}, groundingParts...), "\n")
	if groundingErr := ValidateOutputEvidence(normalized, evidence); groundingErr != nil {
		emitCorrection(emit, groundingErr, "started")
		correctionMessages := append(append([]*schema.AgenticMessage{}, evidenceMessages...),
			schema.UserAgenticMessage(aiSearchPromptText("webui_ai_search_correction", locale,
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
			emitCorrection(emit, secondErr, "failed")
			normalized.Answer = ""
			normalized.Suggestions = []string{}
			normalized.SourceIDs = []string{}
		}
	}
	for sourceID := range market.Sources {
		readSourceIDs[sourceID] = struct{}{}
	}
	allowedSources := filterSourceIDs(normalized.SourceIDs, readSourceIDs)
	officialIDs := make([]string, 0, len(allowedSources))
	references := make([]Source, 0, len(allowedSources))
	marketSources := make([]Source, 0, len(allowedSources))
	seenSources := map[string]struct{}{}
	for _, sourceID := range allowedSources {
		if _, duplicate := seenSources[sourceID]; duplicate {
			continue
		}
		seenSources[sourceID] = struct{}{}
		if source, ok := GetHelpReference(sourceID); ok {
			references = append(references, source)
			continue
		}
		if source, ok := market.Sources[sourceID]; ok {
			marketSources = append(marketSources, source)
			continue
		}
		officialIDs = append(officialIDs, sourceID)
	}
	sources := append([]Source{}, r.responseSources(officialIDs)...)
	sources = append(sources, references...)
	sources = append(sources, marketSources...)
	modelName := route.MaiBotModel
	if strings.TrimSpace(modelName) == "" {
		modelName = route.Profile.Name
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = route.Config.Model
	}
	return Response{
		ModelName: modelName, Answer: normalized.Answer, Suggestions: normalized.Suggestions,
		Sources: sources, ExpandedTerms: normalized.ExpandedTerms, Results: normalized.Results,
		Plugins:         market.SelectPlugins(normalized.PluginIDs),
		InstallRequests: market.InstallRequestList(),
		ConfigUpdates:   market.Updates,
		Artifacts:       artifacts.Artifacts,
		TotalTokens:     totalTokens, GroundingError: groundingError,
		UsedLocalConfig: usedLocalConfig, UsedPluginMarket: market.Used,
	}, nil
}

type agentToolExecution struct {
	Payload      any
	Error        string
	Targets      []string
	Titles       []string
	Count        *int
	SourceIDs    []string
	CandidateIDs []string
	Fatal        error
	Progress     *ProgressEvent
}

func (r *LLMRunner) toolInfos(request Request, harness *HelpHarness, market *MarketStore, diagnosticsEnabled bool) []*schema.ToolInfo {
	tools := append([]*schema.ToolInfo{}, buildSearchToolInfos()...)
	tools = append(tools, buildHarnessToolInfos()...)
	if request.HelpSessionID != nil {
		tools = append(tools, buildHelpArtifactToolInfo())
		if _, ok := harness.ActiveSkills["troubleshoot"]; ok {
			tools = append(tools, buildDiagnosticToolInfos()...)
		}
	}
	if _, ok := harness.ActiveSkills["plugin"]; ok && market.Enabled() {
		tools = append(tools, buildMarketToolInfos(request.AllowPluginConfigWrite)...)
	}
	return tools
}

func (r *LLMRunner) executeAgentTool(
	ctx context.Context, call searchToolCall, request Request, harness *HelpHarness,
	artifacts *HelpArtifactStore, market *MarketStore, diagnosticsEnabled bool,
) agentToolExecution {
	name := call.Name
	switch {
	case isHarnessTool(name):
		payload, event := harness.Execute(name, call.ArgumentsMap)
		return agentToolExecution{Payload: payload, Progress: event}
	case isDiagnosticTool(name):
		if !diagnosticsEnabled || !harnessHasSkill(harness, "troubleshoot") {
			return agentToolExecution{Payload: map[string]any{"error": "仅在独立答疑会话加载 troubleshoot 技能后可读取诊断数据"}}
		}
		if name == diagnosticReadReasoningTool {
			if recordPath, _ := call.ArgumentsMap["record_path"].(string); strings.TrimSpace(recordPath) != "" &&
				!strings.Contains(request.Query, strings.TrimSpace(recordPath)) {
				return agentToolExecution{Payload: map[string]any{"error": "推理文件路径必须由用户在本轮明确提供"}}
			}
		}
		return agentToolExecution{Payload: ExecuteDiagnosticTool(r.instanceDir, name, call.ArgumentsMap)}
	case name == publishHelpArtifactTool:
		if !diagnosticsEnabled {
			return agentToolExecution{Payload: map[string]any{"error": "本步未提供该工具"}}
		}
		if kind, _ := call.ArgumentsMap["kind"].(string); kind == "image" {
			content, _ := call.ArgumentsMap["content"].(string)
			if !strings.Contains(request.Query, strings.TrimSpace(content)) {
				return agentToolExecution{Payload: map[string]any{"error": "图片地址必须来自本轮用户输入"}}
			}
		}
		artifact, err := artifacts.Publish(call.ArgumentsMap)
		if err != nil {
			return agentToolExecution{Payload: map[string]any{"error": err.Error()}}
		}
		return agentToolExecution{Payload: map[string]any{"published": artifact.Kind}}
	case isPluginMarketTool(name):
		if !market.Enabled() || !harnessHasSkill(harness, "plugin") {
			return agentToolExecution{Payload: map[string]any{"error": "本步未提供该工具"}}
		}
		if name == "update_plugin_config" && !request.AllowPluginConfigWrite {
			return agentToolExecution{Payload: map[string]any{"error": "本轮未启用插件配置写入权限"}}
		}
		return agentToolExecution{Payload: market.Execute(ctx, name, call.ArgumentsMap)}
	case name == lookupHelpToolName:
		return r.lookupHelp(ctx, call, request.Candidates)
	default:
		execution := r.executeSearchTool(ctx, call, request.Candidates)
		if execution.Fatal != nil {
			return agentToolExecution{Fatal: execution.Fatal}
		}
		result := agentToolExecution{
			Payload: execution.Payload, Error: execution.Error, Targets: execution.Targets,
			Titles: execution.Titles, SourceIDs: execution.SourceIDs,
		}
		if execution.HasCount {
			count := execution.Count
			result.Count = &count
		}
		if name == searchIndexToolName {
			if payload, ok := execution.Payload.(searchToolPayload); ok {
				for _, document := range payload.Documents {
					result.CandidateIDs = append(result.CandidateIDs, document.ID)
				}
			}
		}
		return result
	}
}

func isHarnessTool(name string) bool {
	_, ok := helpHarnessToolNames[name]
	return ok
}

func isDiagnosticTool(name string) bool {
	_, ok := diagnosticToolNames[name]
	return ok
}

func isPluginMarketTool(name string) bool {
	_, ok := pluginMarketToolNames[name]
	return ok
}

func harnessHasSkill(harness *HelpHarness, skill string) bool {
	if harness == nil {
		return false
	}
	_, ok := harness.ActiveSkills[skill]
	return ok
}

func completionStatus(execution agentToolExecution) string {
	if execution.Error != "" {
		return "failed"
	}
	return "completed"
}

func emitToolProgress(emit Emit, call searchToolCall, status string, execution agentToolExecution) {
	event := NewProgressEvent("tool")
	event.Status = status
	event.Tool = call.Name
	event.Query = call.Query
	event.Targets = searchToolTargets(call.Paths)
	if execution.Targets != nil {
		event.Targets = execution.Targets
	}
	if execution.Error != "" {
		event.Status = "failed"
		event.Error = execution.Error
	}
	event.Titles = append(event.Titles, execution.Titles...)
	event.Count = execution.Count
	if payload, ok := execution.Payload.(map[string]any); ok {
		if documents := mapSliceValue(payload["documents"]); len(documents) > 0 {
			titles := make([]string, 0, len(documents))
			for _, document := range documents {
				if title := stringValue(document["title"]); title != "" {
					titles = append(titles, title)
				}
			}
			if event.Count == nil {
				count := len(documents)
				event.Count = &count
			}
			if len(event.Titles) == 0 {
				event.Titles = limitSearchToolTitles(titles)
			}
		}
		if plugins := mapSliceValue(payload["plugins"]); len(plugins) > 0 {
			titles := make([]string, 0, len(plugins))
			for _, plugin := range plugins {
				if name := stringValue(plugin["name"]); name != "" {
					titles = append(titles, name)
				}
			}
			if event.Count == nil {
				count := len(plugins)
				event.Count = &count
			}
			if len(event.Titles) == 0 {
				event.Titles = limitSearchToolTitles(titles)
			}
		}
		if install, ok := payload["install_request"].(map[string]any); ok {
			encoded, _ := json.Marshal(install)
			var request InstallRequest
			if json.Unmarshal(encoded, &request) == nil {
				event.InstallRequest = &request
				event.Targets = []string{request.PluginID}
			}
		}
		if update, ok := payload["config_update"].(map[string]any); ok {
			encoded, _ := json.Marshal(update)
			var configUpdate ConfigUpdate
			if json.Unmarshal(encoded, &configUpdate) == nil {
				event.ConfigUpdate = &configUpdate
				event.Targets = []string{configUpdate.PluginID}
			}
		}
	}
	emit(event)
}

func emitCorrection(emit Emit, err error, status string) {
	event := NewProgressEvent("correcting")
	event.Status = status
	event.Error = err.Error()
	emit(event)
}

// lookupHelp 一次完成索引扫描与命中位置的正文读取
func (r *LLMRunner) lookupHelp(ctx context.Context, call searchToolCall, candidates []Candidate) agentToolExecution {
	query := strings.TrimSpace(call.Query)
	navigation := searchCandidates(query, candidates, 4)
	documents := make([]any, 0)
	for _, reference := range MatchHelpReferences(query) {
		documents = append(documents, reference)
	}
	docsError := ""
	hits, err := r.officialDocsStore().SearchOfficialDocs(ctx, query, 2)
	if err != nil {
		docsError = "暂时无法读取官方文档"
	} else {
		paths := make([]string, 0, len(hits))
		offsets := map[string]int{}
		for _, hit := range hits {
			paths = append(paths, hit.Path)
			offsets[hit.URL] = hit.SnippetOffset
		}
		read, readErr := r.officialDocsStore().ReadOfficialDocs(ctx, paths, 0)
		if readErr != nil {
			docsError = "暂时无法读取官方文档"
		} else {
			for _, document := range read {
				content := document.Content
				runes := []rune(content)
				if len(runes) > 3000 {
					content = string(runes[:3000])
				}
				payload := map[string]any{
					"source_id": document.SourceID, "title": document.Title, "url": document.URL,
					"offset": document.Offset, "length": document.Length, "content": content,
				}
				if document.NextOffset != nil {
					payload["next_offset"] = document.NextOffset
				} else if len(runes) > 3000 {
					payload["next_offset"] = document.Offset + 3000
				}
				if offset, ok := offsets[document.URL]; ok && offset > 0 {
					payload["snippet_offset"] = offset
				}
				documents = append(documents, payload)
			}
		}
	}
	payload := map[string]any{"navigation": navigation, "documents": documents}
	if docsError != "" {
		payload["docs_error"] = docsError
	}
	sourceIDs := make([]string, 0)
	candidateIDs := make([]string, 0)
	for _, document := range navigation {
		candidateIDs = append(candidateIDs, document.ID)
	}
	for _, raw := range documents {
		if item, ok := raw.(map[string]any); ok {
			if id, ok := item["source_id"].(string); ok && id != "" {
				sourceIDs = append(sourceIDs, id)
			}
		}
	}
	return agentToolExecution{
		Payload: payload, Targets: searchToolTargets(call.Paths), Titles: limitSearchToolTitles(documentTitles(navigation)),
		SourceIDs: sourceIDs, CandidateIDs: candidateIDs,
	}
}

func documentTitles(documents []searchDocument) []string {
	titles := make([]string, 0, len(documents))
	for _, document := range documents {
		titles = append(titles, document.Title)
	}
	return titles
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
	// ArgumentsMap 原始参数，供技能、插件、诊断与成果工具使用
	ArgumentsMap map[string]any
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

// buildInitialSearchMessages 把可信规则和不可信的当前问题、历史记录分开投影
func buildInitialSearchMessages(request Request, locale string) []*schema.AgenticMessage {
	queryJSON, _ := json.Marshal(strings.TrimSpace(request.Query))
	history := make([]map[string]string, 0, len(request.History))
	for _, pair := range request.History {
		history = append(history, map[string]string{"question": pair.Question, "answer": pair.Answer})
	}
	historyJSON, _ := json.Marshal(history)
	policy := aiSearchPromptText("webui_ai_search", locale, map[string]string{
		"candidate_count": strconv.Itoa(len(request.Candidates)),
	})
	input := aiSearchPromptText("webui_ai_search_input", locale, map[string]string{
		"query_json": string(queryJSON), "history_json": string(historyJSON),
	})
	return []*schema.AgenticMessage{
		schema.SystemAgenticMessage(policy),
		schema.UserAgenticMessage(input),
	}
}

// buildSearchUserPrompt 保留旧调用，渲染包含当前问题的单条提示词
func buildSearchUserPrompt(request Request) string {
	return aiSearchPromptText("webui_ai_search_input", NormalizeLanguage(request.Language), map[string]string{
		"query_json": strconv.Quote(strings.TrimSpace(request.Query)), "history_json": "[]",
	})
}

// buildEvidenceMessages 把工具结果转换为普通证据消息，对齐 _build_evidence_messages
//
// 工具轮次（含 FunctionCall）整体丢弃，只保留工具结果渲染出的证据文本与原始对话消息。
func buildEvidenceMessages(messages []*schema.AgenticMessage, harness *HelpHarness) []*schema.AgenticMessage {
	locale := "zh-CN"
	if harness != nil && harness.Locale != "" {
		locale = harness.Locale
	}
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
				toolName := block.FunctionToolResult.Name
				if isHarnessTool(toolName) || toolName == publishHelpArtifactTool {
					continue
				}
				text := functionToolResultText(block.FunctionToolResult)
				converted = append(converted, schema.NewContentBlock(&schema.UserInputText{Text: aiSearchPromptText(
					"webui_ai_search_evidence", locale, map[string]string{
						"tool_name":   toolName,
						"tool_output": text,
					})}))
			}
			if len(converted) > 0 {
				evidence = append(evidence, &schema.AgenticMessage{Role: message.Role, ContentBlocks: converted})
			}
			continue
		}
		evidence = append(evidence, message)
	}
	if harness != nil && len(harness.ActiveSkills) > 0 && len(evidence) > 0 {
		skills := make([]string, 0, len(harness.ActiveSkills))
		for _, instructions := range harness.ActiveSkills {
			skills = append(skills, instructions)
		}
		first := evidence[0]
		policy := MessageText(first)
		policy = strings.TrimSpace(policy + "\n\n" + strings.Join(skills, "\n\n"))
		evidence[0] = schema.SystemAgenticMessage(policy)
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
			Arguments: call.Arguments, ArgumentsMap: arguments,
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
	trimmed := llmjson.Normalize(raw)
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
