package memory

// 相通组持久化模型，对齐 MaiBot src/common/database/sharing_models.py。
// 成员、规则与能力分开存储，同一聊天流可属于多个组。

// SharingGroup 相通组主表
type SharingGroup struct {
	ID        string  `gorm:"column:id;type:varchar;primaryKey"`
	Name      string  `gorm:"column:name;type:varchar;not null"`
	Color     string  `gorm:"column:color;type:varchar;not null"`
	Note      string  `gorm:"column:note;type:varchar;not null"`
	Enabled   bool    `gorm:"column:enabled;not null"`
	Revision  int     `gorm:"column:revision;not null"`
	CreatedAt float64 `gorm:"column:created_at;not null"`
	UpdatedAt float64 `gorm:"column:updated_at;not null"`
}

func (SharingGroup) TableName() string { return "sharing_groups" }

// SharingGroupMember 相通组成员聊天流
type SharingGroupMember struct {
	GroupID   string  `gorm:"column:group_id;type:varchar;primaryKey"`
	SessionID string  `gorm:"column:session_id;type:varchar;primaryKey;index"`
	CreatedAt float64 `gorm:"column:created_at;not null"`
}

func (SharingGroupMember) TableName() string { return "sharing_group_members" }

// SharingGroupCapability 相通组启用的共享能力
type SharingGroupCapability struct {
	GroupID    string `gorm:"column:group_id;type:varchar;primaryKey"`
	Capability string `gorm:"column:capability;type:varchar;primaryKey"`
	Enabled    bool   `gorm:"column:enabled;not null"`
}

func (SharingGroupCapability) TableName() string { return "sharing_group_capabilities" }

// SharingGroupRule 按平台与目标匹配的成员规则
type SharingGroupRule struct {
	ID       string `gorm:"column:id;type:varchar;primaryKey"`
	GroupID  string `gorm:"column:group_id;type:varchar;not null;index"`
	Platform string `gorm:"column:platform;type:varchar;not null"`
	ItemID   string `gorm:"column:item_id;type:varchar;not null"`
	RuleType string `gorm:"column:rule_type;type:varchar;not null"`
}

func (SharingGroupRule) TableName() string { return "sharing_group_rules" }

// SharingGroupMigration 旧配置一次性导入标记与原始载荷
type SharingGroupMigration struct {
	Source     string  `gorm:"column:source;type:varchar;primaryKey"`
	GroupID    *string `gorm:"column:group_id;type:varchar"`
	Payload    string  `gorm:"column:payload;type:varchar;not null"`
	MigratedAt float64 `gorm:"column:migrated_at;not null"`
}

func (SharingGroupMigration) TableName() string { return "sharing_group_migrations" }
