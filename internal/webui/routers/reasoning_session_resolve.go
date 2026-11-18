package webuiapi

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gorm.io/gorm"

	chatsession "MaiGo/internal/chat/session"
	memory "MaiGo/internal/common/database"
	"MaiGo/internal/maisaka/display"
)

// 对齐 MaiBot reasoning_process.py 的 session_infos：目录名解析 + chat_sessions 回填展示名/resolved_session_id。

type reasoningSessionTargetKey struct {
	Platform string
	ChatType string
	TargetID string
}

func fallbackReasoningSessionDisplayName(name, platform, chatType, targetID string) string {
	if platform == "" || chatType == "" || targetID == "" {
		return name
	}
	label := "私聊"
	if chatType == "group" {
		label = "群聊"
	}
	return platform + " " + label + " " + targetID
}

func reasoningSessionSortKey(rec chatsession.Record) float64 {
	if rec.LastActiveTimestamp != nil && !rec.LastActiveTimestamp.IsZero() {
		return float64(rec.LastActiveTimestamp.UnixNano()) / 1e9
	}
	if !rec.CreatedTimestamp.IsZero() {
		return float64(rec.CreatedTimestamp.UnixNano()) / 1e9
	}
	return 0
}

func selectCurrentAccountReasoningSession(
	sessions []chatsession.Record,
	configuredAccounts map[[2]string]struct{},
) (chatsession.Record, bool, bool) {
	configuredMatches := make([]chatsession.Record, 0, len(sessions))
	for _, session := range sessions {
		platform := strings.TrimSpace(session.Platform)
		accountID := ""
		if session.AccountID != nil {
			accountID = strings.TrimSpace(*session.AccountID)
		}
		if platform == "" || accountID == "" {
			continue
		}
		if _, ok := configuredAccounts[[2]string{strings.ToLower(platform), accountID}]; ok {
			configuredMatches = append(configuredMatches, session)
		}
	}
	if len(configuredMatches) > 0 {
		best := configuredMatches[0]
		for _, session := range configuredMatches[1:] {
			if reasoningSessionSortKey(session) > reasoningSessionSortKey(best) {
				best = session
			}
		}
		return best, true, true
	}

	legacySessions := make([]chatsession.Record, 0, len(sessions))
	for _, session := range sessions {
		if session.AccountID == nil || strings.TrimSpace(*session.AccountID) == "" {
			legacySessions = append(legacySessions, session)
		}
	}
	if len(sessions) == 1 {
		return sessions[0], false, true
	}
	if len(legacySessions) == 1 {
		return legacySessions[0], false, true
	}
	var zero chatsession.Record
	return zero, false, false
}

func reasoningChatNameFromSessionRecord(rec chatsession.Record) string {
	if rec.GroupID != nil && strings.TrimSpace(*rec.GroupID) != "" {
		if rec.GroupName != nil && strings.TrimSpace(*rec.GroupName) != "" {
			return strings.TrimSpace(*rec.GroupName)
		}
		return "群聊" + strings.TrimSpace(*rec.GroupID)
	}
	if rec.UserID != nil && strings.TrimSpace(*rec.UserID) != "" {
		privateName := ""
		if rec.UserCardname != nil {
			privateName = strings.TrimSpace(*rec.UserCardname)
		}
		if privateName == "" && rec.UserNickname != nil {
			privateName = strings.TrimSpace(*rec.UserNickname)
		}
		if privateName == "" {
			privateName = "用户" + strings.TrimSpace(*rec.UserID)
		}
		return privateName + "的私聊"
	}
	return rec.SessionID
}

func reasoningChatNameFromLatestMessage(db *gorm.DB, sessionID string) string {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	var row memory.MessageLog
	if err := db.Where("session_id = ?", sessionID).Order("timestamp DESC").Limit(1).Find(&row).Error; err != nil || row.ID == 0 {
		return ""
	}
	if strings.TrimSpace(row.GroupID) != "" {
		if name := strings.TrimSpace(row.GroupName); name != "" {
			return name
		}
		return "群聊" + strings.TrimSpace(row.GroupID)
	}
	privateName := strings.TrimSpace(row.UserCardname)
	if privateName == "" {
		privateName = strings.TrimSpace(row.UserNickname)
	}
	if privateName == "" {
		if userID := strings.TrimSpace(row.ExternalUserID); userID != "" {
			privateName = "用户" + userID
		} else if row.UserID != 0 {
			privateName = "用户" + strconv.FormatInt(row.UserID, 10)
		}
	}
	if privateName == "" {
		return ""
	}
	return privateName + "的私聊"
}

