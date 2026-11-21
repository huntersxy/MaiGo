package personinfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IndividualProfile struct {
	DisplayName string `json:"display_name"`
	IsKnown     bool   `json:"is_known"`
}

type IndividualProfileSource struct {
	IndividualID string           `json:"individual_id"`
	DisplayName  string           `json:"display_name"`
	IsKnown      bool             `json:"is_known"`
	Origins      []map[string]any `json:"origins"`
}

type IndividualMergePreview struct {
	TargetIndividualID string                    `json:"target_individual_id"`
	Revision           string                    `json:"revision"`
	Profile            IndividualProfile         `json:"profile"`
	Conflicts          []string                  `json:"conflicts"`
	Sources            []IndividualProfileSource `json:"sources"`
	Members            map[string]string         `json:"members"`
}

type IndividualMergeInput struct {
	TargetIndividualID string
	ExpectedMembers    map[string]string
	ExpectedRevision   string
	Profile            IndividualProfile
	ResolvedConflicts  []string
}

type IndividualRecord struct {
	Individual     Individual
	ProfileSources []map[string]any
	Members        []PersonInfo
}

type IndividualDirectoryFilter struct {
	Page       int
	PageSize   int
	View       string
	Search     string
	Platform   string
	IsKnown    *bool
	LinkedOnly bool
}

type IndividualDirectoryEntry struct {
	IndividualID   string
	DisplayName    string
	IsKnown        bool
	ProfileSources []map[string]any
	MemberCount    int
	Members        []PersonInfo
}

type IndividualDirectory struct {
	Data     []IndividualDirectoryEntry
	Total    int
	Page     int
	PageSize int
}

type IndividualMergeRequest struct {
	TargetIndividualID string   `json:"target_individual_id"`
	IndividualIDs      []string `json:"individual_ids"`
}

func (r *Registry) GetIndividual(ctx context.Context, individualID string) (*IndividualRecord, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, ErrMemoryUnavailable
	}
	var individual Individual
	if err := db.WithContext(ctx).Where("individual_id = ?", strings.TrimSpace(individualID)).First(&individual).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIndividualNotFound
		}
		return nil, err
	}
	var members []PersonInfo
	if err := db.WithContext(ctx).Preload("Individual").Where("individual_id = ?", individual.IndividualID).
		Order("person_info.id").Find(&members).Error; err != nil {
		return nil, err
	}
	sources, err := decodeIndividualSources(individual.ProfileSourcesJSON)
	if err != nil {
		return nil, err
	}
	return &IndividualRecord{Individual: individual, ProfileSources: sources, Members: members}, nil
}

func (r *Registry) PreviewIndividualMerge(ctx context.Context, request IndividualMergeRequest) (*IndividualMergePreview, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, ErrMemoryUnavailable
	}
	var preview *IndividualMergePreview
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		preview, err = previewIndividualMerge(ctx, tx, request)
		return err
	})
	return preview, err
}

