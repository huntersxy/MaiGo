package chat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/learners"
	"MaiGo/internal/person_info"
)

type LatestChatMessage struct {
	Nickname string
	UserID   int64
	GroupID  *string
	Time     time.Time
}

// chatSessionListRow 是列表/详情共用的聊天流序列化结果
type chatSessionListRow struct {
	ID              uint
	SessionID       string
	DisplayName     string
	ChatType        string
	TargetID        string
	ExternalTarget  string
	Platform        string
	AccountID       *string
	Scope           *string
	UserID          *string
	UserNickname    *string
	UserCardname    *string
	GroupID         *string
	GroupName       *string
	MessageCount    int
	ExpressionCount int
	JargonCount     int
	CreatedAt       float64
	LastActiveAt    *float64
	LatestMessageAt *float64
}

func (d Deps) ChatDB() *gorm.DB {
	if d.Memory == nil {
		return nil
	}
	return d.Memory.GetDB()
}

func chatTargetID(rec *chatsession.Record) string {
	if rec.GroupID != nil && strings.TrimSpace(*rec.GroupID) != "" {
		return strings.TrimSpace(*rec.GroupID)
	}
	if rec.UserID != nil {
		return strings.TrimSpace(*rec.UserID)
	}
	return ""
}

func chatTypeOf(rec *chatsession.Record) string {
	if rec.IsGroup() {
		return "group"
	}
	return "private"
}

func chatTimestampPtr(value *time.Time) *float64 {
	if value == nil || value.IsZero() {
		return nil
	}
	seconds := float64(value.UnixNano()) / 1e9
	return &seconds
}

func ChatDisplayName(rec *chatsession.Record, latest *LatestChatMessage) string {
	if rec.IsGroup() {
		if rec.GroupName != nil && strings.TrimSpace(*rec.GroupName) != "" {
			return strings.TrimSpace(*rec.GroupName)
		}
		return "群聊" + chatTargetID(rec)
	}
	if rec.UserCardname != nil && strings.TrimSpace(*rec.UserCardname) != "" {
		return strings.TrimSpace(*rec.UserCardname) + "的私聊"
	}
	if rec.UserNickname != nil && strings.TrimSpace(*rec.UserNickname) != "" {
		return strings.TrimSpace(*rec.UserNickname) + "的私聊"
	}
	if latest != nil && strings.TrimSpace(latest.Nickname) != "" {
		return strings.TrimSpace(latest.Nickname) + "的私聊"
	}
	if userID := chatTargetID(rec); userID != "" {
		return "用户" + userID + "的私聊"
	}
	return rec.SessionID
}

func (d Deps) LatestChatMessage(ctx context.Context, sessionID string) *LatestChatMessage {
	db := d.ChatDB()
	if db == nil || sessionID == "" {
		return nil
	}
	var row memory.MessageLog
	if err := db.WithContext(ctx).Where("session_id = ?", sessionID).Order("timestamp DESC").Limit(1).Find(&row).Error; err != nil {
		return nil
	}
	if row.ID == 0 {
		return nil
	}
	return &LatestChatMessage{Nickname: row.UserNickname, UserID: row.UserID, Time: row.Timestamp}
}

func (d Deps) chatSessionCounts(ctx context.Context, sessionIDs []string) (map[string]int, map[string]int, map[string]int, error) {
	messages := map[string]int{}
	expressions := map[string]int{}
	jargons := map[string]int{}
	db := d.ChatDB()
	if db == nil || len(sessionIDs) == 0 {
		return messages, expressions, jargons, nil
	}
	type countRow struct {
		SessionID string
		Count     int
	}
	var messageRows []countRow
	if err := db.WithContext(ctx).Model(&memory.MessageLog{}).
		Select("session_id, count(*) AS count").Where("session_id IN ?", sessionIDs).
		Group("session_id").Scan(&messageRows).Error; err != nil {
		return nil, nil, nil, err
	}
	for _, row := range messageRows {
		messages[row.SessionID] = row.Count
	}
	var expressionRows []countRow
	if err := db.WithContext(ctx).Model(&learners.Expression{}).
		Select("session_id, count(*) AS count").Where("session_id IN ?", sessionIDs).
		Group("session_id").Scan(&expressionRows).Error; err == nil {
		for _, row := range expressionRows {
			expressions[row.SessionID] = row.Count
		}
	}
	wanted := map[string]bool{}
	for _, id := range sessionIDs {
		wanted[id] = true
		jargons[id] = 0
	}
	var dictionaries []string
	if err := db.WithContext(ctx).Model(&learners.Jargon{}).
		Where("session_id_dict IS NOT NULL AND session_id_dict <> ''").
		Pluck("session_id_dict", &dictionaries).Error; err == nil {
		for _, raw := range dictionaries {
			var parsed map[string]any
			if json.Unmarshal([]byte(raw), &parsed) != nil {
				continue
			}
			for sessionID := range parsed {
				if wanted[sessionID] {
					jargons[sessionID]++
				}
			}
		}
	}
	return messages, expressions, jargons, nil
}

