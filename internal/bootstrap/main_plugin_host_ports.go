package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"MaiGo/internal/A_memorix"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/person_info"
	"MaiGo/internal/plugin_market"
)

// pluginPersonPort 把人物画像 Registry 接入插件 Host person.* 能力
type pluginPersonPort struct {
	registry *personinfo.Registry
}

func newPluginPersonPort(registry *personinfo.Registry) *pluginPersonPort {
	if registry == nil {
		registry = personinfo.DefaultRegistry()
	}
	return &pluginPersonPort{registry: registry}
}

func (p *pluginPersonPort) GetID(_ context.Context, platform, userID string) (string, error) {
	if p == nil || p.registry == nil {
		return "", fmt.Errorf("人物服务未初始化")
	}
	platform = strings.TrimSpace(platform)
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return "", fmt.Errorf("platform 和 user_id 不能为空")
	}
	return personinfo.PersonID(platform, userID), nil
}

func (p *pluginPersonPort) GetIDByName(ctx context.Context, personName string) (string, error) {
	if p == nil || p.registry == nil {
		return "", fmt.Errorf("人物服务未初始化")
	}
	return p.registry.GetPersonIDByPersonName(ctx, strings.TrimSpace(personName)), nil
}

func (p *pluginPersonPort) GetValue(ctx context.Context, personID, fieldName string) (any, bool, error) {
	if p == nil || p.registry == nil {
		return nil, false, fmt.Errorf("人物服务未初始化")
	}
	personID = strings.TrimSpace(personID)
	fieldName = strings.TrimSpace(fieldName)
	rec, err := p.registry.GetPersonRecord(ctx, personID)
	if err != nil {
		if errors.Is(err, personinfo.ErrPersonNotFound) {
			value, ok := pluginUnknownPersonValue(personID, fieldName)
			return value, ok, nil
		}
		return nil, false, err
	}
	profile := personinfo.ProfileFromRecord(rec)
	switch fieldName {
	case "person_id":
		return profile.PersonID, true, nil
	case "platform":
		return profile.Platform, true, nil
	case "user_id":
		return profile.UserID, true, nil
	case "nickname":
		return profile.Nickname, true, nil
	case "person_name":
		return profile.PersonName, true, nil
	case "is_known":
		return profile.IsKnown, true, nil
	case "know_times":
		return profile.KnowCounts, true, nil
	case "group_cardname_list":
		cards := make([]map[string]string, 0, len(profile.GroupCards))
		for _, card := range profile.GroupCards {
			cards = append(cards, map[string]string{"group_id": card.GroupID, "group_cardname": card.GroupCardname})
		}
		return cards, true, nil
	default:
		return nil, false, nil
	}
}

func pluginUnknownPersonValue(personID, fieldName string) (any, bool) {
	switch fieldName {
	case "person_id":
		return personID, true
	case "is_known":
		return false, true
	case "person_name":
		if len(personID) > 4 {
			return "未知用户" + personID[:4], true
		}
		return "未知用户" + personID, true
	case "know_times":
		return 0, true
	case "group_cardname_list":
		return []map[string]string{}, true
	default:
		return nil, false
	}
}

// pluginFrequencyPort 读取和调整会话回复频率倍率
type pluginFrequencyPort struct {
	runtime func(string) (maisakaRuntimeView, error)
}

// maisakaRuntimeView 只暴露频率端口需要的运行时能力，便于测试替换
type maisakaRuntimeView interface {
	BaseReplyFrequency() float64
	TalkFrequencyAdjust() float64
	AdjustTalkFrequency(float64)
	IsGroupSession() bool
	FocusModeActive() bool
}

func newPluginFrequencyPort(agentRef func() *maisaruntime.Agent) *pluginFrequencyPort {
	return &pluginFrequencyPort{runtime: func(sessionID string) (maisakaRuntimeView, error) {
		if agentRef == nil {
			return nil, fmt.Errorf("Agent 未初始化")
		}
		ag := agentRef()
		if ag == nil {
			return nil, fmt.Errorf("Agent 未初始化")
		}
		runtime, err := ag.SessionRuntime(sessionID)
		if err != nil {
			return nil, err
		}
		return runtime, nil
	}}
}

func (p *pluginFrequencyPort) CurrentTalkValue(_ context.Context, chatID string) (float64, error) {
	runtime, err := p.runtimeFor(chatID)
	if err != nil {
		return 0, err
	}
	base := runtime.BaseReplyFrequency()
	if !runtime.IsGroupSession() {
		return base, nil
	}
	if runtime.FocusModeActive() {
		return 1, nil
	}
	adjust := runtime.TalkFrequencyAdjust()
	if base <= 0 || adjust <= 0 {
		return 0, nil
	}
	return math.Max(0, base*adjust), nil
}

