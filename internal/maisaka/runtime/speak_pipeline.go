package runtime

import (
	"context"
	"time"

	"maibot-go/internal/chat/replyer"
	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	"maibot-go/internal/maisaka/builtin_tool"
	"maibot-go/internal/maisaka/quote"
	"maibot-go/internal/platform_io/drivers/onebot"
	"maibot-go/internal/services/statistics"

	"go.uber.org/zap"
)

func (a *Agent) replyerOptionsForSpeak(ctx context.Context) replyer.Options {
	cfg := replyer.DefaultConfig()
	if bot := a.instanceBotConfig(); bot != nil {
		settings := bot.ReplyPostProcessSettings()
		cfg.EnablePostProcess = settings.Enable
		cfg.TypingSpeed = settings.TypingSpeed
		cfg.Splitter = replyer.SplitterConfig{
			Enable:                  settings.SplitterEnable,
			Mode:                    settings.SplitterMode,
			MaxLength:               settings.MaxLength,
			MaxSentenceNum:          settings.MaxSentenceNum,
			MaxSplitNum:             settings.MaxSplitNum,
			EnableKaomojiProtection: settings.EnableKaomojiProtection,
			EnableOverflowReturnAll: settings.EnableOverflowReturnAll,
		}
		cfg.Typo = replyer.TypoConfig{
			Enable:                     settings.TypoEnable,
			EnableCorrectionQuote:      settings.TypoEnableCorrectionQuote,
			CorrectionQuoteProbability: settings.TypoCorrectionQuoteProbability,
			ErrorRate:                  settings.TypoErrorRate,
			MinFreq:                    settings.TypoMinFreq,
			ToneErrorRate:              settings.TypoToneErrorRate,
			WordReplaceRate:            settings.TypoWordReplaceRate,
		}
	}
	opts := replyer.Options{
		Config:      cfg,
		BotNickname: fallbackBotName,
		Ctx:         ctx,
		LLMSplitter: a.generateSplitterText,
	}
	if runtimeCfg := configFor(ctx); runtimeCfg != nil && runtimeCfg.Chat.TypingSpeed > 0 {
		opts.Config.TypingSpeed = float64(runtimeCfg.Chat.TypingSpeed) / 6
		if opts.Config.TypingSpeed <= 0 {
			opts.Config.TypingSpeed = 1
		}
	}
	return opts
}

func (a *Agent) pipelineSegments(ctx context.Context, content string) []replyer.Segment {
	opts := a.replyerOptionsForSpeak(ctx)
	opts.BotNickname = a.botName()
	if pp, ok := replyer.PostProcessOptionsFrom(ctx); ok {
		opts.SkipPostProcess = pp.Skip
		if pp.EnableSplitter != nil {
			opts.EnableSplitter = pp.EnableSplitter
		}
		if pp.EnableTypo != nil {
			opts.EnableTypo = pp.EnableTypo
		}
	}
	result := replyer.ApplyPipeline(content, opts)
	if len(result.Segments) == 0 {
		return []replyer.Segment{{Text: content}}
	}
	return result.Segments
}

// speakSender 抽出发送入口，便于验证逐段结果而不触达真实网络
type speakSender func(ctx context.Context, msg *outboundMessage) (int64, uint64, bool, error)

// speakHooks 组装发送循环依赖的宿主能力；send 为 nil 时使用真实发送
//
// 发送循环本身在 chat/replyer.SendSegments，这里只做端口适配：
// 真实发送、打字广播、逐段落库与结束后的门控更新。
func (a *Agent) speakHooks(ctx context.Context, send speakSender, abortOnFailure, quotePreviousAllowed bool) replyer.SendHooks {
	if send == nil {
		send = a.sendOutbound
	}
	cfg := configFor(ctx)
	hooks := replyer.SendHooks{
		AbortOnFailure:       abortOnFailure,
		QuotePreviousAllowed: quotePreviousAllowed,
	}
	if cfg != nil {
		hooks.TypingSimulation = cfg.Chat.TypingSimulation
		hooks.TypingSpeed = cfg.Chat.TypingSpeed
	}
	hooks.Send = func(callCtx context.Context, msg *replyer.OutboundMessage) (int64, uint64, bool, error) {
		out := &outboundMessage{
			Kind:     conversationKindForSend(msg.Kind),
			TargetID: msg.TargetID,
			Text:     msg.Text,
			Mentions: msg.Mentions,
			ReplyTo:  msg.ReplyTo,
			Typing:   msg.Typing,
		}
		messageID, arrivalSeq, sent, err := send(callCtx, out)
		// 回写发送入口可能改写的字段，供 replyer 记录逐段结果
		msg.Text = out.Text
		msg.Mentions = out.Mentions
		msg.ReplyTo = out.ReplyTo
		msg.Storage = out.Storage
		msg.ShowLog = out.ShowLog
		msg.MessageID = messageID
		return messageID, arrivalSeq, sent, err
	}
	hooks.BroadcastTyping = func(callCtx context.Context, kind replyer.SendKind, targetID int64, typing, start bool) {
		a.broadcastInternalTyping(conversationKindForSend(kind), targetID, typing, start)
	}
	hooks.AfterSegment = func(callCtx context.Context, msg *replyer.OutboundMessage, arrivalSeq uint64, at time.Time) {
		a.afterSpeakSegment(callCtx, msg, arrivalSeq, at)
	}
	hooks.AfterAll = func(callCtx context.Context, kind replyer.SendKind, targetID int64, lastMessageID int64, segmentCount int, showLog bool) {
		a.afterSpeakAll(conversationKindForSend(kind), targetID, lastMessageID, segmentCount, showLog)
	}
	return hooks
}

