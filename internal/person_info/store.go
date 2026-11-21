package personinfo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/person_info/identity"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
			PersonName: stringPtr(nickname), IsKnown: true, KnowCounts: 1,
			FirstKnownTime: &now, LastKnownTime: &now,
		}, nil
	}

	groupKey := strings.TrimSpace(in.GroupKey)
	if groupKey == "" && in.GroupID > 0 {
		groupKey = strconv.FormatInt(in.GroupID, 10)
	}
	groupNick := strings.TrimSpace(in.GroupNickName)

	var record PersonInfo
	err := db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Exec("UPDATE person_info SET id=id WHERE person_id=?", pid).Error; err != nil {
				return err
			}
		}
		state, err := identity.LoadDatabase(ctx, tx, memory.InstanceRoot())
		if err != nil {
			return err
		}
		userID = state.ResolveUserID(platform, userID)
		pid = PersonID(platform, userID)
		findErr := lockPersonRow(tx, pid).Preload("Individual").First(&record).Error
		if findErr == nil {
			if record.Individual == nil {
				return fmt.Errorf("personinfo: 人物账号缺少 Individual 归属")
			}
			return updatePersonGroupCard(tx, &record, groupKey, groupNick)
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		groupRaw, err := encodeGroupCards(upsertGroupCard(nil, groupKey, groupNick))
		if err != nil {
			return err
		}
		now := time.Now()
		record = PersonInfo{
			IsKnown: true, PersonID: pid, PersonName: stringPtr(nickname),
			Platform: platform, UserID: userID, UserNickname: nickname,
			GroupCardname: jsonTextPtr(groupRaw), KnowCounts: 1,
			FirstKnownTime: &now, LastKnownTime: &now,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		return tx.Preload("Individual").Where("person_id = ?", pid).First(&record).Error
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func lockPersonRow(tx *gorm.DB, personID string) *gorm.DB {
	if tx.Dialector.Name() == "sqlite" {
		if result := tx.Exec("UPDATE person_info SET id = id WHERE person_id = ?", personID); result.Error != nil {
			return result
		}
		return tx.Where("person_id = ?", personID)
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("person_id = ?", personID)
}

func updatePersonGroupCard(tx *gorm.DB, rec *PersonInfo, groupID, card string) error {
	if groupID == "" || card == "" {
		return nil
	}
	raw, err := encodeGroupCards(upsertGroupCard(decodeGroupCards(rec.GroupCardname), groupID, card))
	if err != nil {
		return err
	}
	rec.GroupCardname = jsonTextPtr(raw)
	return tx.Model(&PersonInfo{}).Where("id = ?", rec.ID).Update("group_cardname", rec.GroupCardname).Error
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

// AddGroupNickname 添加或更新群名片
func (r *Registry) AddGroupNickname(ctx context.Context, personID, groupID, groupNickname string) error {
	groupID = strings.TrimSpace(groupID)
	groupNickname = strings.TrimSpace(groupNickname)
	if groupID == "" || groupNickname == "" {
		return nil
	}
	db := r.db(ctx)
	if db == nil {
		return errMemoryUnavailable
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var rec PersonInfo
		if err := lockPersonRow(tx, personID).First(&rec).Error; err != nil {
			return err
		}
		return updatePersonGroupCard(tx, &rec, groupID, groupNickname)
	})
}

// GetPersonIDByPersonName 返回唯一精确匹配的 person_id
func (r *Registry) GetPersonIDByPersonName(ctx context.Context, personName string) string {
	name := strings.TrimSpace(personName)
	if name == "" {
		return ""
	}
	db := r.db(ctx)
	if db == nil || !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return ""
	}
	var ids []string
	if err := db.Model(&PersonInfo{}).
		Joins("JOIN individuals ON individuals.individual_id = person_info.individual_id").
		Where("individuals.display_name = ?", name).Limit(2).Pluck("person_info.person_id", &ids).Error; err != nil {
		return ""
	}
	if len(ids) != 1 {
		return ""
	}
	return ids[0]
}

func (r *Registry) loadPersonByID(ctx context.Context, personID string) (*PersonInfo, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return nil, gorm.ErrRecordNotFound
	}
	state, err := r.IdentityState(ctx)
	if err != nil {
		return nil, err
	}
	personID = state.ResolvePersonID(personID)
	var rec PersonInfo
	if err := db.Preload("Individual").Where("person_id = ?", personID).First(&rec).Error; err != nil {
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
	return rec.Individual != nil && rec.Individual.IsKnown
}

// ResolvePersonID 对齐 resolve_person_id_for_memory：ID 优先，其次唯一精确 person_name
func (r *Registry) ResolvePersonID(ctx context.Context, platform, userID, personName string, strictKnown bool) string {
	rawPlatform := strings.TrimSpace(platform)
	platform = normalizePlatform(platform)
	userID = strings.TrimSpace(userID)
	if platform != "" && userID != "" {
		state, err := r.IdentityState(ctx)
		if err != nil {
			return ""
		}
		userID = state.ResolveUserID(rawPlatform, userID)
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

// UpdateProfileFields 更新账号资料与共享人物资料
func (r *Registry) UpdateProfileFields(ctx context.Context, personID string, upd ProfileUpdate) (*Profile, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errMemoryUnavailable
	}
	if !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return nil, errPersonNotFound
	}

	var rec PersonInfo
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := lockPersonRow(tx, personID).Preload("Individual").First(&rec).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errPersonNotFound
		}
		if err != nil {
			return err
		}
		if rec.Individual == nil {
			return fmt.Errorf("personinfo: 人物账号缺少 Individual 归属")
		}
		if upd.PersonName != nil || upd.IsKnown != nil {
			if upd.ExpectedIndividualID == nil || upd.ExpectedProfile == nil ||
				*upd.ExpectedIndividualID != rec.IndividualID ||
				*upd.ExpectedProfile != (IndividualProfile{DisplayName: rec.Individual.DisplayName, IsKnown: rec.Individual.IsKnown}) {
				return ErrIndividualChanged
			}
			updates := make(map[string]any, 2)
			if upd.PersonName != nil {
				name := strings.TrimSpace(*upd.PersonName)
				if name == "" || len([]rune(name)) > 255 {
					return fmt.Errorf("personinfo: 人物名称长度必须为 1～255")
				}
				updates["display_name"] = name
				rec.Individual.DisplayName = name
			}
			if upd.IsKnown != nil {
				updates["is_known"] = *upd.IsKnown
				rec.Individual.IsKnown = *upd.IsKnown
			}
			result := tx.Model(&Individual{}).Where("individual_id = ? AND display_name = ? AND is_known = ?",
				rec.IndividualID, upd.ExpectedProfile.DisplayName, upd.ExpectedProfile.IsKnown).Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrIndividualChanged
			}
		}
		personUpdates := make(map[string]any, 2)
		if upd.Nickname != nil {
			rec.UserNickname = strings.TrimSpace(*upd.Nickname)
			personUpdates["user_nickname"] = rec.UserNickname
		}
		if upd.GroupCard != nil {
			cards := upsertGroupCard(decodeGroupCards(rec.GroupCardname), upd.GroupCard.GroupID, upd.GroupCard.GroupCardname)
			raw, err := encodeGroupCards(cards)
			if err != nil {
				return err
			}
			rec.GroupCardname = jsonTextPtr(raw)
			personUpdates["group_cardname"] = raw
		}
		if len(personUpdates) == 0 {
			return nil
		}
		return tx.Model(&PersonInfo{}).Where("id = ?", rec.ID).Updates(personUpdates).Error
	})
	if err != nil {
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
