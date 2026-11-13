package personinfo

import "context"

// UpdateProfileFields 更新 person_info 可写字段（DefaultRegistry 便捷入口）
func UpdateProfileFields(ctx context.Context, personID string, upd ProfileUpdate) (*Profile, error) {
	return DefaultRegistry().UpdateProfileFields(ctx, personID, upd)
}
