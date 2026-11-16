package search

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka/builtin_tool"
)

const (
	lookupHelpToolName         = "lookup_help"
	searchOfficialDocsToolName = "search_official_docs"
	readOfficialDocsToolName   = "read_official_docs"
	readLocalConfigToolName    = "read_local_config"
	searchToolMaxTargets       = 8
	searchToolMaxTitles        = 6
)

// LLMRunnerOptions 允许注入官方文档仓库、实例配置读取器与插件工具端口
type LLMRunnerOptions struct {
	DocumentStore     *tools.AISearchDocumentStore
	LocalConfigReader *tools.LocalConfigReader
	InstanceDir       string
	PluginTools       *PluginToolset
}

// NewLLMRunnerWithOptions 构造带显式依赖的执行器；未填写的依赖使用默认实现
func NewLLMRunnerWithOptions(router func() *llm.Router, options LLMRunnerOptions) *LLMRunner {
	if options.DocumentStore == nil {
		options.DocumentStore = tools.DefaultAISearchDocumentStore()
	}
	if options.LocalConfigReader == nil {
		options.LocalConfigReader = tools.NewDefaultLocalConfigReader()
	}
	return &LLMRunner{
		router:            router,
		documentStore:     options.DocumentStore,
		localConfigReader: options.LocalConfigReader,
		instanceDir:       strings.TrimSpace(options.InstanceDir),
		pluginTools:       options.PluginTools,
	}
}

// NewLLMRunnerForInstance 用实例目录构造本地配置读取器与诊断路径
func NewLLMRunnerForInstance(router func() *llm.Router, instanceDir string, pluginTools ...*PluginToolset) *LLMRunner {
	var toolset *PluginToolset
	if len(pluginTools) > 0 {
		toolset = pluginTools[0]
	}
	return NewLLMRunnerWithOptions(router, LLMRunnerOptions{
		LocalConfigReader: tools.NewLocalConfigReader(instanceDir),
		InstanceDir:       instanceDir,
		PluginTools:       toolset,
	})
}

func (r *LLMRunner) officialDocsStore() *tools.AISearchDocumentStore {
	if r != nil && r.documentStore != nil {
		return r.documentStore
	}
	return tools.DefaultAISearchDocumentStore()
}

func (r *LLMRunner) instanceConfigReader() *tools.LocalConfigReader {
	if r != nil && r.localConfigReader != nil {
		return r.localConfigReader
	}
	return tools.NewDefaultLocalConfigReader()
}

func buildSearchToolInfos() []*schema.ToolInfo {
	return []*schema.ToolInfo{
		buildToolInfo(
			lookupHelpToolName,
			"Find WebUI destinations and read the top two official documentation matches at their relevant "+
				"offsets in one call. Prefer this for an initial product question. Use narrow search or read tools "+
				"for follow-up evidence when needed.",
			map[string]any{
				"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "用空格分隔的简短检索词"},
			},
			[]any{"query"},
		),
		buildSearchIndexTool(),
		buildToolInfo(
			searchOfficialDocsToolName,
			"搜索 docs.maibot.chat 上的 MaiBot 官方文档，返回文档路径、标题和相关片段。",
			map[string]any{
				"query": map[string]any{"type": "string", "description": "一个或多个简短检索词，用空格分隔"},
				"limit": map[string]any{"type": "integer", "description": fmt.Sprintf("返回数量，范围 1 到 %d", tools.OfficialDocsSearchMaxLimit)},
			},
			[]any{"query"},
		),
		buildToolInfo(
			readOfficialDocsToolName,
			"按路径读取 docs.maibot.chat 官方文档正文。回答文档问题前应先调用此工具。单次返回的正文有长度上限，结果带有 next_offset 时说明文档还有后续内容。",
			map[string]any{
				"paths": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": fmt.Sprintf("search_official_docs 返回的文档路径，最多 %d 个", tools.OfficialDocsMaxReadCount),
				},
				"offset": map[string]any{"type": "integer", "description": "开始读取的字符位置，默认 0。可填上次返回的 next_offset 继续读取，或填 search_official_docs 返回的 snippet_offset 直接跳到命中位置"},
			},
			[]any{"paths"},
		),
		buildToolInfo(
			readLocalConfigToolName,
			"读取本机 bot_config.toml 与 model_config.toml 中配置节或字段当前实际生效的值，用于核对用户现在的设置。密钥、令牌等敏感内容会被隐藏。",
			map[string]any{
				"paths": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": fmt.Sprintf("点号分隔的配置路径，可以是配置节或具体字段，列表元素用下标表示，例如 chat.reply_timing、emoji.steal_emoji、api_providers.0，最多 %d 个", tools.LocalConfigMaxReadCount),
				},
			},
			[]any{"paths"},
		),
	}
}

