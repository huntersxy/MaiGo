package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	maisaka "maibot-go/internal/maisaka"
	"maibot-go/internal/person_info"
)

// 启发式记忆参考常量
const (
	HeuristicMemoryReferenceMarker = "【启发式记忆-内部参考】"
	SourceChatSummaryPrefix        = "chat_summary:"
	SourcePersonFactPrefix         = "person_fact:"
	HeuristicMinIntervalSeconds    = 600
	HeuristicMinNewMessages        = 12
	HeuristicReferenceMaxChars     = 1200
	HeuristicSearchLimit           = 5
)

// HeuristicMemoryRecallState 单个聊天流的启发式记忆运行时状态
type HeuristicMemoryRecallState struct {
	LastRecallAt         float64
	MessageCountAtRecall int
	LastReference        string
	CacheExpiresAt       float64
}

// HeuristicMemoryContext 一次启发式记忆召回的上下文
type HeuristicMemoryContext struct {
	SessionID       string
	ChatID          string
	Messages        []maisaka.ContextMessage
	Impression      string
	ActivePersonIDs map[string]struct{}
}

// MemoryHit 一条长期记忆命中
type MemoryHit struct {
	Content  string
	Type     string
	Title    string
	Source   string
	Hash     string
	Metadata map[string]any
	Score    float64
}

// HeuristicMemorySearchService 启发式召回依赖的检索端口
type HeuristicMemorySearchService interface {
	BuildChatImpression(ctx context.Context, messages []maisaka.ContextMessage) (string, error)
	SearchRelatedMemory(ctx context.Context, impression string, limit int) ([]MemoryHit, error)
	ResolveHitSources(ctx context.Context, hits []MemoryHit) map[string]string
}

// HeuristicMemoryInjector 根据当前聊天流印象自然拉起长期记忆
type HeuristicMemoryInjector struct {
	Service HeuristicMemorySearchService
	States  map[string]*HeuristicMemoryRecallState

	MinIntervalSeconds int
	MinNewMessages     int
	MaxReferenceChars  int
	SearchLimit        int
	CacheTTLSeconds    int

	// CrossChatEnabled 允许跨聊天召回；GroupToPrivateEnabled / PrivateToGroupEnabled 控制群私跨越
	CrossChatEnabled      bool
	GroupToPrivateEnabled bool
	PrivateToGroupEnabled bool
	// SessionID 当前会话编号；留空时由 BuildInjectionMessage 填充
	SessionID string
	// SessionLookup 查询来源会话是否为群聊；nil 时只允许本会话
	SessionLookup         func(sessionID string) (isGroup bool, ok bool)
	CurrentSessionIsGroup bool
}

// NewHeuristicMemoryInjector 创建启发式记忆注入器
func NewHeuristicMemoryInjector(service HeuristicMemorySearchService) *HeuristicMemoryInjector {
	return &HeuristicMemoryInjector{
		Service:            service,
		States:             map[string]*HeuristicMemoryRecallState{},
		MinIntervalSeconds: HeuristicMinIntervalSeconds,
		MinNewMessages:     HeuristicMinNewMessages,
		MaxReferenceChars:  HeuristicReferenceMaxChars,
	}
}

// BuildInjectionMessage 构造给 Planner/Replyer 共享的一次性启发式记忆参考
func (i *HeuristicMemoryInjector) BuildInjectionMessage(ctx context.Context, sessionID string, messages []maisaka.ContextMessage) string {
	if i == nil || i.Service == nil || len(messages) == 0 {
		return ""
	}
	i.SessionID = strings.TrimSpace(sessionID)
	state := i.stateFor(sessionID)
	now := float64(time.Now().UnixNano()) / 1e9
	if state.LastReference != "" && state.CacheExpiresAt > now {
		return state.LastReference
	}
	// 对齐 Python heuristic_injector.py:96-101 的 filter_command=True：排除命令消息
	filtered := make([]maisaka.ContextMessage, 0, len(messages))
	for _, message := range messages {
		if message.IsCommand {
			continue
		}
		filtered = append(filtered, message)
	}
	if len(filtered) == 0 {
		return ""
	}
	if !i.CanTrigger(state, len(filtered), now) {
		return ""
	}
	impression, err := i.Service.BuildChatImpression(ctx, filtered)
	if err != nil || strings.TrimSpace(impression) == "" {
		return ""
	}
	searchLimit := i.SearchLimit
	if searchLimit <= 0 {
		searchLimit = HeuristicSearchLimit
	}
	hits, err := i.Service.SearchRelatedMemory(ctx, impression, searchLimit)
	if err != nil || len(hits) == 0 {
		return ""
	}
	resolved := i.Service.ResolveHitSources(ctx, hits)
	activePersons := activePersonIDs(messages)
	allowed := make([]MemoryHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if strings.TrimSpace(hit.Content) == "" {
			continue
		}
		if !i.hitAllowed(hit, resolved[hit.Hash], activePersons) {
			continue
		}
		dedupKey := strings.TrimSpace(hit.Hash)
		if dedupKey == "" {
			dedupKey = strings.TrimSpace(hit.Content)
		}
		if _, duplicate := seen[dedupKey]; duplicate {
			continue
		}
		seen[dedupKey] = struct{}{}
		allowed = append(allowed, hit)
		if len(allowed) >= searchLimit {
			break
		}
	}
	if len(allowed) == 0 {
		return ""
	}
	reference := i.formatReference(allowed)
	if reference == "" {
		return ""
	}
	state.LastRecallAt = now
	state.MessageCountAtRecall = len(messages)
	state.LastReference = reference
	if i.CacheTTLSeconds > 0 {
		state.CacheExpiresAt = now + float64(i.CacheTTLSeconds)
	} else {
		state.CacheExpiresAt = 0
	}
	return reference
}