func (p *pluginFrequencyPort) GetAdjust(_ context.Context, chatID string) (float64, error) {
	runtime, err := p.runtimeFor(chatID)
	if err != nil {
		return 0, err
	}
	if !runtime.IsGroupSession() {
		return 1, nil
	}
	return runtime.TalkFrequencyAdjust(), nil
}

func (p *pluginFrequencyPort) SetAdjust(_ context.Context, chatID string, value float64) error {
	runtime, err := p.runtimeFor(chatID)
	if err != nil {
		return err
	}
	runtime.AdjustTalkFrequency(value)
	return nil
}

func (p *pluginFrequencyPort) runtimeFor(chatID string) (maisakaRuntimeView, error) {
	if p == nil || p.runtime == nil {
		return nil, fmt.Errorf("聊天频率服务未初始化")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id 不能为空")
	}
	runtime, err := p.runtime(chatID)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, fmt.Errorf("会话运行时尚未创建: %s", chatID)
	}
	return runtime, nil
}

// pluginKnowledgePort 通过 Memorix Host 执行长期记忆检索
type pluginKnowledgePort struct {
	host func() memorix.Host
}

func newPluginKnowledgePort() *pluginKnowledgePort {
	return &pluginKnowledgePort{host: func() memorix.Host { return memorix.DefaultRegistry().Host() }}
}

func (p *pluginKnowledgePort) Search(ctx context.Context, _ string, args map[string]any) (any, error) {
	query := strings.TrimSpace(pluginStringArg(args, "query"))
	if query == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 query"}, nil
	}
	limit := pluginIntArg(args, "limit", 5)
	if limit < 1 {
		limit = 1
	}
	mode := strings.TrimSpace(pluginStringArg(args, "mode"))
	if mode == "" {
		mode = "search"
	}
	respectFilter := true
	if raw, exists := args["respect_filter"]; exists {
		respectFilter = pluginPythonBool(raw)
	}
	host := p.host
	if host == nil {
		return map[string]any{"success": false, "error": "长期记忆服务未初始化"}, nil
	}
	searcher := host()
	if searcher == nil {
		return map[string]any{"success": false, "error": "长期记忆服务未初始化"}, nil
	}
	result, err := searcher.Search(ctx, memorix.SearchRequest{
		Query:         query,
		Limit:         limit,
		Mode:          mode,
		ChatID:        strings.TrimSpace(pluginStringArg(args, "chat_id")),
		PersonID:      strings.TrimSpace(pluginStringArg(args, "person_id")),
		TimeStart:     args["time_start"],
		TimeEnd:       args["time_end"],
		RespectFilter: respectFilter,
		UserID:        strings.TrimSpace(pluginStringArg(args, "user_id")),
		GroupID:       strings.TrimSpace(pluginStringArg(args, "group_id")),
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	if !result.Success {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = "长期记忆检索失败"
		}
		return map[string]any{"success": false, "error": message}, nil
	}
	text := pluginKnowledgeText(result.Hits, limit)
	if text == "" {
		return map[string]any{"success": true, "content": "你不太了解有关" + query + "的知识"}, nil
	}
	return map[string]any{"success": true, "content": "你知道这些知识: " + text}, nil
}

func pluginKnowledgeText(hits []memorix.Hit, limit int) string {
	lines := make([]string, 0, limit)
	for index, hit := range hits {
		if index >= limit {
			break
		}
		content := strings.TrimSpace(hit.Content)
		content = strings.ReplaceAll(content, "\n", " ")
		if runes := []rune(content); len(runes) > 160 {
			content = string(runes[:160]) + "..."
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, content))
	}
	return strings.Join(lines, "\n")
}

// pluginMarketPort 延迟绑定 WebUI 创建的插件市场服务
type pluginMarketPort struct {
	mu      sync.RWMutex
	service *pluginmarket.Service
}

func newPluginMarketPort() *pluginMarketPort { return &pluginMarketPort{} }

func (p *pluginMarketPort) SetService(service *pluginmarket.Service) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.service = service
	p.mu.Unlock()
}

func (p *pluginMarketPort) current() (*pluginmarket.Service, error) {
	if p == nil {
		return nil, fmt.Errorf("插件市场服务未初始化")
	}
	p.mu.RLock()
	service := p.service
	p.mu.RUnlock()
	if service == nil {
		return nil, fmt.Errorf("插件市场服务未初始化")
	}
	return service, nil
}

