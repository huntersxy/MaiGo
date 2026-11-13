package memory

// 表情包分页查询（WebUI 与工具共用），查询对象为 images 表 image_type='EMOJI'

import (
	"context"
	"strings"
)

// EmojiPage 表情包分页结果
type EmojiPage struct {
	Items      []EmojiRecord `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
	HasMore    bool          `json:"has_more"`
}

// EmojiFilter 表情包筛选条件
type EmojiFilter struct {
	Keyword string `json:"keyword"`
}

func emojiCursorCondition(cursor PageCursor) (string, []any) {
	queryCount := int64(cursor.LastKey)
	return "(query_count < ? OR (query_count = ? AND (last_used_time < ? OR (last_used_time = ? AND id < ?))))",
		[]any{queryCount, queryCount, cursor.LastTime, cursor.LastTime, cursor.LastID}
}

// SearchEmojiPage 关键词分页查询表情包
//
// 排序键为 query_count / last_used_time / id，与 WebUI 的默认排序口径一致。
// last_used_time 可空，查询时以 register_time 兜底参与比较，避免 NULL 掉序。
func (m *Manager) SearchEmojiPage(ctx context.Context, keyword, cursorRaw string, limit int) (EmojiPage, error) {
	limit = NormalizeLimit(limit)
	filter := EmojiFilter{Keyword: strings.TrimSpace(keyword)}
	cursor, err := DecodeCursor(cursorRaw)
	if err != nil {
		return EmojiPage{}, err
	}
	if cursor.FilterHash == "" {
		cursor.FilterHash = CursorFilterHash(filter)
	}
	if err := ValidateCursorFilter(cursor, filter); err != nil {
		return EmojiPage{}, err
	}
	snapshot := CursorTimeOrNow(cursor)
	var rows []EmojiRecord
	q := emojiScope(m.db.WithContext(ctx).Model(&EmojiRecord{}))
	if keyword != "" {
		keywords := strings.Fields(keyword)
		likeConditions := make([]string, 0, len(keywords))
		args := make([]any, 0, len(keywords))
		for _, kw := range keywords {
			likeConditions = append(likeConditions, "description LIKE ?")
			args = append(args, "%"+kw+"%")
		}
		q = q.Where(strings.Join(likeConditions, " OR "), args...)
	}
	if cursor.LastID != 0 {
		condition, args := emojiCursorCondition(cursor)
		q = q.Where(condition, args...)
	}
	if err := q.Order("query_count DESC,last_used_time DESC,id DESC").
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return EmojiPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := EmojiPage{Items: rows, HasMore: hasMore}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		lastTime := snapshot
		if last.LastUsedTime != nil {
			lastTime = *last.LastUsedTime
		}
		page.NextCursor = NewPageCursor("emoji", float64(last.QueryCount), lastTime, last.ID, snapshot, filter)
	}
	return page, nil
}

// ListEmojiHashes 查询哪些表情哈希已经入库
func (m *Manager) ListEmojiHashes(ctx context.Context, hashes []string) (map[string]bool, error) {
	collected := map[string]bool{}
	if len(hashes) == 0 {
		return collected, nil
	}
	var rows []EmojiRecord
	if err := emojiScope(m.db.WithContext(ctx).Model(&EmojiRecord{})).
		Select("image_hash").Where("image_hash IN ?", hashes).Find(&rows).Error; err != nil {
		return collected, err
	}
	for _, row := range rows {
		collected[row.ImageHash] = true
	}
	return collected, nil
}
