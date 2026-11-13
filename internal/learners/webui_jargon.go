package learners

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/chat/session"

	"gorm.io/gorm"
)

var (
	// ErrJargonConflict 目标范围已存在相同内容的手动黑话
	ErrJargonConflict = errors.New("learners: 该范围中已存在相同内容的手动黑话")
	// ErrJargonContentEmpty 黑话内容为空
	ErrJargonContentEmpty = errors.New("learners: 黑话内容不能为空")
)

// parseSessionCountsOrdered 按 JSON 保存顺序解析会话计数字典
//
// Go map 会丢失键顺序，而 WebUI 用第一个键作为主聊天流，必须保留原顺序
func parseSessionCountsOrdered(raw string) ([]string, map[string]int, error) {
	order := make([]string, 0)
	counts := map[string]int{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return order, counts, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	token, err := decoder.Token()
	if err != nil {
		return order, counts, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return order, counts, fmt.Errorf("session_id_dict 不是 JSON 对象")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return order, counts, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return order, counts, fmt.Errorf("session_id_dict 键类型异常")
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return order, counts, err
		}
		if _, exists := counts[key]; !exists {
			order = append(order, key)
		}
		counts[key] = toInt(value)
	}
	if _, err := decoder.Token(); err != nil {
		return order, counts, err
	}
	return order, counts, nil
}

func toInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(n)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func normalizeJargonCreatedBy(createdBy JargonCreatedBy) JargonCreatedBy {
	switch createdBy {
	case JargonCreatedByManual, "manual", "Manual":
		return JargonCreatedByManual
	case JargonCreatedByAI, "ai", "":
		return JargonCreatedByAI
	default:
		return JargonCreatedByAI
	}
}

func (s *gormWebUIStore) jargonRow(ctx context.Context, row Jargon) JargonWebUIRow {
	order, _, _ := parseSessionCountsOrdered(row.SessionIDDict)
	names := s.ChatNames(ctx, order)
	chatNames := make([]string, 0, len(order))
	for _, id := range order {
		if name := names[id]; name != "" {
			chatNames = append(chatNames, name)
		} else {
			chatNames = append(chatNames, truncateSessionID(id))
		}
	}
	item := JargonWebUIRow{
		ID:               row.ID,
		Content:          row.Content,
		Meaning:          row.Meaning,
		SessionIDs:       order,
		ChatNames:        chatNames,
		Count:            row.Count,
		IsComplete:       row.IsComplete,
		IsGlobal:         row.IsGlobal,
		CreatedBy:        normalizeJargonCreatedBy(row.CreatedBy),
		CreatedTimestamp: row.CreatedTimestamp,
		UpdatedTimestamp: row.UpdatedTimestamp,
	}
	if len(chatNames) > 0 {
		item.ChatName = chatNames[0]
	}
	isJargon := row.IsJargon != nil && *row.IsJargon
	item.IsLegacyEmptyMeaning = isJargon && strings.TrimSpace(row.Meaning) == ""
	item.IsJargon = isJargon && !item.IsLegacyEmptyMeaning
	return item
}

// jargonListScope 组装黑话列表过滤条件
func applyJargonListFilters(query *gorm.DB, q JargonWebUIQuery) *gorm.DB {
	if search := strings.TrimSpace(q.Search); search != "" {
		query = query.Where("content LIKE ?", "%"+search+"%")
	}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		candidates := parseSessionIDCandidates(sessionID)
		if len(candidates) > 0 {
			conditions := make([]string, 0, len(candidates))
			args := make([]any, 0, len(candidates))
			for _, candidate := range candidates {
				conditions = append(conditions, "session_id_dict LIKE ?")
				args = append(args, "%"+jsonStringLiteral(candidate)+"%")
			}
			query = query.Where(strings.Join(conditions, " OR "), args...)
		}
	}
	switch q.JargonStatus {
	case "confirmed_jargon":
		query = query.Where(effectiveJargonCondition())
	case "confirmed_not_jargon":
		query = query.Where(noJargonCondition())
	case "manual_jargon":
		query = query.Where("created_by = ?", JargonCreatedByManual)
	case "pending":
		query = query.Where(noJargonCondition())
	case "":
		if q.IsJargon != nil {
			if *q.IsJargon {
				query = query.Where(effectiveJargonCondition())
			} else {
				query = query.Where(noJargonCondition())
			}
		}
	}
	if q.IsComplete != nil {
		query = query.Where("is_complete = ?", *q.IsComplete)
	}
	if q.IsGlobal != nil {
		query = query.Where("is_global = ?", *q.IsGlobal)
	}
	return query
}

