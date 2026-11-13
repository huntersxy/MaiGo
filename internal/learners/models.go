package learners

import (
	"time"
)

// ModifiedBy 对应 MaiBot database_model.ModifiedBy
type ModifiedBy string

const (
	ModifiedByAI   ModifiedBy = "AI"
	ModifiedByUser ModifiedBy = "USER"
)

// JargonCreatedBy 对应 MaiBot database_model.JargonCreatedBy
type JargonCreatedBy string

const (
	JargonCreatedByAI     JargonCreatedBy = "AI"
	JargonCreatedByManual JargonCreatedBy = "MANUAL"
)

// Expression 对应 MaiBot expressions 表，见 src/common/database/database_model.py Expression
type Expression struct {
	ID             uint        `gorm:"primaryKey"`
	Situation      string      `gorm:"type:text;not null;index"`
	Style          string      `gorm:"type:text;not null;index"`
	ContentList    string      `gorm:"column:content_list;type:text;not null"`
	Count          int         `gorm:"not null;default:0"`
	LastActiveTime time.Time   `gorm:"column:last_active_time;index;not null"`
	CreateTime     time.Time   `gorm:"column:create_time;not null"`
	SessionID      *string     `gorm:"column:session_id;type:text;index"`
	Checked        bool        `gorm:"not null;default:false"`
	ModifiedBy     *ModifiedBy `gorm:"column:modified_by;type:text"`
}

func (Expression) TableName() string { return "expressions" }

// Jargon 对应 MaiBot jargons 表
type Jargon struct {
	ID                 uint            `gorm:"primaryKey"`
	Content            string          `gorm:"type:text;not null;index"`
	EvidenceMessages   *string         `gorm:"column:evidence_messages;type:text"`
	Meaning            string          `gorm:"type:text;not null"`
	SessionIDDict      string          `gorm:"column:session_id_dict;type:text;not null"`
	Count              int             `gorm:"not null;default:0"`
	IsJargon           *bool           `gorm:"column:is_jargon"`
	IsComplete         bool            `gorm:"column:is_complete;not null;default:false"`
	IsGlobal           bool            `gorm:"column:is_global;not null;default:false"`
	LastInferenceCount int             `gorm:"column:last_inference_count;not null;default:0"`
	CreatedBy          JargonCreatedBy `gorm:"column:created_by;type:text;not null"`
	CreatedTimestamp   time.Time       `gorm:"column:created_timestamp;index;not null"`
	UpdatedTimestamp   time.Time       `gorm:"column:updated_timestamp;not null"`
}

func (Jargon) TableName() string { return "jargons" }

// HighFrequencyTerm 对应 MaiBot high_frequency_terms 表，黑话上下文匹配加权用
type HighFrequencyTerm struct {
	ID               uint      `gorm:"primaryKey"`
	ChatID           string    `gorm:"column:chat_id;type:text;not null;index"`
	Term             string    `gorm:"type:text;not null"`
	Rank             int       `gorm:"not null;default:0"`
	OccurrenceCount  int       `gorm:"column:occurrence_count;not null;default:0"`
	MessageCount     int       `gorm:"column:message_count;not null;default:0"`
	Frequency        float64   `gorm:"not null;default:0"`
	MessageFrequency float64   `gorm:"column:message_frequency;not null;default:0"`
	CreatedAt        time.Time `gorm:"column:created_at;not null"`
	UpdatedAt        time.Time `gorm:"column:updated_at;not null"`
}

func (HighFrequencyTerm) TableName() string { return "high_frequency_terms" }
