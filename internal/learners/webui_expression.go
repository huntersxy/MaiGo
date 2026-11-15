package learners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/chat/session"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const globalExpressionChatName = "全局（所有聊天）"

// gormWebUIStore Dashboard 查询的 GORM/SQLite 实现
type gormWebUIStore struct {
	db *gorm.DB
}

// NewGORMWebUIStore 基于 GORM 构造 Dashboard 查询实现
func NewGORMWebUIStore(db *gorm.DB) WebUIStore {
	return &gormWebUIStore{db: db}
}

func (s *gormWebUIStore) ListExpressionRows(ctx context.Context, q ExpressionWebUIQuery) ([]ExpressionWebUIRow, int, error) {
	query := s.db.WithContext(ctx).Model(&Expression{})
	query = s.applyExpressionListFilters(ctx, query, q)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(q.Page, q.PageSize)
	sortColumn := "last_active_time"
	if strings.HasPrefix(q.SortBy, "created") {
		sortColumn = "create_time"
	}
	ascending := strings.HasSuffix(q.SortBy, "_asc")
	query = query.Order(sortColumn + " IS NULL")
	query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: sortColumn}, Desc: !ascending})
	query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: !ascending})
	var rows []Expression
	if err := query.Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return s.expressionRows(ctx, rows), int(total), nil
}

// applyExpressionListFilters 组装表达列表过滤条件，顺序对齐 get_expression_list
func (s *gormWebUIStore) applyExpressionListFilters(ctx context.Context, query *gorm.DB, q ExpressionWebUIQuery) *gorm.DB {
	if search := strings.TrimSpace(q.Search); search != "" {
		like := "%" + search + "%"
		query = query.Where("situation LIKE ? OR style LIKE ?", like, like)
	}
	chatID := strings.TrimSpace(q.ChatID)
	chatIDs := make([]string, 0, len(q.ChatIDs))
	for _, id := range q.ChatIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			chatIDs = append(chatIDs, trimmed)
		}
	}
	if chatID != "" || len(chatIDs) > 0 {
		if !q.IncludeLegacy {
			// 对齐 Python：默认列表只允许筛选当前账号可见的聊天流
			visible, err := s.visibleExpressionChatIDs(ctx)
			if err != nil {
				return query.Where("1 = 0")
			}
			if chatID != "" {
				if _, ok := visible[chatID]; !ok {
					return query.Where("1 = 0")
				}
			} else {
				filtered := make([]string, 0, len(chatIDs))
				for _, id := range chatIDs {
					if _, ok := visible[id]; ok {
						filtered = append(filtered, id)
					}
				}
				if len(filtered) == 0 {
					return query.Where("1 = 0")
				}
				chatIDs = filtered
			}
		}
		if chatID != "" {
			query = query.Where("session_id = ?", chatID)
		} else {
			query = query.Where("session_id IN ?", chatIDs)
		}
	} else {
		ids, err := scopeSessionIDs(ctx, s, q.IncludeLegacy)
		if err != nil {
			// 作用域查询失败时退化为只返回全局记录，避免越权展示
			ids = nil
		}
		query = applySessionScope(query, "session_id", ids)
	}
	switch q.ReviewFilter {
	case "user_checked":
		query = query.Where("checked = ? AND modified_by = ?", true, ModifiedByUser)
	case "unchecked":
		query = query.Where("checked = ?", false)
	}
	return query
}

// ExpressionChats 表达方式页面可见聊天流
func (s *gormWebUIStore) ExpressionChats(ctx context.Context, includeLegacy bool) ([]ChatInfo, error) {
	ids, err := scopeSessionIDs(ctx, s, includeLegacy)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []ChatInfo{}, nil
	}
	counts := map[string]int{}
	var countRows []struct {
		SessionID string
		Total     int
	}
	if err := s.db.WithContext(ctx).Model(&Expression{}).
		Select("session_id, COUNT(*) AS total").
		Where("session_id IN ?", ids).Group("session_id").Scan(&countRows).Error; err != nil {
		return nil, err
	}
	for _, row := range countRows {
		counts[row.SessionID] = row.Total
	}
	infos := s.chatInfos(ctx, ids, includeLegacy)
	latest := s.latestMessageTimes(ctx, ids)
	for i := range infos {
		count := counts[infos[i].SessionID]
		infos[i].ExpressionCount = &count
	}
	// 有消息的聊天按时间倒序，其余排最后；同一时间按名称稳定排序
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].ChatName != infos[j].ChatName {
			return infos[i].ChatName < infos[j].ChatName
		}
		return infos[i].SessionID < infos[j].SessionID
	})
	sort.SliceStable(infos, func(i, j int) bool {
		ti, okI := latest[infos[i].SessionID]
		tj, okJ := latest[infos[j].SessionID]
		if okI != okJ {
			return okI
		}
		return ti.After(tj)
	})
	return infos, nil
}

