package runtime

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	maisaka "MaiGo/internal/maisaka"
	maisakactx "MaiGo/internal/maisaka/context"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// restoreProactiveContextMessages 主动回合回填真实用户消息
// 对齐 Python src/maisaka/runtime.py:375-446：优先按会话编号取最近消息，无结果时按群号/用户号回退，
// 过滤清理标记之前的消息与命令消息后转换成本轮可回复的引用目标
func (a *Agent) restoreProactiveContextMessages(ctx context.Context, kind string, targetID int64, sessionID string, limit int) ([]maisaka.ContextMessage, error) {
	if a == nil || a.memory == nil || limit <= 0 {
		return nil, nil
	}
	logs, err := a.memory.ListRecentMessagesBySessionID(ctx, sessionID, limit, 0)
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		// 回退查询不排除命令消息，清理边界交给后续 select 阶段按真实标记判定
		// 对齐 Python src/maisaka/runtime.py:404-421 的 find_messages（未传 filter_command）
		logs, err = a.listRecentMessagesByScope(ctx, kind, targetID, limit)
		if err != nil {
			return nil, err
		}
	}
	logs = maisakactx.SelectMessagesAfterLatestClearMarker(logs, messageLogIsClearMarker, messageLogIsCommand)
	// 对齐 Python runtime.py:396 的 filter_bot=True：按 (platform, account) 对排除所有机器人账号
	logs = a.filterBotMessages(ctx, logs)
	selfID := a.SelfID()
	sessionName := ""
	includeChatID := false
	if a.sessions != nil {
		if session := a.sessions.get(kind, targetID); session != nil {
			sessionName = session.MaisakaRuntimeSessionName()
			if runtime := session.MaisakaRuntime(); runtime != nil {
				includeChatID = runtime.FocusModeActive()
			}
		}
	}
	out := make([]maisaka.ContextMessage, 0, len(logs))
	for _, log := range logs {
		if log.IsNotify || (selfID > 0 && log.UserID == selfID) {
			continue
		}
		message := a.conversationMessageFromLog(log)
		payload := msgToPayloadWithSession(message, sessionID, sessionName, includeChatID)
		if payload == nil {
			continue
		}
		// 回填消息保留图片，避免模型在主动回合看不到历史图片
		appendMediaImageBlocks(payload, log.MediaRefs)
		resolved := resolveDeferredMediaBlocks([]*schema.AgenticMessage{payload})
		converted, ok := agenticMessageToMaisaka(resolved[0], 0)
		if !ok {
			continue
		}
		for _, item := range converted {
			if item.Role == maisaka.RoleUser {
				out = append(out, item)
			}
		}
	}
	return out, nil
}

// listRecentMessagesByScope 按会话来源取最近消息，不排除命令消息
// 对齐 Python find_messages 的 limit_mode="latest"：先按时间倒序取再反转为正序
func (a *Agent) listRecentMessagesByScope(ctx context.Context, kind string, targetID int64, limit int) ([]memory.MessageLog, error) {
	db := a.memory.GetDB()
	if db == nil {
		return nil, nil
	}
	var items []memory.MessageLog
	err := db.WithContext(ctx).
		Where("conversation_kind=? AND target_id=?", kind, targetID).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id")).
		Order("timestamp DESC, id DESC").Limit(limit).Find(&items).Error
	if err != nil {
		return nil, err
	}
	slices.Reverse(items)
	return items, nil
}

// appendMediaImageBlocks 把消息携带的图片以 deferred 占位补进模型消息
func appendMediaImageBlocks(payload *schema.AgenticMessage, refs []media.Ref) {
	if payload == nil || len(refs) == 0 {
		return
	}
	for _, ref := range refs {
		if len(payload.ContentBlocks) > 8 {
			break
		}
		image := deferredMediaImageBlock(ref)
		if image == nil {
			continue
		}
		payload.ContentBlocks = append(payload.ContentBlocks, schema.NewContentBlock(image))
	}
}

// messageLogIsClearMarker 消息库记录是否为已执行的上下文清理边界
func messageLogIsClearMarker(log memory.MessageLog) bool {
	if !log.IsCommand {
		return false
	}
	switch value := log.AdditionalConfig[maisakactx.ClearContextMarkerKey].(type) {
	case bool:
		return value
	case string:
		return value == "true"
	default:
		return false
	}
}

// SetBotAccountPairs 注入机器人账号对提供者，供 filter_bot 排除
func (a *Agent) SetBotAccountPairs(provider func(ctx context.Context) ([][2]string, error)) {
	a.setCallbacks(func(c *AgentCallbacks) { c.BotAccountPairs = provider })
}

// resolveBotAccount 解析单个 Bot 账号；多账号且无会话归属时拒绝猜测
func (a *Agent) resolveBotAccount(ctx context.Context, platform, preferredAccountID string) (string, error) {
	if preferred := strings.TrimSpace(preferredAccountID); preferred != "" {
		return preferred, nil
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return "", nil
	}
	a.callbacksMu.RLock()
	provider := a.callbacks.BotAccountPairs
	a.callbacksMu.RUnlock()
	if provider == nil {
		return "", nil
	}
	pairs, err := provider(ctx)
	if err != nil {
		return "", err
	}
	accounts := make(map[string]struct{})
	for _, pair := range pairs {
		if len(pair) < 2 || !strings.EqualFold(strings.TrimSpace(pair[0]), platform) {
			continue
		}
		if account := strings.TrimSpace(pair[1]); account != "" {
			accounts[account] = struct{}{}
		}
	}
	if len(accounts) == 1 {
		for account := range accounts {
			return account, nil
		}
	}
	if len(accounts) > 1 {
		zap.L().Error("平台存在多个 Bot 账号，缺少聊天流 account_id，无法选择发送身份",
			zap.String("platform", platform))
	}
	return "", nil
}

// filterBotMessages 排除所有机器人账号发出的消息
// 对齐 Python message_repository.py:188-207 的 filter_bot：按 (platform, user_id) 对精确排除
func (a *Agent) filterBotMessages(ctx context.Context, logs []memory.MessageLog) []memory.MessageLog {
	a.callbacksMu.RLock()
	provider := a.callbacks.BotAccountPairs
	a.callbacksMu.RUnlock()
	if a == nil || provider == nil || len(logs) == 0 {
		return logs
	}
	pairs, err := provider(ctx)
	if err != nil || len(pairs) == 0 {
		return logs
	}
	excluded := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		platform := strings.TrimSpace(pair[0])
		account := strings.TrimSpace(pair[1])
		if platform == "" || account == "" {
			continue
		}
		excluded[platform+"\x00"+account] = struct{}{}
	}
	if len(excluded) == 0 {
		return logs
	}
	filtered := make([]memory.MessageLog, 0, len(logs))
	for _, log := range logs {
		key := strings.TrimSpace(log.Platform) + "\x00" + strconv.FormatInt(log.UserID, 10)
		if _, isBot := excluded[key]; isBot {
			continue
		}
		filtered = append(filtered, log)
	}
	return filtered
}

// messageLogIsCommand 消息库记录是否为命令消息
func messageLogIsCommand(log memory.MessageLog) bool {
	return log.IsCommand
}
