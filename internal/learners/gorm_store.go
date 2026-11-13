package learners

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/chat/session"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GORMStore 基于 GORM/SQLite 的 Store 实现
type GORMStore struct {
	db *gorm.DB
}

func NewGORMStore(db *gorm.DB) *GORMStore {
	return &GORMStore{db: db}
}

func (s *GORMStore) UpsertExpression(ctx context.Context, rec ExpressionRecord) (*ExpressionRecord, error) {
	normalizedStyle := NormalizeExpressionStyleForLearning(rec.Style)
	situation := strings.TrimSpace(rec.Situation)
	if situation == "" || normalizedStyle == "" {
		return nil, fmt.Errorf("情景或风格为空")
	}
	sessionKey := strings.TrimSpace(rec.SessionID)
	if sessionKey != "" && chatsession.IsConversationKey(sessionKey) {
		return nil, chatsession.ErrConversationKeyNotSessionID
	}

	// 同 session 找 style+situation 完全一致项，对齐 expression_learner._find_similar_expression
	var rows []Expression
	if err := s.db.WithContext(ctx).Where("session_id IS NOT DISTINCT FROM ?", nullableSessionID(sessionKey)).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if NormalizeExpressionStyleForLearning(row.Style) != normalizedStyle {
			continue
		}
		content, err := decodeContentList(row.ContentList)
		if err != nil {
			return nil, err
		}
		candidates := append([]string{row.Situation}, content...)
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate) == situation {
				content = append(content, situation)
				row.ContentList = mustEncodeContentList(content)
				row.Count++
				row.Checked = rec.Checked
				row.ModifiedBy = rec.ModifiedBy
				row.LastActiveTime = time.Now()
				if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
					return nil, err
				}
				out := expressionToRecord(row)
				return &out, nil
			}
		}
	}

	contentList := mustEncodeContentList([]string{situation})
	now := time.Now()
	row := Expression{
		Situation:      situation,
		Style:          normalizedStyle,
		ContentList:    contentList,
		Count:          1,
		LastActiveTime: now,
		CreateTime:     now,
		SessionID:      nullableSessionID(sessionKey),
		Checked:        rec.Checked,
		ModifiedBy:     rec.ModifiedBy,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	out := expressionToRecord(row)
	return &out, nil
}