// ClearSessionReference 清理当前会话的本轮注入参考
func (i *HeuristicMemoryInjector) ClearSessionReference(sessionID string) {
	if i == nil {
		return
	}
	if state, ok := i.States[sessionID]; ok {
		state.LastReference = ""
		state.CacheExpiresAt = 0
	}
}

// CanTrigger 判断当前是否满足启发式召回条件
func (i *HeuristicMemoryInjector) CanTrigger(state *HeuristicMemoryRecallState, totalMessageCount int, now float64) bool {
	if state == nil {
		return true
	}
	if state.LastRecallAt > 0 && now-state.LastRecallAt < float64(i.MinIntervalSeconds) {
		return false
	}
	if state.MessageCountAtRecall > 0 && totalMessageCount-state.MessageCountAtRecall < i.MinNewMessages {
		return false
	}
	return true
}

func (i *HeuristicMemoryInjector) stateFor(sessionID string) *HeuristicMemoryRecallState {
	if i.States == nil {
		i.States = map[string]*HeuristicMemoryRecallState{}
	}
	state, ok := i.States[sessionID]
	if !ok {
		state = &HeuristicMemoryRecallState{}
		i.States[sessionID] = state
	}
	return state
}

func (i *HeuristicMemoryInjector) formatReference(hits []MemoryHit) string {
	maxChars := i.MaxReferenceChars
	if maxChars <= 0 {
		maxChars = HeuristicReferenceMaxChars
	}
	lines := []string{HeuristicMemoryReferenceMarker}
	for index, hit := range hits {
		label := ""
		labelParts := make([]string, 0, 2)
		if strings.TrimSpace(hit.Title) != "" {
			labelParts = append(labelParts, strings.TrimSpace(hit.Title))
		}
		if strings.TrimSpace(hit.Type) != "" {
			labelParts = append(labelParts, strings.TrimSpace(hit.Type))
		}
		if len(labelParts) > 0 {
			label = "（" + strings.Join(labelParts, " / ") + "）"
		}
		content := strings.Join(strings.Fields(hit.Content), " ")
		line := fmt.Sprintf("%d. %s%s", index+1, label, content)
		if len(lines) > 1 && len(strings.Join(lines, ""))+len(line) > maxChars {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) <= 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// ResolveSourceSessionID 从来源前缀解析会话编号
func ResolveSourceSessionID(source, chatID string) string {
	if trimmed := strings.TrimSpace(chatID); trimmed != "" {
		return trimmed
	}
	trimmed := strings.TrimSpace(source)
	if strings.HasPrefix(trimmed, SourceChatSummaryPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, SourceChatSummaryPrefix))
	}
	return ""
}

func (i *HeuristicMemoryInjector) hitAllowed(hit MemoryHit, resolvedSource string, activePersons map[string]struct{}) bool {
	metadata := hit.Metadata
	source := metadataString(metadata, "source")
	if source == "" {
		source = strings.TrimSpace(resolvedSource)
	}
	if source == "" {
		source = strings.TrimSpace(hit.Source)
	}
	sourceType := metadataString(metadata, "source_type")
	chatID := metadataString(metadata, "chat_id")
	personID := metadataString(metadata, "person_id")
	if personID == "" {
		personID = firstMetadataString(metadata, "person_ids")
	}
	if personID == "" && strings.HasPrefix(source, SourcePersonFactPrefix) {
		personID = strings.TrimSpace(strings.TrimPrefix(source, SourcePersonFactPrefix))
	}
	if hit.Type == "episode" {
		return metadataString(metadata, "source") == SourceChatSummaryPrefix+i.SessionID
	}
	if sourceType == "person_fact" || strings.HasPrefix(source, SourcePersonFactPrefix) {
		if personID == "" {
			return false
		}
		_, active := activePersons[personID]
		return active
	}
	if sourceType == "chat_summary" || strings.HasPrefix(source, SourceChatSummaryPrefix) {
		return i.chatAllowed(ResolveSourceSessionID(source, chatID))
	}
	if chatID != "" {
		return i.chatAllowed(chatID)
	}
	return false
}

func (i *HeuristicMemoryInjector) chatAllowed(sourceSessionID string) bool {
	clean := strings.TrimSpace(sourceSessionID)
	if clean == "" {
		return false
	}
	if clean == i.SessionID {
		return true
	}
	if !i.CrossChatEnabled || i.SessionLookup == nil {
		return false
	}
	sourceIsGroup, ok := i.SessionLookup(clean)
	if !ok {
		return false
	}
	if sourceIsGroup && !i.CurrentSessionIsGroup {
		return i.GroupToPrivateEnabled
	}
	if !sourceIsGroup && i.CurrentSessionIsGroup {
		return i.PrivateToGroupEnabled
	}
	return true
}

func activePersonIDs(messages []maisaka.ContextMessage) map[string]struct{} {
	personIDs := make(map[string]struct{})
	for _, message := range messages {
		platform := strings.TrimSpace(message.Platform)
		userID := strings.TrimSpace(message.UserID)
		if platform == "" || userID == "" {
			continue
		}
		personIDs[personinfo.PersonID(platform, userID)] = struct{}{}
	}
	return personIDs
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	switch values := metadata[key].(type) {
	case []string:
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	case []any:
		for _, value := range values {
			if value == nil {
				continue
			}
			if trimmed := strings.TrimSpace(fmt.Sprint(value)); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