func previewIndividualMerge(ctx context.Context, db *gorm.DB, request IndividualMergeRequest) (*IndividualMergePreview, error) {
	targetID := strings.TrimSpace(request.TargetIndividualID)
	ids := make(map[string]struct{}, len(request.IndividualIDs))
	for _, value := range request.IndividualIDs {
		id := strings.TrimSpace(value)
		if id != "" {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 || len(ids) > 100 || targetID == "" {
		return nil, &IndividualInputError{Message: "请选择 1～100 个人物并指定合并目标"}
	}
	if _, ok := ids[targetID]; !ok {
		return nil, &IndividualInputError{Message: "合并目标必须属于所选人物"}
	}
	orderedIDs := make([]string, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	var individuals []Individual
	if err := db.WithContext(ctx).Where("individual_id IN ?", orderedIDs).Order("individual_id").Find(&individuals).Error; err != nil {
		return nil, err
	}
	if len(individuals) != len(ids) {
		return nil, ErrIndividualNotFound
	}
	var people []PersonInfo
	if err := db.WithContext(ctx).Where("individual_id IN ?", orderedIDs).
		Order("person_info.person_id").Find(&people).Error; err != nil {
		return nil, err
	}
	members := make(map[string]string, len(people))
	memberRevisions := make([]map[string]any, 0, len(people))
	seenIndividuals := make(map[string]bool, len(ids))
	for _, person := range people {
		members[person.PersonID] = person.IndividualID
		seenIndividuals[person.IndividualID] = true
		memberRevisions = append(memberRevisions, map[string]any{
			"person_id":      person.PersonID,
			"individual_id":  person.IndividualID,
			"platform":       person.Platform,
			"user_id":        person.UserID,
			"nickname":       person.UserNickname,
			"group_cardname": nullableJSONText(person.GroupCardname),
		})
	}
	if len(seenIndividuals) != len(ids) {
		return nil, &IndividualInputError{Message: "不能合并没有账号的人物"}
	}
	profiles := make([]IndividualProfileSource, 0, len(individuals))
	profileRevision := make([]map[string]any, 0, len(individuals))
	var finalProfile IndividualProfile
	displayNames := make(map[string]bool)
	knownValues := make(map[bool]bool)
	for _, individual := range individuals {
		origins, err := decodeIndividualSources(individual.ProfileSourcesJSON)
		if err != nil {
			return nil, err
		}
		profile := IndividualProfileSource{
			IndividualID: individual.IndividualID,
			DisplayName:  individual.DisplayName,
			IsKnown:      individual.IsKnown,
			Origins:      origins,
		}
		profiles = append(profiles, profile)
		profileRevision = append(profileRevision, map[string]any{
			"individual_id": profile.IndividualID,
			"display_name":  profile.DisplayName,
			"is_known":      profile.IsKnown,
			"origins":       profile.Origins,
		})
		if individual.IndividualID == targetID {
			finalProfile = IndividualProfile{DisplayName: individual.DisplayName, IsKnown: individual.IsKnown}
		}
		displayNames[individual.DisplayName] = true
		knownValues[individual.IsKnown] = true
	}
	conflicts := make([]string, 0, 2)
	if len(displayNames) > 1 {
		conflicts = append(conflicts, "display_name")
	}
	if len(knownValues) > 1 {
		conflicts = append(conflicts, "is_known")
	}
	sort.Strings(conflicts)
	revisionInput := map[string]any{
		"target":  targetID,
		"sources": profileRevision,
		"members": memberRevisions,
	}
	encoded, err := json.Marshal(revisionInput)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return &IndividualMergePreview{
		TargetIndividualID: targetID,
		Revision:           hex.EncodeToString(digest[:]),
		Profile:            finalProfile,
		Conflicts:          conflicts,
		Sources:            profiles,
		Members:            members,
	}, nil
}

func (r *Registry) MergeIndividuals(ctx context.Context, input IndividualMergeInput) (*IndividualRecord, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, ErrMemoryUnavailable
	}
	var mergedID string
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		request := IndividualMergeRequest{TargetIndividualID: input.TargetIndividualID}
		sourceSet := make(map[string]bool)
		for _, id := range input.ExpectedMembers {
			id = strings.TrimSpace(id)
			if id != "" && !sourceSet[id] {
				sourceSet[id] = true
				request.IndividualIDs = append(request.IndividualIDs, id)
			}
		}
		if len(request.IndividualIDs) < 2 {
			return &IndividualInputError{Message: "请选择至少两个不同的人物"}
		}
		if err := lockIndividualMergeRows(tx, request.IndividualIDs); err != nil {
			return err
		}
		preview, err := previewIndividualMerge(ctx, tx, request)
		if err != nil {
			if errors.Is(err, ErrIndividualNotFound) {
				return ErrIndividualChanged
			}
			return err
		}
		if preview.Revision != input.ExpectedRevision || !sameIndividualMembers(preview.Members, input.ExpectedMembers) {
			return ErrIndividualChanged
		}
		profile := input.Profile
		profile.DisplayName = strings.TrimSpace(profile.DisplayName)
		if profile.DisplayName == "" || len([]rune(profile.DisplayName)) > 255 {
			return &IndividualInputError{Message: "人物名称长度必须为 1～255"}
		}
		resolved := make(map[string]bool, len(input.ResolvedConflicts))
		for _, field := range input.ResolvedConflicts {
			switch field {
			case "display_name", "is_known":
				resolved[field] = true
			default:
				return &IndividualInputError{Message: fmt.Sprintf("不支持的冲突字段: %s", field)}
			}
		}
		for _, conflict := range preview.Conflicts {
			if !resolved[conflict] {
				return &IndividualInputError{Message: fmt.Sprintf("请先处理全部资料冲突：%s", conflict)}
			}
		}
		origins := make([]map[string]any, 0)
		seenOrigins := make(map[string]bool)
		for _, source := range preview.Sources {
			for _, origin := range source.Origins {
				encoded, err := json.Marshal(origin)
				if err != nil {
					return err
				}
				key := string(encoded)
				if !seenOrigins[key] {
					seenOrigins[key] = true
					origins = append(origins, origin)
				}
			}
		}
		originJSON, err := json.Marshal(origins)
		if err != nil {
			return err
		}
		beforeJSON, err := json.Marshal(map[string]any{
			"before":             preview.Sources,
			"after":              profile,
			"resolved_conflicts": input.ResolvedConflicts,
			"revision":           preview.Revision,
		})
		if err != nil {
			return err
		}
		membersJSON, err := json.Marshal(preview.Members)
		if err != nil {
			return err
		}
		var target Individual
		if err := tx.Where("individual_id = ?", preview.TargetIndividualID).First(&target).Error; err != nil {
			return err
		}
		if err := tx.Model(&Individual{}).Where("individual_id = ?", target.IndividualID).Updates(map[string]any{
			"display_name": profile.DisplayName, "is_known": profile.IsKnown, "profile_sources_json": string(originJSON),
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&IndividualMergeEvent{
			TargetIndividualID: target.IndividualID,
			MembersJSON:        string(membersJSON),
			ProfilesJSON:       string(beforeJSON),
			CreatedAt:          time.Now(),
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&PersonInfo{}).Where("individual_id IN ?", request.IndividualIDs).
			Update("individual_id", target.IndividualID).Error; err != nil {
			return err
		}
		if err := tx.Where("individual_id IN ? AND individual_id <> ?", request.IndividualIDs, target.IndividualID).
			Delete(&Individual{}).Error; err != nil {
			return err
		}
		mergedID = target.IndividualID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.GetIndividual(ctx, mergedID)
}

func (r *Registry) ListIndividualDirectory(ctx context.Context, filter IndividualDirectoryFilter) (*IndividualDirectory, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, ErrMemoryUnavailable
	}
	page, size := normalizePersonPage(filter.Page, filter.PageSize)
	view := strings.TrimSpace(filter.View)
	if view == "" {
		view = "individuals"
	}
	if view != "individuals" && view != "persons" {
		return nil, &IndividualInputError{Message: "view 必须为 individuals 或 persons"}
	}
	base := db.WithContext(ctx).Model(&PersonInfo{}).
		Joins("JOIN individuals ON individuals.individual_id = person_info.individual_id")
	if search := strings.TrimSpace(filter.Search); search != "" {
		pattern := "%" + search + "%"
		base = base.Where(`individuals.display_name LIKE ? OR individuals.individual_id LIKE ?
			OR person_info.user_nickname LIKE ? OR person_info.user_id LIKE ? OR person_info.person_id LIKE ?`,
			pattern, pattern, pattern, pattern, pattern)
	}
	if platform := strings.TrimSpace(filter.Platform); platform != "" {
		base = base.Where("person_info.platform = ?", platform)
	}
	if filter.IsKnown != nil {
		base = base.Where("individuals.is_known = ?", *filter.IsKnown)
	}
	if filter.LinkedOnly {
		linked := db.Model(&PersonInfo{}).Select("individual_id").Group("individual_id").Having("COUNT(*) > 1")
		base = base.Where("person_info.individual_id IN (?)", linked)
	}
	result := &IndividualDirectory{Page: page, PageSize: size, Data: []IndividualDirectoryEntry{}}
	if view == "persons" {
		var total int64
		if err := base.Count(&total).Error; err != nil {
			return nil, err
		}
		result.Total = int(total)
		var people []PersonInfo
		if err := base.Preload("Individual").Order("person_info.last_known_time DESC NULLS LAST, person_info.id").
			Offset((page - 1) * size).Limit(size).Find(&people).Error; err != nil {
			return nil, err
		}
		for _, person := range people {
			var count int64
			if err := db.WithContext(ctx).Model(&PersonInfo{}).Where("individual_id = ?", person.IndividualID).Count(&count).Error; err != nil {
				return nil, err
			}
			sources, err := decodeIndividualSources(person.Individual.ProfileSourcesJSON)
			if err != nil {
				return nil, err
			}
			result.Data = append(result.Data, IndividualDirectoryEntry{
				IndividualID: person.IndividualID, DisplayName: person.Individual.DisplayName,
				IsKnown: person.Individual.IsKnown, ProfileSources: sources,
				MemberCount: int(count), Members: []PersonInfo{person},
			})
		}
		return result, nil
	}
	var total int64
	if err := db.WithContext(ctx).Model(&Individual{}).
		Where("individual_id IN (?)", base.Select("person_info.individual_id")).Count(&total).Error; err != nil {
		return nil, err
	}
	result.Total = int(total)
	var groups []individualDirectoryGroupRow
	if err := db.WithContext(ctx).Model(&Individual{}).
		Select("individuals.individual_id, individuals.display_name, individuals.is_known, individuals.profile_sources_json, COUNT(person_info.id) AS member_count, MAX(person_info.last_known_time) AS last_active").
		Joins("JOIN person_info ON person_info.individual_id = individuals.individual_id").
		Where("individuals.individual_id IN (?)", base.Select("person_info.individual_id")).
		Group("individuals.individual_id").Order("last_active DESC NULLS LAST, individuals.individual_id").
		Offset((page - 1) * size).Limit(size).Scan(&groups).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.IndividualID)
	}
	var members []PersonInfo
	if len(ids) > 0 {
		if err := db.WithContext(ctx).Preload("Individual").Where("individual_id IN ?", ids).
			Order("person_info.platform, person_info.id").Find(&members).Error; err != nil {
			return nil, err
		}
	}
	byIndividual := make(map[string][]PersonInfo, len(groups))
	for _, member := range members {
		byIndividual[member.IndividualID] = append(byIndividual[member.IndividualID], member)
	}
	for _, group := range groups {
		sources, err := decodeIndividualSources(group.ProfileSourcesJSON)
		if err != nil {
			return nil, err
		}
		result.Data = append(result.Data, IndividualDirectoryEntry{
			IndividualID: group.IndividualID, DisplayName: group.DisplayName, IsKnown: group.IsKnown,
			ProfileSources: sources, MemberCount: group.MemberCount, Members: byIndividual[group.IndividualID],
		})
	}
	return result, nil
}

type individualDirectoryGroupRow struct {
	IndividualID       string     `gorm:"column:individual_id"`
	DisplayName        string     `gorm:"column:display_name"`
	IsKnown            bool       `gorm:"column:is_known"`
	ProfileSourcesJSON string     `gorm:"column:profile_sources_json"`
	MemberCount        int        `gorm:"column:member_count"`
	LastActive         *time.Time `gorm:"column:last_active"`
}

func decodeIndividualSources(raw string) ([]map[string]any, error) {
	var sources []map[string]any
	if err := json.Unmarshal([]byte(raw), &sources); err != nil || sources == nil {
		if err == nil {
			err = fmt.Errorf("资料来源必须为 JSON 数组")
		}
		return nil, fmt.Errorf("personinfo: 个体来源资料无效: %w", err)
	}
	return sources, nil
}

func nullableJSONText(raw *string) any {
	if raw == nil {
		return nil
	}
	return *raw
}

func sameIndividualMembers(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for personID, individualID := range left {
		if right[personID] != individualID {
			return false
		}
	}
	return true
}

func lockIndividualMergeRows(tx *gorm.DB, ids []string) error {
	if tx.Dialector.Name() == "sqlite" {
		return tx.Exec("UPDATE individuals SET display_name = display_name WHERE individual_id IN ?", ids).Error
	}
	var rows []Individual
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("individual_id IN ?", ids).Find(&rows).Error
}
