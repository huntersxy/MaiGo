package memory

import (
	"fmt"
	"strings"
)

// 人物画像注入常量
const (
	ProfileQueryLimit        = 4
	ProfileTextMaxChars      = 900
	PersonProfileMarker      = "【人物画像-内部参考】"
	PersonProfileMaxProfiles = 3
	ProfileContextMaxChars   = 1000
)

// PersonProfileCandidate 一个可注入画像的人物候选
type PersonProfileCandidate struct {
	PersonID   string
	PersonName string
	UserID     string
	Source     string
}

// PersonProfileQueryService 人物画像查询端口
type PersonProfileQueryService interface {
	ResolvePersonID(platform, userID, personName string) (string, error)
	ProfileAdmin(personID string, limit int, contextText string) (map[string]any, error)
}

// PersonProfileInjectable 判定人物画像注入是否开启
type PersonProfileInjectable interface {
	EnablePersonProfileInjection() bool
}

// BuildProfileInjectionText 从结构化画像段落构建紧凑注入文本
func BuildProfileInjectionText(profileText string, recentLimit int, uncertainFallbackLimit int, uncertainCandidates []string, includeUncertainFallback bool) string {
	text := strings.TrimSpace(profileText)
	var lines []string
	if text != "" {
		lines = append(lines, text)
	}
	selected := dedupeProfileBullets(uncertainCandidates, 2)
	if len(selected) > 0 {
		lines = append(lines, "## 不确定信息（未确认，不可当作确定事实）")
		lines = append(lines, selected...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// CollectPersonProfileCandidates 按当前对象优先顺序收集本轮可注入画像候选
func CollectPersonProfileCandidates(anchor AnchorMessage, pending []AnchorMessage, maxProfiles int) []PersonProfileCandidate {
	limit := maxProfiles
	if limit < 1 {
		limit = 1
	}
	if !anchor.IsGroup {
		if candidate, ok := senderCandidate(anchor, "private_current_user"); ok {
			return []PersonProfileCandidate{candidate}
		}
		return nil
	}
	candidates := make([]PersonProfileCandidate, 0, limit)
	seen := map[string]struct{}{}
	add := func(candidate PersonProfileCandidate, ok bool) bool {
		if !ok || candidate.PersonID == "" {
			return len(candidates) >= limit
		}
		if _, exists := seen[candidate.PersonID]; exists {
			return len(candidates) >= limit
		}
		seen[candidate.PersonID] = struct{}{}
		candidates = append(candidates, candidate)
		return len(candidates) >= limit
	}
	messages := messagesCurrentFirst(anchor, pending)
	for _, message := range messages {
		if add(senderCandidate(message, "recent_speaker")) {
			break
		}
		for _, mention := range message.Mentions {
			if add(profileCandidate(message.Platform, mention.UserID, candidateName(mention.CardName, mention.Nickname, mention.UserID), "at_user")) {
				break
			}
		}
		if len(candidates) >= limit {
			break
		}
		for _, reply := range message.Replies {
			if strings.TrimSpace(reply.UserID) == "" {
				continue
			}
			if add(profileCandidate(message.Platform, reply.UserID, candidateName(reply.CardName, reply.Nickname, reply.UserID), "reply_sender")) {
				break
			}
		}
		if len(candidates) >= limit {
			break
		}
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// AnchorMessage 画像候选抽取所需的轻量消息视图
type AnchorMessage struct {
	MessageID string
	Platform  string
	IsGroup   bool
	UserID    string
	CardName  string
	Nickname  string
	PlainText string
	Mentions  []ProfileMention
	Replies   []ProfileReply
}

// ProfileMention 被 @ 的对象
type ProfileMention struct {
	UserID   string
	CardName string
	Nickname string
}

// ProfileReply 被引用消息的发送者
type ProfileReply struct {
	UserID   string
	CardName string
	Nickname string
}

// BuildPersonProfileInjectionMessages 构造注入 planner 的一次性人物画像内部参考消息
func BuildPersonProfileInjectionMessages(service PersonProfileQueryService, anchor AnchorMessage, pending []AnchorMessage, maxProfiles int) []string {
	if service == nil {
		return nil
	}
	candidates := CollectPersonProfileCandidates(anchor, pending, maxProfiles)
	if len(candidates) == 0 {
		return nil
	}
	var contextParts []string
	for _, message := range append([]AnchorMessage{anchor}, pending...) {
		if text := strings.TrimSpace(message.PlainText); text != "" {
			contextParts = append(contextParts, text)
		}
	}
	contextText := truncateRunes(strings.Join(contextParts, "\n"), ProfileContextMaxChars)
	blocks := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		payload, err := service.ProfileAdmin(candidate.PersonID, ProfileQueryLimit, contextText)
		if err != nil || payload == nil {
			continue
		}
		if success, _ := payload["success"].(bool); !success {
			continue
		}
		uncertain := make([]string, 0)
		if rawCandidates, ok := payload["uncertain_candidates"].([]any); ok {
			for _, item := range rawCandidates {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, _ := entry["text"].(string); text != "" {
					uncertain = append(uncertain, text)
				}
			}
		}
		profileText := BuildProfileInjectionText(extractProfileText(payload), 2, 1, uncertain, false)
		if profileText == "" {
			continue
		}
		displayName := profileDisplayName(candidate, payload)
		blocks = append(blocks, FormatProfilePersonBlock(displayName, profileText))
	}
	return []string{FormatProfileReferenceBlock(blocks)}
}

// FormatProfileReferenceBlock 渲染人物画像参考块
func FormatProfileReferenceBlock(blocks []string) string {
	joined := strings.TrimSpace(strings.Join(blocks, "\n\n"))
	if joined == "" {
		return ""
	}
	return PersonProfileMarker + "\n" +
		"以下内容仅供内部推理，不要向用户逐字复述。\n\n" +
		joined + "\n\n" +
		"使用时把它当作对当前人物的背景理解；若与当前对话冲突，以当前对话为准。"
}

// FormatProfilePersonBlock 渲染单个人物画像块
func FormatProfilePersonBlock(displayName, profileText string) string {
	return fmt.Sprintf("%s：\n  %s", displayName, TruncateProfileText(profileText))
}

// TruncateProfileText 超长画像截断
func TruncateProfileText(profileText string) string {
	normalized := strings.TrimSpace(profileText)
	if len([]rune(normalized)) <= ProfileTextMaxChars {
		return normalized
	}
	return strings.TrimRight(string([]rune(normalized)[:ProfileTextMaxChars]), " ") + "..."
}

func extractProfileText(payload map[string]any) string {
	if text, _ := payload["profile_text"].(string); strings.TrimSpace(text) != "" {
		return text
	}
	text, _ := payload["summary"].(string)
	return text
}

func profileDisplayName(candidate PersonProfileCandidate, payload map[string]any) string {
	if payload != nil {
		if name, _ := payload["person_name"].(string); strings.TrimSpace(name) != "" {
			return name
		}
	}
	return candidateName(candidate.PersonName, candidate.UserID, candidate.PersonID)
}

func senderCandidate(message AnchorMessage, source string) (PersonProfileCandidate, bool) {
	return profileCandidate(message.Platform, message.UserID, candidateName(message.CardName, message.Nickname, message.UserID), source)
}

func profileCandidate(platform, userID, personName, source string) (PersonProfileCandidate, bool) {
	cleanUserID := strings.TrimSpace(userID)
	cleanName := strings.TrimSpace(personName)
	if cleanUserID == "" && cleanName == "" {
		return PersonProfileCandidate{}, false
	}
	return PersonProfileCandidate{
		PersonID:   cleanUserID,
		PersonName: cleanName,
		UserID:     cleanUserID,
		Source:     source,
	}, true
}

func candidateName(cardName, nickname, userID string) string {
	if name := strings.TrimSpace(cardName); name != "" {
		return name
	}
	if name := strings.TrimSpace(nickname); name != "" {
		return name
	}
	return strings.TrimSpace(userID)
}

func messagesCurrentFirst(anchor AnchorMessage, pending []AnchorMessage) []AnchorMessage {
	messages := []AnchorMessage{anchor}
	seen := map[string]struct{}{messageKey(anchor): {}}
	for index := len(pending) - 1; index >= 0; index-- {
		message := pending[index]
		key := messageKey(message)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		messages = append(messages, message)
	}
	return messages
}

func messageKey(message AnchorMessage) string {
	if id := strings.TrimSpace(message.MessageID); id != "" {
		return "message:" + id
	}
	return fmt.Sprintf("object:%p", &message)
}

func dedupeProfileBullets(candidates []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, limit)
	for _, candidate := range candidates {
		text := strings.TrimSpace(candidate)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
