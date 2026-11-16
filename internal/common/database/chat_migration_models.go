package memory

// 聊天迁移任务及复制来源账本，对齐 MaiBot src/common/database/chat_migration_models.py。

// ChatMigrationJob 一次在线迁移任务
type ChatMigrationJob struct {
	ID        string `gorm:"column:id;type:varchar;primaryKey"`
	Status    string `gorm:"column:status;type:varchar;not null"`
	Source    string `gorm:"column:source;type:varchar;not null"`
	Target    string `gorm:"column:target;type:varchar;not null"`
	CreatedAt string `gorm:"column:created_at;type:varchar;not null"`
	Payload   string `gorm:"column:payload;type:text;not null"`
	Error     string `gorm:"column:error;type:text;not null"`
}

func (ChatMigrationJob) TableName() string { return "chat_migration_jobs" }

// ChatMigrationCopy 来源记录到目标记录的幂等账本
type ChatMigrationCopy struct {
	ID        int    `gorm:"column:id;primaryKey;autoIncrement"`
	Table     string `gorm:"column:table_name;type:varchar;not null"`
	Origin    string `gorm:"column:origin;type:varchar;not null"`
	Target    string `gorm:"column:target;type:varchar;not null"`
	TargetKey string `gorm:"column:target_key;type:text;not null"`
	Amounts   string `gorm:"column:amounts;type:text;not null;default:'{}'"`
}

func (ChatMigrationCopy) TableName() string { return "chat_migration_copies" }
