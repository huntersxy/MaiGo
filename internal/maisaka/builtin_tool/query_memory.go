package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"maibot-go/internal/A_memorix"
	"maibot-go/internal/common/database"
	"maibot-go/internal/person_info"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	queryMemoryToolName        = "query_memory"
	queryMemoryToolDescription = "检索长期记忆。涉及回忆、过往经历时可以检索。"
	// defaultMemoryQueryLimit 对齐 Python memory_query_default_limit 的默认值
	defaultMemoryQueryLimit = 5
	// maxMemoryQueryLimit 对齐 Python handle_tool 的 min(limit, 20)
	maxMemoryQueryLimit = 20
	// replyerMemoryReferenceMarker 对齐 Python REPLYER_MEMORY_REFERENCE_MARKER
	replyerMemoryReferenceMarker = "【长期记忆检索结果-内部参考】"
)

var allowedQueryMemoryModes = map[string]struct{}{
	"search": {}, "time": {}, "hybrid": {}, "episode": {}, "aggregate": {},
}

// QueryMemoryInput query_memory 参数，字段描述对齐 Python get_tool_spec
type QueryMemoryInput struct {
	Query      string `json:"query,omitempty" jsonschema_description:"关键词或问题；非纯时间检索必填。"`
	Limit      int    `json:"limit,omitempty" jsonschema_description:"返回条数。"`
	Mode       string `json:"mode,omitempty" jsonschema:"enum=aggregate,enum=episode,enum=hybrid,enum=search,enum=time,default=search" jsonschema_description:"检索路由：search按语义检索事实、偏好等记忆；time按时间范围检索；episode检索经历；aggregate综合检索；hybrid同时使用语义与时间范围。time和hybrid至少提供time_start或time_end；没有时间条件时使用search。"`
	PersonName string `json:"person_name,omitempty" jsonschema_description:"人物名；用于定向过滤。"`
	TimeStart  string `json:"time_start,omitempty" jsonschema_description:"起始时间，仅接受YYYY/MM/DD或YYYY/MM/DD HH:mm。"`
	TimeEnd    string `json:"time_end,omitempty" jsonschema_description:"结束时间，仅接受YYYY/MM/DD或YYYY/MM/DD HH:mm。"`
	// RespectFilter 指针用于区分未传与显式 false，未传时默认 true
	RespectFilter *bool `json:"respect_filter,omitempty" jsonschema:"default=true" jsonschema_description:"是否遵守记忆过滤规则；默认true，模糊来源或整体印象可false。"`
}

// queryMemoryTool 同时实现 eino InvokableTool 与结构化结果接口
type queryMemoryTool struct {
	info *schema.ToolInfo
}

