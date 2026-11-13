package runtime

// Maisaka 启发式长期记忆与图片记忆的一次性注入
// 对照 src/maisaka/memory/heuristic_injector.py 与 image_injector.py 的 planner 注入路径

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"maibot-go/internal/A_memorix"
	"maibot-go/internal/common/database"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/maisaka"
	maisakamemory "maibot-go/internal/maisaka/memory"
	"maibot-go/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	defaultHeuristicWindowSize     = 20
	defaultHeuristicLimit          = 3
	defaultHeuristicMaxChars       = 1200
	defaultHeuristicMinInterval    = 600
	defaultHeuristicMinNewMessages = 12
)

// buildMaisakaPlannerExtraInjections 构造每轮 Planner 的启发式记忆、人物画像与图片记忆注入
func (a *Agent) buildMaisakaPlannerExtraInjections(session *Session) func(context.Context, *maisaka.ContextMessage, []maisaka.ContextMessage) []string {
	if a == nil || session == nil {
		return nil
	}
	instance := a.instanceConfig()
	var integration maibotcfg.AMemorixIntegrationConfig
	var imageConfig maibotcfg.AMemorixImageMemoryConfig
	if instance != nil {
		integration = instance.Bot.AMemorix.Integration
		imageConfig = instance.Bot.AMemorix.ImageMemory
	}
	var heuristic *maisakamemory.HeuristicMemoryInjector
	if integration.HeuristicMemoryRecallEnabled {
		heuristic = maisakamemory.NewHeuristicMemoryInjector(&agentHeuristicMemoryService{agent: a, session: session})
		heuristic.MinIntervalSeconds = firstPositiveInt(integration.HeuristicMemoryRecallMinIntervalSeconds, defaultHeuristicMinInterval)
		heuristic.MinNewMessages = firstPositiveInt(integration.HeuristicMemoryRecallMinNewMessages, defaultHeuristicMinNewMessages)
		heuristic.MaxReferenceChars = firstPositiveInt(integration.HeuristicMemoryRecallMaxChars, defaultHeuristicMaxChars)
		heuristic.SearchLimit = firstPositiveInt(integration.HeuristicMemoryRecallLimit, defaultHeuristicLimit)
		heuristic.CacheTTLSeconds = integration.HeuristicMemoryRecallCacheTtlSeconds
		heuristic.CrossChatEnabled = integration.HeuristicMemoryCrossChatEnabled
		heuristic.GroupToPrivateEnabled = integration.HeuristicMemoryGroupToPrivateEnabled
		heuristic.PrivateToGroupEnabled = integration.HeuristicMemoryPrivateToGroupEnabled
		heuristic.SessionID = sessionIDForSession(session)
		heuristic.CurrentSessionIsGroup = session.kind == memory.ConversationKindGroup
		heuristic.SessionLookup = func(sessionID string) (bool, bool) {
			record, err := a.chatSessions.GetExistingBySessionID(context.Background(), sessionID)
			if err != nil || record == nil {
				return false, false
			}
			return record.IsGroup(), true
		}
	}
	var imageInjector *maisakamemory.ImageMemoryInjector
	if imageConfig.Enabled {
		imageInjector = maisakamemory.NewImageMemoryInjector(&agentImageMemoryService{agent: a, session: session}, true)
		imageInjector.CandidateLimit = firstPositiveInt(imageConfig.CandidateLimit, 3)
		imageInjector.SimilarityThreshold = imageConfig.SimilarityThreshold
	}
	// 对齐 Python person_profile.py:229-236：注入开关与上限都读 a_memorix.integration 配置
	profileEnabled := a.memory != nil && session.memory != nil && integration.EnablePersonProfileInjection
	profileMaxProfiles := firstPositiveInt(integration.PersonProfileInjectionMaxProfiles, maisakamemory.PersonProfileMaxProfiles)
	windowSize := firstPositiveInt(integration.HeuristicMemoryRecallWindowSize, defaultHeuristicWindowSize)
	return func(ctx context.Context, trigger *maisaka.ContextMessage, source []maisaka.ContextMessage) []string {
		out := make([]string, 0, 3)
		history := source
		if runtime := session.MaisakaRuntime(); runtime != nil {
			if full := runtime.History(); len(full) > 0 {
				history = full
			}
		}
		if heuristic != nil && len(history) > 0 {
			if rendered := heuristic.BuildInjectionMessage(ctx, sessionIDForSession(session), history); strings.TrimSpace(rendered) != "" {
				out = append(out, rendered)
			}
		}
		if profileEnabled && trigger != nil {
			anchors := make([]maisakamemory.AnchorMessage, 0, len(source))
			for _, message := range source {
				anchors = append(anchors, anchorMessageFromFocus(message, session.kind == memory.ConversationKindGroup))
			}
			service := &focusPersonProfileService{agent: a, ctx: ctx}
			blocks := maisakamemory.BuildPersonProfileInjectionMessages(
				service,
				anchorMessageFromFocus(*trigger, session.kind == memory.ConversationKindGroup),
				anchors,
				profileMaxProfiles,
			)
			for _, block := range blocks {
				if text := strings.TrimSpace(block); text != "" {
					out = append(out, text)
				}
			}
		}
		if imageInjector != nil {
			imageMessages := source
			if len(imageMessages) == 0 {
				imageMessages = history
			}
			sources, messageIDs := maisakaImageSources(imageMessages, windowSize)
			if len(sources) > 0 {
				rendered, err := imageInjector.BuildInjectionMessage(ctx, sessionIDForSession(session), sources, messageIDs)
				if err != nil {
					zap.L().Debug("Maisaka 图片记忆注入失败，已跳过", zap.Error(err))
				} else if strings.TrimSpace(rendered) != "" {
					out = append(out, rendered)
				}
			}
		}
		return out
	}
}

