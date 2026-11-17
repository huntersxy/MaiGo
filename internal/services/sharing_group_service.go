package services

// 相通组唯一数据来源，对齐 MaiBot src/services/sharing_group_service.py。
// 运行时读取快照，只展开当前会话直接所属的组。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	chatsession "MaiGo/internal/chat/session"
	memory "MaiGo/internal/common/database"
	maibotcfg "MaiGo/internal/config/maibot"
)

// SharingCapabilities 共享能力与展示名
var SharingCapabilities = map[string]string{"expression": "表达", "jargon": "黑话", "memory": "记忆"}

var sharingColors = []string{"#39845A", "#397FAE", "#B97132", "#9863A5", "#B45C6E", "#438E91"}

var sharingColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

var (
	sharingGroupsMu sync.RWMutex
	sharingSnapshot []SharingGroupSnapshot
	sharingDB       *gorm.DB
)

// ErrSharingGroupNotFound 相通组不存在，对齐 Python KeyError
var ErrSharingGroupNotFound = errors.New("相通组不存在")

// ErrSharingGroupConflict 更新冲突或输入无效
var ErrSharingGroupConflict = errors.New("相通组已被修改，请刷新后重试")

// SharingMemberRule 按平台与目标匹配成员
type SharingMemberRule struct {
	Platform string `json:"platform"`
	ItemID   string `json:"item_id"`
	RuleType string `json:"rule_type"`
}

// SharingGroupInput 管理端提交的完整组定义
type SharingGroupInput struct {
	Name         string              `json:"name"`
	Color        string              `json:"color"`
	Note         string              `json:"note"`
	Enabled      bool                `json:"enabled"`
	Revision     *int                `json:"revision"`
	MemberIDs    []string            `json:"member_ids"`
	Rules        []SharingMemberRule `json:"rules"`
	Capabilities map[string]bool     `json:"capabilities"`
}

// SharingGroupSnapshot 运行时组快照
type SharingGroupSnapshot struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Color        string              `json:"color"`
	Note         string              `json:"note"`
	Enabled      bool                `json:"enabled"`
	Revision     int                 `json:"revision"`
	CreatedAt    float64             `json:"created_at"`
	UpdatedAt    float64             `json:"updated_at"`
	MemberIDs    []string            `json:"member_ids"`
	Rules        []SharingMemberRule `json:"rules"`
	Capabilities map[string]bool     `json:"capabilities"`
	ResolvedIDs  []string            `json:"resolved_ids"`
}

// InitializeSharingGroups 绑定数据库并执行旧配置的一次性导入；已初始化时直接返回
func InitializeSharingGroups(ctx context.Context, db *gorm.DB, bot *maibotcfg.BotConfigFile) error {
	if db == nil {
		return errors.New("相通组数据库未接线")
	}
	sharingGroupsMu.Lock()
	defer sharingGroupsMu.Unlock()
	if sharingSnapshot != nil {
		return nil
	}
	sharingDB = db
	if err := importLegacySharingGroups(ctx, db, bot); err != nil {
		return err
	}
	snapshot, err := loadSharingSnapshotLocked(ctx, db)
	if err != nil {
		return err
	}
	sharingSnapshot = snapshot
	return nil
}

// RefreshSharingGroups 外部事务更新成员关系后重新加载共享范围
func RefreshSharingGroups(ctx context.Context) error {
	sharingGroupsMu.Lock()
	defer sharingGroupsMu.Unlock()
	if sharingDB == nil {
		return errors.New("相通组服务尚未初始化")
	}
	snapshot, err := loadSharingSnapshotLocked(ctx, sharingDB)
	if err != nil {
		return err
	}
	sharingSnapshot = snapshot
	return nil
}

// ListSharingGroups 返回快照副本，管理端可安全修改
func ListSharingGroups(ctx context.Context) []SharingGroupSnapshot {
	sharingGroupsMu.RLock()
	defer sharingGroupsMu.RUnlock()
	if sharingSnapshot == nil {
		return []SharingGroupSnapshot{}
	}
	return cloneSharingSnapshot(sharingSnapshot)
}

