package personinfo

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// GetPersonRecord 按 person_id 读取原始记录，对齐 WebUI 人物详情
func (r *Registry) GetPersonRecord(ctx context.Context, personID string) (*PersonInfo, error) {
	rec, err := r.loadPersonByID(ctx, personID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPersonNotFound
		}
		if errors.Is(err, errMemoryUnavailable) {
			return nil, ErrMemoryUnavailable
		}
		return nil, err
	}
	return rec, nil
}

// DeletePerson 删除指定 person_id，返回被删除记录的展示名
func (r *Registry) DeletePerson(ctx context.Context, personID string) (string, error) {
	rec, err := r.GetPersonRecord(ctx, personID)
	if err != nil {
		return "", err
	}
	display := strings.TrimSpace(rec.PersonName)
	if display == "" {
		display = strings.TrimSpace(rec.UserNickname)
	}
	if display == "" {
		display = rec.UserID
	}
	if err := r.db(ctx).Where("person_id = ?", personID).Delete(&PersonInfo{}).Error; err != nil {
		return "", err
	}
	return display, nil
}

// PersonStats 人物统计：总数、已认识、未知与平台分布
func (r *Registry) PersonStats(ctx context.Context) (total, known int, platforms map[string]int, err error) {
	db := r.db(ctx)
	if db == nil {
		return 0, 0, nil, ErrMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return 0, 0, map[string]int{}, nil
	}
	var totals []struct {
		IsKnown bool
		Total   int
	}
	if err := db.WithContext(ctx).Model(&PersonInfo{}).
		Select("is_known, COUNT(*) AS total").Group("is_known").Scan(&totals).Error; err != nil {
		return 0, 0, nil, err
	}
	platforms = map[string]int{}
	for _, row := range totals {
		total += row.Total
		if row.IsKnown {
			known += row.Total
		}
	}
	var platformRows []struct {
		Platform string
		Total    int
	}
	if err := db.WithContext(ctx).Model(&PersonInfo{}).
		Select("platform, COUNT(*) AS total").Group("platform").Scan(&platformRows).Error; err != nil {
		return 0, 0, nil, err
	}
	for _, row := range platformRows {
		if strings.TrimSpace(row.Platform) == "" {
			continue
		}
		platforms[row.Platform] = row.Total
	}
	return total, known, platforms, nil
}

// BatchDeletePersons 批量删除，缺失的 ID 计入失败列表
func (r *Registry) BatchDeletePersons(ctx context.Context, personIDs []string) (deleted int, failed []string) {
	failed = make([]string, 0)
	for _, personID := range personIDs {
		if strings.TrimSpace(personID) == "" {
			failed = append(failed, personID)
			continue
		}
		if _, err := r.DeletePerson(ctx, personID); err != nil {
			failed = append(failed, personID)
			continue
		}
		deleted++
	}
	return deleted, failed
}