// agentHeuristicMemoryService 把 A_Memorix 检索与 utils 模型接到启发式注入器
type agentHeuristicMemoryService struct {
	agent   *Agent
	session *Session
}

// heuristicImpressionMessages 对齐 Python generate_response(prompt)：只发送一条 user 消息
func heuristicImpressionMessages(rendered string) []*schema.AgenticMessage {
	return []*schema.AgenticMessage{schema.UserAgenticMessage(rendered)}
}

func (s *agentHeuristicMemoryService) BuildChatImpression(ctx context.Context, messages []maisaka.ContextMessage) (string, error) {
	instance := s.agent.instanceConfig()
	if instance == nil {
		return "", fmt.Errorf("实例配置未就绪")
	}
	integration := instance.Bot.AMemorix.Integration
	windowSize := firstPositiveInt(integration.HeuristicMemoryRecallWindowSize, defaultHeuristicWindowSize)
	recent := messages
	if len(recent) > windowSize {
		recent = recent[len(recent)-windowSize:]
	}
	manager, err := prompt.NewManager()
	if err != nil {
		return "", fmt.Errorf("加载启发式记忆提示词失败: %w", err)
	}
	if strings.TrimSpace(instance.Dir) != "" {
		if loadErr := manager.LoadFromDir(prompt.CustomPromptsRoot(instance.Dir)); loadErr != nil {
			zap.L().Warn("加载实例启发式记忆提示词失败", zap.Error(loadErr))
		}
	}
	rendered, err := manager.LoadPrompt("heuristic_memory_impression", prompt.InitialLocale(), map[string]string{
		"chat_identity":  s.chatIdentity(),
		"message_window": heuristicMessageWindow(recent),
	})
	if err != nil {
		return "", fmt.Errorf("渲染启发式记忆提示词失败: %w", err)
	}
	model := s.impressionModel(ctx)
	if model == nil {
		return heuristicLocalImpression(recent), nil
	}
	response, err := model.Generate(ctx, heuristicImpressionMessages(rendered))
	if err != nil {
		return "", fmt.Errorf("启发式记忆印象生成失败: %w", err)
	}
	impression := strings.TrimSpace(maisakaResponseFromAgentic(response).Content)
	if impression == "" {
		return "", fmt.Errorf("启发式记忆印象为空")
	}
	return impression, nil
}

