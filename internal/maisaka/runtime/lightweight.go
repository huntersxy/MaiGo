package runtime

import (
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
)

// isLightweightPrivateSession 只匹配 experimental.lightweight_private_chats 指定私聊
func (a *Agent) isLightweightPrivateSession(kind string, targetID int64) bool {
	if a == nil || kind != memory.ConversationKindPrivate || targetID <= 0 {
		return false
	}
	bot := a.instanceBotConfig()
	if bot == nil || len(bot.Experimental.LightweightPrivateChats) == 0 {
		return false
	}
	platform := strings.TrimSpace(a.conversationPlatform(kind, targetID))
	itemID := strconv.FormatInt(targetID, 10)
	for _, target := range bot.Experimental.LightweightPrivateChats {
		if strings.TrimSpace(target.RuleType) != "private" {
			continue
		}
		targetPlatform := strings.TrimSpace(target.Platform)
		targetItemID := strings.TrimSpace(target.ItemId)
		if (targetPlatform == "*" || targetPlatform == platform) &&
			(targetItemID == "*" || targetItemID == itemID) {
			return true
		}
	}
	return false
}