// ExpressionChatTargets 可作为导入目标的全部已知聊天流
func (s *gormWebUIStore) ExpressionChatTargets(ctx context.Context, includeLegacy bool) ([]ChatInfo, error) {
	var rows []chatsession.Record
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if !includeLegacy && !isCurrentAccountSession(&row) {
			continue
		}
		ids = append(ids, row.SessionID)
	}
	infos := s.chatInfos(ctx, ids, includeLegacy)
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].ChatName < infos[j].ChatName })
	return infos, nil
}

// chatInfos 批量构建聊天信息，保持入参顺序无关
func (s *gormWebUIStore) chatInfos(ctx context.Context, sessionIDs []string, includeLegacy bool) []ChatInfo {
	if len(sessionIDs) == 0 {
		return []ChatInfo{}
	}
	var rows []chatsession.Record
	if err := s.db.WithContext(ctx).Where("session_id IN ?", sessionIDs).Find(&rows).Error; err != nil {
		rows = nil
	}
	byID := make(map[string]chatsession.Record, len(rows))
	for _, row := range rows {
		byID[row.SessionID] = row
	}
	out := make([]ChatInfo, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		info := ChatInfo{SessionID: id, UseExpression: true, EnableLearning: true}
		rec, ok := byID[id]
		if ok {
			info.ChatName = displayNameForRecord(&rec)
			info.Platform = strings.TrimSpace(rec.Platform)
			if rec.AccountID != nil {
				info.AccountID = strings.TrimSpace(*rec.AccountID)
			}
			if rec.GroupID != nil {
				info.TargetID = strings.TrimSpace(*rec.GroupID)
			} else if rec.UserID != nil {
				info.TargetID = strings.TrimSpace(*rec.UserID)
			}
			info.IsGroup = rec.IsGroup()
			info.IsLegacy = info.Platform == "" || info.AccountID == ""
			info.IsOtherAccount = !info.IsLegacy && !isCurrentAccountSession(&rec)
		} else {
			info.ChatName = truncateSessionID(id)
			info.IsLegacy = true
		}
		if info.ChatName == "" {
			info.ChatName = truncateSessionID(id)
		}
		out = append(out, info)
	}
	return out
}

// isCurrentAccountSession 对齐 is_current_account_session：platform/account_id 均非空
// 本地平台（internal/webui）的 Bot 账号恒为本实例，对齐 Python get_bot_accounts 的本地平台分支；
// 早期内部聊天会话没有写 account_id，按本实例处理才能出现在导入目标里
func isCurrentAccountSession(rec *chatsession.Record) bool {
	if rec == nil {
		return false
	}
	platform := strings.ToLower(strings.TrimSpace(rec.Platform))
	if platform == "internal" || platform == "webui" {
		return true
	}
	accountID := ""
	if rec.AccountID != nil {
		accountID = strings.TrimSpace(*rec.AccountID)
	}
	return platform != "" && accountID != ""
}

// latestMessageTimes 每聊天流最新消息时间，用于聊天列表排序
func (s *gormWebUIStore) latestMessageTimes(ctx context.Context, sessionIDs []string) map[string]time.Time {
	out := make(map[string]time.Time, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return out
	}
	var rows []struct {
		SessionID string
		Latest    sql.NullString
	}
	if err := s.db.WithContext(ctx).Table("mai_messages").
		Select("session_id, MAX(timestamp) AS latest").
		Where("session_id IN ?", sessionIDs).Group("session_id").Scan(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		if latest, ok := parseSQLiteDateTime(row.Latest.String); ok {
			out[row.SessionID] = latest
		}
	}
	return out
}

