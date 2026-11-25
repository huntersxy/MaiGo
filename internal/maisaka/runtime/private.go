package runtime

import (
	"context"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/services"

	"go.uber.org/zap"
)

func (a *Agent) schedulePrivateThink(targetID int64, receivedAt time.Time) {
	if a.stopping.Load() || a.ctx.Err() != nil || a.privateConcurrency.IsBusy(targetID) {
		return
	}
	if sessionID := a.resolveMaiBotSessionID(context.Background(), memory.ConversationKindPrivate, targetID); sessionID != "" &&
		services.ChatMigrationGateInstance.Blocked(sessionID) {
		return
	}
	a.armPrivateThinkTimer(targetID, a.messageQuietRemaining(memory.ConversationKindPrivate, targetID, receivedAt))
}

func (a *Agent) afterPrivateThink(targetID int64) {
	pending := a.sessions.get(memory.ConversationKindPrivate, targetID).pendingAfterTurn()
	if hasActionableMessage(pending) {
		a.schedulePrivateThink(targetID, latestReceivedAt(pending))
	}
}

func (a *Agent) flushPrivateThink(targetID int64, expected *pendingThink, generation uint64) {
	a.privateMu.Lock()
	pending := a.pendingPrivateThinks[targetID]
	// 挂起的思考只能由最新一代定时器触发；进入 running 前同样持有锁，
	// schedulePrivateThink 会在 running 期间跳过创建挂起项，因此这里无需复查
	if pending != expected || pending.generation != generation || a.privateStopped || a.stopping.Load() || a.ctx.Err() != nil || a.privateConcurrency.IsBusy(targetID) {
		a.privateMu.Unlock()
		return
	}
	delete(a.pendingPrivateThinks, targetID)
	a.privateMu.Unlock()

	session := a.sessions.get(memory.ConversationKindPrivate, targetID)
	pendingMsgs := session.pendingAfterTurn()
	if len(pendingMsgs) == 0 || !hasActionableMessage(pendingMsgs) {
		return
	}
	quiet := a.messageQuietRemaining(memory.ConversationKindPrivate, targetID, latestReceivedAt(pendingMsgs))
	result := a.evaluatePrivateMessageTurn(targetID, quiet)
	a.applyPrivateTurnDecision(targetID, result)
}

func (a *Agent) thinkPrivate(targetID int64) {
	session := a.sessions.get(memory.ConversationKindPrivate, targetID)
	pending := session.PendingMessages()
	session.markTurnInputs(pending)
	a.emitMonitorThinkStart(memory.ConversationKindPrivate, targetID)
	defer a.emitMonitorThinkEnd(memory.ConversationKindPrivate, targetID)

	generation := a.acquireGeneration()
	if generation == nil {
		return
	}
	defer generation.wg.Done()
	state, err := newRunState()
	if err != nil {
		zap.L().Warn("加载本轮配置失败", zap.Int64("target_id", targetID), zap.Error(err))
		return
	}
	if !a.conversationAllowed(memory.ConversationKindPrivate, targetID) {
		return
	}
	if len(pending) == 0 || !hasActionableMessage(pending) {
		return
	}
	a.completeConversationWait(memory.ConversationKindPrivate, targetID, 0, sessionHasActionableUserSinceWait(session))
	a.consumeMessageDebounce(memory.ConversationKindPrivate, targetID)
	ctx := withRunState(a.ctx, state)
	ctx = withGeneration(ctx, generation)
	ctx = a.buildConversationToolContext(ctx, memory.ConversationKindPrivate, targetID, session)
	a.restoreFocusCheckpoint(ctx, session)
	if a.attachMaisakaRuntime(memory.ConversationKindPrivate, targetID, session) != nil {
		defer a.saveFocusCheckpoint(ctx, session)
	}
	tc := tools.GetToolContext(ctx)
	tc.ThinkStartArrivalSeq = maxPendingSeq(pending)

	messages, transcriptIDs, err := a.assembleMessages(ctx, session)
	if err != nil {
		zap.L().Warn("装配私聊上下文失败", zap.Int64("target_id", targetID), zap.Error(err))
		return
	}
	if a.runMaisakaTurn(ctx, memory.ConversationKindPrivate, targetID, session, messages, transcriptIDs) {
		return
	}
	if runErr := a.runAgent(ctx, generation.privateAgent, messages); runErr != nil {
		if isContextOverflowError(runErr) && a.compactAfterOverflow(ctx, memory.ConversationKindPrivate, targetID, generation.privateTools, a.passthroughModelInput, session) {
			if retryErr := a.retryAfterOverflow(ctx, memory.ConversationKindPrivate, targetID, generation.privateAgent, session); retryErr == nil {
				return
			}
		}
		zap.L().Warn("私聊思考失败", zap.Int64("target_id", targetID), zap.Error(runErr))
		return
	}
}

