package imagesystem

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// GORMStore 基于 GORM 的 images 表实现
type GORMStore struct {
	db *gorm.DB
}

// NewGORMStore 创建图片记录存储
func NewGORMStore(db *gorm.DB) *GORMStore {
	return &GORMStore{db: db}
}

// AutoMigrate 创建或更新 images 表
func (s *GORMStore) AutoMigrate() error {
	if s == nil || s.db == nil {
		return errors.New("图片存储数据库未初始化")
	}
	return s.db.AutoMigrate(&ImageRecord{})
}

// GetByHash 按哈希与类型查询，未找到返回 nil, nil
func (s *GORMStore) GetByHash(ctx context.Context, imageHash, imageType string) (*ImageRecord, error) {
	var record ImageRecord
	err := s.db.WithContext(ctx).
		Where("image_hash = ? AND image_type = ?", imageHash, imageType).
		Limit(1).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// Create 新建图片记录
func (s *GORMStore) Create(ctx context.Context, record *ImageRecord) error {
	if record == nil {
		return errors.New("图片记录不能为空")
	}
	return s.db.WithContext(ctx).Create(record).Error
}

// Save 全量保存图片记录
func (s *GORMStore) Save(ctx context.Context, record *ImageRecord) error {
	if record == nil {
		return errors.New("图片记录不能为空")
	}
	return s.db.WithContext(ctx).Save(record).Error
}

// Delete 按主键删除图片记录
func (s *GORMStore) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return errors.New("图片记录编号不能为空")
	}
	return s.db.WithContext(ctx).Delete(&ImageRecord{}, id).Error
}

// List 按类型列出图片记录
func (s *GORMStore) List(ctx context.Context, imageType string) ([]ImageRecord, error) {
	var records []ImageRecord
	query := s.db.WithContext(ctx)
	if imageType != "" {
		query = query.Where("image_type = ?", imageType)
	}
	if err := query.Order("id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListPaths 按类型列出存储路径
func (s *GORMStore) ListPaths(ctx context.Context, imageType string) ([]string, error) {
	var paths []string
	query := s.db.WithContext(ctx).Model(&ImageRecord{})
	if imageType != "" {
		query = query.Where("image_type = ?", imageType)
	}
	if err := query.Pluck("full_path", &paths).Error; err != nil {
		return nil, err
	}
	return paths, nil
}