// parseSQLiteDateTime 对齐 SQLAlchemy DateTime 结果处理：SQLite 文本转 time.Time
// 聚合列没有声明类型，驱动只返回文本，需按 DateTime 列的写入格式还原
func parseSQLiteDateTime(text string) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (s *gormWebUIStore) ExpressionStats(ctx context.Context, includeLegacy bool) (ExpressionStats, error) {
	ids, err := scopeSessionIDs(ctx, s, includeLegacy)
	if err != nil {
		return ExpressionStats{TopChats: map[string]int{}}, err
	}
	stats := ExpressionStats{TopChats: map[string]int{}}
	var total int64
	if err := applySessionScope(s.db.WithContext(ctx).Model(&Expression{}), "session_id", ids).
		Count(&total).Error; err != nil {
		return stats, err
	}
	stats.Total = int(total)

	since := time.Now().Add(-7 * 24 * time.Hour)
	var recent int64
	if err := applySessionScope(s.db.WithContext(ctx).Model(&Expression{}), "session_id", ids).
		Where("create_time IS NOT NULL AND create_time >= ?", since).Count(&recent).Error; err != nil {
		return stats, err
	}
	stats.Recent7Days = int(recent)

	var chatRows []struct {
		SessionID string
		Total     int
	}
	query := s.db.WithContext(ctx).Model(&Expression{}).
		Select("session_id, COUNT(*) AS total").
		Where("session_id IS NOT NULL").Group("session_id")
	if len(ids) > 0 {
		query = query.Where("session_id IN ?", ids)
	} else {
		query = query.Where("1 = 0")
	}
	if err := query.Scan(&chatRows).Error; err != nil {
		return stats, err
	}
	for _, row := range chatRows {
		if strings.TrimSpace(row.SessionID) == "" {
			continue
		}
		stats.TopChats[row.SessionID] = row.Total
	}
	stats.ChatCount = len(stats.TopChats)
	// 只保留前 10，排序口径与 Python sorted(..., reverse=True)[:10]
	stats.TopChats = topN(stats.TopChats, 10)
	return stats, nil
}

