package websocket

import (
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/maisaka/monitor"
)

// subscribeMonitor 处理麦麦监控域订阅：分页模式下发会话概要，回放模式补发事件账本
func (s *unifiedWSSession) subscribeMonitor(requestID any, data map[string]any) error {
	paged := strings.TrimSpace(wsAnyString(data["mode"])) == "paged"
	sinceEventID := monitorNonNegativeInt(data["since_event_id"])
	replayLimit := monitorReplayLimit(data["replay_limit"])
	mode := "replay"
	if paged {
		mode = "paged"
	}
	s.subs[s.subKey(monitor.Domain, monitor.Topic)] = struct{}{}
	if err := s.sendResponse(requestID, true, map[string]any{
		"domain":         monitor.Domain,
		"topic":          monitor.Topic,
		"mode":           mode,
		"since_event_id": sinceEventID,
		"replay_limit":   replayLimit,
	}, nil); err != nil {
		return err
	}
	if err := s.sendMonitorEvent(monitor.Event{Event: "planner.reset", Data: map[string]any{}}); err != nil {
		return err
	}
	store := monitor.CurrentEventStore()
	if paged {
		summaries := []map[string]any{}
		if store != nil {
			loaded, err := store.LoadSessionSummaries()
			if err != nil {
				zap.L().Warn("读取麦麦观察会话概要失败", zap.Error(err))
			} else {
				summaries = loaded
			}
		}
		if err := s.sendEvent(monitor.Domain, "sessions.snapshot", monitor.Topic, map[string]any{
			"sessions":  summaries,
			"timestamp": nowMonitorTimestamp(),
		}); err != nil {
			return err
		}
	} else if store != nil {
		events, err := store.Replay(sinceEventID, replayLimit)
		if err != nil {
			zap.L().Warn("回放麦麦观察事件失败", zap.Error(err))
		} else {
			for _, event := range events {
				if err := s.sendMonitorReplayEvent(event); err != nil {
					return err
				}
			}
		}
	}
	if err := s.sendEvent(monitor.Domain, "stage.snapshot", monitor.Topic, map[string]any{
		"entries":   monitor.StageStatusSnapshotPayload(),
		"timestamp": nowMonitorTimestamp(),
	}); err != nil {
		return err
	}
	s.ensureMonitorStream()
	return nil
}

// handleMonitorCall 处理麦麦监控域调用请求
func (s *unifiedWSSession) handleMonitorCall(requestID any, message map[string]any) error {
	method := strings.TrimSpace(wsAnyString(message["method"]))
	if method != "history.load" {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "unsupported_method",
			"message": "不支持的监控方法: " + method,
		})
	}
	data, _ := message["data"].(map[string]any)
	sessionID := strings.TrimSpace(wsAnyString(data["session_id"]))
	if sessionID == "" {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "missing_session",
			"message": "历史加载请求缺少 session_id",
		})
	}
	store := monitor.CurrentEventStore()
	if store == nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "history_unavailable",
			"message": "麦麦观察事件账本未初始化",
		})
	}
	beforeEventID := monitorNonNegativeInt(data["before_event_id"])
	limit := monitorHistoryLimit(data["limit"])
	page, err := store.LoadSessionEventPage(sessionID, beforeEventID, limit)
	if err != nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "history_unavailable",
			"message": err.Error(),
		})
	}
	if beforeEventID == 0 && page.HasMore {
		events, err := store.LoadSessionSubagentEvents(sessionID, page.OldestEventID, monitor.MaxSubagentHistoryEvents)
		if err != nil {
			zap.L().Warn("读取麦麦观察子代理历史失败", zap.Error(err))
		} else if len(events) > 0 {
			payload := make([]map[string]any, 0, len(events))
			for _, event := range events {
				payload = append(payload, map[string]any{"event": event.Event, "data": event.Data})
			}
			if err := s.sendEvent(monitor.Domain, "subagent.history", monitor.Topic, map[string]any{
				"session_id": sessionID,
				"events":     payload,
			}); err != nil {
				return err
			}
		}
	}
	for _, event := range page.Events {
		data := make(map[string]any, len(event.Data)+1)
		for key, value := range event.Data {
			data[key] = value
		}
		data["replayed"] = true
		if err := s.sendEvent(monitor.Domain, event.Event, monitor.Topic, data); err != nil {
			return err
		}
	}
	return s.sendResponse(requestID, true, map[string]any{
		"session_id":      sessionID,
		"count":           len(page.Events),
		"oldest_event_id": page.OldestEventID,
		"has_more":        page.HasMore,
	}, nil)
}

func monitorReplayLimit(value any) int {
	raw, ok := monitorNonNegativeIntOK(value)
	if !ok {
		return monitor.DefaultReplayLimit
	}
	if raw < 1 {
		return 1
	}
	if raw > monitor.MaxReplayLimit {
		return monitor.MaxReplayLimit
	}
	return int(raw)
}

func monitorHistoryLimit(value any) int {
	raw, ok := monitorNonNegativeIntOK(value)
	if !ok {
		return monitor.DefaultHistoryPageSize
	}
	if raw < 1 {
		return 1
	}
	if raw > monitor.MaxHistoryPageSize {
		return monitor.MaxHistoryPageSize
	}
	return int(raw)
}

func monitorNonNegativeInt(value any) int64 {
	raw, _ := monitorNonNegativeIntOK(value)
	return raw
}

func monitorNonNegativeIntOK(value any) (int64, bool) {
	switch v := value.(type) {
	case bool:
		return 0, false
	case int:
		return clampMonitorInt(int64(v)), true
	case int64:
		return clampMonitorInt(v), true
	case float64:
		return clampMonitorInt(int64(v)), true
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return 0, false
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, false
		}
		return clampMonitorInt(parsed), true
	default:
		return 0, false
	}
}

func clampMonitorInt(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nowMonitorTimestamp() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