// Info 返回工具声明
func (t *queryMemoryTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

// InvokableRun 供 eino ToolsNode 直接调用，失败时以错误暴露
func (t *queryMemoryTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	result, err := t.InvokeStructuredToolResult(ctx, argumentsInJSON)
	if err != nil {
		return "", err
	}
	if !result.Success {
		if strings.TrimSpace(result.ErrorMessage) == "" {
			return "", fmt.Errorf("query_memory 执行失败")
		}
		return "", fmt.Errorf("%s", result.ErrorMessage)
	}
	return result.Content, nil
}

// InvokeStructuredToolResult 返回 Python ToolExecutionResult 对应的结构化结果
func (t *queryMemoryTool) InvokeStructuredToolResult(ctx context.Context, argumentsInJSON string) (StructuredToolResult, error) {
	input := &QueryMemoryInput{}
	if strings.TrimSpace(argumentsInJSON) != "" {
		if err := sonic.UnmarshalString(argumentsInJSON, input); err != nil {
			return StructuredToolResult{}, fmt.Errorf("query_memory 参数解析失败: %w", err)
		}
	}
	return runQueryMemory(ctx, input)
}

// NewQueryMemoryTool 构造 query_memory 工具
func NewQueryMemoryTool() (tool.InvokableTool, error) {
	plain, err := utils.InferTool(queryMemoryToolName, queryMemoryToolDescription, queryMemoryRun)
	if err != nil {
		return nil, err
	}
	info, err := plain.Info(context.Background())
	if err != nil {
		return nil, err
	}
	return &queryMemoryTool{info: info}, nil
}

// queryMemoryRun 是 InferTool 的适配函数，仅用于生成声明
func queryMemoryRun(ctx context.Context, input *QueryMemoryInput) (string, error) {
	result, err := runQueryMemory(ctx, input)
	if err != nil {
		return "", err
	}
	if !result.Success {
		if strings.TrimSpace(result.ErrorMessage) == "" {
			return "", fmt.Errorf("query_memory 执行失败")
		}
		return "", fmt.Errorf("%s", result.ErrorMessage)
	}
	return result.Content, nil
}

// runQueryMemory 执行 query_memory 主流程，逐项对齐 Python handle_tool
func runQueryMemory(ctx context.Context, input *QueryMemoryInput) (StructuredToolResult, error) {
	if input == nil {
		input = &QueryMemoryInput{}
	}
	mode := normalizeQueryMemoryMode(input.Mode)
	if _, ok := allowedQueryMemoryModes[mode]; !ok {
		return queryMemoryFailure(fmt.Sprintf("不支持的检索模式：%s。可选值：search/time/hybrid/episode/aggregate。", mode)), nil
	}

	defaultLimit := memoryQueryDefaultLimit(ctx)
	limit := normalizeQueryMemoryLimit(input.Limit, defaultLimit)
	query := strings.TrimSpace(input.Query)
	timeStart := normalizeQueryMemoryTime(input.TimeStart)
	timeEnd := normalizeQueryMemoryTime(input.TimeEnd)
	if query == "" && timeStart == nil && timeEnd == nil {
		return queryMemoryFailure("query_memory 需要提供 query，或至少提供 time_start/time_end 中的一个。"), nil
	}

	tc := GetToolContext(ctx)
	sessionID, platform, userID, groupID := queryMemoryConversation(tc)
	personName := strings.TrimSpace(input.PersonName)
	personID, personErr := resolveQueryMemoryPersonID(ctx, personName, platform, userID, groupID)
	if personErr != "" {
		return queryMemoryFailure(personErr), nil
	}
	if personName != "" && personID == "" {
		return queryMemoryFailure(fmt.Sprintf("无法唯一确认人物 %s，请使用明确的人物 ID 或不重名的称呼。", personName)), nil
	}
	respectFilter := true
	if input.RespectFilter != nil {
		respectFilter = *input.RespectFilter
	}

	result, primaryHitCount, fallbackApplied, fallbackReason, fallbackQuery, effectiveMode, err := searchQueryMemory(ctx, query, limit, mode, personID, queryTimeString(timeStart), queryTimeString(timeEnd), respectFilter)
	if err != nil {
		return queryMemoryFailure("长期记忆检索失败：" + err.Error()), nil
	}

	structured := buildQueryMemoryStructuredContent(result, query, mode, effectiveMode, limit, sessionID, personName, personID, timeStart, timeEnd, respectFilter, userID, groupID, fallbackApplied, fallbackReason, fallbackQuery, primaryHitCount)
	if !result.Success {
		errorMessage := strings.TrimSpace(result.Error)
		if errorMessage == "" {
			errorMessage = "长期记忆检索失败。"
		}
		return StructuredToolResult{Success: false, ErrorMessage: errorMessage, StructuredContent: structured}, nil
	}

	content := buildQueryMemorySuccessContent(result, limit)
	if fallbackApplied {
		content = "提示：人物定向检索未命中，已自动降级为关键词检索。\n" + content
	}
	metadata := map[string]any{
		"memory_feedback_task": map[string]any{"enabled": true},
	}
	if reference := buildReplyerMemoryReference(structured); reference != "" {
		metadata["replyer_memory_reference"] = reference
	}
	return StructuredToolResult{Success: true, Content: content, StructuredContent: structured, Metadata: metadata}, nil
}

// searchQueryMemory 优先走 Memorix Host；Host 未就绪时走 legacy 记忆检索并返回 Python 形状
func searchQueryMemory(ctx context.Context, query string, limit int, mode string, personID string, timeStart string, timeEnd string, respectFilter bool) (memorix.SearchResult, int, bool, string, string, string, error) {
	effectiveMode := mode
	host, ok := memorixHostReady(ctx)
	if !ok {
		result, err := legacyQueryMemorySearch(ctx, query, limit, mode)
		if err != nil {
			return memorix.SearchResult{}, 0, false, "", "", effectiveMode, err
		}
		return result, len(result.Hits), false, "", "", effectiveMode, nil
	}
	result, err := host.Search(ctx, buildMemorixSearchRequest(ctx, query, limit, mode, personID, timeStart, timeEnd, respectFilter))
	if err != nil {
		return memorix.SearchResult{}, 0, false, "", "", effectiveMode, err
	}
	primaryHitCount := len(result.Hits)
	fallbackApplied := false
	fallbackReason := ""
	fallbackQuery := ""
	// Python 仅在成功、人物过滤、未命中且有关键词时降级为关键词检索
	if result.Success && personID != "" && !result.Filtered && len(result.Hits) == 0 && query != "" {
		fallbackApplied = true
		fallbackReason = "person_filter_miss"
		fallbackQuery = query
		effectiveMode = "search"
		fallback, fallbackErr := host.Search(ctx, buildMemorixSearchRequest(ctx, query, limit, "search", "", "", "", respectFilter))
		if fallbackErr != nil {
			zap.L().Warn("关键词降级检索异常，回退原结果", zap.String("query", query), zap.Error(fallbackErr))
		} else if fallback.Success {
			result = fallback
		} else {
			zap.L().Warn("关键词降级检索失败，回退原结果", zap.String("query", query), zap.String("error", fallback.Error))
		}
	}
	return result, primaryHitCount, fallbackApplied, fallbackReason, fallbackQuery, effectiveMode, nil
}

// legacyQueryMemorySearch Host 未就绪时按数据库记忆检索构造 Python 形状结果
func legacyQueryMemorySearch(ctx context.Context, query string, limit int, mode string) (memorix.SearchResult, error) {
	if mode != "search" {
		return memorix.SearchResult{}, fmt.Errorf("长期记忆服务未就绪，无法执行 %s 模式检索", mode)
	}
	if query == "" {
		return memorix.SearchResult{}, fmt.Errorf("长期记忆服务未就绪，无法执行纯时间检索")
	}
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil {
		return memorix.SearchResult{}, fmt.Errorf("记忆检索能力未初始化")
	}
	page, err := tc.MemoryMgr.SearchMemories(ctx, memory.MemorySearchQuery{
		Keywords: tokenizeMemoryQuery(query),
		Match:    "or",
		Limit:    limit,
	})
	if err != nil {
		return memorix.SearchResult{}, err
	}
	hits := make([]memorix.Hit, 0, len(page.Items))
	for _, item := range page.Items {
		hits = append(hits, memorix.Hit{
			Content:   item.ContentPreview,
			Score:     item.Similarity,
			HitType:   item.MemoryType,
			Source:    "pg",
			Metadata:  map[string]any{},
			EpisodeID: "",
			Title:     item.Label,
		})
	}
	return memorix.SearchResult{Success: true, Hits: hits}, nil
}

func normalizeQueryMemoryMode(raw string) string {
	mode := strings.TrimSpace(strings.ToLower(raw))
	if mode == "" {
		return "search"
	}
	return mode
}

func normalizeQueryMemoryLimit(raw int, defaultLimit int) int {
	limit := raw
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxMemoryQueryLimit {
		limit = maxMemoryQueryLimit
	}
	return limit
}

func memoryQueryDefaultLimit(ctx context.Context) int {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryQueryDefaultLimit <= 0 {
		return defaultMemoryQueryLimit
	}
	return tc.MemoryQueryDefaultLimit
}

// normalizeQueryMemoryTime 对齐 Python _normalize_optional_time，ISO 日期把前两段 - 换成 /
func normalizeQueryMemoryTime(raw string) any {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	if isISOQueryMemoryTime(text) {
		return strings.Replace(text, "-", "/", 2)
	}
	return text
}

func isISOQueryMemoryTime(text string) bool {
	if len(text) != 10 && len(text) != 16 {
		return false
	}
	if text[4] != '-' || text[7] != '-' {
		return false
	}
	for i, char := range text {
		if i == 4 || i == 7 {
			continue
		}
		if i == 10 {
			if char != ' ' {
				return false
			}
			continue
		}
		if i == 13 {
			if char != ':' {
				return false
			}
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func queryTimeString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// queryMemoryConversation 对齐 MaiChatSession：群聊 group_id 非空、私聊 user_id 非空
func queryMemoryConversation(tc *ToolContext) (sessionID string, platform string, userID string, groupID string) {
	if tc == nil {
		return "", "", "", ""
	}
	sessionID = strings.TrimSpace(tc.SessionID)
	platform = strings.TrimSpace(tc.Platform)
	switch tc.ConversationKind {
	case memory.ConversationKindGroup:
		groupID = strconv.FormatInt(tc.TargetID, 10)
	case memory.ConversationKindPrivate:
		userID = strconv.FormatInt(tc.TargetID, 10)
	}
	return sessionID, platform, userID, groupID
}

// resolveQueryMemoryPersonID 对齐 Python _resolve_person_id
func resolveQueryMemoryPersonID(ctx context.Context, personName string, platform string, userID string, groupID string) (string, string) {
	cleanName := strings.TrimSpace(personName)
	if cleanName != "" {
		identity, err := personinfo.DefaultRegistry().ResolveIdentity(ctx, "", personPlatform(platform), "", cleanName)
		if err != nil {
			return "", "人物身份解析失败: " + err.Error()
		}
		if identity == nil || !identity.Success {
			errorText := "身份解析失败"
			if identity != nil && strings.TrimSpace(identity.Error) != "" {
				errorText = identity.Error
			}
			return "", "人物身份解析失败: " + errorText
		}
		return strings.TrimSpace(identity.PersonID), ""
	}
	if groupID == "" && platform != "" && userID != "" {
		personID := strings.TrimSpace(personinfo.DefaultRegistry().ResolvePersonID(ctx, personPlatform(platform), userID, "", false))
		if personID != "" {
			return personID, ""
		}
	}
	return "", ""
}

func buildQueryMemoryStructuredContent(
	result memorix.SearchResult,
	query string,
	mode string,
	effectiveMode string,
	limit int,
	chatID string,
	personName string,
	personID string,
	timeStart any,
	timeEnd any,
	respectFilter bool,
	userID string,
	groupID string,
	fallbackApplied bool,
	fallbackReason string,
	fallbackQuery string,
	primaryHitCount int,
) map[string]any {
	return map[string]any{
		"success":           result.Success,
		"error":             result.Error,
		"summary":           result.Summary,
		"hits":              queryMemoryHitMaps(result.Hits),
		"filtered":          result.Filtered,
		"query":             query,
		"mode":              mode,
		"effective_mode":    effectiveMode,
		"limit":             limit,
		"chat_id":           chatID,
		"person_name":       personName,
		"person_id":         personID,
		"time_start":        timeStart,
		"time_end":          timeEnd,
		"respect_filter":    respectFilter,
		"user_id":           userID,
		"group_id":          groupID,
		"fallback_applied":  fallbackApplied,
		"fallback_reason":   fallbackReason,
		"fallback_query":    fallbackQuery,
		"primary_hit_count": primaryHitCount,
	}
}

// queryMemoryHitMaps 输出 Python MemoryHit.to_dict 的全字段
func queryMemoryHitMaps(hits []memorix.Hit) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		metadata := hit.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		out = append(out, map[string]any{
			"content":    hit.Content,
			"score":      hit.Score,
			"type":       hit.HitType,
			"source":     hit.Source,
			"hash":       hit.Hash,
			"metadata":   metadata,
			"episode_id": hit.EpisodeID,
			"title":      hit.Title,
		})
	}
	return out
}

// buildQueryMemorySuccessContent 对齐 Python _build_success_content
func buildQueryMemorySuccessContent(result memorix.SearchResult, limit int) string {
	summary := strings.TrimSpace(result.Summary)
	snippet := renderQueryMemoryHits(result.Hits, limit)
	if len(result.Hits) > 0 {
		if snippet != "" {
			return snippet
		}
		if summary != "" {
			return summary
		}
		return "已找到匹配的长期记忆。"
	}
	if result.Filtered {
		return "当前请求被聊天过滤策略跳过，未执行长期记忆检索。"
	}
	return "未找到匹配的长期记忆。"
}

// renderQueryMemoryHits 对齐 Python MemorySearchResult.to_text(truncate_content=False)
func renderQueryMemoryHits(hits []memorix.Hit, limit int) string {
	if len(hits) == 0 {
		return ""
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(hits) {
		limit = len(hits)
	}
	lines := make([]string, 0, limit)
	for index, hit := range hits[:limit] {
		content := strings.TrimSpace(hit.Content)
		content = strings.ReplaceAll(content, "\n", " ")
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, content))
	}
	return strings.Join(lines, "\n")
}

// buildReplyerMemoryReference 对齐 Python _build_replyer_memory_reference
func buildReplyerMemoryReference(structured map[string]any) string {
	rawHits, ok := structured["hits"].([]map[string]any)
	if !ok {
		return ""
	}
	lines := []string{replyerMemoryReferenceMarker}
	query := strings.TrimSpace(stringValue(structured["query"]))
	mode := strings.TrimSpace(stringValue(structured["mode"]))
	effectiveMode := strings.TrimSpace(stringValue(structured["effective_mode"]))
	if query != "" {
		lines = append(lines, "查询："+query)
	}
	if mode != "" {
		modeText := mode
		if effectiveMode != "" && effectiveMode != mode {
			modeText = mode + " -> " + effectiveMode
		}
		lines = append(lines, "模式："+modeText)
	}
	hitLines := make([]string, 0, len(rawHits))
	for index, hit := range rawHits {
		content := strings.TrimSpace(stringValue(hit["content"]))
		if content == "" {
			continue
		}
		hitType := strings.TrimSpace(stringValue(hit["type"]))
		title := strings.TrimSpace(stringValue(hit["title"]))
		labelParts := make([]string, 0, 2)
		for _, part := range []string{title, hitType} {
			if part != "" {
				labelParts = append(labelParts, part)
			}
		}
		label := ""
		if len(labelParts) > 0 {
			label = "（" + strings.Join(labelParts, " / ") + "）"
		}
		normalized := strings.Join(strings.Fields(content), " ")
		hitLines = append(hitLines, fmt.Sprintf("%d. %s%s", index+1, label, normalized))
	}
	if len(hitLines) == 0 {
		return ""
	}
	lines = append(lines, fmt.Sprintf("命中：%d 条", len(hitLines)))
	lines = append(lines, hitLines...)
	return strings.Join(lines, "\n")
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func queryMemoryFailure(message string) StructuredToolResult {
	return StructuredToolResult{Success: false, ErrorMessage: message}
}