func reasoningChatDisplayName(db *gorm.DB, rec chatsession.Record) string {
	fromRecord := reasoningChatNameFromSessionRecord(rec)
	if fromRecord != rec.SessionID {
		return fromRecord
	}
	if fromMessage := reasoningChatNameFromLatestMessage(db, rec.SessionID); fromMessage != "" {
		return fromMessage
	}
	return fromRecord
}

func reasoningSessionTargetOf(rec chatsession.Record) (reasoningSessionTargetKey, bool) {
	platform := strings.TrimSpace(rec.Platform)
	if platform == "" {
		return reasoningSessionTargetKey{}, false
	}
	if rec.GroupID != nil {
		if groupID := strings.TrimSpace(*rec.GroupID); groupID != "" {
			return reasoningSessionTargetKey{Platform: platform, ChatType: "group", TargetID: groupID}, true
		}
	}
	if rec.UserID != nil {
		if userID := strings.TrimSpace(*rec.UserID); userID != "" {
			return reasoningSessionTargetKey{Platform: platform, ChatType: "private", TargetID: userID}, true
		}
	}
	return reasoningSessionTargetKey{}, false
}

func loadReasoningSessionCandidatesByTarget(
	db *gorm.DB,
	targetKeys map[reasoningSessionTargetKey]struct{},
) map[reasoningSessionTargetKey][]chatsession.Record {
	candidates := make(map[reasoningSessionTargetKey][]chatsession.Record, len(targetKeys))
	seen := make(map[reasoningSessionTargetKey]map[string]struct{}, len(targetKeys))
	for key := range targetKeys {
		candidates[key] = nil
		seen[key] = map[string]struct{}{}
	}
	if db == nil || len(targetKeys) == 0 {
		return candidates
	}
	platforms := make([]string, 0, len(targetKeys))
	platformSeen := map[string]struct{}{}
	for key := range targetKeys {
		if _, ok := platformSeen[key.Platform]; ok {
			continue
		}
		platformSeen[key.Platform] = struct{}{}
		platforms = append(platforms, key.Platform)
	}
	var rows []chatsession.Record
	if err := db.Where("platform IN ?", platforms).Find(&rows).Error; err != nil {
		return candidates
	}
	for _, row := range rows {
		key, ok := reasoningSessionTargetOf(row)
		if !ok {
			continue
		}
		if _, wanted := targetKeys[key]; !wanted {
			continue
		}
		sessionID := strings.TrimSpace(row.SessionID)
		if sessionID == "" {
			continue
		}
		if _, exists := seen[key][sessionID]; exists {
			continue
		}
		seen[key][sessionID] = struct{}{}
		candidates[key] = append(candidates[key], row)
	}
	return candidates
}

func (d Deps) configuredReasoningBotAccounts(ctx context.Context) map[[2]string]struct{} {
	accounts := map[[2]string]struct{}{}
	if d.BotAccountPairs == nil {
		return accounts
	}
	pairs, err := d.BotAccountPairs(ctx)
	if err != nil {
		return accounts
	}
	for _, pair := range pairs {
		platform := strings.ToLower(strings.TrimSpace(pair[0]))
		accountID := strings.TrimSpace(pair[1])
		if platform == "" || accountID == "" {
			continue
		}
		accounts[[2]string{platform, accountID}] = struct{}{}
	}
	return accounts
}

