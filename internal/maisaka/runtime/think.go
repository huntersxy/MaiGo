package runtime

import (
	"context"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/config"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/services"

	"go.uber.org/zap"
)

func (a *Agent) conversationThinkLoop() {
	defer a.wg.Done()
	interval := time.Duration(config.Snapshot().Agent.ThinkInterval) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
			a.thinkCycle()
			next := time.Duration(config.Snapshot().Agent.ThinkInterval) * time.Second
			if next <= 0 {
				next = 15 * time.Second
			}
			timer.Reset(next)
		}
	}
}

func (a *Agent) thinkCycle() {
	groupIDs, err := a.memory.ListActiveGroupIDs(a.ctx)
	if err != nil {
		zap.L().Warn("读取可思考群聊失败", zap.Error(err))
		return
	}
	for _, groupID := range groupIDs {
		session := a.sessions.get(memory.ConversationKindGroup, groupID)
		pending := session.PendingMessages()
		if len(pending) == 0 {
			continue
		}
		a.scheduleGroupThink(groupID, latestReceivedAt(pending))
	}
	targets, err := a.memory.ListConversationTargets(a.ctx, memory.ConversationKindPrivate, true)
	if err != nil {
		zap.L().Warn("读取可思考私聊失败", zap.Error(err))
		return
	}
	for _, target := range targets {
		if target.Blocked {
			continue
		}
		session := a.sessions.get(memory.ConversationKindPrivate, target.TargetID)
		pending := session.PendingMessages()
		if len(pending) == 0 {
			continue
		}
		a.schedulePrivateThink(target.TargetID, latestReceivedAt(pending))
	}
}

// latestReceivedAt 取待处理消息里最新的一条进入时间，防抖从消息进来到达开始算
func latestReceivedAt(pending []*sessionUser) time.Time {
	var latest time.Time
	for _, user := range pending {
		if user != nil && user.ReceivedAt.After(latest) {
			latest = user.ReceivedAt
		}
	}
	return latest
}

func (a *Agent) scheduleGroupThink(groupID int64, receivedAt time.Time) {
	if a.stopping.Load() || a.ctx.Err() != nil || a.groupConcurrency.IsBusy(groupID) {
		return
	}
	if sessionID := a.resolveMaiBotSessionID(context.Background(), memory.ConversationKindGroup, groupID); sessionID != "" &&
		services.ChatMigrationGateInstance.Blocked(sessionID) {
		return
	}
	delay := a.messageQuietRemaining(memory.ConversationKindGroup, groupID, receivedAt)
	a.armGroupThinkTimer(groupID, delay)
}

func (a *Agent) afterGroupThink(groupID int64) {
	pending := a.sessions.get(memory.ConversationKindGroup, groupID).pendingAfterTurn()
	if hasActionableMessage(pending) {
		a.scheduleGroupThink(groupID, latestReceivedAt(pending))
		return
	}
	a.armGroupSilenceCheck(groupID, time.Time{})
}

// resetTimer 在会话锁内递增代次，旧回调即使已启动也不能消费新批次
func (p *pendingThink) resetTimer(delay time.Duration, fire func(*pendingThink, uint64)) {
	p.generation++
	generation := p.generation
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(delay, func() { fire(p, generation) })
}

