package desktoppet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/maisaka/monitor"
)

type HistoryProvider func(context.Context, map[string]int64) (map[string]any, error)

func (s *Service) SetHistoryProvider(provider HistoryProvider) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.historyProvider = provider
	s.mu.Unlock()
}

func (s *Service) publishHistory(ctx context.Context, request map[string]any, send func(context.Context, map[string]any) error) {
	requestID := asString(request["request_id"])
	commandID, err := newCommandID()
	if err != nil {
		return
	}
	response := map[string]any{"id": commandID, "type": "history", "request_id": requestID}
	cursors, err := historyCursors(request["cursors"])
	if err != nil {
		response["error"] = err.Error()
	} else {
		s.mu.RLock()
		provider := s.historyProvider
		s.mu.RUnlock()
		if provider == nil {
			response["error"] = "桌宠历史服务未就绪"
		} else {
			historyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			result, loadErr := provider(historyCtx, cursors)
			cancel()
			if loadErr != nil {
				response["error"] = loadErr.Error()
			} else {
				for key, value := range result {
					response[key] = value
				}
			}
		}
	}
	if err := send(ctx, response); err != nil && ctx.Err() == nil {
		s.setError(fmt.Errorf("发送桌宠历史失败: %w", err))
	}
}

func historyCursors(value any) (map[string]int64, error) {
	result := make(map[string]int64)
	items, ok := value.(map[string]any)
	if !ok || len(items) > 100 {
		return nil, fmt.Errorf("历史记录游标格式无效")
	}
	for sessionID, raw := range items {
		if len(sessionID) > 200 {
			return nil, fmt.Errorf("历史记录游标无效")
		}
		cursor, ok := asInt(raw)
		if !ok || cursor < 0 {
			return nil, fmt.Errorf("历史记录游标无效")
		}
		result[sessionID] = int64(cursor)
	}
	return result, nil
}

func LoadMonitorHistory(sessionIDs []string, cursors map[string]int64, loadMedia func(kind, hash string) (string, error)) (map[string]any, error) {
	store := monitor.CurrentEventStore()
	if store == nil {
		return nil, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	events := make([]monitor.Event, 0)
	nextCursors := make(map[string]int64)
	for _, sessionID := range sessionIDs {
		before, requested := cursors[sessionID]
		if len(cursors) > 0 && !requested {
			continue
		}
		page, err := store.LoadSessionEventPage(sessionID, before, monitor.DefaultHistoryPageSize)
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if page.HasMore {
			nextCursors[sessionID] = page.OldestEventID
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		return historyEventID(events[i]) < historyEventID(events[j])
	})
	items := make([]map[string]any, 0)
	byMessage := make(map[string]int)
	for _, event := range events {
		if event.Event != monitor.EventMessageIngested && event.Event != monitor.EventMessageSent && event.Event != monitor.EventMessageUpdated {
			continue
		}
		messageID := strings.TrimSpace(historyText(event.Data["message_id"]))
		if messageID == "" {
			continue
		}
		parts := []map[string]any{{"kind": "text", "text": historyText(event.Data["content"])}}
		if media, ok := event.Data["media"].([]any); ok {
			for _, raw := range media {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				kind := historyText(item["kind"])
				if kind != "image" && kind != "emoji" {
					continue
				}
				source := historyText(item["data_url"])
				if source == "" && loadMedia != nil {
					var err error
					source, err = loadMedia(kind, historyText(item["hash"]))
					if err != nil {
						return nil, err
					}
				}
				if source != "" {
					parts = append(parts, map[string]any{"kind": "image", "src": source})
				}
			}
		}
		who := "me"
		if event.Event == monitor.EventMessageSent {
			who = "mai"
		}
		row := map[string]any{"id": messageID, "who": who, "parts": parts}
		if index, exists := byMessage[messageID]; exists {
			items[index] = row
		} else {
			byMessage[messageID] = len(items)
			items = append(items, row)
		}
	}
	return map[string]any{"items": items, "cursors": nextCursors, "has_more": len(nextCursors) > 0}, nil
}

func historyEventID(event monitor.Event) int64 {
	value, ok := asInt(event.Data["event_id"])
	if !ok {
		return 0
	}
	return int64(value)
}

func historyText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