func (s *GORMStore) GetExpression(ctx context.Context, id uint) (*ExpressionRecord, error) {
	var row Expression
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errorsIsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out, err := expressionToRecordE(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GORMStore) ListExpressions(ctx context.Context, filter ExpressionFilter) ([]ExpressionRecord, int, error) {
	q := s.db.WithContext(ctx).Model(&Expression{})
	if filter.CheckedOnly {
		q = q.Where("checked = ?", true)
	}
	if filter.UserCheckedOnly {
		q = q.Where("checked = ? AND modified_by = ?", true, ModifiedByUser)
	}
	switch {
	case filter.IncludeGlobal && len(filter.SessionIDs) > 0:
		q = q.Where("session_id IS NULL OR session_id IN ?", filter.SessionIDs)
	case filter.IncludeGlobal:
		q = q.Where("session_id IS NULL")
	case len(filter.SessionIDs) > 0:
		q = q.Where("session_id IN ?", filter.SessionIDs)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var rows []Expression
	if err := q.Order("last_active_time DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]ExpressionRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := expressionToRecordE(row)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	return out, int(total), nil
}

func (s *GORMStore) TouchExpressionLastActive(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&Expression{}).Where("id IN ?", ids).Update("last_active_time", now)
	if res.Error != nil {
		return res.Error
	}
	return nil
}

func (s *GORMStore) SetExpressionChecked(ctx context.Context, id uint, checked bool, modifiedBy *ModifiedBy) error {
	res := s.db.WithContext(ctx).Model(&Expression{}).Where("id = ?", id).Updates(map[string]any{
		"checked":     checked,
		"modified_by": modifiedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *GORMStore) DeleteExpression(ctx context.Context, id uint) error {
	res := s.db.WithContext(ctx).Delete(&Expression{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *GORMStore) CreateJargon(ctx context.Context, rec JargonRecord) (*JargonRecord, error) {
	row, err := jargonRecordToModel(rec)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if row.CreatedTimestamp.IsZero() {
		row.CreatedTimestamp = now
	}
	if row.UpdatedTimestamp.IsZero() {
		row.UpdatedTimestamp = now
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	out := jargonToRecord(row)
	return &out, nil
}

func (s *GORMStore) UpdateJargon(ctx context.Context, rec JargonRecord) (*JargonRecord, error) {
	if rec.ID == 0 {
		return nil, fmt.Errorf("缺少黑话 ID")
	}
	if rec.CreatedBy == JargonCreatedByManual {
		return s.GetJargon(ctx, rec.ID)
	}
	dict, err := encodeSessionCounts(rec.SessionCounts)
	if err != nil {
		return nil, err
	}
	result := s.db.WithContext(ctx).Model(&Jargon{}).Where("id = ?", rec.ID).
		Updates(jargonUpdateValues(rec, dict, time.Now()))
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.GetJargon(ctx, rec.ID)
}

// jargonUpdateValues 对齐 Python _update_jargon 的字段集合，不整行覆盖
func jargonUpdateValues(rec JargonRecord, sessionIDDict string, now time.Time) map[string]any {
	var evidence *string
	if rec.EvidenceMessages != "" {
		value := rec.EvidenceMessages
		evidence = &value
	}
	return map[string]any{
		"count":             rec.Count,
		"evidence_messages": evidence,
		"session_id_dict":   sessionIDDict,
		"is_global":         rec.IsGlobal,
		"updated_timestamp": now,
	}
}

// UpdateJargonInference 只更新推断结果字段，对齐 Python _modify_jargon_entry
func (s *GORMStore) UpdateJargonInference(ctx context.Context, rec JargonRecord) error {
	if rec.ID == 0 {
		return fmt.Errorf("缺少黑话 ID")
	}
	result := s.db.WithContext(ctx).Model(&Jargon{}).Where("id = ?", rec.ID).
		Updates(jargonInferenceUpdateValues(rec, time.Now()))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func jargonInferenceUpdateValues(rec JargonRecord, now time.Time) map[string]any {
	return map[string]any{
		"meaning":              rec.Meaning,
		"is_jargon":            rec.IsJargon,
		"is_complete":          rec.IsComplete,
		"last_inference_count": rec.LastInferenceCount,
		"updated_timestamp":    now,
	}
}

func (s *GORMStore) GetJargon(ctx context.Context, id uint) (*JargonRecord, error) {
	var row Jargon
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errorsIsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out := jargonToRecord(row)
	return &out, nil
}

func (s *GORMStore) ListJargons(ctx context.Context, filter JargonFilter) ([]JargonRecord, int, error) {
	q := s.db.WithContext(ctx).Model(&Jargon{})
	if filter.IsJargon != nil {
		q = q.Where("is_jargon = ?", *filter.IsJargon)
	}
	// session 范围过滤在内存侧做，对齐 MaiBot session_id_dict JSON
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var rows []Jargon
	if err := q.Order("updated_timestamp DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]JargonRecord, 0, len(rows))
	for _, row := range rows {
		rec := jargonToRecord(row)
		if len(filter.SessionIDs) > 0 && !jargonInScope(rec.SessionCounts, filter.SessionIDs) && !rec.IsGlobal {
			continue
		}
		out = append(out, rec)
	}
	return out, int(total), nil
}

func (s *GORMStore) SetJargonIsJargon(ctx context.Context, id uint, isJargon bool) error {
	res := s.db.WithContext(ctx).Model(&Jargon{}).Where("id = ?", id).Update("is_jargon", isJargon)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *GORMStore) DeleteJargon(ctx context.Context, id uint) error {
	res := s.db.WithContext(ctx).Delete(&Jargon{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *GORMStore) UpsertJargonCandidate(ctx context.Context, sessionID, content string) (*JargonRecord, bool, error) {
	return s.UpsertJargonEntry(ctx, sessionID, content, nil)
}

// UpsertJargonEntry 学习批次写入：合并证据消息组并累加会话计数
func (s *GORMStore) UpsertJargonEntry(ctx context.Context, sessionID, content string, evidence []JargonEvidenceGroup) (*JargonRecord, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false, fmt.Errorf("词条为空")
	}
	related := ResolveRelatedSessionIDs(sessionID)
	var rows []Jargon
	if err := s.db.WithContext(ctx).Where("content = ?", content).Find(&rows).Error; err != nil {
		return nil, false, err
	}

	var matched *Jargon
	for i := range rows {
		row := &rows[i]
		rec := jargonToRecord(*row)
		if !row.IsGlobal && !jargonInScope(rec.SessionCounts, related) {
			continue
		}
		if row.CreatedBy == JargonCreatedByManual {
			matched = row
			break
		}
		if matched == nil {
			matched = row
		}
	}

	now := time.Now()
	if matched != nil {
		if matched.CreatedBy == JargonCreatedByManual {
			out := jargonToRecord(*matched)
			return &out, false, nil
		}
		counts, err := parseSessionCounts(matched.SessionIDDict)
		if err != nil {
			return nil, false, err
		}
		if counts == nil {
			counts = map[string]int{}
		}
		if sessionID != "" {
			counts[sessionID] = counts[sessionID] + 1
		}
		dict, err := encodeSessionCounts(counts)
		if err != nil {
			return nil, false, err
		}
		matched.Count++
		matched.SessionIDDict = dict
		matched.UpdatedTimestamp = now
		matched.EvidenceMessages = mergeJargonEvidenceForUpdate(matched.EvidenceMessages, evidence)
		updated, err := s.UpdateJargon(ctx, jargonToRecord(*matched))
		if err != nil {
			return nil, false, err
		}
		return updated, false, nil
	}

	counts := map[string]int{}
	if sessionID != "" {
		counts[sessionID] = 1
	}
	dict, err := encodeSessionCounts(counts)
	if err != nil {
		return nil, false, err
	}
	isJargon := false
	row := Jargon{
		Content:            content,
		EvidenceMessages:   EncodeJargonEvidenceGroups(evidence),
		Meaning:            "",
		SessionIDDict:      dict,
		Count:              1,
		IsJargon:           &isJargon,
		IsComplete:         false,
		IsGlobal:           false,
		LastInferenceCount: 0,
		CreatedBy:          JargonCreatedByAI,
		CreatedTimestamp:   now,
		UpdatedTimestamp:   now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, false, err
	}
	out := jargonToRecord(row)
	return &out, true, nil
}

func (s *GORMStore) ListJargonCandidates(ctx context.Context, relatedSessionIDs []string, includeGlobalShare bool) ([]JargonCandidate, error) {
	var rows []Jargon
	q := s.db.WithContext(ctx).Where("is_jargon = ? AND meaning <> ''", true).Order("count DESC, id DESC")
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	related := make(map[string]struct{}, len(relatedSessionIDs))
	for _, id := range relatedSessionIDs {
		if id = strings.TrimSpace(id); id != "" {
			related[id] = struct{}{}
		}
	}
	out := make([]JargonCandidate, 0, len(rows))
	for _, row := range rows {
		content := strings.TrimSpace(row.Content)
		meaning := strings.TrimSpace(row.Meaning)
		if content == "" || meaning == "" {
			continue
		}
		if !row.IsGlobal && !includeGlobalShare {
			counts, _ := parseSessionCounts(row.SessionIDDict)
			if !jargonInScope(counts, relatedSessionIDs) {
				continue
			}
		}
		out = append(out, JargonCandidate{
			Content:       content,
			Meaning:       meaning,
			Count:         row.Count,
			IsGlobal:      row.IsGlobal,
			SessionIDDict: row.SessionIDDict,
		})
	}
	return out, nil
}

func (s *GORMStore) ListHighFrequencyTerms(ctx context.Context, chatID string) (map[string]HighFrequencyTermSummary, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return map[string]HighFrequencyTermSummary{}, nil
	}
	var rows []HighFrequencyTerm
	if err := s.db.WithContext(ctx).Where("chat_id = ?", chatID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]HighFrequencyTermSummary, len(rows))
	for _, row := range rows {
		key := NormalizeMatchText(row.Term)
		if key == "" {
			continue
		}
		out[key] = HighFrequencyTermSummary{
			Term:            row.Term,
			Rank:            row.Rank,
			OccurrenceCount: row.OccurrenceCount,
		}
	}
	return out, nil
}

// expressionToRecord 解析数据库行；content_list 非法时记错误并按空表达降级
//
// 读取路径（Get/List）改用 expressionToRecordE 显式返回错误，对齐 Python
// from_db_instance 抛 JSONDecodeError 的行为；其余投影调用点降级但留痕。
func expressionToRecord(row Expression) ExpressionRecord {
	rec, err := expressionToRecordE(row)
	if err != nil {
		zap.L().Error("解析 expressions.content_list 失败，按空表达降级",
			zap.Uint("expression_id", row.ID), zap.String("raw", row.ContentList), zap.Error(err))
	}
	return rec
}

func expressionToRecordE(row Expression) (ExpressionRecord, error) {
	content, err := decodeContentList(row.ContentList)
	if err != nil {
		return ExpressionRecord{}, fmt.Errorf("expressions[%d].content_list 解析失败: %w", row.ID, err)
	}
	sessionID := ""
	if row.SessionID != nil {
		sessionID = *row.SessionID
	}
	return ExpressionRecord{
		ID:             row.ID,
		Situation:      row.Situation,
		Style:          row.Style,
		Content:        content,
		Count:          row.Count,
		LastActiveTime: row.LastActiveTime,
		CreateTime:     row.CreateTime,
		SessionID:      sessionID,
		Checked:        row.Checked,
		ModifiedBy:     row.ModifiedBy,
	}, nil
}

func jargonToRecord(row Jargon) JargonRecord {
	counts, _ := parseSessionCounts(row.SessionIDDict)
	isJargon := false
	if row.IsJargon != nil {
		isJargon = *row.IsJargon
	}
	evidence := ""
	if row.EvidenceMessages != nil {
		evidence = *row.EvidenceMessages
	}
	return JargonRecord{
		ID:                 row.ID,
		Content:            row.Content,
		Meaning:            row.Meaning,
		EvidenceMessages:   evidence,
		SessionCounts:      counts,
		Count:              row.Count,
		IsJargon:           isJargon,
		IsComplete:         row.IsComplete,
		IsGlobal:           row.IsGlobal,
		LastInferenceCount: row.LastInferenceCount,
		CreatedBy:          row.CreatedBy,
		CreatedTimestamp:   row.CreatedTimestamp,
		UpdatedTimestamp:   row.UpdatedTimestamp,
	}
}

func jargonRecordToModel(rec JargonRecord) (Jargon, error) {
	for sessionID := range rec.SessionCounts {
		if chatsession.IsConversationKey(sessionID) {
			return Jargon{}, chatsession.ErrConversationKeyNotSessionID
		}
	}
	dict, err := encodeSessionCounts(rec.SessionCounts)
	if err != nil {
		return Jargon{}, err
	}
	isJargon := rec.IsJargon
	var evidence *string
	if rec.EvidenceMessages != "" {
		evidence = &rec.EvidenceMessages
	}
	return Jargon{
		ID:                 rec.ID,
		Content:            strings.TrimSpace(rec.Content),
		Meaning:            rec.Meaning,
		EvidenceMessages:   evidence,
		SessionIDDict:      dict,
		Count:              rec.Count,
		IsJargon:           &isJargon,
		IsComplete:         rec.IsComplete,
		IsGlobal:           rec.IsGlobal,
		LastInferenceCount: rec.LastInferenceCount,
		CreatedBy:          rec.CreatedBy,
		UpdatedTimestamp:   rec.UpdatedTimestamp,
		CreatedTimestamp:   rec.CreatedTimestamp,
	}, nil
}

func decodeContentList(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	return items, nil
}

func mustEncodeContentList(items []string) string {
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func encodeSessionCounts(counts map[string]int) (string, error) {
	if counts == nil {
		return "{}", nil
	}
	b, err := json.Marshal(counts)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseSessionCounts(raw string) (map[string]int, error) {
	out := map[string]int{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jargonInScope(counts map[string]int, relatedSessionIDs []string) bool {
	if len(counts) == 0 {
		return false
	}
	for _, sessionID := range relatedSessionIDs {
		if _, ok := counts[sessionID]; ok {
			return true
		}
	}
	return false
}

func nullableSessionID(sessionID string) *string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return &sessionID
}

func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return page, size
}

func errorsIsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}

// DB 返回底层连接，供 Dashboard 查询层复用同一连接池
func (s *GORMStore) DB() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.db
}
