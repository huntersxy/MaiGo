package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"

	maisaka "maibot-go/internal/maisaka"
	maisakactx "maibot-go/internal/maisaka/context"
)

// ErrChatSessionNotFound 聊天流不存在，对齐 WebUI active-context 的 404
var ErrChatSessionNotFound = errors.New("聊天流不存在")

// ActiveChatContext 当前运行聊天流的上下文概览
type ActiveChatContext struct {
	Memory        []string
	Jargon        []ActiveJargonEntry
	Tools         []string
	RuntimeActive bool
	Platform      string
	IsGroup       bool
	GroupID       string
	UserID        string
}

// ActiveJargonEntry 一条黑话释义
type ActiveJargonEntry struct {
	Name    string
	Meaning string
}

// ChatActiveContext 读取运行中会话的滚动摘要、黑话与活跃工具；会话不存在返回 ErrChatSessionNotFound
func (a *Agent) ChatActiveContext(ctx context.Context, sessionID string) (ActiveChatContext, error) {
	sessionID = strings.TrimSpace(sessionID)
	result := ActiveChatContext{Memory: []string{}, Jargon: []ActiveJargonEntry{}, Tools: []string{}}
	if a == nil || a.chatSessions == nil || sessionID == "" {
		return result, ErrChatSessionNotFound
	}
	record, err := a.chatSessions.GetExistingBySessionID(ctx, sessionID)
	if err != nil {
		return result, ErrChatSessionNotFound
	}
	if record == nil {
		return result, ErrChatSessionNotFound
	}
	result.Platform = record.Platform
	result.IsGroup = record.IsGroup()
	if record.GroupID != nil {
		result.GroupID = strings.TrimSpace(*record.GroupID)
	}
	if record.UserID != nil {
		result.UserID = strings.TrimSpace(*record.UserID)
	}

	kind := "private"
	target := result.UserID
	if result.IsGroup {
		kind = "group"
		target = result.GroupID
	}
	targetID, err := strconv.ParseInt(target, 10, 64)
	if err != nil || targetID <= 0 {
		return result, nil
	}
	session := a.sessions.get(kind, targetID)
	if session == nil {
		return result, nil
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil {
		return result, nil
	}
	result.RuntimeActive = true
	jargonSeen := map[string]bool{}
	for _, message := range runtime.History() {
		if maisakactx.IsContextSummaryMessage(message) {
			if text := strings.TrimSpace(message.Content); text != "" {
				result.Memory = append(result.Memory, text)
			}
			continue
		}
		if message.Role != maisaka.RoleReference || message.MetadataString("reference_type") != maisaka.JargonReferenceType {
			continue
		}
		for _, entry := range activeJargonEntries(message.Metadata["jargon_entries"]) {
			if entry.Name == "" || jargonSeen[entry.Name] {
				continue
			}
			jargonSeen[entry.Name] = true
			result.Jargon = append(result.Jargon, entry)
		}
	}
	return result, nil
}

func activeJargonEntries(raw any) []ActiveJargonEntry {
	entries := make([]ActiveJargonEntry, 0)
	switch value := raw.(type) {
	case [][2]string:
		for _, item := range value {
			entries = append(entries, ActiveJargonEntry{Name: strings.TrimSpace(item[0]), Meaning: strings.TrimSpace(item[1])})
		}
	case [][]string:
		for _, item := range value {
			if len(item) >= 2 {
				entries = append(entries, ActiveJargonEntry{Name: strings.TrimSpace(item[0]), Meaning: strings.TrimSpace(item[1])})
			}
		}
	case [][2]any:
		for _, item := range value {
			name, _ := item[0].(string)
			meaning, _ := item[1].(string)
			entries = append(entries, ActiveJargonEntry{Name: strings.TrimSpace(name), Meaning: strings.TrimSpace(meaning)})
		}
	case []any:
		for _, item := range value {
			switch pair := item.(type) {
			case []any:
				if len(pair) >= 2 {
					name, _ := pair[0].(string)
					meaning, _ := pair[1].(string)
					entries = append(entries, ActiveJargonEntry{Name: strings.TrimSpace(name), Meaning: strings.TrimSpace(meaning)})
				}
			case [2]any:
				name, _ := pair[0].(string)
				meaning, _ := pair[1].(string)
				entries = append(entries, ActiveJargonEntry{Name: strings.TrimSpace(name), Meaning: strings.TrimSpace(meaning)})
			}
		}
	}
	return entries
}