func buildToolInfo(name, description string, properties map[string]any, required []any) *schema.ToolInfo {
	parameters := map[string]any{"type": "object", "properties": properties, "required": required}
	encoded, _ := json.Marshal(parameters)
	var parameterSchema jsonschema.Schema
	_ = json.Unmarshal(encoded, &parameterSchema)
	return &schema.ToolInfo{
		Name:        name,
		Desc:        description,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameterSchema),
	}
}

type officialDocsSearchPayload struct {
	Query     string                          `json:"query"`
	Documents []tools.OfficialDocSearchResult `json:"documents"`
}

type officialDocsReadPayload struct {
	Documents []tools.OfficialDocReadResult `json:"documents"`
}

type localConfigPayload struct {
	Documents []map[string]any `json:"documents"`
}

type searchToolExecution struct {
	Payload   any
	Error     string
	Targets   []string
	Titles    []string
	Count     int
	HasCount  bool
	SourceIDs []string
	Fatal     error
}

func (r *LLMRunner) executeSearchTool(ctx context.Context, call searchToolCall, candidates []Candidate) searchToolExecution {
	switch call.Name {
	case searchIndexToolName:
		indexCall := call
		indexCall.Limit = resolveSearchToolLimit(call, searchIndexDefault, searchIndexMaxLimit)
		payload := executeSearchIndexTool(indexCall, candidates)
		titles := make([]string, 0, len(payload.Documents))
		for _, document := range payload.Documents {
			titles = append(titles, document.Title)
		}
		return searchToolExecution{
			Payload: payload, Titles: limitSearchToolTitles(titles),
			Count: len(payload.Documents), HasCount: true,
		}
	case searchOfficialDocsToolName:
		query := strings.TrimSpace(call.Query)
		limit := resolveSearchToolLimit(call, tools.AISearchDefaultToolLimit, tools.OfficialDocsSearchMaxLimit)
		documents, err := r.officialDocsStore().SearchOfficialDocs(ctx, query, limit)
		if err != nil {
			return failedOfficialDocsTool(err, call.Paths)
		}
		titles := make([]string, 0, len(documents))
		for _, document := range documents {
			titles = append(titles, document.Title)
		}
		return searchToolExecution{
			Payload: officialDocsSearchPayload{Query: query, Documents: documents},
			Titles:  limitSearchToolTitles(titles), Count: len(documents), HasCount: true,
			Targets: searchToolTargets(call.Paths),
		}
	case readOfficialDocsToolName:
		offset := call.Offset
		if offset < 0 {
			offset = 0
		}
		documents, err := r.officialDocsStore().ReadOfficialDocs(ctx, call.Paths, offset)
		if err != nil {
			return failedOfficialDocsTool(err, call.Paths)
		}
		sourceIDs := make([]string, 0, len(documents))
		titles := make([]string, 0, len(documents))
		for _, document := range documents {
			sourceIDs = append(sourceIDs, document.SourceID)
			titles = append(titles, document.Title)
		}
		return searchToolExecution{
			Payload: officialDocsReadPayload{Documents: documents},
			Targets: searchToolTargets(call.Paths), Titles: limitSearchToolTitles(titles),
			Count: len(documents), HasCount: true, SourceIDs: sourceIDs,
		}
	case readLocalConfigToolName:
		if len(call.Paths) == 0 {
			return searchToolExecution{
				Payload: localConfigPayload{Documents: []map[string]any{}}, Targets: []string{},
				Titles: []string{}, Count: 0, HasCount: true,
			}
		}
		documents, err := r.instanceConfigReader().Read(call.Paths)
		if err != nil {
			// 对齐 Python：read_local_config 失败只是工具结果里的错误，不中止整次检索
			message := fmt.Sprintf("读取本地配置失败: %v", err)
			return searchToolExecution{
				Payload: map[string]any{"documents": []map[string]any{}, "error": message},
				Error:   message, Targets: searchToolTargets(call.Paths),
			}
		}
		titles := make([]string, 0, len(documents))
		for _, document := range documents {
			if title, ok := document["title"].(string); ok {
				titles = append(titles, title)
			}
		}
		return searchToolExecution{
			Payload: localConfigPayload{Documents: documents},
			Targets: searchToolTargets(call.Paths), Titles: limitSearchToolTitles(titles),
			Count: len(documents), HasCount: true,
		}
	default:
		message := fmt.Sprintf("未知工具: %s", call.Name)
		return searchToolExecution{
			Payload: map[string]string{"error": message}, Error: message,
			Targets: searchToolTargets(call.Paths),
		}
	}
}

