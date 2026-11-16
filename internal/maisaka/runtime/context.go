package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/config"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"
	maisakactx "MaiGo/internal/maisaka/context"
	"MaiGo/internal/platform_io/drivers/onebot"

	"github.com/cloudwego/eino/components/tool"
)

func (a *Agent) buildGroupContext(groupID int64) string {
	if a.memory == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	target, err := a.memory.WithContext(ctx).GetConversationTarget(ctx, memory.ConversationKindGroup, groupID)
	if err != nil || target == nil {
		return ""
	}
	name := strings.TrimSpace(target.Name)
	if name == "" {
		return ""
	}
	return fmt.Sprintf("- 群名: %s", name)
}

func resolveMessageDisplayName(groupCard, nickname string) string {
	if card := strings.TrimSpace(groupCard); card != "" {
		return card
	}
	if name := strings.TrimSpace(nickname); name != "" {
		return name
	}
	return ""
}

func (a *Agent) resolveMentionDisplayName(ctx context.Context, msg *onebot.ConversationMessage, userID int64) string {
	selfID := a.SelfID()
	if selfID > 0 && userID == selfID {
		return botMentionDisplayName(a.botName())
	}
	if displayName := strings.TrimSpace(msg.AtNames[userID]); displayName != "" {
		return displayName
	}
	if msg.ConversationKind == memory.ConversationKindPrivate {
		if userID == msg.UserID && msg.Nickname != "" {
			return msg.Nickname
		}
		return fmt.Sprintf("%d", userID)
	}
	return fmt.Sprintf("%d", userID)
}

