package chatsession

import (
	"strings"
	"time"
)

// Record 对齐 MaiBot ORM chat_sessions
type Record struct {
	ID                  uint       `gorm:"primaryKey"`
	SessionID           string     `gorm:"column:session_id;type:text;not null;uniqueIndex"`
	CreatedTimestamp    time.Time  `gorm:"column:created_timestamp;not null"`
	LastActiveTimestamp *time.Time `gorm:"column:last_active_timestamp"`
	UserID              *string    `gorm:"column:user_id;type:text;index"`
	UserNickname        *string    `gorm:"column:user_nickname;type:text"`
	UserCardname        *string    `gorm:"column:user_cardname;type:text"`
	GroupID             *string    `gorm:"column:group_id;type:text;index"`
	GroupName           *string    `gorm:"column:group_name;type:text"`
	Platform            string     `gorm:"column:platform;type:text;not null;index"`
	AccountID           *string    `gorm:"column:account_id;type:text;index"`
	Scope               *string    `gorm:"column:scope;type:text;index"`
}

func (Record) TableName() string { return "chat_sessions" }

func (r Record) IsGroup() bool {
	return r.GroupID != nil && strings.TrimSpace(*r.GroupID) != ""
}

func (r Record) TargetID() string {
	if r.IsGroup() {
		return strings.TrimSpace(*r.GroupID)
	}
	if r.UserID != nil {
		return strings.TrimSpace(*r.UserID)
	}
	return ""
}
