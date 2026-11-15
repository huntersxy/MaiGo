package imagesystem

import "MaiGo/internal/chat/image_system/schema"

// ImageType 图片记录类型，对齐 Python ImageType
//
// 落库值是 SQLAlchemy Enum 的枚举名（大写），模型定义在 schema 子包
// （internal/common/database 也要复用，不能直接 import 本包以免成环）。
const (
	ImageTypeEmoji = schema.ImageTypeEmoji
	ImageTypeImage = schema.ImageTypeImage
)

// ImageRecord 对齐 Python images 表，表情与普通图片共用
type ImageRecord = schema.ImageRecord

// ManagedImage 图片运行时对象，对应 Python MaiImage 的 image_manager 相关字段
type ManagedImage struct {
	FullPath     string
	FileName     string
	FileHash     string
	ImageFormat  string
	ImageBytes   []byte
	Description  string
	VLMProcessed bool
}