// buildConversationToolContext 构造工具执行上下文，会话内的消息编号与媒体都由会话维护
func (a *Agent) buildConversationToolContext(ctx context.Context, kind string, targetID int64, session *Session) context.Context {
	instance := a.instanceConfig()
	sessionID := a.resolveMaiBotSessionID(ctx, kind, targetID)
	memoryQueryDefaultLimit := 0
	if instance != nil {
		memoryQueryDefaultLimit = instance.Bot.AMemorix.Integration.MemoryQueryDefaultLimit
	}
	effectiveReplyTo := int64(0)
	accountID := ""
	if a.chatSessions != nil && strings.TrimSpace(sessionID) != "" {
		if record, err := a.chatSessions.GetExistingBySessionID(ctx, sessionID); err == nil && record != nil && record.AccountID != nil {
			accountID = strings.TrimSpace(*record.AccountID)
		}
	}
	tc := &tools.ToolContext{
		ConversationKind:        kind,
		Platform:                a.conversationPlatform(kind, targetID),
		AccountID:               accountID,
		SessionID:               sessionID,
		TargetID:                targetID,
		MemoryMgr:               a.memory.WithContext(ctx),
		Session:                 session,
		StickerDir:              config.Boot().StickerDir,
		Media:                   a.currentMedia(),
		SnapshotLogID:           session.LatestLogID(),
		RunID:                   newRunID(),
		MemoryReadVersions:      map[uint]uint64{},
		EffectiveReplyTo:        &effectiveReplyTo,
		EnableReplyAt:           kind == memory.ConversationKindGroup && a.replyAtEnabled(),
		MemoryQueryDefaultLimit: memoryQueryDefaultLimit,
		ResolvePicture: func(msgID string, index int) (string, string, error) {
			return resolveSessionPicture(session, msgID, index)
		},
		ResolveEmojiAttachment: func(callCtx context.Context, reference any) (tools.EmojiAttachment, error) {
			return a.resolveEmojiAttachment(callCtx, session, sessionID, reference)
		},
		SendEmojiAttachment: func(callCtx context.Context, target int64, attachment tools.EmojiAttachment) error {
			return a.sendEmojiAttachment(callCtx, kind, target, attachment)
		},
		SpeakCallback: func(callCtx context.Context, gid int64, content string, replyTo int64, mentions []int64) error {
			if kind == memory.ConversationKindPrivate {
				return a.doPrivateSpeak(callCtx, gid, content, replyTo)
			}
			return a.doSpeak(callCtx, gid, content, replyTo, mentions)
		},
		ReplySendCallback: func(callCtx context.Context, gid int64, content string, replyTo int64, mentions []int64) (tools.ReplySendOutcome, error) {
			return a.doReplySend(callCtx, session, kind, gid, content, replyTo, mentions)
		},
		ResolveMessageAuthor: func(msgID string) (tools.MessageAuthor, bool) {
			return a.resolveMessageAuthor(session, msgID)
		},
		ResolveBotAccount: func(callCtx context.Context, platform, preferredAccountID string) (string, error) {
			return a.resolveBotAccount(callCtx, platform, preferredAccountID)
		},
		SendStickerCallback: func(callCtx context.Context, gid int64, fileName string, description string) error {
			if kind == memory.ConversationKindPrivate {
				return a.doPrivateSticker(callCtx, gid, fileName, description)
			}
			return a.doSendSticker(callCtx, gid, fileName, description)
		},
		SendImageURLCallback: func(callCtx context.Context, imageURL string) error {
			if kind == memory.ConversationKindPrivate {
				return a.doPrivateImage(callCtx, targetID, imageURL)
			}
			return a.doSendImageURL(callCtx, targetID, imageURL)
		},
		GetNewMessagesCallback: func(callCtx context.Context) (any, error) {
			return a.readNewMessages(callCtx, session)
		},
		ResolveMessageSender: func(msgID string) (int64, bool) {
			runtime := session.MaisakaRuntime()
			if runtime == nil {
				return 0, false
			}
			raw := strings.TrimSpace(msgID)
			history := runtime.History()
			messageIDs := make([]string, 0, len(history))
			for _, item := range history {
				if item.MessageID != "" {
					messageIDs = append(messageIDs, item.MessageID)
				}
			}
			if expanded, ok := maisaka.ExpandMessageIDAliases(raw, maisaka.BuildAliasMap(messageIDs)).(string); ok && expanded != "" {
				raw = expanded
			}
			message, ok := runtime.FindSourceMessageByID(raw)
			if !ok {
				return 0, false
			}
			senderID, err := strconv.ParseInt(strings.TrimSpace(message.UserID), 10, 64)
			if err != nil || senderID <= 0 {
				return 0, false
			}
			return senderID, true
		},
		ReplyerCallback: func(callCtx context.Context, replyReference string, msgID string, replyToolArgs map[string]any) (tools.ReplyerCallbackResult, error) {
			var target *maisaka.ContextMessage
			var history []maisaka.ContextMessage
			if runtime := session.MaisakaRuntime(); runtime != nil {
				history = runtime.History()
				if raw := strings.TrimSpace(msgID); raw != "" {
					if message, ok := runtime.FindSourceMessageByID(raw); ok {
						target = &message
					}
				}
			}
			args := map[string]any{}
			for key, value := range replyToolArgs {
				args[key] = value
			}
			args["reply_reference"] = replyReference
			if raw := strings.TrimSpace(msgID); raw != "" {
				args["msg_id"] = raw
			}
			result, err := a.generateReplyerTextResult(callCtx, replyReference, target, history, kind == memory.ConversationKindGroup, args)
			if err != nil {
				return tools.ReplyerCallbackResult{}, err
			}
			return tools.ReplyerCallbackResult{
				Text:          result.Text,
				MonitorDetail: result.MonitorDetail,
				PromptHTMLURI: result.PromptHTMLURI,
			}, nil
		},
		PreviousTargetReply: func(msgID string) string {
			return session.previousReplyToTarget(msgID)
		},
		RecordReplyTarget: func(msgID string, content string) {
			session.recordReplyToTarget(msgID, content)
		},
		ImageMemorySearch: func(callCtx context.Context, messageID string, componentPath string, limit int) (map[string]any, error) {
			return searchImageMemory(callCtx, session, sessionID, messageID, componentPath, limit)
		},
		FileToolHost:           a.newFileToolHost(sessionID),
		SessionMessageCallback: a.deliverSessionMessage,
		InternalTaskCallback:   a.EnqueueInternalTaskWithImages,
		SourceImages:           session.SourceImages,
		ReadForwardCallback: func(callCtx context.Context, id string, index int) (any, error) {
			return a.readForward(callCtx, id, index)
		},
		GroupInfoCallback: func(callCtx context.Context) string {
			if kind != memory.ConversationKindGroup {
				return ""
			}
			return a.buildGroupContext(targetID)
		},
		RecallCallback: func(messageID int64, arrivalSeq uint64) {
			a.onRecall(kind, targetID, messageID, arrivalSeq)
		},
		BeginWait: func(seconds int) error {
			a.beginConversationWait(kind, targetID, seconds)
			return nil
		},
		BeginWaitCounted: func(seconds int) (int, error) {
			return a.beginConversationWaitCounted(kind, targetID, seconds)
		},
		PlannerMessageSteering: a.plannerMessageSteeringEnabled(),
		LLMRouter:              a.llmRouterSnapshot(),
		SubagentRunner: func(runCtx context.Context, task string, granted []string, maxRounds int) (string, map[string]int, error) {
			return a.runMaisakaSubagent(runCtx, kind, targetID, task, granted, maxRounds)
		},
	}
	tc.AgentFilesHost = a.newAgentFilesHost(sessionID)
	if instance := a.instanceBotConfig(); instance != nil {
		tc.BotName = strings.TrimSpace(instance.Bot.Nickname)
	}
	if instance != nil {
		tc.ToolSearchTailLoading = instance.Bot.Experimental.ToolSearchTailLoading
	}
	if generation := generationFrom(ctx); generation != nil {
		var chatTools []tool.BaseTool
		if kind == memory.ConversationKindPrivate {
			chatTools = generation.privateTools
		} else {
			chatTools = generation.groupTools
		}
		tc.ToolCatalog = tools.BuildToolCatalog(ctx, chatTools)
		tc.DeferredToolInfos = tools.CollectDeferredToolInfos(ctx, chatTools, tc.ToolCatalog)
		tc.SubagentLLM = generation.plannerModel
		tc.UtilsLLM = generation.utilsModel
	}
	return tools.WithToolContext(ctx, tc)
}