// effectiveJargonCondition 有效黑话：is_jargon=true 且含义非空
func effectiveJargonCondition() string {
	return "(is_jargon = true AND length(trim(coalesce(meaning, ''))) > 0)"
}

// noJargonCondition 非黑话：未标记或缺少含义（含历史空含义记录）
func noJargonCondition() string {
	return "(is_jargon IS NOT TRUE OR is_jargon = false OR NOT " + effectiveJargonCondition() + ")"
}

// parseSessionIDCandidates 对齐 parse_session_id_candidates：JSON 数组取首列，否则原值
func parseSessionIDCandidates(sessionID string) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(sessionID), &parsed); err != nil {
		return []string{sessionID}
	}
	items, ok := parsed.([]any)
	if !ok {
		return []string{sessionID}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if nested, ok := item.([]any); ok && len(nested) > 0 {
			out = append(out, strings.TrimSpace(fmt.Sprint(nested[0])))
		}
	}
	if len(out) == 0 {
		return []string{sessionID}
	}
	return out
}

// jsonStringLiteral 生成 JSON 键字面量（不带引号差异），用于 session_id_dict LIKE 匹配
func jsonStringLiteral(sessionID string) string {
	raw, err := json.Marshal(sessionID)
	if err != nil {
		return sessionID
	}
	return string(raw)
}

// applyJargonScopeFilter 共享组范围：session_id_dict 含任一聊天流
func applyJargonScopeFilter(query *gorm.DB, sessionIDs []string) (*gorm.DB, bool) {
	conditions := make([]string, 0, len(sessionIDs))
	args := make([]any, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, raw := range sessionIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		token := jsonStringLiteral(id)
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		conditions = append(conditions, "session_id_dict LIKE ?")
		args = append(args, "%"+token+"%")
	}
	if len(conditions) == 0 {
		return query, false
	}
	return query.Where(strings.Join(conditions, " OR "), args...), true
}

func (s *gormWebUIStore) ListJargonRows(ctx context.Context, q JargonWebUIQuery) ([]JargonWebUIRow, int, error) {
	query := applyJargonListFilters(s.db.WithContext(ctx).Model(&Jargon{}), q)
	if len(q.SessionIDs) > 0 {
		scoped, ok := applyJargonScopeFilter(query, q.SessionIDs)
		if !ok {
			return []JargonWebUIRow{}, 0, nil
		}
		query = scoped
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(q.Page, q.PageSize)
	sortColumn := "updated_timestamp"
	if strings.HasPrefix(q.SortBy, "created") {
		sortColumn = "created_timestamp"
	}
	ascending := strings.HasSuffix(q.SortBy, "_asc")
	direction := "DESC"
	if ascending {
		direction = "ASC"
	}
	query = query.Order(sortColumn + " IS NULL").Order(sortColumn + " " + direction).Order("id " + direction)
	var rows []Jargon
	if err := query.Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]JargonWebUIRow, 0, len(rows))
	sessionIDs := make([]string, 0, len(rows))
	parsed := make([][]string, len(rows))
	for i, row := range rows {
		order, _, _ := parseSessionCountsOrdered(row.SessionIDDict)
		parsed[i] = order
		sessionIDs = append(sessionIDs, order...)
	}
	names := s.ChatNames(ctx, sessionIDs)
	for i, row := range rows {
		order := parsed[i]
		item := JargonWebUIRow{
			ID:               row.ID,
			Content:          row.Content,
			Meaning:          row.Meaning,
			SessionIDs:       order,
			Count:            row.Count,
			IsComplete:       row.IsComplete,
			IsGlobal:         row.IsGlobal,
			CreatedBy:        normalizeJargonCreatedBy(row.CreatedBy),
			CreatedTimestamp: row.CreatedTimestamp,
			UpdatedTimestamp: row.UpdatedTimestamp,
		}
		chatNames := make([]string, 0, len(order))
		for _, id := range order {
			if name := names[id]; name != "" {
				chatNames = append(chatNames, name)
			} else {
				chatNames = append(chatNames, truncateSessionID(id))
			}
		}
		item.ChatNames = chatNames
		if len(chatNames) > 0 {
			item.ChatName = chatNames[0]
		}
		isJargon := row.IsJargon != nil && *row.IsJargon
		item.IsLegacyEmptyMeaning = isJargon && strings.TrimSpace(row.Meaning) == ""
		item.IsJargon = isJargon && !item.IsLegacyEmptyMeaning
		out = append(out, item)
	}
	return out, int(total), nil
}

