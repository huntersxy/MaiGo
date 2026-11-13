package personinfo

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// upsertPersonOnRegister 对齐 Person.register_person：已认识只补群名片，未认识才建档
func (r *Registry) upsertPersonOnRegister(ctx context.Context, pid, platform, userID, nickname string, in RegisterInput) (*PersonInfo, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		// 仅 member_profiles 路径时仍返回合成 Profile
		now := time.Now()
		return &PersonInfo{
			PersonID: pid, Platform: platform, UserID: userID, UserNickname: nickname,
			PersonName: nickname, IsKnown: true, KnowCounts: 1,
			FirstKnownTime: &now, LastKnownTime: &now,
		}, nil
	}

	groupKey := strings.TrimSpace(in.GroupKey)
	if groupKey == "" && in.GroupID > 0 {
		groupKey = strconv.FormatInt(in.GroupID, 10)
	}
	groupNick := strings.TrimSpace(in.GroupNickName)

	var existing PersonInfo
	findErr := db.Where("person_id = ?", pid).First(&existing).Error
	if findErr != nil && findErr != gorm.ErrRecordNotFound {
		return nil, findErr
	}
	notFound := findErr == gorm.ErrRecordNotFound

	// is_person_known(person_id=...) 为真：只做 add_group_nick_name
	if !notFound && existing.IsKnown {
		if groupKey == "" || groupNick == "" {
			return &existing, nil
		}
		cards := upsertGroupCard(decodeGroupCards(existing.GroupCardname), groupKey, groupNick)
		raw, encErr := encodeGroupCards(cards)
		if encErr != nil {
			return nil, encErr
		}
		existing.GroupCardname = jsonTextPtr(raw)
		// sync_to_database 由刚重新加载的 Person 触发，know_since/last_know 此时为空
		if saveErr := r.syncLoadedPerson(ctx, &existing); saveErr != nil {
			return nil, saveErr
		}
		return &existing, nil
	}

	// 未认识：register_person 新建 Person 后 sync_to_database 覆盖整行
	now := time.Now()
	cards := make([]GroupCardEntry, 0, 1)
	if groupKey != "" && groupNick != "" {
		cards = upsertGroupCard(cards, groupKey, groupNick)
	}
	groupRaw, err := encodeGroupCards(cards)
	if err != nil {
		return nil, err
	}
	memRaw, err := encodeMemoryPoints(nil)
	if err != nil {
		return nil, err
	}

	if notFound {
		rec := PersonInfo{
			IsKnown: true, PersonID: pid, PersonName: nickname, NameReason: "用户注册时设置的昵称",
			Platform: platform, UserID: userID, UserNickname: nickname,
			GroupCardname: jsonTextPtr(groupRaw), MemoryPoints: jsonTextPtr(memRaw), KnowCounts: 1,
			FirstKnownTime: &now, LastKnownTime: &now,
		}
		if err := db.Create(&rec).Error; err != nil {
			return nil, err
		}
		return &rec, nil
	}

	existing.IsKnown = true
	existing.Platform = platform
	existing.UserID = userID
	existing.UserNickname = nickname
	existing.PersonName = nickname
	existing.NameReason = "用户注册时设置的昵称"
	existing.KnowCounts = 1
	existing.FirstKnownTime = &now
	existing.LastKnownTime = &now
	existing.MemoryPoints = jsonTextPtr(memRaw)
	existing.GroupCardname = jsonTextPtr(groupRaw)
	if err := db.Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// syncLoadedPerson 复刻 Person.sync_to_database：未认识不写；整行回写，know_since/last_know 为空
func (r *Registry) syncLoadedPerson(ctx context.Context, rec *PersonInfo) error {
	if rec == nil || !rec.IsKnown {
		return nil
	}
	db := r.db(ctx)
	if db == nil {
		return errMemoryUnavailable
	}
	rec.FirstKnownTime = nil
	rec.LastKnownTime = nil
	return db.Save(rec).Error
}

