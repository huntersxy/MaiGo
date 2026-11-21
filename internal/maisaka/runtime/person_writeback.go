package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
	"MaiGo/internal/person_info"
	"MaiGo/internal/platform_io/drivers/onebot"

	"MaiGo/internal/platform_io"
	"go.uber.org/zap"
)

// SetSentMessageHandler 注入发送成功后的记忆自动化通知端口
func (a *Agent) SetSentMessageHandler(handler func(context.Context, *onebot.ConversationMessage) error) {
	if a == nil {
		return
	}
	a.setCallbacks(func(c *AgentCallbacks) { c.SentMessageHandler = handler })
}

// notifySentMessage 在成功发送后同步通知记忆自动化，入队失败只记录真实错误
func (a *Agent) notifySentMessage(ctx context.Context, msg *onebot.ConversationMessage) {
	if a == nil || msg == nil {
		return
	}
	notifyCtx := context.WithoutCancel(ctx)
	// 出站消息不带会话号；摘要写回按 MaiBot session_id 统计消息，缺号会被静默跳过
	if msg.SessionID == "" {
		msg.SessionID = a.resolveMaiBotSessionID(notifyCtx, msg.ConversationKind, msg.TargetID)
	}
	a.callbacksMu.RLock()
	handler := a.callbacks.SentMessageHandler
	a.callbacksMu.RUnlock()
	if handler == nil {
		return
	}
	if err := handler(notifyCtx, msg); err != nil {
		zap.L().Warn("发送后记忆自动化失败", zap.Error(err))
	}
}

// HandleSentPersonFact 处理一条已成功发送的机器人消息，提取人物事实并入写回队列
// 由记忆自动化协调器在发送成功后调用，统一处理人物事实写回
func (a *Agent) HandleSentPersonFact(ctx context.Context, botMsg *onebot.ConversationMessage) error {
	if a == nil || botMsg == nil || a.memory == nil {
		return nil
	}
	if !a.personFactWritebackEnabled() {
		return nil
	}
	ex := personinfo.DefaultRegistry().FactExtractorFromRegistry()
	if ex == nil {
		return nil
	}
	replyText := strings.TrimSpace(botMsg.FinalContent)
	if replyText == "" {
		replyText = strings.TrimSpace(botMsg.Content)
	}
	if personinfo.LooksEphemeralReply(replyText) {
		return nil
	}
	return a.runPersonFactWriteback(ctx, botMsg, replyText, ex)
}

func (a *Agent) runPersonFactWriteback(ctx context.Context, botMsg *onebot.ConversationMessage, replyText string, ex *personinfo.FactExtractor) error {
	target, evidence, sessionID, platform, userID, groupID := a.resolvePersonFactTarget(ctx, botMsg)
	if target == nil || !target.IsKnown || len(evidence) == 0 {
		return nil
	}
	evidenceText := formatPersonFactEvidence(evidence)
	facts, err := ex.ExtractFactsFromReply(ctx, targetDisplayName(target), replyText, evidenceText)
	if err != nil || len(facts) == 0 {
		return err
	}

	evidenceIDs := make([]string, 0, len(evidence))
	for _, log := range evidence {
		if log.MessageID != 0 {
			evidenceIDs = append(evidenceIDs, strconv.FormatInt(log.MessageID, 10))
		}
	}

	personName := targetDisplayName(target)
	writebackErrs := make([]error, 0, len(facts))
	for _, fact := range facts {
		if strings.TrimSpace(fact.Fact) == "" {
			continue
		}
		if err := personinfo.EnqueueMemoryWriteback(ctx, personinfo.AnswerWritebackInput{
			PersonName:         personName,
			MemoryContent:      fact.Fact,
			ChatID:             sessionID,
			PersonID:           target.PersonID,
			Platform:           platform,
			UserID:             userID,
			GroupID:            groupID,
			EvidenceSource:     "user_supported",
			EvidenceMessageIDs: evidenceIDs,
			// fact_claim 只放三个固定键，对齐 Python memory_flow_service.py:184-192：
			// 语义判断由提取模型完成，来源 ID 由本次实际输入的消息确定，
			// 不能把模型生成的消息 ID / 引用当作真实证据写库。
			FactClaim: map[string]any{
				"authority":       "direct_user",
				"stability":       "stable",
				"profile_section": "stable_facts",
			},
		}); err != nil {
			writebackErrs = append(writebackErrs, err)
		}
	}
	return errors.Join(writebackErrs...)
}

type personFactTarget struct {
	PersonID string
	IsKnown  bool
	Profile  *personinfo.Profile
}

func targetDisplayName(t *personFactTarget) string {
	if t == nil || t.Profile == nil {
		return ""
	}
	name := strings.TrimSpace(t.Profile.PersonName)
	if name == "" {
		name = strings.TrimSpace(t.Profile.Nickname)
	}
	return name
}

