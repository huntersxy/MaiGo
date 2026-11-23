package runtime

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/chat/debounce"
	"MaiGo/internal/common/database"
	"MaiGo/internal/config"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// instanceBotConfig 返回当前 MaiBot 实例 bot 配置；无 InstanceHolder 时为 nil
func (a *Agent) instanceBotConfig() *maibotcfg.BotConfigFile {
	if a == nil {
		return nil
	}
	a.instanceMu.RLock()
	holder := a.instanceHolder
	a.instanceMu.RUnlock()
	if holder == nil {
		return nil
	}
	cfg := holder.Config()
	if cfg == nil {
		return nil
	}
	return &cfg.Bot
}

// instanceConfigSnapshot 返回当前 MaiBot 实例完整配置；无 InstanceHolder 时为 nil
func (a *Agent) instanceConfigSnapshot() *maibotcfg.InstanceConfig {
	if a == nil {
		return nil
	}
	a.instanceMu.RLock()
	holder := a.instanceHolder
	a.instanceMu.RUnlock()
	if holder == nil {
		return nil
	}
	return holder.Config()
}

// replyAtEnabled 返回当前实例是否允许回复时 @ 用户
func (a *Agent) replyAtEnabled() bool {
	if bot := a.instanceBotConfig(); bot != nil {
		return bot.Chat.EnableReplyAt
	}
	return true
}

func plannerMessageSteeringFromEnv() bool {
	raw := strings.TrimSpace(os.Getenv("MAIBOT_PLANNER_MESSAGE_STEERING"))
	if raw == "" {
		return false
	}
	ok, err := strconv.ParseBool(raw)
	return err == nil && ok
}

func (a *Agent) plannerMessageSteeringEnabled() bool {
	if bot := a.instanceBotConfig(); bot != nil {
		return bot.Chat.ReplyTiming.PlannerMessageSteering
	}
	return plannerMessageSteeringFromEnv()
}

func (a *Agent) groupDebounceMode() debounce.Mode {
	if bot := a.instanceBotConfig(); bot != nil {
		return debounce.NormalizeMode(maibotcfg.GroupDebounceMode(bot.Chat.ReplyTiming))
	}
	return debounce.ModeOff
}

func (a *Agent) privateDebounceMode() debounce.Mode {
	if bot := a.instanceBotConfig(); bot != nil {
		return debounce.NormalizeMode(maibotcfg.PrivateDebounceMode(bot.Chat.ReplyTiming))
	}
	return debounce.ModeOff
}

func (a *Agent) gateFrequency(groupID int64) float64 {
	return a.conversationReplyFrequency(a.ctx, memory.ConversationKindGroup, groupID)
}

func (a *Agent) privateReplyFrequency(targetID int64) float64 {
	return a.conversationReplyFrequency(a.ctx, memory.ConversationKindPrivate, targetID)
}

func (a *Agent) conversationReplyFrequency(ctx context.Context, kind string, targetID int64) float64 {
	isGroup := kind == memory.ConversationKindGroup
	bot := a.instanceBotConfig()
	if bot == nil {
		if isGroup {
			return a.gateFrequencyFromRuntime(targetID)
		}
		return 1
	}
	platform, itemID := a.replyTimingTarget(ctx, kind, targetID)
	if isGroup {
		return maibotcfg.GroupTalkFrequency(bot.Chat.ReplyTiming, platform, itemID)
	}
	return maibotcfg.PrivateTalkFrequency(bot.Chat.ReplyTiming, platform, itemID)
}

func (a *Agent) replyTimingTarget(ctx context.Context, kind string, targetID int64) (string, string) {
	index := a.sessionIndex()
	if sessionID, ok := index.chatSessionID(kind, targetID); ok && a.chatSessions != nil {
		if record, err := a.chatSessions.GetExistingBySessionID(ctx, sessionID); err == nil {
			return record.Platform, record.TargetID()
		}
	}
	platform := a.conversationPlatform(kind, targetID)
	if groupKey := a.conversationGroupKey(kind, targetID); groupKey != "" {
		return platform, groupKey
	}
	if route, ok := index.route(kind, targetID); ok && route.TargetID != "" {
		return platform, route.TargetID
	}
	return platform, strconv.FormatInt(targetID, 10)
}

func (a *Agent) hasStrongInteraction(pending []*sessionUser) bool {
	for _, user := range pending {
		if user == nil {
			continue
		}
		if user.IsAgentMessage {
			return true
		}
		if !user.Mentioned {
			continue
		}
		if bot := a.instanceBotConfig(); bot != nil {
			rt := bot.Chat.ReplyTiming
			if rt.InevitableAtReply || rt.MentionedBotReply {
				return true
			}
			continue
		}
		return true
	}
	return false
}

func (a *Agent) observeInboundDebounce(msg *onebot.ConversationMessage) {
	if msg == nil || msg.MessageID == 0 || msg.UserID == a.SelfID() {
		return
	}
	mode := a.groupDebounceMode()
	if msg.ConversationKind == memory.ConversationKindPrivate {
		mode = a.privateDebounceMode()
	}
	if mode == debounce.ModeOff {
		a.turnSchedule.syncDebounceMode(msg.ConversationKind, msg.TargetID, mode)
		return
	}
	bareImage, pureEmoji := debounce.ShapeFromMessage(msg)
	now := time.Now()
	a.turnSchedule.observeDebounce(msg.ConversationKind, msg.TargetID, debounce.ObserveInput{
		Author:      strconv.FormatInt(msg.UserID, 10),
		IsBareImage: bareImage,
		IsPureEmoji: pureEmoji,
		AtBot:       msg.IsMentioned,
		ReceivedAt:  msg.ReceivedAt,
		WallTime:    now,
		Now:         now,
		Mode:        mode,
		IsGroup:     msg.ConversationKind == memory.ConversationKindGroup,
	})
}

func (a *Agent) messageQuietRemaining(kind string, targetID int64, fallbackReceivedAt time.Time) time.Duration {
	mode := a.groupDebounceMode()
	if kind == memory.ConversationKindPrivate {
		mode = a.privateDebounceMode()
	}
	if mode != debounce.ModeOff {
		remain := a.turnSchedule.debounceRemaining(kind, targetID, time.Now())
		if remain > 0 {
			return remain
		}
	}
	debounceMS := config.Snapshot().Agent.ThinkDebounceMS
	if debounceMS <= 0 {
		return 0
	}
	return remainingDebounce(fallbackReceivedAt, time.Now(), time.Duration(debounceMS)*time.Millisecond)
}

func (a *Agent) consumeMessageDebounce(kind string, targetID int64) {
	a.turnSchedule.consumeDebounce(kind, targetID)
}

func (a *Agent) applyReplyTimingFromInstance(cfg *maibotcfg.InstanceConfig) {
	if a == nil || cfg == nil {
		return
	}
	groupMode := debounce.NormalizeMode(maibotcfg.GroupDebounceMode(cfg.Bot.Chat.ReplyTiming))
	privateMode := debounce.NormalizeMode(maibotcfg.PrivateDebounceMode(cfg.Bot.Chat.ReplyTiming))
	a.turnSchedule.applyDebounceModes(groupMode, privateMode)
}
