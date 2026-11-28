package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Individual struct {
	IndividualID       string    `gorm:"type:varchar(32);primaryKey" json:"individual_id"`
	DisplayName        string    `gorm:"type:varchar(255);not null" json:"display_name"`
	IsKnown            bool      `gorm:"not null;default:false" json:"is_known"`
	ProfileSourcesJSON string    `gorm:"type:text;not null;default:'[]'" json:"-"`
	CreatedAt          time.Time `gorm:"type:datetime;not null" json:"created_at"`
}

func (Individual) TableName() string { return "individuals" }

type IndividualMergeEvent struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	TargetIndividualID string    `gorm:"type:varchar(32);index;not null" json:"target_individual_id"`
	MembersJSON        string    `gorm:"type:text;not null" json:"-"`
	ProfilesJSON       string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	CreatedAt          time.Time `gorm:"type:datetime;not null" json:"created_at"`
}

func (IndividualMergeEvent) TableName() string { return "individual_merge_events" }

// PersonInfo 对齐 MaiBot database_model.PersonInfo（表 person_info）
type PersonInfo struct {
	ID             uint        `gorm:"primaryKey" json:"id"`
	IsKnown        bool        `gorm:"not null;default:false" json:"is_known"`
	PersonID       string      `gorm:"type:varchar(255);uniqueIndex;not null" json:"person_id"`
	IndividualID   string      `gorm:"type:varchar(32);index;not null" json:"individual_id"`
	Individual     *Individual `gorm:"foreignKey:IndividualID;references:IndividualID" json:"-"`
	PersonName     *string     `gorm:"type:varchar(255)" json:"person_name,omitempty"`
	Platform       string      `gorm:"type:varchar(100);index;not null" json:"platform"`
	UserID         string      `gorm:"type:varchar(255);index;not null" json:"user_id"`
	UserNickname   string      `gorm:"type:varchar(255);index;not null" json:"user_nickname"`
	GroupCardname  *string     `gorm:"type:varchar" json:"-"`
	KnowCounts     int         `gorm:"not null;default:0" json:"know_counts"`
	FirstKnownTime *time.Time  `json:"first_known_time,omitempty"`
	LastKnownTime  *time.Time  `json:"last_known_time,omitempty"`
}

func (PersonInfo) TableName() string { return "person_info" }

func (person *PersonInfo) BeforeCreate(tx *gorm.DB) error {
	if person.IndividualID == "" {
		person.IndividualID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	var existing Individual
	err := tx.Where("individual_id = ?", person.IndividualID).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	displayName := ""
	if person.PersonName != nil {
		displayName = strings.TrimSpace(*person.PersonName)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(person.UserNickname)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(person.UserID)
	}
	sources, err := json.Marshal([]map[string]any{{
		"individual_id": person.IndividualID,
		"person_id":     person.PersonID,
		"platform":      person.Platform,
		"user_id":       person.UserID,
		"display_name":  displayName,
		"is_known":      person.IsKnown,
	}})
	if err != nil {
		return err
	}
	return tx.Create(&Individual{
		IndividualID:       person.IndividualID,
		DisplayName:        displayName,
		IsKnown:            person.IsKnown,
		ProfileSourcesJSON: string(sources),
		CreatedAt:          time.Now(),
	}).Error
}