func remainingDebounce(receivedAt, now time.Time, debounce time.Duration) time.Duration {
	if receivedAt.IsZero() {
		return debounce
	}
	delay := receivedAt.Add(debounce).Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

func (a *Agent) takeGroupTimer(groupID int64, expected *pendingThink, generation uint64) bool {
	a.pendingGroupMu.Lock()
	defer a.pendingGroupMu.Unlock()
	armed, ok := a.pendingGroupThinks[groupID]
	if !ok || armed != expected || armed.generation != generation || a.stopping.Load() || a.ctx.Err() != nil {
		return false
	}

	delete(a.pendingGroupThinks, groupID)
	return true
}

func (a *Agent) flushPendingThink(groupID int64, expected *pendingThink, generation uint64) {
	if !a.takeGroupTimer(groupID, expected, generation) || a.groupConcurrency.IsBusy(groupID) {
		return
	}
	session := a.sessions.get(memory.ConversationKindGroup, groupID)
	pendingMsgs := session.PendingMessages()
	if len(pendingMsgs) == 0 || !hasActionableMessage(pendingMsgs) {
		return
	}
	quiet := a.messageQuietRemaining(memory.ConversationKindGroup, groupID, latestReceivedAt(pendingMsgs))
	result := a.evaluateGroupMessageTurn(groupID, pendingMsgs, quiet)
	a.applyGroupTurnDecision(groupID, result)
}

func (a *Agent) clearPendingThinks() {
	a.pendingGroupMu.Lock()
	defer a.pendingGroupMu.Unlock()

	for groupID, pending := range a.pendingGroupThinks {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		delete(a.pendingGroupThinks, groupID)
	}
}

func (a *Agent) thinkGroup(groupID int64) {
	silence := a.gates.consumeSilenceTrigger(groupID)
	session := a.sessions.get(memory.ConversationKindGroup, groupID)
	pending := session.PendingMessages()
	session.markTurnInputs(pending)
	a.emitMonitorThinkStart(memory.ConversationKindGroup, groupID)
	defer a.emitMonitorThinkEnd(memory.ConversationKindGroup, groupID)

	generation := a.acquireGeneration()
	if generation == nil {
		return
	}
	defer generation.wg.Done()
	state, err := newRunState()
	if err != nil {
		zap.L().Warn("加载本轮配置失败", zap.Int64("group_id", groupID), zap.Error(err))
		return
	}
	if !a.conversationAllowed(memory.ConversationKindGroup, groupID) {
		return
	}
	if !silence && !hasActionableMessage(pending) {
		return
	}
	a.completeConversationWait(memory.ConversationKindGroup, groupID, 0, !silence && sessionHasActionableUserSinceWait(session))
	a.consumeMessageDebounce(memory.ConversationKindGroup, groupID)
	ctx := withRunState(a.ctx, state)
	ctx = withGeneration(ctx, generation)
	ctx = a.buildConversationToolContext(ctx, memory.ConversationKindGroup, groupID, session)
	a.restoreFocusCheckpoint(ctx, session)
	if a.attachMaisakaRuntime(memory.ConversationKindGroup, groupID, session) != nil {
		defer a.saveFocusCheckpoint(ctx, session)
	}
	tc := tools.GetToolContext(ctx)
	tc.ThinkStartArrivalSeq = maxPendingSeq(pending)

	messages, transcriptIDs, err := a.assembleMessages(ctx, session)
	if err != nil {
		zap.L().Warn("装配群聊上下文失败", zap.Int64("group_id", groupID), zap.Error(err))
		return
	}
	if a.runMaisakaTurn(ctx, memory.ConversationKindGroup, groupID, session, messages, transcriptIDs) {
		return
	}
	if runErr := a.runAgent(ctx, generation.groupAgent, messages); runErr != nil {
		if isContextOverflowError(runErr) && a.compactAfterOverflow(ctx, memory.ConversationKindGroup, groupID, generation.groupTools, a.passthroughModelInput, session) {
			if retryErr := a.retryAfterOverflow(ctx, memory.ConversationKindGroup, groupID, generation.groupAgent, session); retryErr == nil {
				return
			}
		}
		zap.L().Error("群聊思考失败", zap.Int64("group_id", groupID), zap.Error(runErr))
		return
	}
}

func (a *Agent) conversationAllowed(kind string, targetID int64) bool {
	if a.stopping.Load() || a.ctx.Err() != nil {
		return false
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	allowed, err := a.memory.ConversationAllowed(ctx, kind, targetID)
	if err != nil {
		zap.L().Warn("思考启动许可检查失败", zap.String("conversation_kind", kind), zap.Int64("target_id", targetID), zap.Error(err))
	}
	return err == nil && allowed
}

// hasActionableMessage 观察事件只能进入后续上下文，不能单独触发回应
func hasActionableMessage(pending []*sessionUser) bool {
	for _, user := range pending {
		if user != nil && (user.MessageID != 0 || user.IsAgentMessage) {
			return true
		}
	}
	return false
}

func (a *Agent) doSpeak(ctx context.Context, groupID int64, content string, replyTo int64, mentions []int64) error {
	segments := a.pipelineSegments(ctx, content)
	if tc := tools.GetToolContext(ctx); tc != nil {
		if session, ok := tc.Session.(*Session); ok {
			replyTo = a.applyQuoteDecision(ctx, session, memory.ConversationKindGroup, groupID, replyTo, firstSegmentText(segments), "")
		}
		if tc.EffectiveReplyTo != nil {
			*tc.EffectiveReplyTo = replyTo
		}
	}
	return a.sendGroupSpeakSegments(ctx, groupID, segments, replyTo, mentions)
}

func (a *Agent) doSendSticker(ctx context.Context, groupID int64, fileName string, description string) error {
	out := &outboundMessage{
		Kind:      memory.ConversationKindGroup,
		TargetID:  groupID,
		ImageFile: fileName,
		IsSticker: true,
	}
	msgID, arrivalSeq, sent, err := a.sendOutbound(ctx, out)
	if err != nil {
		zap.L().Error("发送表情包失败", zap.Int64("group_id", groupID), zap.String("file_name", fileName), zap.Error(err))
		return err
	}
	if !sent {
		zap.L().Info("表情包发送被 Hook 中止", zap.Int64("group_id", groupID))
		return nil
	}

	msg := &onebot.ConversationMessage{
		Platform:         a.conversationPlatform(memory.ConversationKindGroup, groupID),
		ConversationKind: memory.ConversationKindGroup,
		MessageID:        msgID,
		TargetID:         groupID,
		UserID:           a.SelfID(),
		Nickname:         a.botName(),
		Content:          "",
		MessageParts:     []onebot.MessagePart{{Kind: "image", Index: 0}},
		Time:             time.Now(),
		ArrivalSeq:       arrivalSeq,
		Images: []onebot.ImageInfo{
			{
				SubType: 1,
				Desc:    description,
			},
		},
	}
	a.HandleMessage(ctx, msg)
	zap.L().Info("发送表情包成功", zap.Int64("group_id", groupID), zap.String("desc", description))
	return nil
}

func (a *Agent) doSendImageURL(ctx context.Context, groupID int64, imageURL string) error {
	out := &outboundMessage{
		Kind:        memory.ConversationKindGroup,
		TargetID:    groupID,
		ImageFile:   imageURL,
		ReplyAnchor: tools.ReplyAnchorFromContext(ctx),
	}
	msgID, arrivalSeq, sent, err := a.sendOutbound(ctx, out)
	if err != nil {
		return err
	}
	if !sent {
		zap.L().Info("图片发送被 Hook 中止", zap.Int64("group_id", groupID))
		return nil
	}
	imageURL = out.ImageFile
	a.HandleMessage(ctx, &onebot.ConversationMessage{
		Platform:         a.conversationPlatform(memory.ConversationKindGroup, groupID),
		ConversationKind: memory.ConversationKindGroup,
		MessageID:        msgID,
		TargetID:         groupID,
		UserID:           a.SelfID(),
		Nickname:         a.botName(),
		MessageParts:     []onebot.MessagePart{{Kind: "image", Index: 0}},
		Images:           []onebot.ImageInfo{{URL: imageURL}},
		Time:             time.Now(),
		ArrivalSeq:       arrivalSeq,
	})
	// 对齐 Python reply attach_pic：已发送图片作为独立 guided_reply 条目进入历史并调度识图
	if session := a.sessions.get(memory.ConversationKindGroup, groupID); session != nil {
		imageComponent := maisaka.ImageComponent{MediaRef: imageURL}
		if len(decodeAgentImageData(imageURL)) > 0 {
			imageComponent.HasBinary = true
		}
		session.appendSentImagesToHistory(msgID, []maisaka.ImageComponent{imageComponent}, time.Now(), a.botName())
	}
	return nil
}
