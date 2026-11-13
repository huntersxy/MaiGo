package monitor

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	MonitorEventSchemaVersion   = 1
	MaxMonitorEventRecords      = 10000
	MaxMonitorEventAgeHours     = 72
	DefaultReplayLimit          = 1000
	MaxReplayLimit              = MaxMonitorEventRecords
	DefaultHistoryPageSize      = 100
	MaxHistoryPageSize          = 500
	MaxSubagentHistoryEvents    = 500
	cleanupCheckIntervalRecords = 200
	cleanupCheckIntervalSeconds = 60
)

var (
	sessionMessageEventTypes  = []string{"message.ingested", "message.sent"}
	subagentHistoryEventTypes = []string{
		"session.start",
		"subagent.started",
		"subagent.round",
		"subagent.tool_result",
		"subagent.finished",
	}
	plannerEvents = map[string]struct{}{
		"planner.progress":  {},
		"planner.finalized": {},
	}
	nonPersistedEvents = map[string]struct{}{
		"stage.status":   {},
		"stage.removed":  {},
		"stage.snapshot": {},
	}
	globalStoreMu sync.RWMutex
	globalStore   *EventStore
)

// MonitorEventPage 单个聊天流的一页历史事件，Events 按 event_id 升序排列
type MonitorEventPage struct {
	Events        []Event
	OldestEventID int64
	HasMore       bool
}

// EventStore 麦麦观察事件账本
type EventStore struct {
	db                  *gorm.DB
	cleanupMu           sync.Mutex
	recordsSinceCleanup int
	lastCleanupAt       time.Time
}

// NewEventStore 创建事件账本
func NewEventStore(db *gorm.DB) *EventStore {
	return &EventStore{db: db, lastCleanupAt: time.Now()}
}

// SetEventStore 设置全局事件账本，供 Publish 与 WebUI 回放使用
func SetEventStore(store *EventStore) {
	globalStoreMu.Lock()
	globalStore = store
	globalStoreMu.Unlock()
}

// CurrentEventStore 返回全局事件账本
func CurrentEventStore() *EventStore {
	globalStoreMu.RLock()
	defer globalStoreMu.RUnlock()
	return globalStore
}

// Record 写入一条麦麦观察事件并返回带 event_id 的清洗后 payload
func (s *EventStore) Record(eventType string, payload map[string]any) (map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	cleaned, err := sanitizeMonitorPayload(payload)
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	timestamp := coerceMonitorFloat(cleaned["timestamp"], now)
	if eventType == "message.ingested" || eventType == "message.updated" {
		timestamp = coerceMonitorFloat(cleaned["received_at"], timestamp)
	}
	sessionID := ""
	if value, ok := cleaned["session_id"]; ok && value != nil {
		sessionID = strings.TrimSpace(fmt.Sprint(value))
	}

	createdAt := time.Now().UTC()
	var eventID int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"INSERT INTO maisaka_monitor_events(event_type,session_id,timestamp,schema_version,payload_json,created_at) VALUES(?,?,?,?, '{}', ?)",
			eventType, sessionID, timestamp, MonitorEventSchemaVersion, createdAt,
		).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT last_insert_rowid()").Scan(&eventID).Error; err != nil {
			return err
		}
		if eventID == 0 {
			return fmt.Errorf("未获得 event_id")
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("写入麦麦观察事件失败: %w", err)
	}
	cleaned["event_id"] = eventID
	cleaned["schema_version"] = MonitorEventSchemaVersion
	raw, err := sonic.MarshalString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("序列化麦麦观察事件失败: %w", err)
	}
	if err := s.db.Exec("UPDATE maisaka_monitor_events SET payload_json=? WHERE event_id=?", raw, eventID).Error; err != nil {
		return nil, fmt.Errorf("更新麦麦观察事件失败: %w", err)
	}
	if err := s.maybeCleanup(); err != nil {
		zap.L().Debug("麦麦观察事件清理失败", zap.Error(err))
	}
	return cleaned, nil
}

