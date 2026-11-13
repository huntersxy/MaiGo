package messagereceive

import (
	"context"
	"errors"

	"maibot-go/internal/chat/session"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SessionRepository 聊天流持久化端口，默认实现写 MaiBot chat_sessions 表
type SessionRepository interface {
	LoadAll(ctx context.Context) ([]chatsession.Record, error)
	Get(ctx context.Context, sessionID string) (*chatsession.Record, error)
	Save(ctx context.Context, record *chatsession.Record) error
}

// GormSessionRepository 基于 GORM 的聊天流仓库
type GormSessionRepository struct {
	db *gorm.DB
}

// NewGormSessionRepository 构造仓库；db 为空时返回 nil
func NewGormSessionRepository(db *gorm.DB) *GormSessionRepository {
	if db == nil {
		return nil
	}
	return &GormSessionRepository{db: db}
}

// LoadAll 加载全部聊天流
func (r *GormSessionRepository) LoadAll(ctx context.Context) ([]chatsession.Record, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var rows []chatsession.Record
	if err := r.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Get 按 session_id 查询聊天流，不存在返回 nil, nil
func (r *GormSessionRepository) Get(ctx context.Context, sessionID string) (*chatsession.Record, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var row chatsession.Record
	err := r.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Save 按 session_id upsert 聊天流
func (r *GormSessionRepository) Save(ctx context.Context, record *chatsession.Record) error {
	if r == nil || r.db == nil || record == nil {
		return nil
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "session_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"last_active_timestamp", "user_id", "user_nickname", "user_cardname",
			"group_id", "group_name", "account_id", "scope",
		}),
	}).Create(record).Error
}
