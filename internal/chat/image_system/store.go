package imagesystem

import "context"

// Store 图片记录持久化端口，对应 Python 的 images 表访问
type Store interface {
	// GetByHash 按哈希与类型查询，未找到返回 nil, nil
	GetByHash(ctx context.Context, imageHash, imageType string) (*ImageRecord, error)
	Create(ctx context.Context, record *ImageRecord) error
	Save(ctx context.Context, record *ImageRecord) error
	Delete(ctx context.Context, id uint) error
	List(ctx context.Context, imageType string) ([]ImageRecord, error)
	ListPaths(ctx context.Context, imageType string) ([]string, error)
}
