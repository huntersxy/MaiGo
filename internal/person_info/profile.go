package personinfo

import "time"

// Profile 入站与工具层使用的合并画像视图
type Profile struct {
	PersonID       string           `json:"person_id"`
	Platform       string           `json:"platform"`
	UserID         string           `json:"user_id"`
	IsKnown        bool             `json:"is_known"`
	Nickname       string           `json:"nickname"`
	PersonName     string           `json:"person_name"`
	NameReason     string           `json:"name_reason,omitempty"`
	GroupCards     []GroupCardEntry `json:"group_cards,omitempty"`
	MemoryPoints   []string         `json:"memory_points,omitempty"`
	KnowCounts     int              `json:"know_counts"`
	FirstKnownTime *time.Time       `json:"first_known_time,omitempty"`
	LastKnownTime  *time.Time       `json:"last_known_time,omitempty"`
}

// ProfileUpdate 可写字段，零值表示不修改
type ProfileUpdate struct {
	Nickname     *string
	PersonName   *string
	NameReason   *string
	MemoryPoints *[]string
	IsKnown      *bool
	GroupCard    *GroupCardEntry
}

// ProfileFromRecord 将 person_info 行转为 Profile
func ProfileFromRecord(rec *PersonInfo) Profile {
	return profileFromRecord(rec)
}

func profileFromRecord(rec *PersonInfo) Profile {
	if rec == nil {
		return Profile{}
	}
	name := rec.PersonName
	if name == "" {
		name = rec.UserNickname
	}
	return Profile{
		PersonID:       rec.PersonID,
		Platform:       rec.Platform,
		UserID:         rec.UserID,
		IsKnown:        rec.IsKnown,
		Nickname:       rec.UserNickname,
		PersonName:     name,
		NameReason:     rec.NameReason,
		GroupCards:     decodeGroupCards(rec.GroupCardname),
		MemoryPoints:   decodeMemoryPoints(rec.MemoryPoints),
		KnowCounts:     rec.KnowCounts,
		FirstKnownTime: rec.FirstKnownTime,
		LastKnownTime:  rec.LastKnownTime,
	}
}