// newRunID 生成当前 ReAct 的幂等分组标识
func newRunID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// readForward 读取合并转发内容，index 为负表示先看这份转发的概览

// resolveSessionPicture 把上下文图片解析为发送层可接受的路径或 data URL
// 判定顺序与失败文案对齐 Python resolve_context_image，无法读取时不再把原始引用当路径返回
func resolveSessionPicture(session *Session, msgID string, index int) (string, string, error) {
	if session == nil {
		return "", "", fmt.Errorf("图片附件解析能力未初始化。")
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil {
		return "", "", fmt.Errorf("图片附件解析能力未初始化。")
	}
	history := runtime.History()
	image, err := maisakactx.ResolveContextImage(history, msgID, index)
	if err != nil {
		return "", "", err
	}
	reference := maisakactx.ImageReference(maisakactx.ExpandContextMessageID(history, msgID))
	ref := strings.TrimSpace(image.MediaRef)
	if ref == "" {
		return "", "", fmt.Errorf("目标图片数据不可读取：%s，index=%d", reference, index)
	}
	if resolved, found := session.ResolveMediaByHash(ref); found {
		if path := strings.TrimSpace(resolved.Path); path != "" {
			return path, image.Description, nil
		}
		return "", "", fmt.Errorf("目标图片数据不可读取：%s，index=%d", reference, index)
	}
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "base64://") {
		return ref, image.Description, nil
	}
	if info, statErr := os.Stat(ref); statErr == nil && !info.IsDir() {
		return ref, image.Description, nil
	}
	// 媒体哈希无法落到本地文件时报不可读取，不能把哈希当路径交给发送层
	if mediaLooksLikeHash(ref) {
		return "", "", fmt.Errorf("目标图片数据不可读取：%s，index=%d", reference, index)
	}
	if isBase64PicturePayload(ref) {
		return "base64://" + ref, image.Description, nil
	}
	return "", "", fmt.Errorf("目标图片数据不可读取：%s，index=%d", reference, index)
}

// mediaLooksLikeHash 判断引用是否是媒体短哈希或完整 md5
func mediaLooksLikeHash(ref string) bool {
	if len(ref) != 8 && len(ref) != 32 {
		return false
	}
	for _, char := range ref {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}
	return true
}

// isBase64PicturePayload 判断引用是否为可直接发送的 base64 图片数据
func isBase64PicturePayload(ref string) bool {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if decoded, decodeErr := encoding.DecodeString(ref); decodeErr == nil && len(decoded) > 0 {
			return true
		}
	}
	return false
}

func (a *Agent) readForward(ctx context.Context, id string, index int) (any, error) {
	forward, err := a.memory.WithContext(ctx).GetMessageForward(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("合并转发 %s 不存在或已过期", id)
	}
	nodes := forward.Nodes
	if index < 0 {
		summaries := make([]tools.ForwardMessageSummary, 0, len(nodes))
		for position, node := range nodes {
			summaries = append(summaries, tools.ForwardMessageSummary{
				Index:   position,
				Sender:  node.Sender,
				Preview: forwardPreview(node.Content),
				Nested:  node.Nested,
			})
		}
		return &tools.ReadForwardOutput{Success: true, ID: id, Count: len(nodes), Messages: summaries}, nil
	}
	if index >= len(nodes) {
		return nil, fmt.Errorf("合并转发 %s 一共 %d 条消息", id, len(nodes))
	}
	node := nodes[index]
	return &tools.ReadForwardOutput{
		Success: true,
		ID:      id,
		Index:   index,
		Sender:  node.Sender,
		Content: node.Content,
		Nested:  node.Nested,
	}, nil
}

// forwardPreview 截取转发消息的预览
func forwardPreview(content string) string {
	runes := []rune(content)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return content
}

// readNewMessages 读取本轮思考开始后到达的新消息
func (a *Agent) readNewMessages(ctx context.Context, session *Session) (any, error) {
	tc := tools.GetToolContext(ctx)
	if tc == nil {
		return nil, tools.NewTerminalToolError(fmt.Errorf("消息读取能力未初始化"))
	}
	// 只返回上次读取之后的新消息，重复读取不会把同一批消息再喂一遍
	floor := max(tc.ThinkStartArrivalSeq, tc.ObservedThroughSeq)
	messages := make([]map[string]any, 0, 4)
	observed := floor
	for _, user := range session.MessagesAfter(floor) {
		if user.TranscriptID != 0 {
			tc.VisibleTranscriptIDs = append(tc.VisibleTranscriptIDs, user.TranscriptID)
		}
		messages = append(messages, map[string]any{
			"message_id":  user.MessageID,
			"sender_id":   user.SenderID,
			"nickname":    user.Nickname,
			"content":     user.Text,
			"arrival_seq": user.ArrivalSeq,
		})
		observed = max(observed, user.ArrivalSeq)
	}
	tc.ObservedThroughSeq = observed
	return map[string]any{"success": true, "complete": true, "messages": messages}, nil
}