func failedOfficialDocsTool(err error, paths []string) searchToolExecution {
	message := "暂时无法读取官方文档: " + err.Error()
	return searchToolExecution{
		Payload: map[string]string{"error": message}, Error: message,
		Targets: searchToolTargets(paths),
	}
}

func resolveSearchToolLimit(call searchToolCall, defaultValue, maximum int) int {
	limit := defaultValue
	if call.LimitSet {
		limit = call.Limit
	}
	if limit < 1 {
		return 1
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func searchToolTargets(paths []string) []string {
	targets := make([]string, 0, len(paths))
	for index, path := range paths {
		if index >= searchToolMaxTargets {
			break
		}
		targets = append(targets, path)
	}
	return targets
}

func limitSearchToolTitles(titles []string) []string {
	if len(titles) > searchToolMaxTitles {
		titles = titles[:searchToolMaxTitles]
	}
	return titles
}

func filterSourceIDs(sourceIDs []string, readSourceIDs map[string]struct{}) []string {
	filtered := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if _, ok := readSourceIDs[sourceID]; ok {
			filtered = append(filtered, sourceID)
		}
	}
	return filtered
}

func (r *LLMRunner) responseSources(sourceIDs []string) []Source {
	documents := r.officialDocsStore().BuildSources(sourceIDs)
	sources := make([]Source, 0, len(documents))
	for _, document := range documents {
		sources = append(sources, Source{Title: document.Title, URL: document.URL})
	}
	return sources
}

func parseSearchToolArguments(raw string) map[string]any {
	arguments := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &arguments)
	}
	return arguments
}

func resolveSearchToolQuery(raw any) string {
	query, _ := raw.(string)
	query = strings.TrimSpace(query)
	runes := []rune(query)
	if len(runes) > tools.AISearchMaxToolQueryLength {
		query = string(runes[:tools.AISearchMaxToolQueryLength])
	}
	return query
}

func searchToolLimit(arguments map[string]any) (int, bool) {
	value, ok := arguments["limit"].(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	limit := int(value)
	if float64(limit) != value {
		return 0, false
	}
	return limit, true
}

func searchToolOffset(arguments map[string]any) int {
	value, ok := arguments["offset"].(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	offset := int(value)
	if float64(offset) != value || offset < 0 {
		return 0
	}
	return offset
}

func searchToolPaths(arguments map[string]any) []string {
	raw, ok := arguments["paths"].([]any)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok {
			paths = append(paths, text)
		}
	}
	return paths
}