func (s *gormWebUIStore) GetJargonRow(ctx context.Context, id uint) (*JargonWebUIRow, error) {
	var row Jargon
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	item := s.jargonRow(ctx, row)
	return &item, nil
}

func (s *gormWebUIStore) JargonStats(ctx context.Context) (JargonStats, error) {
	stats := JargonStats{TopChats: map[string]int{}}
	db := s.db.WithContext(ctx)
	var total int64
	if err := db.Model(&Jargon{}).Count(&total).Error; err != nil {
		return stats, err
	}
	stats.Total = int(total)
	countWhere := func(where string, args ...any) (int, error) {
		var n int64
		q := db.Model(&Jargon{})
		if where != "" {
			q = q.Where(where, args...)
		}
		if err := q.Count(&n).Error; err != nil {
			return 0, err
		}
		return int(n), nil
	}
	var err error
	if stats.ConfirmedJargon, err = countWhere(effectiveJargonCondition()); err != nil {
		return stats, err
	}
	if stats.ConfirmedNotJargon, err = countWhere(noJargonCondition()); err != nil {
		return stats, err
	}
	if stats.Pending, err = countWhere("is_jargon IS NULL"); err != nil {
		return stats, err
	}
	if stats.ManualJargon, err = countWhere("created_by = ?", JargonCreatedByManual); err != nil {
		return stats, err
	}
	if stats.GlobalCount, err = countWhere("is_global = ?", true); err != nil {
		return stats, err
	}
	if stats.CompleteCount, err = countWhere("is_complete = ?", true); err != nil {
		return stats, err
	}
	var dicts []string
	if err := db.Model(&Jargon{}).Pluck("session_id_dict", &dicts).Error; err != nil {
		return stats, err
	}
	counter := map[string]int{}
	for _, dict := range dicts {
		order, _, err := parseSessionCountsOrdered(dict)
		if err != nil {
			continue
		}
		for _, sessionID := range order {
			counter[sessionID]++
		}
	}
	stats.ChatCount = len(counter)
	stats.TopChats = topN(counter, 5)
	return stats, nil
}