// stopPrivateTimers 停止私聊防抖定时器并禁止新的私聊思考，供停机排空使用
func (a *Agent) stopPrivateTimers() {
	a.privateMu.Lock()
	defer a.privateMu.Unlock()
	a.privateStopped = true
	for targetID, pending := range a.pendingPrivateThinks {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		delete(a.pendingPrivateThinks, targetID)
	}
}

func (a *Agent) doPrivateSpeak(ctx context.Context, targetID int64, content string, replyTo int64) error {
	segments := a.pipelineSegments(ctx, content)
	if tc := tools.GetToolContext(ctx); tc != nil {
		if session, ok := tc.Session.(*Session); ok {
			replyTo = a.applyQuoteDecision(ctx, session, memory.ConversationKindPrivate, targetID, replyTo, firstSegmentText(segments), "")
		}
		if tc.EffectiveReplyTo != nil {
			*tc.EffectiveReplyTo = replyTo
		}
	}
	return a.sendPrivateSpeakSegments(ctx, targetID, segments, replyTo)
}

func (a *Agent) doPrivateSticker(ctx context.Context, targetID int64, fileName string, description string) error {
	out := &outboundMessage{
		Kind:      memory.ConversationKindPrivate,
		TargetID:  targetID,
		ImageFile: fileName,
		IsSticker: true,
	}
	messageID, seq, sent, err := a.sendOutbound(ctx, out)
	if err != nil {
		zap.L().Error("发送私聊表情包失败", zap.Int64("target_id", targetID), zap.String("file_name", fileName), zap.Error(err))
		return err
	}
	if !sent {
		zap.L().Info("私聊表情包发送被 Hook 中止", zap.Int64("target_id", targetID))
		return nil
	}
	a.HandleMessage(ctx, &onebot.ConversationMessage{Platform: a.conversationPlatform(memory.ConversationKindPrivate, targetID), ConversationKind: memory.ConversationKindPrivate, MessageID: messageID, ExternalMessageID: out.ExternalMessageID, TargetID: targetID, UserID: a.SelfID(), Nickname: a.botName(), Images: []onebot.ImageInfo{{SubType: 1, Desc: description}}, MessageParts: []onebot.MessagePart{{Kind: "image", Index: 0}}, Time: time.Now(), ReceivedAt: time.Now(), ArrivalSeq: seq})
	return nil
}

func (a *Agent) doPrivateImage(ctx context.Context, targetID int64, imageURL string) error {
	out := &outboundMessage{
		Kind:        memory.ConversationKindPrivate,
		TargetID:    targetID,
		ImageFile:   imageURL,
		ReplyAnchor: tools.ReplyAnchorFromContext(ctx),
	}
	messageID, seq, sent, err := a.sendOutbound(ctx, out)
	if err != nil {
		return err
	}
	if !sent {
		zap.L().Info("私聊图片发送被 Hook 中止", zap.Int64("target_id", targetID))
		return nil
	}
	imageURL = out.ImageFile
	a.HandleMessage(ctx, &onebot.ConversationMessage{Platform: a.conversationPlatform(memory.ConversationKindPrivate, targetID), ConversationKind: memory.ConversationKindPrivate, MessageID: messageID, ExternalMessageID: out.ExternalMessageID, TargetID: targetID, UserID: a.SelfID(), Nickname: a.botName(), Images: []onebot.ImageInfo{{URL: imageURL}}, MessageParts: []onebot.MessagePart{{Kind: "image", Index: 0}}, Time: time.Now(), ReceivedAt: time.Now(), ArrivalSeq: seq})
	// 对齐 Python reply attach_pic：已发送图片作为独立 guided_reply 条目进入历史并调度识图
	if session := a.sessions.get(memory.ConversationKindPrivate, targetID); session != nil {
		imageComponent := maisaka.ImageComponent{MediaRef: imageURL}
		if len(decodeAgentImageData(imageURL)) > 0 {
			imageComponent.HasBinary = true
		}
		session.appendSentImagesToHistory(messageID, []maisaka.ImageComponent{imageComponent}, time.Now(), a.botName())
	}
	return nil
}