func (p *pluginMarketPort) Search(ctx context.Context, args map[string]any) (any, error) {
	service, err := p.current()
	if err != nil {
		return nil, err
	}
	request, err := pluginMarketSearchRequest(args)
	if err != nil {
		return nil, err
	}
	result, err := service.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "result": result.Payload()}, nil
}

func (p *pluginMarketPort) GetDetail(ctx context.Context, args map[string]any) (any, error) {
	service, err := p.current()
	if err != nil {
		return nil, err
	}
	pluginID := strings.TrimSpace(pluginStringArg(args, "plugin_id"))
	if pluginID == "" {
		return nil, fmt.Errorf("plugin_id 不能为空")
	}
	if len([]rune(pluginID)) > 500 {
		return nil, fmt.Errorf("plugin_id 长度不能超过 500")
	}
	result, err := service.Detail(ctx, pluginID, pluginBoolArg(args, "force_refresh"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "result": result.Payload()}, nil
}

func (p *pluginMarketPort) GetReadme(ctx context.Context, args map[string]any) (any, error) {
	service, err := p.current()
	if err != nil {
		return nil, err
	}
	pluginID := strings.TrimSpace(pluginStringArg(args, "plugin_id"))
	if pluginID == "" {
		return nil, fmt.Errorf("plugin_id 不能为空")
	}
	if len([]rune(pluginID)) > 500 {
		return nil, fmt.Errorf("plugin_id 长度不能超过 500")
	}
	maxChars := pluginIntArg(args, "max_chars", 8000)
	if maxChars < 500 || maxChars > 20000 {
		return nil, fmt.Errorf("max_chars 必须在 500 到 20000 之间")
	}
	branch := strings.TrimSpace(pluginStringArg(args, "branch"))
	if branch == "" {
		branch = "main"
	}
	if len([]rune(branch)) > 200 {
		return nil, fmt.Errorf("branch 长度不能超过 200")
	}
	preferLocal := true
	if _, exists := args["prefer_local"]; exists {
		preferLocal = pluginBoolArg(args, "prefer_local")
	}
	result, err := service.GetReadme(ctx, pluginID, maxChars, preferLocal, branch, pluginBoolArg(args, "force_refresh"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "result": result.Payload()}, nil
}

func pluginMarketSearchRequest(args map[string]any) (pluginmarket.SearchRequest, error) {
	request := pluginmarket.SearchRequest{
		Query:           pluginStringArg(args, "query"),
		Category:        pluginStringArg(args, "category"),
		Page:            pluginIntArg(args, "page", 1),
		Limit:           pluginIntArg(args, "limit", 20),
		SortBy:          pluginmarket.MarketSort(pluginStringArg(args, "sort_by")),
		CompatibleOnly:  pluginBoolArg(args, "compatible_only"),
		IncludeManifest: pluginBoolArg(args, "include_manifest"),
		IncludeStats:    pluginBoolArg(args, "include_stats"),
		ForceRefresh:    pluginBoolArg(args, "force_refresh"),
	}
	if len([]rune(request.Query)) > 500 {
		return request, fmt.Errorf("query 长度不能超过 500")
	}
	if len([]rune(request.Category)) > 100 {
		return request, fmt.Errorf("category 长度不能超过 100")
	}
	if request.Page < 1 {
		return request, fmt.Errorf("page 必须大于等于 1")
	}
	if request.Limit < 1 || request.Limit > 50 {
		return request, fmt.Errorf("limit 必须在 1 到 50 之间")
	}
	if request.SortBy == "" {
		request.SortBy = pluginmarket.SortName
	}
	switch request.SortBy {
	case pluginmarket.SortName, pluginmarket.SortDownloads, pluginmarket.SortLikes, pluginmarket.SortRating,
		pluginmarket.SortDownloadsGrowth7, pluginmarket.SortLikesGrowth7,
		pluginmarket.SortDownloadsGrowth30, pluginmarket.SortLikesGrowth30:
	default:
		return request, fmt.Errorf("sort_by 不支持: %s", request.SortBy)
	}
	return request, nil
}

func pluginStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch value := args[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func pluginBoolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	return pluginBoolValue(args[key])
}

// pluginPythonBool 对齐 Python bool()：非空字符串（包括 "false"）为真
func pluginPythonBool(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	default:
		return true
	}
}

func pluginBoolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case nil:
		return false
	default:
		return false
	}
}

func pluginIntArg(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}
