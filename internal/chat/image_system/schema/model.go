package schema

import "time"

// ImageType 图片记录类型，对齐 Python src/common/database/database_model.py 的 ImageType 枚举
//
// Python 侧该列是 SQLAlchemy 的 Enum(ImageType)，未传 values_callable，
// 因此落库的是**枚举名**（大写 EMOJI / IMAGE），不是枚举值（小写 emoji / image）。
// 证据：src/common/database/migrations/v2_to_v3.py:240 `if image_type != "EMOJI"`；
// database_model.py 的 `image_type VARCHAR(5)` 恰好容纳大写名；
// legacy_v1_to_v2.py:528 的 _deduce_image_type_name 返回 "EMOJI"/"IMAGE"。
const (
	ImageTypeEmoji = "EMOJI"
	ImageTypeImage = "IMAGE"
)

// ImageRecord 对齐 MaiBot database_model.Images（表 images），表情与普通图片共用一张表。
//
// full_path 存相对实例根目录的 POSIX 路径，用 ResolveStoredImagePath /
// SerializeStoredImagePath 与真实路径互转。独立成无数据库驱动依赖的子包，
// 便于 internal/common/database 复用同一模型定义而不产生 import 环。
type ImageRecord struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	ImageHash    string     `gorm:"column:image_hash;type:varchar(255);index;not null" json:"image_hash"`
	Description  string     `gorm:"type:text;not null;default:''" json:"description"`
	FullPath     string     `gorm:"column:full_path;type:text;not null" json:"full_path"`
	ImageType    string     `gorm:"column:image_type;type:varchar(5);index;not null;default:'EMOJI'" json:"image_type"`
	QueryCount   int        `gorm:"column:query_count;not null;default:0" json:"query_count"`
	IsRegistered bool       `gorm:"column:is_registered;not null;default:false" json:"is_registered"`
	IsBanned     bool       `gorm:"column:is_banned;not null;default:false" json:"is_banned"`
	NoFileFlag   bool       `gorm:"column:no_file_flag;not null;default:false" json:"no_file_flag"`
	RecordTime   time.Time  `gorm:"column:record_time;index;not null" json:"record_time"`
	RegisterTime *time.Time `gorm:"column:register_time" json:"register_time,omitempty"`
	LastUsedTime *time.Time `gorm:"column:last_used_time" json:"last_used_time,omitempty"`
	VLMProcessed bool       `gorm:"column:vlm_processed;not null;default:false" json:"vlm_processed"`
}

// TableName 表名与 Python 一致
func (ImageRecord) TableName() string { return "images" }
