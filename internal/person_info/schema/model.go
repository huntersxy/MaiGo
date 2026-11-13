package schema

import "time"

// PersonInfo 对齐 MaiBot database_model.PersonInfo（表 person_info）
// 独立成无数据库驱动依赖的子包，便于 SQLite AutoMigrate 守卫测试复用同一模型定义
type PersonInfo struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	IsKnown      bool   `gorm:"not null;default:false" json:"is_known"`
	PersonID     string `gorm:"type:varchar(255);uniqueIndex;not null" json:"person_id"`
	PersonName   string `gorm:"type:varchar(255)" json:"person_name,omitempty"`
	NameReason   string `gorm:"type:varchar" json:"name_reason,omitempty"`
	Platform     string `gorm:"type:varchar(100);index;not null" json:"platform"`
	UserID       string `gorm:"type:varchar(255);index;not null" json:"user_id"`
	UserNickname string `gorm:"type:varchar(255);index;not null" json:"user_nickname"`
	// Python 侧 group_cardname / memory_points 都是 Optional[str]（VARCHAR，可空），不是 JSONB
	GroupCardname  *string    `gorm:"type:varchar" json:"-"`
	MemoryPoints   *string    `gorm:"type:varchar" json:"-"`
	KnowCounts     int        `gorm:"not null;default:0" json:"know_counts"`
	FirstKnownTime *time.Time `json:"first_known_time,omitempty"`
	LastKnownTime  *time.Time `json:"last_known_time,omitempty"`
}

func (PersonInfo) TableName() string { return "person_info" }
