package personinfo

import "time"

// Profile 入站与工具层使用的合并画像视图
type Profile struct {
	ID             uint             `json:"id"`
	PersonID       string           `json:"person_id"`
	IndividualID   string           `json:"individual_id"`
	SourceName     *string          `json:"source_person_name"`
	Platform       string           `json:"platform"`
	UserID         string           `json:"user_id"`
	IsKnown        bool             `json:"is_known"`
	Nickname       string           `json:"nickname"`
	PersonName     string           `json:"person_name"`
	GroupCards     []GroupCardEntry `json:"group_cards,omitempty"`
	KnowCounts     int              `json:"know_counts"`
	FirstKnownTime *time.Time       `json:"first_known_time,omitempty"`
	LastKnownTime  *time.Time       `json:"last_known_time,omitempty"`
}

// ProfileUpdate 可写字段，零值表示不修改
type ProfileUpdate struct {
	Nickname             *string
	PersonName           *string
	IsKnown              *bool
	ExpectedIndividualID *string
	ExpectedProfile      *IndividualProfile
	GroupCard            *GroupCardEntry
}

// ProfileFromRecord 将 person_info 行转为 Profile
func ProfileFromRecord(rec *PersonInfo) Profile {
	return profileFromRecord(rec)
}

func profileFromRecord(rec *PersonInfo) Profile {
	if rec == nil {
		return Profile{}
	}
	return Profile{
		ID:             rec.ID,
		PersonID:       rec.PersonID,
		IndividualID:   rec.IndividualID,
		SourceName:     rec.PersonName,
		Platform:       rec.Platform,
		UserID:         rec.UserID,
		IsKnown:        rec.Individual != nil && rec.Individual.IsKnown,
		Nickname:       rec.UserNickname,
		PersonName:     individualDisplayName(rec),
		GroupCards:     decodeGroupCards(rec.GroupCardname),
		KnowCounts:     rec.KnowCounts,
		FirstKnownTime: rec.FirstKnownTime,
		LastKnownTime:  rec.LastKnownTime,
	}
}

func individualDisplayName(rec *PersonInfo) string {
	if rec == nil {
		return ""
	}
	if rec.Individual != nil {
		return rec.Individual.DisplayName
	}
	if rec.PersonName != nil {
		return *rec.PersonName
	}
	return ""
}