func (s *agentHeuristicMemoryService) SearchRelatedMemory(ctx context.Context, impression string, limit int) ([]maisakamemory.MemoryHit, error) {
	instance := s.agent.instanceConfig()
	if instance == nil {
		return nil, fmt.Errorf("实例配置未就绪")
	}
	integration := instance.Bot.AMemorix.Integration
	crossChat := integration.HeuristicMemoryCrossChatEnabled
	if limit <= 0 {
		limit = firstPositiveInt(integration.HeuristicMemoryRecallLimit, defaultHeuristicLimit)
	}
	requestLimit := limit * 4
	if requestLimit > 20 {
		requestLimit = 20
	}
	request := memorix.SearchRequest{
		Query:         impression,
		Limit:         requestLimit,
		Mode:          "search",
		ChatID:        sessionIDForSession(s.session),
		RespectFilter: true,
	}
	if crossChat {
		request.ChatID = ""
		request.RespectFilter = false
	}
	result, err := memorix.DefaultRegistry().Host().Search(ctx, request)
	if err != nil {
		return nil, err
	}
	if !result.Success || result.Filtered {
		return nil, nil
	}
	hits := make([]maisakamemory.MemoryHit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		if strings.TrimSpace(hit.Content) == "" {
			continue
		}
		hits = append(hits, maisakamemory.MemoryHit{
			Content:  hit.Content,
			Type:     hit.HitType,
			Title:    hit.Title,
			Source:   hit.Source,
			Hash:     hit.Hash,
			Metadata: hit.Metadata,
			Score:    hit.Score,
		})
	}
	return hits, nil
}

func (s *agentHeuristicMemoryService) ResolveHitSources(ctx context.Context, hits []maisakamemory.MemoryHit) map[string]string {
	hashes := make([]string, 0, len(hits))
	for _, hit := range hits {
		hash := strings.TrimSpace(hit.Hash)
		if hash == "" || heuristicHitHasScopeHint(hit) {
			continue
		}
		hashes = append(hashes, hash)
	}
	if len(hashes) == 0 {
		return map[string]string{}
	}
	invoker, ok := memorix.DefaultRegistry().Host().(interface {
		Invoke(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error)
	})
	if !ok {
		return map[string]string{}
	}
	payload, err := invoker.Invoke(ctx, "memory_delete_admin", map[string]any{
		"action":   "preview",
		"mode":     "paragraph",
		"selector": map[string]any{"hashes": hashes},
	}, nil)
	if err != nil {
		zap.L().Debug("启发式记忆命中来源解析失败，已按未知来源处理", zap.Error(err))
		return map[string]string{}
	}
	root, ok := payload.(map[string]any)
	if !ok {
		var converted map[string]any
		raw, marshalErr := sonic.Marshal(payload)
		if marshalErr != nil || sonic.Unmarshal(raw, &converted) != nil {
			return map[string]string{}
		}
		root = converted
	}
	if success, _ := root["success"].(bool); !success {
		return map[string]string{}
	}
	sources := make(map[string]string)
	items, _ := root["items"].([]any)
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item["item_type"])) != "paragraph" {
			continue
		}
		hash := strings.TrimSpace(fmt.Sprint(item["item_hash"]))
		source := strings.TrimSpace(fmt.Sprint(item["source"]))
		if hash != "" && source != "" {
			sources[hash] = source
		}
	}
	return sources
}

func heuristicHitHasScopeHint(hit maisakamemory.MemoryHit) bool {
	metadata := hit.Metadata
	if metadata != nil {
		for _, key := range []string{"source", "chat_id", "person_id", "person_ids"} {
			if value, ok := metadata[key]; ok && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
				return true
			}
		}
	}
	source := strings.TrimSpace(hit.Source)
	return strings.HasPrefix(source, maisakamemory.SourceChatSummaryPrefix) || strings.HasPrefix(source, maisakamemory.SourcePersonFactPrefix)
}