// Replay 按 event_id 返回可重放的麦麦观察事件
func (s *EventStore) Replay(sinceEventID int64, limit int) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	limit = clampMonitorLimit(limit, 1, MaxReplayLimit)
	var records []monitorRecord
	var err error
	if sinceEventID > 0 {
		err = s.db.Raw(
			"SELECT event_id,event_type,payload_json FROM maisaka_monitor_events WHERE event_id>? ORDER BY event_id LIMIT ?",
			sinceEventID, limit,
		).Scan(&records).Error
	} else {
		err = s.db.Raw(
			"SELECT event_id,event_type,payload_json FROM maisaka_monitor_events ORDER BY event_id DESC LIMIT ?",
			limit,
		).Scan(&records).Error
		if err == nil {
			reverseMonitorRecords(records)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("读取麦麦观察事件失败: %w", err)
	}
	events := make([]Event, 0, len(records))
	for _, record := range records {
		event, err := recordToReplayEvent(record)
		if err != nil {
			return nil, err
		}
		if !isReplayableMonitorEvent(event) {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

// LoadSessionSummaries 汇总账本中每个聊天流的概要
func (s *EventStore) LoadSessionSummaries() ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	var rows []monitorSummaryRow
	if err := s.db.Raw(
		`SELECT
			session_id,
			COUNT(*) AS event_count,
			MAX(event_id) AS last_event_id,
			MAX(timestamp) AS last_activity,
			MAX(CASE WHEN event_type = 'session.start' THEN event_id END) AS start_event_id,
			MAX(CASE WHEN event_type IN ('message.ingested','message.sent') THEN event_id END) AS message_event_id
		FROM maisaka_monitor_events
		WHERE session_id <> ''
		GROUP BY session_id`,
	).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取麦麦观察会话概要失败: %w", err)
	}
	ids := make([]int64, 0, len(rows)*2)
	for _, row := range rows {
		if row.StartEventID.Valid {
			ids = append(ids, row.StartEventID.Int64)
		}
		if row.MessageEventID.Valid {
			ids = append(ids, row.MessageEventID.Int64)
		}
	}
	details := make(map[int64]Event, len(ids))
	if len(ids) > 0 {
		var records []monitorRecord
		if err := s.db.Raw(
			"SELECT event_id,event_type,payload_json FROM maisaka_monitor_events WHERE event_id IN ?",
			uniqueInt64(ids),
		).Scan(&records).Error; err != nil {
			return nil, fmt.Errorf("读取麦麦观察会话详情失败: %w", err)
		}
		for _, record := range records {
			event, err := recordToReplayEvent(record)
			if err != nil {
				return nil, err
			}
			details[record.EventID] = event
		}
	}
	summaries := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var sessionStart any
		if row.StartEventID.Valid {
			if event, ok := details[row.StartEventID.Int64]; ok {
				sessionStart = event.Data
			}
		}
		var latestMessage any
		if row.MessageEventID.Valid {
			if event, ok := details[row.MessageEventID.Int64]; ok {
				latestMessage = map[string]any{"event": event.Event, "data": event.Data}
			}
		}
		summaries = append(summaries, map[string]any{
			"session_id":     row.SessionID,
			"event_count":    row.EventCount,
			"last_event_id":  row.LastEventID,
			"last_activity":  row.LastActivity,
			"session_start":  sessionStart,
			"latest_message": latestMessage,
		})
	}
	return summaries, nil
}

