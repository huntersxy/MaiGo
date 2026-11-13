package native

// 聊天摘要身份适配，复用 A_Memorix 身份表并保留宿主消息回查边界

import (
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// summaryIdentityStoreAdapter 把身份表适配为摘要身份存储
type summaryIdentityStoreAdapter struct {
	store *storage.Store
}

// GetPersonIdentity 读取身份映射
func (a summaryIdentityStoreAdapter) GetPersonIdentity(personID string) map[string]any {
	identity, err := a.store.GetPersonIdentity(personID)
	if err != nil || identity == nil {
		return nil
	}
	return identity.ToMap()
}

// GetPersonProfileAliasOverride 读取人物称呼覆盖
func (a summaryIdentityStoreAdapter) GetPersonProfileAliasOverride(personID string) map[string]any {
	override, err := a.store.GetPersonProfileAliasOverride(personID)
	if err != nil {
		return nil
	}
	return override
}

// ObservePersonIdentity 登记身份
func (a summaryIdentityStoreAdapter) ObservePersonIdentity(input utils.IdentityObservation) (map[string]any, error) {
	return a.store.ObservePersonIdentity(input.PersonID, input.Platform, input.UserID,
		input.Aliases, input.GroupID, input.PreferredName)
}

// summaryIdentityHostAdapter 提供摘要所需的身份解析与消息渲染
type summaryIdentityHostAdapter struct {
	store           *storage.Store
	findMessages    func(platform string, sessionID string, messageID string, limit int) ([]utils.SessionMessage, error)
	botSelfIDs      map[string]map[string]struct{}
	botSelfResolver func(platform string, userID string) bool
}

// PersonID 按平台用户 ID 解析 person_id，未登记时回退平台用户 ID
func (a summaryIdentityHostAdapter) PersonID(platform, userID string) string {
	platform = normalizeSummaryPlatform(platform)
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return ""
	}
	rows, err := a.store.Query(
		"SELECT person_id FROM person_identities WHERE platform=? AND user_id=? LIMIT 1", platform, userID)
	if err == nil && len(rows) > 0 {
		if personID := strings.TrimSpace(utils.PyStr(rows[0]["person_id"])); personID != "" {
			return personID
		}
	}
	return userID
}

// PersonProfile 读取人物主档中的称呼
func (a summaryIdentityHostAdapter) PersonProfile(personID string) (utils.PersonProfileRow, bool) {
	identity, err := a.store.GetPersonIdentity(personID)
	if err != nil || identity == nil {
		return utils.PersonProfileRow{}, false
	}
	return utils.PersonProfileRow{PersonName: identity.CanonicalName, UserNickname: identity.CanonicalName}, true
}

// HistoricalNicknames 读取历史称呼
func (a summaryIdentityHostAdapter) HistoricalNicknames(platform, identityPlatform, userID string) ([]string, error) {
	platform = normalizeSummaryPlatform(platform)
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return nil, nil
	}
	rows, err := a.store.Query(
		`SELECT DISTINCT a.alias FROM person_identity_aliases a
		 JOIN person_identities i ON i.person_id = a.person_id
		 WHERE i.platform=? AND i.user_id=? ORDER BY a.alias`, platform, userID)
	if err != nil {
		return nil, err
	}
	aliases := make([]string, 0, len(rows))
	for _, row := range rows {
		if alias := strings.TrimSpace(utils.PyStr(row["alias"])); alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return aliases, nil
}

// FindMessages 回查被引用消息，消息库由宿主注入
func (a summaryIdentityHostAdapter) FindMessages(platform, sessionID, messageID string, limit int) ([]utils.SessionMessage, error) {
	if a.findMessages == nil {
		return nil, fmt.Errorf("摘要身份消息回查未接入: platform=%s session=%s message=%s", platform, sessionID, messageID)
	}
	return a.findMessages(platform, sessionID, messageID, limit)
}

// RenderWithoutReplyComponents 渲染非引用组件
func (a summaryIdentityHostAdapter) RenderWithoutReplyComponents(message utils.SessionMessage, components []utils.ChatMessageComponent) string {
	parts := make([]string, 0, len(components))
	for _, component := range components {
		switch component.Kind {
		case utils.ComponentText:
			parts = append(parts, component.Text)
		case utils.ComponentAt:
			parts = append(parts, "@"+component.TargetUserNickname)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// IsBotSelf 判断是否机器人自身，平台账号未登记时按未知平台处理
func (a summaryIdentityHostAdapter) IsBotSelf(platform, userID string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return false
	}
	// 实时解析器已合并显式配置、环境变量与账号服务，存在时以它为准
	// 静态表只作为没有解析器时的兜底，避免禁用后的旧快照继续命中
	if a.botSelfResolver != nil {
		return a.botSelfResolver(platform, userID)
	}
	accounts := a.botSelfIDs[platform]
	if len(accounts) == 0 {
		return false
	}
	_, ok := accounts[userID]
	return ok
}

// normalizeBotSelfIDs 归一化平台机器人账号表，空平台与空账号直接忽略
func normalizeBotSelfIDs(raw map[string][]string) map[string]map[string]struct{} {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{}, len(raw))
	for platform, accounts := range raw {
		platform = strings.ToLower(strings.TrimSpace(platform))
		if platform == "" {
			continue
		}
		for _, account := range accounts {
			account = strings.TrimSpace(account)
			if account == "" {
				continue
			}
			if out[platform] == nil {
				out[platform] = map[string]struct{}{}
			}
			out[platform][account] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeSummaryPlatform 取平台标识的末段，对齐 Python split("-", 1)[-1]
func normalizeSummaryPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if index := strings.Index(platform, "-"); index >= 0 {
		platform = platform[index+1:]
	}
	return platform
}