func (s *agentHeuristicMemoryService) chatIdentity() string {
	session := s.session
	parts := []string{"chat_type: private", "platform: " + sessionPlatform(session, s.agent)}
	if session != nil && session.kind == "group" {
		parts[0] = "chat_type: group"
		parts = append(parts, "group_id: "+strconv.FormatInt(session.targetID, 10))
	} else if session != nil {
		parts = append(parts, "user_id: "+strconv.FormatInt(session.targetID, 10))
	}
	parts = append(parts, "display_name: "+sessionIDForSession(session))
	return strings.Join(parts, "\n")
}

func (s *agentHeuristicMemoryService) impressionModel(ctx context.Context) model.AgenticModel {
	if generation := generationFrom(ctx); generation != nil {
		if generation.utilsModel != nil {
			return generation.utilsModel
		}
		if generation.model != nil {
			return generation.model
		}
	}
	if generation := s.agent.currentGeneration(); generation != nil {
		if generation.utilsModel != nil {
			return generation.utilsModel
		}
		return generation.model
	}
	return nil
}

// heuristicMessageWindow 把最近消息压成提示词窗口文本
func heuristicMessageWindow(messages []maisaka.ContextMessage) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		text := strings.TrimSpace(message.Content)
		if text == "" {
			continue
		}
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
		runes := []rune(text)
		if len(runes) > 220 {
			text = strings.TrimRight(string(runes[:220]), " ") + "..."
		}
		sender := message.MetadataString("sender_name")
		if strings.TrimSpace(sender) == "" {
			sender = firstNonEmptyString(message.UserID, "unknown_user")
		}
		lines = append(lines, "- "+sender+": "+text)
	}
	if len(lines) == 0 {
		return "no text messages available"
	}
	return strings.Join(lines, "\n")
}

// heuristicLocalImpression 模型不可用时的本地退化印象，只使用消息原文
func heuristicLocalImpression(messages []maisaka.ContextMessage) string {
	lines := make([]string, 0, 6)
	start := len(messages) - 6
	if start < 0 {
		start = 0
	}
	for _, message := range messages[start:] {
		text := strings.TrimSpace(message.Content)
		if text == "" {
			continue
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, " / ")
}

func firstPositiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func sessionIDForSession(session *Session) string {
	if session == nil {
		return ""
	}
	if runtime := session.MaisakaRuntime(); runtime != nil {
		return runtime.SessionID()
	}
	return ""
}

func sessionPlatform(session *Session, agent *Agent) string {
	if session == nil || agent == nil {
		return ""
	}
	return agent.conversationPlatform(session.kind, session.targetID)
}

func maisakaImageSources(messages []maisaka.ContextMessage, windowSize int) ([][]maisakamemory.ContextImageSource, []string) {
	if windowSize <= 0 {
		windowSize = defaultHeuristicWindowSize
	}
	start := len(messages) - windowSize
	if start < 0 {
		start = 0
	}
	sources := make([][]maisakamemory.ContextImageSource, 0, len(messages)-start)
	ids := make([]string, 0, len(messages)-start)
	for _, message := range messages[start:] {
		if len(message.Images) == 0 {
			continue
		}
		components := make([]maisakamemory.ContextImageSource, 0, len(message.Images))
		for index, image := range message.Images {
			hash := imageMediaHash(image.MediaRef)
			if hash == "" {
				continue
			}
			components = append(components, maisakamemory.ContextImageSource{
				BinaryHash:    hash,
				ComponentPath: strconv.Itoa(index),
			})
		}
		if len(components) == 0 {
			continue
		}
		sources = append(sources, components)
		ids = append(ids, message.MessageID)
	}
	return sources, ids
}

// imageMediaHash 对 base64、文件或引用计算内容哈希
func imageMediaHash(mediaRef string) string {
	raw := strings.TrimSpace(mediaRef)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "base64://") {
		raw = strings.TrimPrefix(raw, "base64://")
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) > 0 {
		return imageSHA256(decoded)
	}
	if bytes, err := os.ReadFile(raw); err == nil && len(bytes) > 0 {
		return imageSHA256(bytes)
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return imageSHA256([]byte(raw))
	}
	return ""
}