// LoadSessionEventPage 读取某个聊天流早于 before_event_id 的最近一页事件
func (s *EventStore) LoadSessionEventPage(sessionID string, beforeEventID int64, limit int) (MonitorEventPage, error) {
	if s == nil || s.db == nil {
		return MonitorEventPage{}, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	limit = clampMonitorLimit(limit, 1, MaxHistoryPageSize)
	var records []monitorRecord
	var err error
	if beforeEventID > 0 {
		err = s.db.Raw(
			"SELECT event_id,event_type,payload_json FROM maisaka_monitor_events WHERE session_id=? AND event_id<? ORDER BY event_id DESC LIMIT ?",
			sessionID, beforeEventID, limit+1,
		).Scan(&records).Error
	} else {
		err = s.db.Raw(
			"SELECT event_id,event_type,payload_json FROM maisaka_monitor_events WHERE session_id=? ORDER BY event_id DESC LIMIT ?",
			sessionID, limit+1,
		).Scan(&records).Error
	}
	if err != nil {
		return MonitorEventPage{}, fmt.Errorf("读取麦麦观察会话历史失败: %w", err)
	}
	hasMore := len(records) > limit
	pageRecords := records
	if hasMore {
		pageRecords = records[:limit]
	}
	reverseMonitorRecords(pageRecords)
	page := MonitorEventPage{HasMore: hasMore}
	if len(pageRecords) > 0 {
		page.OldestEventID = pageRecords[0].EventID
	}
	for _, record := range pageRecords {
		event, err := recordToReplayEvent(record)
		if err != nil {
			return MonitorEventPage{}, err
		}
		if !isReplayableMonitorEvent(event) {
			continue
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}

// LoadSessionSubagentEvents 读取某个聊天流早于 before_event_id 的子代理事件
func (s *EventStore) LoadSessionSubagentEvents(sessionID string, beforeEventID int64, limit int) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	if limit <= 0 {
		limit = MaxSubagentHistoryEvents
	}
	var records []monitorRecord
	if err := s.db.Raw(
		"SELECT event_id,event_type,payload_json FROM maisaka_monitor_events WHERE session_id=? AND event_type IN ? AND event_id<? ORDER BY event_id DESC LIMIT ?",
		sessionID, subagentHistoryEventTypes, beforeEventID, limit,
	).Scan(&records).Error; err != nil {
		return nil, fmt.Errorf("读取麦麦观察子代理历史失败: %w", err)
	}
	reverseMonitorRecords(records)
	events := make([]Event, 0, len(records))
	for _, record := range records {
		event, err := recordToReplayEvent(record)
		if err != nil {
			return nil, err
		}
		if isReplayableMonitorEvent(event) {
			events = append(events, event)
		}
	}
	return events, nil
}

// Cleanup 清理超出保留策略的麦麦观察事件记录
func (s *EventStore) Cleanup() (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("麦麦观察事件账本未初始化")
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	return s.cleanupLocked()
}

func (s *EventStore) maybeCleanup() error {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	s.recordsSinceCleanup++
	if s.recordsSinceCleanup < cleanupCheckIntervalRecords && time.Since(s.lastCleanupAt) < cleanupCheckIntervalSeconds*time.Second {
		return nil
	}
	_, err := s.cleanupLocked()
	s.recordsSinceCleanup = 0
	s.lastCleanupAt = time.Now()
	return err
}

func (s *EventStore) cleanupLocked() (int, error) {
	cutoff := time.Now().Add(-MaxMonitorEventAgeHours * time.Hour)
	age := s.db.Exec("DELETE FROM maisaka_monitor_events WHERE created_at < ?", cutoff)
	if age.Error != nil {
		return 0, fmt.Errorf("清理过期麦麦观察事件失败: %w", age.Error)
	}
	excess := s.db.Exec(
		"DELETE FROM maisaka_monitor_events WHERE event_id NOT IN (SELECT event_id FROM maisaka_monitor_events ORDER BY event_id DESC LIMIT ?)",
		MaxMonitorEventRecords,
	)
	if excess.Error != nil {
		return 0, fmt.Errorf("清理超量麦麦观察事件失败: %w", excess.Error)
	}
	return int(age.RowsAffected + excess.RowsAffected), nil
}

type monitorRecord struct {
	EventID     int64  `gorm:"column:event_id"`
	EventType   string `gorm:"column:event_type"`
	PayloadJSON string `gorm:"column:payload_json"`
}

type monitorSummaryRow struct {
	SessionID      string        `gorm:"column:session_id"`
	EventCount     int64         `gorm:"column:event_count"`
	LastEventID    int64         `gorm:"column:last_event_id"`
	LastActivity   float64       `gorm:"column:last_activity"`
	StartEventID   sql.NullInt64 `gorm:"column:start_event_id"`
	MessageEventID sql.NullInt64 `gorm:"column:message_event_id"`
}

func recordToReplayEvent(record monitorRecord) (Event, error) {
	var payload map[string]any
	if err := sonic.UnmarshalString(record.PayloadJSON, &payload); err != nil {
		return Event{}, fmt.Errorf("解析麦麦观察事件 payload 失败: %w", err)
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	if _, ok := payload["event_id"]; !ok {
		payload["event_id"] = record.EventID
	}
	if _, ok := payload["schema_version"]; !ok {
		payload["schema_version"] = MonitorEventSchemaVersion
	}
	return Event{Event: record.EventType, Data: payload}, nil
}

func isReplayableMonitorEvent(event Event) bool {
	if _, ok := plannerEvents[event.Event]; !ok {
		return true
	}
	runID, _ := event.Data["run_id"].(string)
	return strings.TrimSpace(runID) != ""
}

func sanitizeMonitorPayload(payload map[string]any) (map[string]any, error) {
	cleaned, ok := sanitizeMonitorValue(payload).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("麦麦观察事件 payload 必须是字典")
	}
	return cleaned, nil
}

func sanitizeMonitorValue(value any) any {
	switch v := value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case time.Time:
		return v.Format(time.RFC3339Nano)
	case map[string]any:
		cleaned := make(map[string]any, len(v))
		for key, item := range v {
			if key == "data_url" {
				continue
			}
			cleaned[key] = sanitizeMonitorValue(item)
		}
		return cleaned
	case []any:
		cleaned := make([]any, 0, len(v))
		for _, item := range v {
			cleaned = append(cleaned, sanitizeMonitorValue(item))
		}
		return cleaned
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map:
		cleaned := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			if key == "data_url" {
				continue
			}
			cleaned[key] = sanitizeMonitorValue(iter.Value().Interface())
		}
		return cleaned
	case reflect.Slice, reflect.Array:
		cleaned := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			cleaned = append(cleaned, sanitizeMonitorValue(rv.Index(i).Interface()))
		}
		return cleaned
	default:
		return value
	}
}

func coerceMonitorFloat(value any, fallback float64) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int8:
		return float64(v)
	case int16:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case uint:
		return float64(v)
	case uint8:
		return float64(v)
	case uint16:
		return float64(v)
	case uint32:
		return float64(v)
	case uint64:
		return float64(v)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func clampMonitorLimit(limit, defaultLimit, maxLimit int) int {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func reverseMonitorRecords(records []monitorRecord) {
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
}

func uniqueInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
