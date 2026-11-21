package personinfo

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// PersonListFilter 对齐 webui/routers/person.py 列表查询
type PersonListFilter struct {
	Page                  int
	PageSize              int
	Search                string
	IsKnown               *bool
	Platform              string
	UserID                string
	ExcludeNumericUserIDs bool
}

// ListPersons 分页读取 person_info
func (r *Registry) ListPersons(ctx context.Context, filter PersonListFilter) ([]PersonInfo, int, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, 0, errMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return nil, 0, nil
	}
	page, size := normalizePersonPage(filter.Page, filter.PageSize)
	q := db.WithContext(ctx).Model(&PersonInfo{}).
		Joins("JOIN individuals ON individuals.individual_id = person_info.individual_id")
	q = applyPersonListFilter(q, filter)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []PersonInfo
	if err := q.Preload("Individual").Order("person_info.last_known_time DESC NULLS LAST, person_info.id DESC").
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, int(total), nil
}

func applyPersonListFilter(q *gorm.DB, filter PersonListFilter) *gorm.DB {
	search := strings.TrimSpace(filter.Search)
	if search != "" {
		like := "%" + search + "%"
		// Python 侧用 col(...).contains()，即大小写敏感的 LIKE
		q = q.Where("individuals.display_name LIKE ? OR person_info.user_nickname LIKE ? OR person_info.user_id LIKE ?", like, like, like)
	}
	if filter.IsKnown != nil {
		q = q.Where("individuals.is_known = ?", *filter.IsKnown)
	}
	if platform := strings.TrimSpace(filter.Platform); platform != "" {
		q = q.Where("person_info.platform = ?", platform)
	}
	if userID := strings.TrimSpace(filter.UserID); userID != "" {
		q = q.Where("person_info.user_id = ?", userID)
	}
	if filter.ExcludeNumericUserIDs {
		if q.Dialector.Name() == "postgres" {
			q = q.Where("person_info.user_id !~ '^[0-9]+$'")
		} else {
			q = q.Where("person_info.user_id GLOB '*[^0-9]*'")
		}
	}
	return q
}

func normalizePersonPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}