// SharingResolvedMembers 快照内的成员加上按规则命中的全部聊天流
func SharingResolvedMembers(ctx context.Context, group SharingGroupSnapshot) map[string]struct{} {
	ids := make(map[string]struct{}, len(group.ResolvedIDs))
	for _, id := range group.ResolvedIDs {
		ids[id] = struct{}{}
	}
	if sharingDB == nil {
		return ids
	}
	for _, session := range sharingChatSessions(ctx, sharingDB) {
		target := sharingSessionTarget(session)
		kind := "group"
		if !session.IsGroup() {
			kind = "private"
		}
		for _, rule := range group.Rules {
			if rule.RuleType != "all" && rule.RuleType != kind {
				continue
			}
			if (rule.Platform == "*" || rule.Platform == session.Platform) && (rule.ItemID == "*" || rule.ItemID == target) {
				ids[session.SessionID] = struct{}{}
			}
		}
	}
	return ids
}

// ResolveSharingScope 返回会话可共享的聊天流范围与是否全平台分享
func ResolveSharingScope(sessionID string, capability string) (map[string]struct{}, bool, error) {
	result := make(map[string]struct{})
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return result, false, nil
	}
	sharingGroupsMu.RLock()
	snapshot := sharingSnapshot
	sharingGroupsMu.RUnlock()
	if snapshot == nil {
		return result, false, errors.New("相通组服务尚未初始化")
	}
	result[sessionID] = struct{}{}
	globalScope := false
	ctx := context.Background()
	for _, group := range snapshot {
		if !group.Enabled || !group.Capabilities[capability] {
			continue
		}
		allChats := SharingGroupIsGlobal(group)
		members := SharingResolvedMembers(ctx, group)
		if _, ok := members[sessionID]; ok || allChats {
			for id := range members {
				result[id] = struct{}{}
			}
			globalScope = globalScope || allChats
		}
	}
	return result, globalScope, nil
}

// SharingGroupIsGlobal 判断组是否命中全平台全目标通配
func SharingGroupIsGlobal(group SharingGroupSnapshot) bool {
	for _, rule := range group.Rules {
		if rule.Platform == "*" && rule.ItemID == "*" && rule.RuleType == "all" {
			return true
		}
	}
	return false
}