func (s *gormWebUIStore) JargonChats(ctx context.Context, includeEmpty bool) ([]JargonChat, error) {
	var rows []Jargon
	if err := s.db.WithContext(ctx).Model(&Jargon{}).Select("session_id_dict, is_global").Find(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[string]int{}
	seen := map[string]struct{}{}
	for _, row := range rows {
		order, _, err := parseSessionCountsOrdered(row.SessionIDDict)
		if err != nil {
			continue
		}
		for _, sessionID := range order {
			counts[sessionID]++
			if includeEmpty || !row.IsGlobal {
				seen[sessionID] = struct{}{}
			}
		}
	}
	ids := sortedKeys(seen)
	var records []chatsession.Record
	if includeEmpty {
		if err := s.db.WithContext(ctx).Find(&records).Error; err != nil {
			return nil, err
		}
	} else if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Where("session_id IN ?", ids).Find(&records).Error; err != nil {
			return nil, err
		}
	}
	byID := make(map[string]JargonChat, len(records)+len(ids))
	for _, rec := range records {
		chat := JargonChat{
			SessionID:   rec.SessionID,
			ChatName:    displayNameForRecord(&rec),
			Platform:    strings.TrimSpace(rec.Platform),
			JargonCount: counts[rec.SessionID],
			IsGroup:     rec.IsGroup(),
		}
		if rec.AccountID != nil {
			chat.AccountID = strings.TrimSpace(*rec.AccountID)
		}
		if rec.GroupID != nil {
			chat.TargetID = strings.TrimSpace(*rec.GroupID)
		} else if rec.UserID != nil {
			chat.TargetID = strings.TrimSpace(*rec.UserID)
		}
		if chat.ChatName == "" {
			chat.ChatName = truncateSessionID(rec.SessionID)
		}
		byID[chat.SessionID] = chat
	}
	// 兼容旧数据：黑话里保留但 chat_sessions 已不存在的聊天流
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			continue
		}
		byID[id] = JargonChat{SessionID: id, ChatName: truncateSessionID(id), JargonCount: counts[id]}
	}
	result := make([]JargonChat, 0, len(byID))
	for _, chat := range byID {
		result = append(result, chat)
	}
	latestIDs := make([]string, 0, len(byID))
	for id := range byID {
		latestIDs = append(latestIDs, id)
	}
	latest := s.latestMessageTimes(ctx, latestIDs)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ChatName != result[j].ChatName {
			return result[i].ChatName < result[j].ChatName
		}
		return result[i].SessionID < result[j].SessionID
	})
	sort.SliceStable(result, func(i, j int) bool {
		ti, okI := latest[result[i].SessionID]
		tj, okJ := latest[result[j].SessionID]
		if okI != okJ {
			return okI
		}
		return ti.After(tj)
	})
	return result, nil
}

func (s *gormWebUIStore) CreateJargonRow(ctx context.Context, in JargonCreateInput) (*JargonWebUIRow, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, ErrJargonContentEmpty
	}
	meaning := strings.TrimSpace(in.Meaning)
	target := make(map[string]struct{}, len(in.SessionIDs))
	for _, id := range in.SessionIDs {
		target[id] = struct{}{}
	}
	var created Jargon
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var same []Jargon
		if err := tx.Where("content = ?", content).Find(&same).Error; err != nil {
			return err
		}
		for _, row := range same {
			if normalizeJargonCreatedBy(row.CreatedBy) != JargonCreatedByManual {
				continue
			}
			if jargonScopesOverlap(row, target, in.IsGlobal) {
				return ErrJargonConflict
			}
		}
		for i := range same {
			row := same[i]
			if normalizeJargonCreatedBy(row.CreatedBy) != JargonCreatedByAI {
				continue
			}
			if !jargonScopesOverlap(row, target, in.IsGlobal) {
				continue
			}
			if err := tx.Delete(&Jargon{}, row.ID).Error; err != nil {
				return err
			}
		}
		now := time.Now()
		dict := map[string]int{}
		for _, id := range in.SessionIDs {
			dict[id] = 1
		}
		raw, err := encodeSessionCounts(dict)
		if err != nil {
			return err
		}
		isJargon := meaning != ""
		created = Jargon{
			Content:          content,
			Meaning:          meaning,
			SessionIDDict:    raw,
			Count:            0,
			IsJargon:         &isJargon,
			IsComplete:       false,
			IsGlobal:         in.IsGlobal,
			CreatedBy:        JargonCreatedByManual,
			CreatedTimestamp: now,
			UpdatedTimestamp: now,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, err
	}
	item := s.jargonRow(ctx, created)
	return &item, nil
}

func jargonScopesOverlap(row Jargon, targetSessionIDs map[string]struct{}, targetIsGlobal bool) bool {
	if targetIsGlobal || row.IsGlobal {
		return true
	}
	order, _, err := parseSessionCountsOrdered(row.SessionIDDict)
	if err != nil {
		return false
	}
	for _, id := range order {
		if _, ok := targetSessionIDs[id]; ok {
			return true
		}
	}
	return false
}