// reasoningSessionHashPattern 判断日志目录名是否为聊天会话 MD5
func isReasoningSessionHash(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, char := range name {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}

func reasoningSessionRecordByID(db *gorm.DB, sessionID string) (chatsession.Record, bool) {
	var zero chatsession.Record
	if db == nil || !isReasoningSessionHash(sessionID) {
		return zero, false
	}
	var row chatsession.Record
	if err := db.Where("session_id = ?", sessionID).First(&row).Error; err != nil {
		return zero, false
	}
	return row, true
}

func reasoningSessionMatchesConfiguredAccount(rec chatsession.Record, configuredAccounts map[[2]string]struct{}) bool {
	if rec.AccountID == nil {
		return false
	}
	platform := strings.ToLower(strings.TrimSpace(rec.Platform))
	accountID := strings.TrimSpace(*rec.AccountID)
	if platform == "" || accountID == "" {
		return false
	}
	_, ok := configuredAccounts[[2]string{platform, accountID}]
	return ok
}

// applyReasoningSessionRecord 用 chat_sessions 记录补齐目录对应的真实会话身份
func applyReasoningSessionRecord(info *reasoningSessionInfo, rec chatsession.Record, matchedCurrent bool, db *gorm.DB) {
	info.Platform = strings.TrimSpace(rec.Platform)
	if rec.GroupID != nil && strings.TrimSpace(*rec.GroupID) != "" {
		info.ChatType = "group"
		info.TargetID = strings.TrimSpace(*rec.GroupID)
	} else if rec.UserID != nil && strings.TrimSpace(*rec.UserID) != "" {
		info.ChatType = "private"
		info.TargetID = strings.TrimSpace(*rec.UserID)
	}
	if sessionID := strings.TrimSpace(rec.SessionID); sessionID != "" {
		info.ResolvedSessionID = stringPointer(sessionID)
	}
	info.DisplayName = reasoningChatDisplayName(db, rec)
	if rec.AccountID != nil {
		if accountID := strings.TrimSpace(*rec.AccountID); accountID != "" {
			info.AccountID = stringPointer(accountID)
		}
	}
	info.MatchedCurrentAccount = matchedCurrent
}

// reasoningDirectoryNameForRecord 返回聊天会话对应的平台目录名
func reasoningDirectoryNameForRecord(rec chatsession.Record) string {
	platform := display.NormalizePlatformName(rec.Platform)
	if platform == "" {
		return ""
	}
	if rec.GroupID != nil {
		if groupID := strings.TrimSpace(*rec.GroupID); groupID != "" {
			return platform + "_group_" + display.NormalizePreviewName(groupID)
		}
	}
	if rec.UserID != nil {
		if userID := strings.TrimSpace(*rec.UserID); userID != "" {
			return platform + "_private_" + display.NormalizePreviewName(userID)
		}
	}
	return ""
}

// resolveMigratedPromptPreviewPath 把历史哈希会话路径回退到迁移后的平台命名目录
func (d Deps) resolveMigratedPromptPreviewPath(root, relative string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 3 || !isReasoningSessionHash(parts[1]) {
		return "", false
	}
	var db *gorm.DB
	if d.Memory != nil {
		db = d.Memory.GetDB()
	}
	record, found := reasoningSessionRecordByID(db, parts[1])
	if !found {
		return "", false
	}
	named := reasoningDirectoryNameForRecord(record)
	if named == "" || named == parts[1] {
		return "", false
	}
	candidate := filepath.Join(root, filepath.FromSlash(parts[0]), named, filepath.FromSlash(strings.Join(parts[2:], "/")))
	if !strings.HasPrefix(candidate, root+string(os.PathSeparator)) || !pathExists(candidate) {
		return "", false
	}
	return candidate, true
}

func resolveReasoningSessionInfo(
	name string,
	configuredAccounts map[[2]string]struct{},
	candidatesByKey map[reasoningSessionTargetKey][]chatsession.Record,
	db *gorm.DB,
) *reasoningSessionInfo {
	info := &reasoningSessionInfo{Name: name, DisplayName: name}
	platform, chatType, targetID, ok := parseReasoningSessionDirectoryName(name)
	if !ok {
		// 历史目录名直接使用会话 MD5，按 chat_sessions 回填真实身份
		if record, found := reasoningSessionRecordByID(db, name); found {
			applyReasoningSessionRecord(info, record, reasoningSessionMatchesConfiguredAccount(record, configuredAccounts), db)
		}
		return info
	}
	info.Platform = platform
	info.ChatType = chatType
	info.TargetID = targetID
	matched, matchedCurrent, found := selectCurrentAccountReasoningSession(
		candidatesByKey[reasoningSessionTargetKey{Platform: platform, ChatType: chatType, TargetID: targetID}],
		configuredAccounts,
	)
	if !found {
		info.DisplayName = fallbackReasoningSessionDisplayName(name, platform, chatType, targetID)
		return info
	}
	applyReasoningSessionRecord(info, matched, matchedCurrent, db)
	return info
}

func (d Deps) buildReasoningSessionInfos(sessions []string) map[string]*reasoningSessionInfo {
	infos := make(map[string]*reasoningSessionInfo, len(sessions))
	if len(sessions) == 0 {
		return infos
	}
	var db *gorm.DB
	if d.Memory != nil {
		db = d.Memory.GetDB()
	}
	configuredAccounts := d.configuredReasoningBotAccounts(context.Background())
	targetKeys := map[reasoningSessionTargetKey]struct{}{}
	for _, name := range sessions {
		platform, chatType, targetID, ok := parseReasoningSessionDirectoryName(name)
		if !ok {
			continue
		}
		targetKeys[reasoningSessionTargetKey{Platform: platform, ChatType: chatType, TargetID: targetID}] = struct{}{}
	}
	candidates := loadReasoningSessionCandidatesByTarget(db, targetKeys)
	for _, name := range sessions {
		infos[name] = resolveReasoningSessionInfo(name, configuredAccounts, candidates, db)
	}
	return infos
}