func topN(counter map[string]int, limit int) map[string]int {
	type kv struct {
		key   string
		count int
	}
	items := make([]kv, 0, len(counter))
	for key, count := range counter {
		items = append(items, kv{key, count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].key < items[j].key
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make(map[string]int, len(items))
	for _, item := range items {
		out[item.key] = item.count
	}
	return out
}

func (s *gormWebUIStore) ExpressionReviewStats(ctx context.Context) (ReviewStats, error) {
	base := func() *gorm.DB { return s.db.WithContext(ctx).Model(&Expression{}) }
	var stats ReviewStats
	count := func(q *gorm.DB) (int, error) {
		var total int64
		if err := q.Count(&total).Error; err != nil {
			return 0, err
		}
		return int(total), nil
	}
	var err error
	if stats.Total, err = count(base()); err != nil {
		return stats, err
	}
	if stats.Unchecked, err = count(base().Where("checked = ?", false)); err != nil {
		return stats, err
	}
	if stats.Passed, err = count(base().Where("checked = ?", true)); err != nil {
		return stats, err
	}
	if stats.AIChecked, err = count(base().Where("checked = ? AND modified_by = ?", true, ModifiedByAI)); err != nil {
		return stats, err
	}
	if stats.UserChecked, err = count(base().Where("checked = ? AND modified_by = ?", true, ModifiedByUser)); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *gormWebUIStore) ExpressionReviewRows(ctx context.Context, q ReviewListQuery) ([]ExpressionWebUIRow, int, error) {
	applyFilter := func(query *gorm.DB) *gorm.DB {
		switch q.FilterType {
		case "unchecked":
			query = query.Where("checked = ?", false)
		case "passed":
			query = query.Where("checked = ?", true)
		case "all":
		default:
			query = query.Where("1 = 0")
		}
		if search := strings.TrimSpace(q.Search); search != "" {
			like := "%" + search + "%"
			query = query.Where("situation LIKE ? OR style LIKE ?", like, like)
		}
		if chatID := strings.TrimSpace(q.ChatID); chatID != "" {
			query = query.Where("session_id = ?", chatID)
		}
		if len(q.ExcludeIDs) > 0 {
			query = query.Where("id NOT IN ?", q.ExcludeIDs)
		}
		return query
	}
	var total int64
	if err := applyFilter(s.db.WithContext(ctx).Model(&Expression{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(q.Page, q.PageSize)
	offset := (page - 1) * size
	if q.Order == "random" {
		if int(total) > size {
			offset = rand.Intn(int(total) - size + 1)
		} else {
			offset = 0
		}
	}
	query := applyFilter(s.db.WithContext(ctx).Model(&Expression{}))
	if q.Order == "random" {
		query = query.Order("id ASC")
	} else {
		query = query.Order("create_time DESC NULLS LAST").Order("id DESC")
	}
	var rows []Expression
	if err := query.Offset(offset).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return s.expressionRows(ctx, rows), int(total), nil
}

func (s *gormWebUIStore) GetExpressionRow(ctx context.Context, id uint) (*ExpressionWebUIRow, error) {
	var row Expression
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows := s.expressionRows(ctx, []Expression{row})
	return &rows[0], nil
}

func (s *gormWebUIStore) CreateExpressionRow(ctx context.Context, in ExpressionCreateInput) (*ExpressionWebUIRow, error) {
	now := time.Now()
	contentList := strings.TrimSpace(in.ContentList)
	if contentList == "" {
		contentList = "[]"
	}
	row := Expression{
		Situation:      strings.TrimSpace(in.Situation),
		Style:          strings.TrimSpace(in.Style),
		ContentList:    contentList,
		Count:          in.Count,
		LastActiveTime: now,
		CreateTime:     now,
		SessionID:      in.SessionID,
		Checked:        in.Checked || in.IsGlobal,
		ModifiedBy:     in.ModifiedBy,
	}
	if row.ModifiedBy == nil && in.IsGlobal {
		modifiedBy := ModifiedByUser
		row.ModifiedBy = &modifiedBy
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	rows := s.expressionRows(ctx, []Expression{row})
	return &rows[0], nil
}

func (s *gormWebUIStore) UpdateExpressionRow(ctx context.Context, id uint, in ExpressionUpdateInput) (*ExpressionWebUIRow, error) {
	var row Expression
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if in.Situation != nil {
		row.Situation = *in.Situation
	}
	if in.Style != nil {
		row.Style = *in.Style
	}
	if in.HasChatID && in.ChatID != nil {
		row.SessionID = nullableSessionID(*in.ChatID)
	}
	row.LastActiveTime = time.Now()
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return nil, err
	}
	rows := s.expressionRows(ctx, []Expression{row})
	return &rows[0], nil
}

func (s *gormWebUIStore) SetExpressionReviewStatus(ctx context.Context, id uint, approved bool) (*ExpressionWebUIRow, error) {
	var row Expression
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	row.Checked = approved
	row.LastActiveTime = time.Now()
	if approved {
		modifiedBy := ModifiedByUser
		row.ModifiedBy = &modifiedBy
	} else {
		row.ModifiedBy = nil
	}
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return nil, err
	}
	rows := s.expressionRows(ctx, []Expression{row})
	return &rows[0], nil
}

func (s *gormWebUIStore) DeleteExpressionRows(ctx context.Context, ids []uint) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&Expression{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

func (s *gormWebUIStore) ClearExpressionsBySession(ctx context.Context, sessionID string) (int, error) {
	res := s.db.WithContext(ctx).Where("session_id = ?", strings.TrimSpace(sessionID)).Delete(&Expression{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

func (s *gormWebUIStore) ImportExpressions(ctx context.Context, sessionID *string, items []ExpressionImportItem) (ImportResult, error) {
	var result ImportResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []struct {
			Situation string
			Style     string
		}
		scope := tx.Model(&Expression{}).Select("situation, style")
		if sessionID == nil {
			scope = scope.Where("session_id IS NULL")
		} else {
			scope = scope.Where("session_id = ?", *sessionID)
		}
		if err := scope.Find(&existing).Error; err != nil {
			return err
		}
		pairs := make(map[[2]string]struct{}, len(existing))
		for _, row := range existing {
			pairs[[2]string{row.Situation, row.Style}] = struct{}{}
		}
		now := time.Now()
		for _, item := range items {
			situation := strings.TrimSpace(item.Situation)
			style := strings.TrimSpace(item.Style)
			if situation == "" || style == "" {
				result.Failed++
				continue
			}
			key := [2]string{situation, style}
			if _, ok := pairs[key]; ok {
				result.Skipped++
				continue
			}
			contentList := strings.TrimSpace(item.ContentList)
			if contentList == "" {
				contentList = "[]"
			}
			lastActive := now
			if item.LastActiveTime != nil {
				lastActive = *item.LastActiveTime
			}
			createTime := now
			if item.CreateTime != nil {
				createTime = *item.CreateTime
			}
			row := Expression{
				Situation:      situation,
				Style:          style,
				ContentList:    contentList,
				Count:          item.Count,
				LastActiveTime: lastActive,
				CreateTime:     createTime,
				SessionID:      sessionID,
				Checked:        item.Checked,
				ModifiedBy:     item.ModifiedBy,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			pairs[key] = struct{}{}
			result.Imported++
		}
		return nil
	})
	return result, err
}

func (s *gormWebUIStore) ExportExpressions(ctx context.Context, q ExpressionExportQuery) ([]ExpressionExportRow, error) {
	query := s.expressionExportBase(ctx, q)
	var rows []Expression
	if err := query.Order("last_active_time DESC NULLS LAST").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ExpressionExportRow, 0, len(rows))
	for _, row := range rows {
		item := ExpressionExportRow{
			Situation:   row.Situation,
			Style:       row.Style,
			ContentList: row.ContentList,
			Count:       row.Count,
			Checked:     row.Checked,
			ModifiedBy:  row.ModifiedBy,
		}
		lastActive := row.LastActiveTime
		item.LastActiveTime = &lastActive
		createTime := row.CreateTime
		item.CreateTime = &createTime
		out = append(out, item)
	}
	return out, nil
}

func (s *gormWebUIStore) expressionExportBase(ctx context.Context, q ExpressionExportQuery) *gorm.DB {
	query := s.db.WithContext(ctx).Model(&Expression{})
	if q.HasChatID {
		query = query.Where("session_id = ?", q.ChatID)
	}
	if q.HasChatIDs {
		query = query.Where("session_id IN ?", q.ChatIDs)
	}
	if q.HasIDs {
		query = query.Where("id IN ?", q.IDs)
	}
	return query
}

func (s *gormWebUIStore) MissingExpressionIDs(ctx context.Context, q ExpressionExportQuery) ([]uint, error) {
	if !q.HasIDs {
		return nil, nil
	}
	var foundIDs []uint
	if err := s.expressionExportBase(ctx, q).Pluck("id", &foundIDs).Error; err != nil {
		return nil, err
	}
	found := make(map[uint]struct{}, len(foundIDs))
	for _, id := range foundIDs {
		found[id] = struct{}{}
	}
	unique := uniqueIDs(q.IDs)
	missing := MissingIDs(unique, found)
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return missing, nil
}

func uniqueIDs(ids []uint) []uint {
	out := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *gormWebUIStore) BatchReviewExpressions(ctx context.Context, items []ReviewBatchItem) []ReviewBatchResult {
	results := make([]ReviewBatchResult, 0, len(items))
	for _, item := range items {
		var row Expression
		err := s.db.WithContext(ctx).First(&row, item.ID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			results = append(results, ReviewBatchResult{ID: item.ID, Success: false, Message: fmt.Sprintf("未找到 ID 为 %d 的表达方式", item.ID)})
			continue
		}
		if err != nil {
			results = append(results, ReviewBatchResult{ID: item.ID, Success: false, Message: err.Error()})
			continue
		}
		if item.Approved {
			modifiedBy := ModifiedByUser
			row.Checked = true
			row.ModifiedBy = &modifiedBy
			row.LastActiveTime = time.Now()
			err = s.db.WithContext(ctx).Save(&row).Error
		} else {
			err = s.db.WithContext(ctx).Delete(&row).Error
		}
		if err != nil {
			results = append(results, ReviewBatchResult{ID: item.ID, Success: false, Message: err.Error()})
			continue
		}
		message := "拒绝并删除"
		if item.Approved {
			message = "通过"
		}
		results = append(results, ReviewBatchResult{ID: item.ID, Success: true, Message: message})
	}
	return results
}

// expressionRows 把模型行转换为 WebUI 行，并批量解析聊天名
func (s *gormWebUIStore) expressionRows(ctx context.Context, rows []Expression) []ExpressionWebUIRow {
	out := make([]ExpressionWebUIRow, 0, len(rows))
	sessionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.SessionID != nil && strings.TrimSpace(*row.SessionID) != "" {
			sessionIDs = append(sessionIDs, strings.TrimSpace(*row.SessionID))
		}
	}
	names := s.ChatNames(ctx, sessionIDs)
	for _, row := range rows {
		item := ExpressionWebUIRow{
			ID:             row.ID,
			Situation:      row.Situation,
			Style:          row.Style,
			ContentList:    row.ContentList,
			Count:          row.Count,
			LastActiveTime: row.LastActiveTime,
			CreateTime:     row.CreateTime,
			Checked:        row.Checked,
			ModifiedBy:     row.ModifiedBy,
		}
		if row.SessionID != nil {
			item.SessionID = strings.TrimSpace(*row.SessionID)
		}
		if item.SessionID == "" {
			item.ChatName = globalExpressionChatName
		} else if name := names[item.SessionID]; name != "" {
			item.ChatName = name
		} else {
			item.ChatName = truncateSessionID(item.SessionID)
		}
		out = append(out, item)
	}
	return out
}