func (s *gormWebUIStore) UpdateJargonRow(ctx context.Context, id uint, in JargonUpdateInput) (*JargonWebUIRow, error) {
	var row Jargon
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	changed := false
	setSessionCount := func(ids []string) error {
		countValue := row.Count
		if countValue < 1 {
			countValue = 1
		}
		dict := make(map[string]int, len(ids))
		for _, id := range ids {
			dict[id] = countValue
		}
		raw, err := encodeSessionCounts(dict)
		if err != nil {
			return err
		}
		row.SessionIDDict = raw
		return nil
	}
	switch {
	case in.HasSessionIDs:
		if err := setSessionCount(in.SessionIDs); err != nil {
			return nil, err
		}
		changed = true
	case in.HasSessionID && in.SessionID != nil:
		if err := setSessionCount([]string{*in.SessionID}); err != nil {
			return nil, err
		}
		changed = true
	}
	if in.Content != nil {
		content := strings.TrimSpace(*in.Content)
		if content == "" {
			return nil, ErrJargonContentEmpty
		}
		row.Content = content
		changed = true
	}
	if in.Meaning != nil || in.HasMeaning {
		if in.Meaning == nil {
			row.Meaning = ""
		} else {
			row.Meaning = strings.TrimSpace(*in.Meaning)
		}
		changed = true
	}
	if in.IsGlobal != nil {
		row.IsGlobal = *in.IsGlobal
		changed = true
	}
	if in.IsJargon != nil || in.HasIsJargon {
		if in.IsJargon == nil {
			row.IsJargon = nil
		} else {
			isJargon := *in.IsJargon
			row.IsJargon = &isJargon
		}
		changed = true
	}
	if in.CreatedBy != nil {
		row.CreatedBy = *in.CreatedBy
		changed = true
	}
	if changed {
		row.UpdatedTimestamp = time.Now()
		if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
			return nil, err
		}
	}
	item := s.jargonRow(ctx, row)
	return &item, nil
}