func (a *Agent) resolvePersonFactTarget(ctx context.Context, botMsg *onebot.ConversationMessage) (*personFactTarget, []memory.MessageLog, string, string, string, string) {
	if botMsg == nil {
		return nil, nil, "", "", "", ""
	}
	reg := personinfo.DefaultRegistry()
	platform := a.conversationPlatform(botMsg.ConversationKind, botMsg.TargetID)
	if strings.TrimSpace(platform) == "" {
		platform = platformio.PlatformQQ
	}
	sessionID := a.resolveMaiBotSessionID(ctx, botMsg.ConversationKind, botMsg.TargetID)
	groupID := ""
	userID := ""
	if botMsg.ConversationKind == memory.ConversationKindGroup {
		groupID = strconv.FormatInt(botMsg.TargetID, 10)
	} else if botMsg.ConversationKind == memory.ConversationKindPrivate {
		userID = strconv.FormatInt(botMsg.TargetID, 10)
		pid := personinfo.PersonID(platform, userID)
		if userID != "" && userID != strconv.FormatInt(a.SelfID(), 10) && reg.IsKnown(ctx, pid) {
			prof, _ := reg.GetProfile(ctx, pid)
			return &personFactTarget{PersonID: pid, IsKnown: true, Profile: prof}, collectTargetEvidence(ctx, a, botMsg, pid), sessionID, platform, userID, groupID
		}
	}

	if botMsg.Reply != nil && botMsg.Reply.MessageID != 0 {
		if log, err := a.memory.WithContext(ctx).GetMessageLogByScope(botMsg.ConversationKind, botMsg.TargetID, botMsg.Reply.MessageID); err == nil && log != nil {
			if t := targetFromMessageLog(ctx, reg, log, platform); t != nil {
				return t, []memory.MessageLog{*log}, sessionID, platform, strconv.FormatInt(log.UserID, 10), groupID
			}
		}
	}

	logs, err := a.memory.GetRecentMessagesScope(ctx, botMsg.ConversationKind, botMsg.TargetID, botMsg.MessageID, 8, 0)
	if err != nil {
		return nil, nil, sessionID, platform, userID, groupID
	}
	var target *personFactTarget
	var evidence []memory.MessageLog
	for i := len(logs) - 1; i >= 0; i-- {
		log := logs[i]
		if log.UserID == a.SelfID() {
			continue
		}
		if t := targetFromMessageLog(ctx, reg, &log, platform); t != nil {
			target = t
			evidence = collectTargetEvidence(ctx, a, botMsg, t.PersonID)
			userID = strconv.FormatInt(log.UserID, 10)
			break
		}
	}
	return target, evidence, sessionID, platform, userID, groupID
}

func targetFromMessageLog(ctx context.Context, reg *personinfo.Registry, log *memory.MessageLog, platform string) *personFactTarget {
	if log == nil || log.UserID == 0 {
		return nil
	}
	pid := personinfo.PersonID(platform, strconv.FormatInt(log.UserID, 10))
	if !reg.IsKnown(ctx, pid) {
		return nil
	}
	prof, err := reg.GetProfile(ctx, pid)
	if err != nil {
		return nil
	}
	return &personFactTarget{PersonID: pid, IsKnown: true, Profile: prof}
}

func collectTargetEvidence(ctx context.Context, a *Agent, botMsg *onebot.ConversationMessage, personID string) []memory.MessageLog {
	logs, err := a.memory.GetRecentMessagesScope(ctx, botMsg.ConversationKind, botMsg.TargetID, botMsg.MessageID, 8, 0)
	if err != nil {
		return nil
	}
	platform := a.conversationPlatform(botMsg.ConversationKind, botMsg.TargetID)
	if strings.TrimSpace(platform) == "" {
		platform = platformio.PlatformQQ
	}
	out := make([]memory.MessageLog, 0, 3)
	for i := len(logs) - 1; i >= 0 && len(out) < 3; i-- {
		log := logs[i]
		if log.UserID == a.SelfID() {
			continue
		}
		if personinfo.PersonID(platform, strconv.FormatInt(log.UserID, 10)) != personID {
			continue
		}
		text := strings.TrimSpace(log.ProcessedPlainText)
		if text == "" {
			text = strings.TrimSpace(log.ProcessedPlainText)
		}
		if text == "" {
			continue
		}
		out = append(out, log)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func formatPersonFactEvidence(logs []memory.MessageLog) string {
	var b strings.Builder
	for _, log := range logs {
		text := strings.TrimSpace(log.ProcessedPlainText)
		if text == "" {
			text = strings.TrimSpace(log.ProcessedPlainText)
		}
		if text == "" {
			continue
		}
		name := strings.TrimSpace(log.UserNickname)
		if name != "" {
			b.WriteString(name)
			b.WriteString(": ")
		}
		b.WriteString(text)
		b.WriteString(" [目标用户发言]\n")
	}
	return strings.TrimSpace(b.String())
}