func upsertGroupCard(list []GroupCardEntry, groupID, card string) []GroupCardEntry {
	groupID = strings.TrimSpace(groupID)
	card = strings.TrimSpace(card)
	if groupID == "" || card == "" {
		return list
	}
	for i := range list {
		if list[i].GroupID == groupID {
			list[i].GroupCardname = card
			return list
		}
	}
	return append(list, GroupCardEntry{GroupID: groupID, GroupCardname: card})
}

func (r *Registry) loadPersonByID(ctx context.Context, personID string) (*PersonInfo, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return nil, gorm.ErrRecordNotFound
	}
	var rec PersonInfo
	if err := db.Where("person_id = ?", personID).First(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetProfile 按 person_id 读取画像
func (r *Registry) GetProfile(ctx context.Context, personID string) (*Profile, error) {
	rec, err := r.loadPersonByID(ctx, personID)
	if err != nil {
		return nil, err
	}
	p := profileFromRecord(rec)
	return &p, nil
}

// IsKnown 对齐 MaiBot is_person_known(person_id=...)
func (r *Registry) IsKnown(ctx context.Context, personID string) bool {
	rec, err := r.loadPersonByID(ctx, personID)
	if err != nil {
		return false
	}
	return rec.IsKnown
}

// ResolvePersonID 对齐 resolve_person_id_for_memory：ID 优先，其次唯一精确 person_name
func (r *Registry) ResolvePersonID(ctx context.Context, platform, userID, personName string, strictKnown bool) string {
	platform = normalizePlatform(platform)
	userID = strings.TrimSpace(userID)
	if platform != "" && userID != "" {
		pid := PersonID(platform, userID)
		if strictKnown && !r.IsKnown(ctx, pid) {
			return ""
		}
		return pid
	}
	name := strings.TrimSpace(personName)
	if name == "" {
		return ""
	}
	pid := r.GetPersonIDByPersonName(ctx, name)
	if pid == "" {
		return ""
	}
	if strictKnown && !r.IsKnown(ctx, pid) {
		return ""
	}
	return pid
}

// UpdateProfileFields 更新 person_info 可写字段，必要时同步 member_profiles 昵称
func (r *Registry) UpdateProfileFields(ctx context.Context, personID string, upd ProfileUpdate) (*Profile, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return nil, errPersonNotFound
	}

	var rec PersonInfo
	if err := db.Where("person_id = ?", personID).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errPersonNotFound
		}
		return nil, err
	}

	if upd.Nickname != nil {
		rec.UserNickname = strings.TrimSpace(*upd.Nickname)
	}
	if upd.PersonName != nil {
		rec.PersonName = strings.TrimSpace(*upd.PersonName)
	}
	if upd.NameReason != nil {
		rec.NameReason = strings.TrimSpace(*upd.NameReason)
	}
	if upd.IsKnown != nil {
		rec.IsKnown = *upd.IsKnown
	}
	if upd.MemoryPoints != nil {
		raw, err := encodeMemoryPoints(*upd.MemoryPoints)
		if err != nil {
			return nil, err
		}
		rec.MemoryPoints = jsonTextPtr(raw)
	}
	if upd.GroupCard != nil {
		cards := upsertGroupCard(decodeGroupCards(rec.GroupCardname), upd.GroupCard.GroupID, upd.GroupCard.GroupCardname)
		raw, err := encodeGroupCards(cards)
		if err != nil {
			return nil, err
		}
		rec.GroupCardname = jsonTextPtr(raw)
	}

	now := time.Now()
	rec.LastKnownTime = &now

	if err := db.Clauses(clause.Returning{}).Save(&rec).Error; err != nil {
		return nil, err
	}

	mem := r.manager()
	if mem != nil && upd.Nickname != nil {
		if qqID := ParseQQUserID(rec.UserID); qqID > 0 {
			_, _ = mem.GetOrCreateMemberProfile(qqID, rec.UserNickname, time.Now())
		}
	}

	p := profileFromRecord(&rec)
	return &p, nil
}
