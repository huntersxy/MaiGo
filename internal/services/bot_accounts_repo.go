package services

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// GormBotAccountRepository 使用 GORM 持久化 bot_platform_accounts
type GormBotAccountRepository struct {
	DB *gorm.DB
}

func (r *GormBotAccountRepository) db() (*gorm.DB, error) {
	if r == nil || r.DB == nil {
		return nil, errors.New("Bot 平台账号数据库未接线")
	}
	return r.DB, nil
}

func (r *GormBotAccountRepository) Ensure(ctx context.Context) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	// 表结构由迁移引擎（steps_v35_v46）持有；这里只补齐模型声明的唯一索引，
	// 不跑 AutoMigrate——旧表的表级 UNIQUE 会让 glebarez 重建路径误解析出 UNIQUE 列
	return db.WithContext(ctx).Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_bot_platform_accounts_platform_account ON bot_platform_accounts(platform, account_id)",
	).Error
}

func (r *GormBotAccountRepository) List(ctx context.Context) ([]BotPlatformAccount, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	var rows []BotPlatformAccount
	if err := db.WithContext(ctx).Order("platform asc, account_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *GormBotAccountRepository) Find(ctx context.Context, platform, accountID string) (*BotPlatformAccount, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	var row BotPlatformAccount
	result := db.WithContext(ctx).Where("platform = ? AND account_id = ?", platform, accountID).First(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &row, nil
}

func (r *GormBotAccountRepository) FindByID(ctx context.Context, id uint64) (*BotPlatformAccount, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	var row BotPlatformAccount
	result := db.WithContext(ctx).First(&row, id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &row, nil
}

func (r *GormBotAccountRepository) Save(ctx context.Context, account *BotPlatformAccount) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if account.ID == 0 {
		return db.WithContext(ctx).Create(account).Error
	}
	return db.WithContext(ctx).Save(account).Error
}

func (r *GormBotAccountRepository) Delete(ctx context.Context, id uint64) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Delete(&BotPlatformAccount{}, id).Error
}

func (r *GormBotAccountRepository) RemainingIDs(ctx context.Context, platform string) ([]uint64, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	var ids []uint64
	if err := db.WithContext(ctx).Model(&BotPlatformAccount{}).Where("platform = ?", platform).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