func (s *gormWebUIStore) DeleteJargonRows(ctx context.Context, ids []uint) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&Jargon{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

func (s *gormWebUIStore) SetJargonStatus(ctx context.Context, ids []uint, isJargon bool) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Model(&Jargon{}).Where("id IN ?", ids).
		Updates(map[string]any{"is_jargon": isJargon, "updated_timestamp": time.Now()})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

func (s *gormWebUIStore) ImportJargons(ctx context.Context, in JargonImportInput) (ImportResult, error) {
	var result ImportResult
	target := make(map[string]struct{}, len(in.TargetSessionIDs))
	for _, id := range in.TargetSessionIDs {
		target[id] = struct{}{}
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range in.Items {
			content := strings.TrimSpace(item.Content)
			if content == "" {
				result.Failed++
				continue
			}
			meaning := strings.TrimSpace(item.Meaning)
			var same []Jargon
			if err := tx.Where("content = ?", content).Find(&same).Error; err != nil {
				return err
			}
			overlapping := make([]Jargon, 0, len(same))
			for _, row := range same {
				if in.IsGlobal {
					if row.IsGlobal {
						overlapping = append(overlapping, row)
					}
					continue
				}
				if jargonScopesOverlap(row, target, false) {
					overlapping = append(overlapping, row)
				}
			}
			if len(overlapping) > 0 && in.ConflictStrategy != "overwrite" {
				result.Skipped++
				continue
			}
			for _, row := range overlapping {
				if err := tx.Delete(&Jargon{}, row.ID).Error; err != nil {
					return err
				}
			}
			dict := make(map[string]int, len(in.TargetSessionIDs))
			perSession := item.Count
			if perSession < 1 {
				perSession = 1
			}
			for _, id := range in.TargetSessionIDs {
				dict[id] = perSession
			}
			raw, err := encodeSessionCounts(dict)
			if err != nil {
				return err
			}
			count := item.Count
			if count < 0 {
				count = 0
			}
			now := time.Now()
			isJargon := item.IsJargon && meaning != ""
			row := Jargon{
				Content:          content,
				Meaning:          meaning,
				SessionIDDict:    raw,
				Count:            count,
				IsJargon:         &isJargon,
				IsComplete:       item.IsComplete,
				IsGlobal:         in.IsGlobal,
				CreatedBy:        JargonCreatedByManual,
				CreatedTimestamp: now,
				UpdatedTimestamp: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			result.Imported++
		}
		return nil
	})
	return result, err
}

func (s *gormWebUIStore) ExportJargons(ctx context.Context, q JargonExportQuery) ([]JargonExportRow, error) {
	query, ok := s.jargonExportBase(ctx, q)
	if !ok {
		return []JargonExportRow{}, nil
	}
	var rows []Jargon
	if err := query.Order("count DESC").Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	chatSessions := map[string]chatsession.Record{}
	if q.IncludeChatInfo {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			order, _, err := parseSessionCountsOrdered(row.SessionIDDict)
			if err != nil {
				continue
			}
			ids = append(ids, order...)
		}
		if len(ids) > 0 {
			var records []chatsession.Record
			if err := s.db.WithContext(ctx).Where("session_id IN ?", ids).Find(&records).Error; err != nil {
				return nil, err
			}
			for _, rec := range records {
				chatSessions[rec.SessionID] = rec
			}
		}
	}
	out := make([]JargonExportRow, 0, len(rows))
	for _, row := range rows {
		item := JargonExportRow{
			Content:    row.Content,
			Meaning:    row.Meaning,
			Count:      row.Count,
			IsJargon:   row.IsJargon != nil && *row.IsJargon,
			IsComplete: row.IsComplete,
			IsGlobal:   row.IsGlobal,
			CreatedBy:  normalizeJargonCreatedBy(row.CreatedBy),
		}
		if q.IncludeChatInfo {
			order, counts, err := parseSessionCountsOrdered(row.SessionIDDict)
			if err == nil {
				item.Targets = make([]JargonTarget, 0, len(order))
				for _, id := range order {
					rec, ok := chatSessions[id]
					if !ok {
						continue
					}
					if target, ok := chatSessionExportTarget(rec, counts[id]); ok {
						item.Targets = append(item.Targets, target)
					}
				}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *gormWebUIStore) jargonExportBase(ctx context.Context, q JargonExportQuery) (*gorm.DB, bool) {
	query := s.db.WithContext(ctx).Model(&Jargon{})
	if q.HasSessionIDs {
		scoped, ok := applyJargonScopeFilter(query, q.SessionIDs)
		if !ok {
			return nil, false
		}
		query = scoped
	}
	if q.HasIDs {
		query = query.Where("id IN ?", uniqueIDs(q.IDs))
	}
	return query, true
}

func (s *gormWebUIStore) MissingJargonIDs(ctx context.Context, q JargonExportQuery) ([]uint, error) {
	if !q.HasIDs {
		return nil, nil
	}
	base, ok := s.jargonExportBase(ctx, q)
	if !ok {
		return uniqueIDs(q.IDs), nil
	}
	var foundIDs []uint
	if err := base.Pluck("id", &foundIDs).Error; err != nil {
		return nil, err
	}
	found := make(map[uint]struct{}, len(foundIDs))
	for _, id := range foundIDs {
		found[id] = struct{}{}
	}
	missing := MissingIDs(uniqueIDs(q.IDs), found)
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return missing, nil
}

func chatSessionExportTarget(rec chatsession.Record, count int) (JargonTarget, bool) {
	platform := strings.TrimSpace(rec.Platform)
	accountID := ""
	if rec.AccountID != nil {
		accountID = strings.TrimSpace(*rec.AccountID)
	}
	scope := ""
	if rec.Scope != nil {
		scope = strings.TrimSpace(*rec.Scope)
	}
	if rec.GroupID != nil && strings.TrimSpace(*rec.GroupID) != "" {
		return JargonTarget{Platform: platform, ID: strings.TrimSpace(*rec.GroupID), Type: "group", AccountID: accountID, Scope: scope, Count: count}, true
	}
	if rec.UserID != nil && strings.TrimSpace(*rec.UserID) != "" {
		return JargonTarget{Platform: platform, ID: strings.TrimSpace(*rec.UserID), Type: "private", AccountID: accountID, Scope: scope, Count: count}, true
	}
	return JargonTarget{}, false
}