// afterSpeakSegment 每段发送成功后的统计、引用记录、落库与历史写入
func (a *Agent) afterSpeakSegment(ctx context.Context, msg *replyer.OutboundMessage, arrivalSeq uint64, at time.Time) {
	kind := conversationKindForSend(msg.Kind)
	stats.RecordOutbound(chatsession.ConversationKey(kind, msg.TargetID))

	parts := make([]onebot.MessagePart, 0, len(msg.Mentions)+1)
	for _, userID := range msg.Mentions {
		if userID > 0 {
			parts = append(parts, onebot.MessagePart{Kind: "at", AtUserID: userID})
		}
	}
	parts = append(parts, onebot.MessagePart{Kind: "text", Text: msg.Text})

	conversation := &onebot.ConversationMessage{
		Platform:         a.conversationPlatform(kind, msg.TargetID),
		ConversationKind: kind,
		MessageID:        msg.MessageID,
		TargetID:         msg.TargetID,
		UserID:           a.SelfID(),
		Nickname:         a.botName(),
		Content:          msg.Text,
		MessageParts:     parts,
		Time:             at,
		ArrivalSeq:       arrivalSeq,
	}
	if kind == memory.ConversationKindPrivate {
		conversation.ReceivedAt = at
	}
	if msg.ReplyTo != 0 {
		conversation.Reply = &onebot.ReplyInfo{MessageID: msg.ReplyTo}
	}
	a.recordQuoteOutbound(ctx, kind, msg.TargetID, msg.MessageID, a.SelfID(), conversation.Time)
	if len(msg.Mentions) > 0 {
		conversation.AtList = msg.Mentions
		conversation.AtNames = make(map[int64]string, len(msg.Mentions))
		for _, userID := range msg.Mentions {
			if userID > 0 {
				conversation.AtNames[userID] = a.resolveMentionDisplayName(ctx, conversation, userID)
			}
		}
	}
	if msg.Storage {
		a.onMessage(conversation)
	} else {
		a.notifySentMessage(ctx, conversation)
	}
	// 对齐 Python reply 工具：已发送的可见回复以 guided_reply 写入 Maisaka 历史
	if session := a.sessions.get(kind, msg.TargetID); session != nil {
		session.appendGuidedReplyToHistory(msg.MessageID, msg.ReplyTo, msg.Text, conversation.Time, a.botName())
	}
}

// afterSpeakAll 全部段发送结束后更新会话与门控状态
func (a *Agent) afterSpeakAll(kind string, targetID int64, lastMessageID int64, segmentCount int, showLog bool) {
	if lastMessageID == 0 {
		return
	}
	if session := a.sessions.get(kind, targetID); session != nil {
		session.NoteSelfSpoke(time.Now())
	}
	if kind != memory.ConversationKindGroup {
		return
	}
	a.gates.markReply(targetID)
	markRepliedThisTurn(context.Background())
	if showLog {
		zap.L().Info("发言成功", zap.Int64("group_id", targetID), zap.Int("segments", segmentCount))
	}
}

// conversationKindForSend 把 replyer 的发送类型映射回会话类型
func conversationKindForSend(kind replyer.SendKind) string {
	if kind == replyer.SendKindPrivate {
		return memory.ConversationKindPrivate
	}
	return memory.ConversationKindGroup
}

func (a *Agent) sendGroupSpeakSegments(ctx context.Context, groupID int64, segments []replyer.Segment, initialReplyTo int64, mentions []int64) error {
	_, err := a.sendGroupSpeakSegmentsDetailed(ctx, groupID, segments, initialReplyTo, mentions, a.sendOutbound, false)
	return err
}

// sendGroupSpeakSegmentsDetailed 逐段发送并返回真实结果；abortOnFailure 对齐 Python reply 的失败即停止
func (a *Agent) sendGroupSpeakSegmentsDetailed(ctx context.Context, groupID int64, segments []replyer.Segment, initialReplyTo int64, mentions []int64, send speakSender, abortOnFailure bool) ([]tools.ReplySendSegment, error) {
	// 引用频率为 never 时非首段一律不引用，对齐 Python reply 的 quote_previous 门控
	quotePreviousAllowed := a.quoteFrequency(true) != quote.FrequencyNever
	hooks := a.speakHooks(ctx, send, abortOnFailure, quotePreviousAllowed)
	return replyer.SendSegments(ctx, replyer.SendKindGroup, groupID, segments, initialReplyTo, mentions, hooks)
}

func (a *Agent) sendPrivateSpeakSegments(ctx context.Context, targetID int64, segments []replyer.Segment, initialReplyTo int64) error {
	_, err := a.sendPrivateSpeakSegmentsDetailed(ctx, targetID, segments, initialReplyTo, a.sendOutbound, false)
	return err
}

// sendPrivateSpeakSegmentsDetailed 逐段发送并返回真实结果；abortOnFailure 对齐 Python reply 的失败即停止
func (a *Agent) sendPrivateSpeakSegmentsDetailed(ctx context.Context, targetID int64, segments []replyer.Segment, initialReplyTo int64, send speakSender, abortOnFailure bool) ([]tools.ReplySendSegment, error) {
	// 引用频率为 never 时非首段一律不引用，对齐 Python reply 的 quote_previous 门控
	quotePreviousAllowed := a.quoteFrequency(false) != quote.FrequencyNever
	hooks := a.speakHooks(ctx, send, abortOnFailure, quotePreviousAllowed)
	return replyer.SendSegments(ctx, replyer.SendKindPrivate, targetID, segments, initialReplyTo, nil, hooks)
}
