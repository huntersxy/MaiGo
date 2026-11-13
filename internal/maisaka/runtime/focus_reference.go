package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/learners"
	"maibot-go/internal/maisaka"
	mmemory "maibot-go/internal/maisaka/memory"

	"go.uber.org/zap"
)

// BuildPlannerInjectedUserMessages 复用成员聊天的自动记忆与人物画像注入
func (m *focusGroupMember) BuildPlannerInjectedUserMessages(profile *maisaka.ContextMessage, sources []maisaka.ContextMessage, deferredNotice string) []string {
	if m == nil || m.runtime == nil || m.session == nil {
		return nil
	}
	injected := make([]string, 0, 3)
	if deferredNotice != "" {
		injected = append(injected, deferredNotice)
	}
	if m.agent == nil || m.session.memory == nil {
		return injected
	}
	ctx, cancel := m.agent.persistenceContext()
	defer cancel()
	userIDs := focusMemberUserIDs(profile, sources)
	pendingTexts := make([]string, 0, len(sources))
	for _, source := range sources {
		if text := strings.TrimSpace(source.Content); text != "" {
			pendingTexts = append(pendingTexts, text)
		}
	}
	if snapshot, err := m.session.memory.WithContext(ctx).BuildAutoReference(ctx, m.session.kind, m.session.targetID, userIDs, pendingTexts); err == nil && strings.TrimSpace(snapshot.Text) != "" {
		injected = append(injected, snapshot.Text)
	} else if err != nil {
		zap.L().Debug("Focus 共享组自动记忆注入失败", zap.String("session_id", m.sessionID), zap.Error(err))
	}
	hasExtras := m.runtime.HasPlannerExtraInjections()
	if hasExtras {
		if extras := m.runtime.BuildPlannerExtraInjections(ctx, profile, sources); len(extras) > 0 {
			injected = append(injected, extras...)
		}
	} else if profile != nil {
		anchor := anchorMessageFromFocus(*profile, m.session.kind == memory.ConversationKindGroup)
		if blocks := mmemory.BuildPersonProfileInjectionMessages(&focusPersonProfileService{agent: m.agent, ctx: ctx}, anchor, nil, mmemory.PersonProfileMaxProfiles); len(blocks) > 0 {
			injected = append(injected, blocks...)
		}
	}
	return injected
}

// RefreshJargonReferenceMessage 复用成员聊天最近历史生成黑话参考
func (m *focusGroupMember) RefreshJargonReferenceMessage() *maisaka.ContextMessage {
	if m == nil || m.runtime == nil || m.session == nil {
		return nil
	}
	ctx, cancel := m.agent.persistenceContext()
	defer cancel()
	history := m.runtime.History()
	messages := make([]learners.ContextMessage, 0, len(history))
	excluded := make([]string, 0)
	for _, message := range history {
		if learners.IsJargonReferenceText(message.Content) {
			excluded = append(excluded, learners.ExtractJargonReferenceContents(message.Content)...)
			continue
		}
		if message.Role != maisaka.RoleUser || message.Source != maisaka.SourceUser {
			continue
		}
		text := strings.TrimSpace(message.Content)
		if text == "" {
			continue
		}
		messages = append(messages, learners.ContextMessage{
			MessageID:  message.MessageID,
			SourceKind: "user",
			Text:       text,
			Platform:   message.Platform,
			UserID:     message.UserID,
		})
	}
	if len(messages) == 0 {
		return nil
	}
	matches := learners.MatchJargon(ctx, m.sessionID, messages, excluded)
	if len(matches) == 0 {
		return nil
	}
	body := strings.TrimSpace(learners.BuildJargonReferenceBody(matches))
	if body == "" {
		return nil
	}
	return &maisaka.ContextMessage{
		Role:           maisaka.RoleReference,
		Content:        body,
		Source:         maisaka.SourceReference,
		Timestamp:      time.Now(),
		CountInContext: false,
		SessionID:      m.sessionID,
		Metadata:       map[string]any{"remaining_uses": nil},
	}
}

// ChatInstructions 返回当前聊天的群聊补充指令
func (m *focusGroupMember) ChatInstructions() string {
	if m == nil || m.agent == nil || m.session == nil {
		return ""
	}
	ctx, cancel := m.agent.persistenceContext()
	defer cancel()
	return m.agent.focusChatInstructions(ctx, m.session)
}

