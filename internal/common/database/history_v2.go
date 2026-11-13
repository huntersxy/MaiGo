package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

type HistoryQueryV2 struct {
	ConversationKind string
	TargetID         int64
	UserID           int64
	Keywords         []string
	Match            string
	ExcludeKeywords  []string
	From             *time.Time
	To               *time.Time
	MaxMessageLogID  uint
	FilterCommand    bool
	Cursor           string
	Limit            int
}

type HistoryFilter struct {
	ConversationKind string     `json:"conversation_kind"`
	TargetID         int64      `json:"target_id"`
	UserID           int64      `json:"user_id"`
	Keywords         []string   `json:"keywords"`
	Match            string     `json:"match"`
	ExcludeKeywords  []string   `json:"exclude_keywords"`
	From             *time.Time `json:"from,omitempty"`
	To               *time.Time `json:"to,omitempty"`
	MaxMessageLogID  uint       `json:"max_message_log_id,omitempty"`
	FilterCommand    bool       `json:"filter_command,omitempty"`
}

type HistoryPage struct {
	Items      []MessageLog `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
	HasMore    bool         `json:"has_more"`
}

type historySearchRow struct {
	MessageLog
	SearchScore float64 `gorm:"column:search_score"`
}

func historyKeywordScoreExpr() string {
	// SQLite 无 pg_trgm.similarity；与 similarityScore 一致：包含关键词记 1，否则 0
	return "CASE WHEN lower(ml.processed_plain_text) LIKE '%' || ? || '%' THEN 1.0 ELSE 0.0 END"
}

func historyCursorCondition(scoreExpr string, scoreArgs []any, cursor PageCursor) (string, []any) {
	if scoreExpr == "" {
		return "ml.id<?", []any{cursor.LastID}
	}
	args := make([]any, 0, len(scoreArgs)*2+3)
	args = append(args, scoreArgs...)
	args = append(args, scoreArgs...)
	args = append(args, cursor.LastKey, cursor.LastKey, cursor.LastID)
	return "(" + scoreExpr + " < ? OR (" + scoreExpr + " = ? AND ml.id < ?))", args
}

func (m *Manager) SearchHistoryV2(ctx context.Context, input HistoryQueryV2) (HistoryPage, error) {
	match, err := NormalizeMatch(input.Match)
	if err != nil {
		return HistoryPage{}, err
	}
	limit := NormalizeLimit(input.Limit)
	filter := HistoryFilter{
		ConversationKind: strings.TrimSpace(input.ConversationKind), TargetID: input.TargetID, UserID: input.UserID,
		Keywords: NormalizeKeywords(input.Keywords), Match: match, ExcludeKeywords: NormalizeKeywords(input.ExcludeKeywords),
		From: input.From, To: input.To, MaxMessageLogID: input.MaxMessageLogID,
		FilterCommand: input.FilterCommand,
	}
	cursor, err := DecodeCursor(input.Cursor)
	if err != nil {
		return HistoryPage{}, err
	}
	if cursor.FilterHash == "" {
		cursor.FilterHash = CursorFilterHash(filter)
	}
	if err := ValidateCursorFilter(cursor, filter); err != nil {
		return HistoryPage{}, err
	}
	q := m.db.WithContext(ctx).Table("mai_messages ml").
		Where(NoticeExternalMessageIDPredicate("ml.external_message_id"))
	if filter.ConversationKind != "" {
		q = q.Where("ml.conversation_kind=?", filter.ConversationKind)
	}
	if filter.TargetID > 0 {
		q = q.Where("ml.target_id=?", filter.TargetID)
	}
	if filter.UserID != 0 {
		q = q.Where("ml.user_id=?", filter.UserID)
	}
	if filter.From != nil {
		q = q.Where("ml.timestamp>=?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("ml.timestamp<=?", *filter.To)
	}
	if filter.MaxMessageLogID > 0 {
		q = q.Where("ml.id<=?", filter.MaxMessageLogID)
	}
	if filter.FilterCommand {
		q = q.Where("ml.is_command=?", false)
	}

	keywords := NormalizeKeywords(input.Keywords)
	excludes := NormalizeKeywords(input.ExcludeKeywords)
	scoreParts := make([]string, 0, len(keywords))
	scoreArgs := make([]any, 0, len(keywords))
	if len(keywords) > 0 {
		parts := make([]string, 0, len(keywords))
		args := make([]any, 0, len(keywords)*2)
		for _, keyword := range keywords {
			pattern := "%" + strings.ToLower(keyword) + "%"
			parts = append(parts, "(lower(ml.processed_plain_text) LIKE ? OR lower(ml.processed_plain_text) LIKE ?)")
			args = append(args, pattern, pattern)
			scoreParts = append(scoreParts, historyKeywordScoreExpr())
			scoreArgs = append(scoreArgs, strings.ToLower(keyword))
		}
		join := " AND "
		if match == "or" {
			join = " OR "
		}
		q = q.Where("("+strings.Join(parts, join)+")", args...)
	}
	for _, keyword := range excludes {
		pattern := "%" + strings.ToLower(keyword) + "%"
		q = q.Where("NOT (lower(ml.processed_plain_text) LIKE ? OR lower(ml.processed_plain_text) LIKE ?)", pattern, pattern)
	}

	scoreExpr := ""
	if len(scoreParts) > 0 {
		// SQLite：单参数 max/min 是聚合；多参数才是标量。单关键词直接用 CASE。
		if len(scoreParts) == 1 {
			scoreExpr = scoreParts[0]
		} else if match == "and" {
			scoreExpr = "min(" + strings.Join(scoreParts, ",") + ")"
		} else {
			scoreExpr = "max(" + strings.Join(scoreParts, ",") + ")"
		}
		q = q.Select("ml.*, ("+scoreExpr+") AS search_score", scoreArgs...)
	} else {
		q = q.Select("ml.*")
	}
	if cursor.LastID != 0 {
		condition, args := historyCursorCondition(scoreExpr, scoreArgs, cursor)
		q = q.Where(condition, args...)
	}
	order := "ml.id DESC"
	if scoreExpr != "" {
		order = "search_score DESC,ml.id DESC"
	}
	var rows []historySearchRow
	if err := q.Order(order).Limit(limit + 1).Find(&rows).Error; err != nil {
		return HistoryPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]MessageLog, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.MessageLog)
	}
	page := HistoryPage{Items: items, HasMore: hasMore}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = NewPageCursor("similarity", last.SearchScore, time.Time{}, last.ID, time.Time{}, filter)
	}
	return page, nil
}

type HistoryRange struct {
	ConversationKind string
	TargetID         int64
	AroundMessageID  int64
	TopicID          uint
	MaxMessageLogID  uint
	FilterCommand    bool
	Before           int
	After            int
}

func (m *Manager) ReadHistoryRange(ctx context.Context, input HistoryRange) ([]MessageLog, error) {
	if input.ConversationKind == "" || input.TargetID <= 0 {
		return nil, fmt.Errorf("历史读取需要会话来源")
	}
	if input.AroundMessageID == 0 && input.TopicID == 0 {
		return nil, fmt.Errorf("历史读取需要消息或话题")
	}
	before, after := input.Before, input.After
	if before <= 0 {
		before = 5
	}
	if after <= 0 {
		after = 5
	}
	if before > 15 {
		before = 15
	}
	if after > 15 {
		after = 15
	}
	var rows []MessageLog
	if input.AroundMessageID != 0 {
		var anchor MessageLog
		anchorQuery := m.db.WithContext(ctx).Where("conversation_kind=? AND target_id=? AND message_id=?", input.ConversationKind, input.TargetID, input.AroundMessageID).
			Where(NoticeExternalMessageIDPredicate("external_message_id"))
		if input.FilterCommand {
			anchorQuery = anchorQuery.Where("is_command=?", false)
		}
		if err := anchorQuery.Take(&anchor).Error; err != nil {
			return nil, err
		}
		if input.MaxMessageLogID > 0 && anchor.ID > input.MaxMessageLogID {
			return nil, fmt.Errorf("消息超出当前读取快照")
		}
		var beforeRows []MessageLog
		if before > 0 {
			qBefore := m.db.WithContext(ctx).Where("conversation_kind=? AND target_id=? AND id<?", input.ConversationKind, input.TargetID, anchor.ID).
				Where(NoticeExternalMessageIDPredicate("external_message_id"))
			if input.FilterCommand {
				qBefore = qBefore.Where("is_command=?", false)
			}
			if input.MaxMessageLogID > 0 {
				qBefore = qBefore.Where("id<=?", input.MaxMessageLogID)
			}
			if err := qBefore.Order("id DESC").Limit(before).Find(&beforeRows).Error; err != nil {
				return nil, err
			}
			slices.Reverse(beforeRows)
		}
		var afterRows []MessageLog
		if after > 0 {
			qAfter := m.db.WithContext(ctx).Where("conversation_kind=? AND target_id=? AND id>?", input.ConversationKind, input.TargetID, anchor.ID).
				Where(NoticeExternalMessageIDPredicate("external_message_id"))
			if input.FilterCommand {
				qAfter = qAfter.Where("is_command=?", false)
			}
			if input.MaxMessageLogID > 0 {
				qAfter = qAfter.Where("id<=?", input.MaxMessageLogID)
			}
			if err := qAfter.Order("id ASC").Limit(after).Find(&afterRows).Error; err != nil {
				return nil, err
			}
		}
		rows = append(rows, beforeRows...)
		rows = append(rows, anchor)
		rows = append(rows, afterRows...)
		return rows, nil
	}

	var topic TopicThreadRow
	if err := m.db.WithContext(ctx).Where("id=? AND conversation_kind=? AND target_id=?", input.TopicID, input.ConversationKind, input.TargetID).Take(&topic).Error; err != nil {
		return nil, fmt.Errorf("话题不属于指定会话")
	}
	qTopic := m.db.WithContext(ctx).Where("conversation_kind=? AND target_id=?", input.ConversationKind, input.TargetID).
		Where(NoticeExternalMessageIDPredicate("external_message_id"))
	qTopic = qTopic.Where(`id IN (
		SELECT message_log_id FROM topic_assignments WHERE topic_id=?
		UNION
		SELECT src.message_log_id FROM topic_summary_sources src
		JOIN topic_summaries ts ON ts.id=src.summary_id WHERE ts.topic_id=?
	)`, input.TopicID, input.TopicID)
	if input.MaxMessageLogID > 0 {
		qTopic = qTopic.Where("id<=?", input.MaxMessageLogID)
	}
	if input.FilterCommand {
		qTopic = qTopic.Where("is_command=?", false)
	}
	if err := qTopic.Order("id ASC").Limit(before + after + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