// agentImageMemoryService 用 A_Memorix image_memory component 查询同图与相似图
type agentImageMemoryService struct {
	agent   *Agent
	session *Session
}

func (s *agentImageMemoryService) SearchImageMemory(ctx context.Context, contentHash, externalRef, sessionID string, candidateLimit int, similarityThreshold float64) (maisakamemory.ImageMemorySearchResult, error) {
	if strings.TrimSpace(contentHash) == "" {
		return maisakamemory.ImageMemorySearchResult{Success: true}, nil
	}
	host := memorix.DefaultRegistry().Host()
	invoker, ok := host.(imageMemoryInvoker)
	if !ok {
		return maisakamemory.ImageMemorySearchResult{Success: false, Error: "A_Memorix 图片记忆服务未接线"}, nil
	}
	limit := candidateLimit
	if limit <= 0 {
		limit = 3
	}
	payload := map[string]any{
		"action":               "search",
		"content_hash":         contentHash,
		"current_external_ref": externalRef,
		"chat_id":              sessionID,
		"session_id":           sessionID,
		"candidate_limit":      limit,
	}
	if similarityThreshold > 0 {
		payload["similarity_threshold"] = similarityThreshold
	}
	result, err := invoker.Invoke(ctx, "image_memory", payload, nil)
	if err != nil {
		return maisakamemory.ImageMemorySearchResult{Success: false, Error: err.Error()}, nil
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		return maisakamemory.ImageMemorySearchResult{Success: false, Error: "图片记忆返回非对象"}, nil
	}
	success := true
	if value, exists := mapped["success"].(bool); exists {
		success = value
	}
	searchResult := maisakamemory.ImageMemorySearchResult{
		Success: success,
		Error:   mapStringValue(mapped, "error"),
	}
	for _, hit := range mapList(mapped["hits"]) {
		item := maisakamemory.ImageMemoryHit{
			MatchKind:  mapStringValue(hit, "match_kind"),
			Similarity: mapNumberValue(hit, "similarity"),
		}
		for _, observation := range mapList(hit["observations"]) {
			if text := imageObservationText(observation); text != "" {
				item.Observations = append(item.Observations, text)
			}
		}
		for _, memory := range mapList(hit["related_memories"]) {
			content := imageObservationText(memory)
			if content == "" {
				continue
			}
			item.RelatedMemories = append(item.RelatedMemories, maisakamemory.ImageRelatedMemory{
				Content:    content,
				TargetType: mapStringValue(memory, "target_type"),
				TargetID:   mapStringValue(memory, "target_id"),
			})
		}
		searchResult.Hits = append(searchResult.Hits, item)
	}
	return searchResult, nil
}

// mapList 兼容 Go 运行时直接返回的 []map 与 JSON 解码后的 []any
func mapList(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	}
	return nil
}

func mapStringValue(mapped map[string]any, key string) string {
	if mapped == nil {
		return ""
	}
	value, _ := mapped[key].(string)
	return strings.TrimSpace(value)
}

func mapNumberValue(mapped map[string]any, key string) float64 {
	if mapped == nil {
		return 0
	}
	switch value := mapped[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	}
	return 0
}

func imageObservationText(mapped map[string]any) string {
	if mapped == nil {
		return ""
	}
	return firstNonEmptyString(mapStringValue(mapped, "content"), mapStringValue(mapped, "text"), mapStringValue(mapped, "summary"))
}

// imageSHA256 计算图片内容摘要，供 A_Memorix 同图检索
func imageSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