// SaveSharingGroup 新建或按 revision 乐观更新组
func SaveSharingGroup(ctx context.Context, input SharingGroupInput, groupID *string) (SharingGroupSnapshot, error) {
	input.Name = strings.TrimSpace(input.Name)
	if err := validateSharingGroupInput(input); err != nil {
		return SharingGroupSnapshot{}, err
	}
	sharingGroupsMu.Lock()
	defer sharingGroupsMu.Unlock()
	if sharingDB == nil {
		return SharingGroupSnapshot{}, errors.New("相通组服务尚未初始化")
	}
	var current *SharingGroupSnapshot
	if groupID != nil {
		for index := range sharingSnapshot {
			if sharingSnapshot[index].ID == *groupID {
				copied := sharingSnapshot[index]
				current = &copied
				break
			}
		}
		if current == nil {
			return SharingGroupSnapshot{}, ErrSharingGroupNotFound
		}
	}
	existingIDs := make(map[string]struct{})
	if current != nil {
		for _, id := range current.MemberIDs {
			existingIDs[id] = struct{}{}
		}
	}
	sessionSet := make(map[string]struct{})
	for _, session := range sharingChatSessions(ctx, sharingDB) {
		sessionSet[session.SessionID] = struct{}{}
	}
	for _, id := range input.MemberIDs {
		if _, existed := existingIDs[id]; existed {
			continue
		}
		if _, ok := sessionSet[id]; !ok {
			return SharingGroupSnapshot{}, fmt.Errorf("聊天流不存在：%s", id)
		}
	}
	now := float64(time.Now().UnixNano()) / 1e9
	err := sharingDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if groupID == nil {
			id := uuid.NewString()
			groupID = &id
			if err := tx.Create(&memory.SharingGroup{
				ID: id, Name: input.Name, Color: input.Color, Note: input.Note,
				Enabled: input.Enabled, Revision: 1, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				return err
			}
		} else {
			revision := 0
			if input.Revision != nil {
				revision = *input.Revision
			}
			result := tx.Model(&memory.SharingGroup{}).
				Where("id = ? AND revision = ?", *groupID, revision).
				Updates(map[string]any{
					"name": input.Name, "color": input.Color, "note": input.Note,
					"enabled": input.Enabled, "updated_at": now, "revision": gorm.Expr("revision + 1"),
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrSharingGroupConflict
			}
		}
		if err := tx.Where("group_id = ?", *groupID).Delete(&memory.SharingGroupMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", *groupID).Delete(&memory.SharingGroupRule{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", *groupID).Delete(&memory.SharingGroupCapability{}).Error; err != nil {
			return err
		}
		for _, sessionID := range input.MemberIDs {
			if err := tx.Create(&memory.SharingGroupMember{GroupID: *groupID, SessionID: sessionID, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		for _, rule := range input.Rules {
			if err := tx.Create(&memory.SharingGroupRule{
				ID: uuid.NewString(), GroupID: *groupID,
				Platform: rule.Platform, ItemID: rule.ItemID, RuleType: rule.RuleType,
			}).Error; err != nil {
				return err
			}
		}
		for capability, enabled := range input.Capabilities {
			if err := tx.Create(&memory.SharingGroupCapability{
				GroupID: *groupID, Capability: capability, Enabled: enabled,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SharingGroupSnapshot{}, err
	}
	snapshot, err := loadSharingSnapshotLocked(ctx, sharingDB)
	if err != nil {
		return SharingGroupSnapshot{}, err
	}
	sharingSnapshot = snapshot
	for _, group := range snapshot {
		if group.ID == *groupID {
			return group, nil
		}
	}
	return SharingGroupSnapshot{}, ErrSharingGroupNotFound
}

// DeleteSharingGroup 按 revision 删除组及其成员、规则与能力
func DeleteSharingGroup(ctx context.Context, groupID string, revision int) error {
	sharingGroupsMu.Lock()
	defer sharingGroupsMu.Unlock()
	if sharingDB == nil {
		return errors.New("相通组服务尚未初始化")
	}
	err := sharingDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group memory.SharingGroup
		if err := tx.Where("id = ?", groupID).Take(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSharingGroupNotFound
			}
			return err
		}
		if group.Revision != revision {
			return ErrSharingGroupConflict
		}
		for _, model := range []any{&memory.SharingGroupMember{}, &memory.SharingGroupRule{}, &memory.SharingGroupCapability{}} {
			if err := tx.Where("group_id = ?", groupID).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Where("id = ?", groupID).Delete(&memory.SharingGroup{}).Error
	})
	if err != nil {
		return err
	}
	snapshot, err := loadSharingSnapshotLocked(ctx, sharingDB)
	if err != nil {
		return err
	}
	sharingSnapshot = snapshot
	return nil
}

func validateSharingGroupInput(input SharingGroupInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return errors.New("相通组名称不能为空")
	}
	if len([]rune(name)) > 100 {
		return errors.New("相通组名称不能超过 100 个字符")
	}
	if !sharingColorPattern.MatchString(input.Color) {
		return errors.New("相通组颜色必须是 #RRGGBB 格式")
	}
	if len([]rune(input.Note)) > 4000 {
		return errors.New("相通组备注不能超过 4000 个字符")
	}
	if input.Revision != nil && *input.Revision < 1 {
		return errors.New("相通组版本号必须大于等于 1")
	}
	for capability := range input.Capabilities {
		if _, ok := SharingCapabilities[capability]; !ok {
			return errors.New("未知的共享能力")
		}
	}
	seenMembers := make(map[string]struct{}, len(input.MemberIDs))
	for _, id := range input.MemberIDs {
		if _, exists := seenMembers[id]; exists {
			return fmt.Errorf("成员不能重复：%s", id)
		}
		seenMembers[id] = struct{}{}
	}
	seenRules := make(map[string]struct{}, len(input.Rules))
	for _, rule := range input.Rules {
		if rule.Platform == "" || len([]rune(rule.Platform)) > 100 {
			return errors.New("成员规则平台不能为空且不能超过 100 个字符")
		}
		if rule.ItemID == "" || len([]rune(rule.ItemID)) > 255 {
			return errors.New("成员规则目标不能为空且不能超过 255 个字符")
		}
		if rule.RuleType != "group" && rule.RuleType != "private" && rule.RuleType != "all" {
			return fmt.Errorf("成员规则类型无效：%s（可选 group、private、all）", rule.RuleType)
		}
		key := rule.Platform + "\x00" + rule.ItemID + "\x00" + rule.RuleType
		if _, exists := seenRules[key]; exists {
			return errors.New("成员规则不能重复")
		}
		seenRules[key] = struct{}{}
	}
	return nil
}

func loadSharingSnapshotLocked(ctx context.Context, db *gorm.DB) ([]SharingGroupSnapshot, error) {
	var groups []memory.SharingGroup
	if err := db.WithContext(ctx).Order("created_at, id").Find(&groups).Error; err != nil {
		return nil, err
	}
	var members []memory.SharingGroupMember
	if err := db.WithContext(ctx).Find(&members).Error; err != nil {
		return nil, err
	}
	var rules []memory.SharingGroupRule
	if err := db.WithContext(ctx).Find(&rules).Error; err != nil {
		return nil, err
	}
	var capabilities []memory.SharingGroupCapability
	if err := db.WithContext(ctx).Find(&capabilities).Error; err != nil {
		return nil, err
	}
	sessions := sharingChatSessions(ctx, db)
	snapshot := make([]SharingGroupSnapshot, 0, len(groups))
	for _, group := range groups {
		item := SharingGroupSnapshot{
			ID: group.ID, Name: group.Name, Color: group.Color, Note: group.Note,
			Enabled: group.Enabled, Revision: group.Revision,
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
			MemberIDs: []string{}, Rules: []SharingMemberRule{}, Capabilities: map[string]bool{},
		}
		for _, member := range members {
			if member.GroupID == group.ID {
				item.MemberIDs = append(item.MemberIDs, member.SessionID)
			}
		}
		for _, rule := range rules {
			if rule.GroupID == group.ID {
				item.Rules = append(item.Rules, SharingMemberRule{
					Platform: rule.Platform, ItemID: rule.ItemID, RuleType: rule.RuleType,
				})
			}
		}
		for _, capability := range capabilities {
			if capability.GroupID == group.ID {
				item.Capabilities[capability.Capability] = capability.Enabled
			}
		}
		resolved := make(map[string]struct{}, len(item.MemberIDs))
		for _, id := range item.MemberIDs {
			resolved[id] = struct{}{}
		}
		for _, session := range sessions {
			target := sharingSessionTarget(session)
			kind := "group"
			if !session.IsGroup() {
				kind = "private"
			}
			for _, rule := range item.Rules {
				if rule.RuleType != "all" && rule.RuleType != kind {
					continue
				}
				if (rule.Platform == "*" || rule.Platform == session.Platform) && (rule.ItemID == "*" || rule.ItemID == target) {
					resolved[session.SessionID] = struct{}{}
				}
			}
		}
		item.ResolvedIDs = make([]string, 0, len(resolved))
		for id := range resolved {
			item.ResolvedIDs = append(item.ResolvedIDs, id)
		}
		sort.Strings(item.ResolvedIDs)
		snapshot = append(snapshot, item)
	}
	return snapshot, nil
}

func sharingChatSessions(ctx context.Context, db *gorm.DB) []chatsession.Record {
	var records []chatsession.Record
	if db == nil {
		return nil
	}
	if err := db.WithContext(ctx).Find(&records).Error; err != nil {
		return nil
	}
	return records
}

func sharingSessionTarget(session chatsession.Record) string {
	if session.IsGroup() {
		if session.GroupID == nil {
			return ""
		}
		return *session.GroupID
	}
	if session.UserID == nil {
		return ""
	}
	return *session.UserID
}

func cloneSharingSnapshot(source []SharingGroupSnapshot) []SharingGroupSnapshot {
	cloned := make([]SharingGroupSnapshot, len(source))
	for index, group := range source {
		cloned[index] = group
		// 管理端按数组遍历；空集合必须编码为 []，不能是 null
		cloned[index].MemberIDs = append([]string{}, group.MemberIDs...)
		cloned[index].ResolvedIDs = append([]string{}, group.ResolvedIDs...)
		cloned[index].Rules = append([]SharingMemberRule{}, group.Rules...)
		cloned[index].Capabilities = make(map[string]bool, len(group.Capabilities))
		for key, value := range group.Capabilities {
			cloned[index].Capabilities[key] = value
		}
	}
	return cloned
}

func importLegacySharingGroups(ctx context.Context, db *gorm.DB, bot *maibotcfg.BotConfigFile) error {
	var marker memory.SharingGroupMigration
	err := db.WithContext(ctx).Where("source = ?", "config-v1-complete").Take(&marker).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	sources := []struct {
		capability string
		groups     []maibotcfg.ChatStreamGroup
	}{}
	if bot != nil {
		sources = append(sources,
			struct {
				capability string
				groups     []maibotcfg.ChatStreamGroup
			}{"expression", bot.Expression.ExpressionGroups},
			struct {
				capability string
				groups     []maibotcfg.ChatStreamGroup
			}{"jargon", bot.Jargon.JargonGroups},
			struct {
				capability string
				groups     []maibotcfg.ChatStreamGroup
			}{"memory", bot.AMemorix.SharedMemoryGroups},
		)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		serial := 0
		for _, source := range sources {
			for index, oldGroup := range source.groups {
				groupID := uuid.NewString()
				now := float64(time.Now().UnixNano()) / 1e9
				if err := tx.Create(&memory.SharingGroup{
					ID:      groupID,
					Name:    fmt.Sprintf("%s相通 %d", SharingCapabilities[source.capability], index+1),
					Color:   sharingColors[serial%len(sharingColors)],
					Enabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
				}).Error; err != nil {
					return err
				}
				if err := tx.Create(&memory.SharingGroupCapability{
					GroupID: groupID, Capability: source.capability, Enabled: true,
				}).Error; err != nil {
					return err
				}
				seen := make(map[string]struct{})
				for _, target := range oldGroup.Targets {
					platform := strings.TrimSpace(target.Platform)
					itemID := strings.TrimSpace(target.ItemId)
					if platform == "" || itemID == "" {
						continue
					}
					ruleType := strings.TrimSpace(target.RuleType)
					if ruleType == "" {
						ruleType = "group"
					}
					if source.capability != "memory" && platform == "*" && itemID == "*" {
						ruleType = "all"
					}
					key := platform + "\x00" + itemID + "\x00" + ruleType
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}
					if err := tx.Create(&memory.SharingGroupRule{
						ID: uuid.NewString(), GroupID: groupID,
						Platform: platform, ItemID: itemID, RuleType: ruleType,
					}).Error; err != nil {
						return err
					}
				}
				payload, _ := json.Marshal(oldGroup)
				if err := tx.Create(&memory.SharingGroupMigration{
					Source: fmt.Sprintf("%s:%d", source.capability, index), GroupID: &groupID,
					Payload: string(payload), MigratedAt: now,
				}).Error; err != nil {
					return err
				}
				serial++
			}
		}
		if bot != nil && bot.AMemorix.GlobalMemorySharingEnabled {
			now := float64(time.Now().UnixNano()) / 1e9
			groupID := uuid.NewString()
			if err := tx.Create(&memory.SharingGroup{
				ID: groupID, Name: "全部聊天的记忆", Color: sharingColors[serial%len(sharingColors)],
				Enabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Create(&memory.SharingGroupCapability{GroupID: groupID, Capability: "memory", Enabled: true}).Error; err != nil {
				return err
			}
			if err := tx.Create(&memory.SharingGroupRule{
				ID: uuid.NewString(), GroupID: groupID, Platform: "*", ItemID: "*", RuleType: "all",
			}).Error; err != nil {
				return err
			}
			if err := tx.Create(&memory.SharingGroupMigration{
				Source: "memory:global", GroupID: &groupID, Payload: "true", MigratedAt: now,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&memory.SharingGroupMigration{
			Source: "config-v1-complete", Payload: "{}", MigratedAt: float64(time.Now().UnixNano()) / 1e9,
		}).Error
	})
}