// AppendToolResultMediaMessages 把工具结果里的媒体项回填为后续上下文消息
func (m *focusGroupMember) AppendToolResultMediaMessages(call maisaka.ToolCall, result maisaka.ToolExecutionResult) []maisaka.ContextMessage {
	if len(result.Items) == 0 {
		return nil
	}
	out := make([]maisaka.ContextMessage, 0, len(result.Items))
	callID := strings.TrimSpace(call.CallID)
	if callID == "" {
		callID = strings.TrimSpace(call.Name)
	}
	if callID == "" {
		callID = "tool"
	}
	for index, item := range result.Items {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "image", "emoji", "media", "picture":
		default:
			continue
		}
		ref := strings.TrimSpace(item.Data)
		if ref == "" {
			continue
		}
		// 索引格式对齐 Python tool_result:<call_id>:<content_items 中的 1 基序号>
		mediaIndex := fmt.Sprintf("tool_result:%s:%d", callID, index+1)
		message := maisaka.ContextMessage{
			Role:           maisaka.RoleUser,
			Content:        strings.TrimSpace(item.Text),
			Source:         maisaka.SourceToolResult,
			Timestamp:      time.Now(),
			CountInContext: false,
			MessageID:      mediaIndex,
			ToolCallID:     call.CallID,
			ToolName:       call.Name,
			MediaIndex:     mediaIndex,
			SessionID:      m.sessionID,
			Images: []maisaka.ImageComponent{{
				MediaRef:    ref,
				Format:      strings.TrimSpace(item.MIMEType),
				Description: strings.TrimSpace(item.Text),
			}},
		}
		out = append(out, message)
	}
	return out
}

// BuildToolResultHistoryContent 工具结果进入共享历史时优先使用压缩结果
func (m *focusGroupMember) BuildToolResultHistoryContent(call maisaka.ToolCall, result maisaka.ToolExecutionResult, condensed string) string {
	if strings.TrimSpace(condensed) != "" {
		return condensed
	}
	return result.Content
}

// RecordToolExecutionEffects 工具副作用已在 InvokeTool 内完成；这里只记录监控可见的元数据
func (m *focusGroupMember) RecordToolExecutionEffects(invocation maisaka.ToolInvocation, result maisaka.ToolExecutionResult, spec *maisaka.ToolSpec) {
	if m == nil || m.runtime == nil {
		return
	}
	name := invocation.ToolName
	if name == "" && spec != nil {
		name = spec.Name
	}
	m.runtime.UpdateStageStatus("工具结果", fmt.Sprintf("%s 已执行，success=%t", name, result.Success))
}

// focusPersonProfileService 把宿主记忆系统接入 Maisaka 人物画像端口
type focusPersonProfileService struct {
	agent *Agent
	ctx   context.Context
}

// ResolvePersonID 解析平台用户到人物编号；当前宿主以平台用户编号作为成员画像主键
func (s *focusPersonProfileService) ResolvePersonID(platform, userID, personName string) (string, error) {
	if s == nil || s.agent == nil || s.agent.memory == nil {
		return "", fmt.Errorf("人物画像服务未初始化")
	}
	personID := strings.TrimSpace(userID)
	if personID == "" {
		return "", fmt.Errorf("人物画像缺少 user_id：platform=%s name=%s", platform, personName)
	}
	return personID, nil
}

// ProfileAdmin 查询宿主成员画像；当前只暴露昵称，未接 Memorix 富画像
func (s *focusPersonProfileService) ProfileAdmin(personID string, limit int, contextText string) (map[string]any, error) {
	if s == nil || s.agent == nil || s.agent.memory == nil {
		return nil, fmt.Errorf("人物画像服务未初始化")
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(personID), 10, 64)
	if err != nil || userID <= 0 {
		return nil, fmt.Errorf("人物画像编号非法：%s", personID)
	}
	profile, err := s.agent.memory.GetMemberProfile(userID)
	if err != nil || profile == nil {
		return map[string]any{"success": false}, nil
	}
	text := strings.TrimSpace(profile.Nickname)
	if text == "" {
		return map[string]any{"success": false}, nil
	}
	return map[string]any{
		"success":      true,
		"person_name":  text,
		"profile_text": "昵称：" + text,
	}, nil
}

func anchorMessageFromFocus(message maisaka.ContextMessage, isGroup bool) mmemory.AnchorMessage {
	return mmemory.AnchorMessage{
		MessageID: message.MessageID,
		Platform:  message.Platform,
		IsGroup:   isGroup,
		UserID:    message.UserID,
		PlainText: message.Content,
	}
}

func focusMemberUserIDs(profile *maisaka.ContextMessage, sources []maisaka.ContextMessage) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(sources)+1)
	add := func(raw string) {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if profile != nil {
		add(profile.UserID)
	}
	for i := len(sources) - 1; i >= 0 && len(out) < 8; i-- {
		add(sources[i].UserID)
	}
	return out
}