func (d Deps) buildChatSessionRow(ctx context.Context, rec *chatsession.Record, messageCount, expressionCount, jargonCount int) chatSessionListRow {
	var latest *LatestChatMessage
	if rec.IsGroup() {
		if rec.GroupName == nil || strings.TrimSpace(*rec.GroupName) == "" {
			latest = d.LatestChatMessage(ctx, rec.SessionID)
		}
	} else if rec.UserNickname == nil && rec.UserCardname == nil {
		latest = d.LatestChatMessage(ctx, rec.SessionID)
	}
	targetID := chatTargetID(rec)
	// external_target 是平台侧原始目标标识；QQ 为群号/QQ，非 QQ 为原始字符串
	externalTarget := targetID
	row := chatSessionListRow{
		ID:              rec.ID,
		SessionID:       rec.SessionID,
		DisplayName:     ChatDisplayName(rec, latest),
		ChatType:        chatTypeOf(rec),
		TargetID:        targetID,
		ExternalTarget:  externalTarget,
		Platform:        rec.Platform,
		AccountID:       rec.AccountID,
		Scope:           rec.Scope,
		UserID:          rec.UserID,
		UserNickname:    rec.UserNickname,
		UserCardname:    rec.UserCardname,
		GroupID:         rec.GroupID,
		GroupName:       rec.GroupName,
		MessageCount:    messageCount,
		ExpressionCount: expressionCount,
		JargonCount:     jargonCount,
		CreatedAt:       float64(rec.CreatedTimestamp.UnixNano()) / 1e9,
		LastActiveAt:    chatTimestampPtr(rec.LastActiveTimestamp),
	}
	if latest != nil {
		row.LatestMessageAt = chatTimestampPtr(&latest.Time)
	}
	return row
}

func (row chatSessionListRow) response() map[string]any {
	return map[string]any{
		"id":                row.ID,
		"session_id":        row.SessionID,
		"display_name":      row.DisplayName,
		"chat_type":         row.ChatType,
		"target_id":         row.TargetID,
		"external_target":   row.ExternalTarget,
		"platform":          row.Platform,
		"account_id":        row.AccountID,
		"scope":             row.Scope,
		"user_id":           row.UserID,
		"user_nickname":     row.UserNickname,
		"user_cardname":     row.UserCardname,
		"group_id":          row.GroupID,
		"group_name":        row.GroupName,
		"message_count":     row.MessageCount,
		"expression_count":  row.ExpressionCount,
		"jargon_count":      row.JargonCount,
		"created_at":        row.CreatedAt,
		"last_active_at":    row.LastActiveAt,
		"latest_message":    "",
		"latest_message_at": row.LatestMessageAt,
	}
}

func (d Deps) listChatSessionRows(ctx context.Context, limit, offset int) ([]chatSessionListRow, int64, error) {
	db := d.ChatDB()
	if db == nil {
		return nil, 0, gorm.ErrInvalidDB
	}
	var total int64
	if err := db.WithContext(ctx).Model(&chatsession.Record{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []chatsession.Record
	if err := db.WithContext(ctx).Model(&chatsession.Record{}).
		Order("CASE WHEN last_active_timestamp IS NULL THEN 1 ELSE 0 END").
		Order("last_active_timestamp DESC").Order("created_timestamp DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		if rec.SessionID != "" {
			ids = append(ids, rec.SessionID)
		}
	}
	messages, expressions, jargons, err := d.chatSessionCounts(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]chatSessionListRow, 0, len(records))
	for index := range records {
		rec := &records[index]
		rows = append(rows, d.buildChatSessionRow(ctx, rec, messages[rec.SessionID], expressions[rec.SessionID], jargons[rec.SessionID]))
	}
	return rows, total, nil
}

func (d Deps) findChatSession(ctx context.Context, sessionID string) (*chatsession.Record, error) {
	db := d.ChatDB()
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var record chatsession.Record
	if err := db.WithContext(ctx).Where("session_id = ?", sessionID).Limit(1).Find(&record).Error; err != nil {
		return nil, err
	}
	if record.ID == 0 {
		return nil, nil
	}
	return &record, nil
}

func (d Deps) resolveChatTargetRecords(ctx context.Context, platform, targetID, ruleType string) ([]chatsession.Record, error) {
	manager := chatsession.NewManager(d.Memory.GetDB())
	return manager.ResolveSessionsByTarget(ctx, platform, targetID, ruleType)
}

func (d Deps) chatPlatformCounts(ctx context.Context) ([]map[string]any, error) {
	db := d.ChatDB()
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	type platformRow struct {
		Platform string
		Count    int
	}
	var rows []platformRow
	if err := db.WithContext(ctx).Model(&personinfo.PersonInfo{}).
		Select("platform, count(*) AS count").Group("platform").Order("count DESC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Platform) == "" {
			continue
		}
		result = append(result, map[string]any{"platform": row.Platform, "count": row.Count})
	}
	return result, nil
}
