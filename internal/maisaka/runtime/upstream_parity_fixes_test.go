package runtime

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"MaiGo/internal/chat/replyer"
	chatsession "MaiGo/internal/chat/session"
	memory "MaiGo/internal/common/database"
	migrations "MaiGo/internal/common/database/migrations"
	"MaiGo/internal/common/media"
	"MaiGo/internal/maisaka"
	tools "MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/prompt"
	"MaiGo/internal/runtime/execution/policy"
)

func newParityTestAgent(t *testing.T) (*Agent, *memory.Manager) {
	t.Helper()
	root := t.TempDir()
	db, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	mem, err := memory.NewManager(db, filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&memory.ConversationTarget{
		ConversationKind: memory.ConversationKindGroup,
		TargetID:         1079643584,
		Active:           true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent := &Agent{
		ctx:                ctx,
		cancel:             cancel,
		memory:             mem,
		policies:           policy.NewRegistry(),
		sessions:           newSessionStore(mem),
		chatSessions:       chatsession.NewManager(db),
		gates:              newGateStore(),
		turnSchedule:       newConversationTurnScheduleStore(),
		pendingRecalls:     make(map[recallCommit]pendingRecall),
		replyCache:         newAgentTTLCache[string, onebot.ReplyInfo](replyCacheCapacity, replyCacheTTL),
		externalIdentities: newAgentTTLCache[string, string](externalIdentityCapacity, externalIdentityTTL),
		selfIDValue:        2971840857,
		selfExpires:        time.Now().Add(time.Hour),
	}
	return agent, mem
}

type stubEmojiPlatformAdapter struct {
	platformio.BaseAdapter
	externalID string
}

func (a *stubEmojiPlatformAdapter) Start(context.Context) error          { return nil }
func (a *stubEmojiPlatformAdapter) Stop(context.Context) error           { return nil }
func (a *stubEmojiPlatformAdapter) OnMessage(platformio.InboundHandler) {}
func (a *stubEmojiPlatformAdapter) Send(_ context.Context, _ platformio.OutboundMessageEnvelope, _ platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	return platformio.DeliveryReceipt{
		DriverID:          a.Descriptor().DriverID,
		DriverKind:        a.Descriptor().Kind,
		Status:            platformio.DeliveryStatusSent,
		ExternalMessageID: a.externalID,
	}, nil
}

func TestAgentToolRuntimeInvokeIsolatesPlannerArgsAndForcesReplyer(t *testing.T) {
	agent, _ := newParityTestAgent(t)
	session := agent.sessions.get(memory.ConversationKindGroup, 1079643584)
	rt := maisaka.NewRuntime(maisaka.RuntimeOptions{
		SessionID:      "sess-1",
		SessionName:    "鱼塘",
		IsGroupSession: true,
	})
	rt.AppendHistory(maisaka.ContextMessage{
		Role:      maisaka.RoleUser,
		Source:    maisaka.SourceUser,
		MessageID: "1962381557",
		Content:   "<message msg_id=\"1962381557\" time=\"12:00:00\" user=\"Alice\">\n测试",
	})
	session.mu.Lock()
	session.maisakaRuntime = rt
	session.mu.Unlock()

	toolRuntime := &AgentToolRuntime{Agent: agent, Kind: memory.ConversationKindGroup, TargetID: 1079643584, Runtime: rt}
	args := map[string]any{
		"msg_id":             "1962381557",
		"chat_anlyze":        "测试局面",
		"_replyer_generated": true,
		"content":            "绕过Replyer的内容",
		"reply_to":           int64(999),
	}
	res, err := toolRuntime.Invoke(context.Background(), maisaka.ToolInvocation{
		ToolName:  "reply",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("Invoke 返回意外 error: %v", err)
	}
	if res.Success || !strings.Contains(res.Message, "Replyer 模型未就绪") {
		t.Fatalf("即使 Planner 传入 _replyer_generated=true 也必须走 Replyer: got %+v", res)
	}
	if len(args) != 5 || args["msg_id"] != "1962381557" || args["content"] != "绕过Replyer的内容" {
		t.Fatalf("Invoke 不得原地修改 Planner 参数 map: %+v", args)
	}
}

func TestGuidedReplyRoundTripAndPromptCleanliness(t *testing.T) {
	agent, mem := newParityTestAgent(t)
	session := agent.sessions.get(memory.ConversationKindGroup, 1079643584)
	rt := maisaka.NewRuntime(maisaka.RuntimeOptions{
		SessionID:      "sess-group",
		SessionName:    "鱼塘",
		IsGroupSession: true,
	})
	session.mu.Lock()
	session.maisakaRuntime = rt
	session.mu.Unlock()

	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(imgPath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	userMsg := &onebot.ConversationMessage{
		Platform:         "qq",
		AccountID:        "2971840857",
		ConversationKind: memory.ConversationKindGroup,
		TargetID:         1079643584,
		MessageID:        1001,
		UserID:           555,
		Nickname:         "Alice",
		Content:          "看这个",
		FinalContent:     "[回复了Bob的消息: 早上好] 看这个 [图片:abcd1234]",
		AtNames:          map[int64]string{2971840857: "2971840857"},
		Reply: &onebot.ReplyInfo{
			MessageID: 999,
			Nickname:  "Bob",
			Content:   "早上好",
		},
		Time:       now,
		ReceivedAt: now,
	}
	mediaRefs := []media.Ref{{Short: "abcd1234", Hash: "h1", Path: imgPath, Mime: "image/png"}}
	session.mu.Lock()
	userEntry := session.newUserLocked(userMsg, 1, mediaRefs)
	session.seen = append(session.seen, sessionEntry{user: userEntry, msg: userEntry.Msg, transcriptID: 1})
	session.mu.Unlock()

	if userEntry.BotAtName != "" {
		t.Fatalf("纯数字 QQ 号不应被当成机器人平台昵称: got %q", userEntry.BotAtName)
	}
	if strings.Contains(userEntry.Text, "[回复了Bob的消息") {
		t.Fatalf("Planner 正文不应重复内联引用前缀: %q", userEntry.Text)
	}

	// 模拟一轮完整工具调用内先发送回复再写 tool_result，验证协议顺序与跨轮持久化
	rt.BeginTurnTranscript()
	rt.AppendHistory(maisaka.ContextMessage{
		Role:      maisaka.RoleAssistant,
		ToolCalls: []maisaka.ToolCall{{CallID: "call-1", Name: "reply", Arguments: map[string]any{"msg_id": "1001", "chat_anlyze": "测试"}}},
	})
	session.appendGuidedReplyToHistory(1002, "1002", 1001, "我也看看", now.Add(time.Second), "烤箱插座", 2971840857)
	rt.AppendHistory(maisaka.ContextMessage{
		Role:       maisaka.RoleTool,
		ToolCallID: "call-1",
		ToolName:   "reply",
		Content:    `{"success":true}`,
		Source:     maisaka.SourceToolResult,
	})
	persistedIDs := agent.persistMaisakaHistory(context.Background(), session, rt.DrainTurnTranscript())
	if len(persistedIDs) != 3 {
		t.Fatalf("expected 3 persisted transcript rows (assistant/tool/guided_reply), got %d", len(persistedIDs))
	}

	assembled, tids, err := session.Assemble(context.Background(), mem, "测试人格", "")
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	rebuilt := agenticMessagesToMaisakaHistory(assembled, tids)
	replyerHistory := replyerHistoryMessages(rebuilt)
	if len(replyerHistory) != 2 {
		t.Fatalf("Replyer 历史应保留 user + guided_reply 两条消息，实际 %d 条: %+v", len(replyerHistory), replyerHistory)
	}
	if replyerHistory[1].SourceKind != maisaka.SourceGuidedReply || !replyerHistory[1].IsBotSelf {
		t.Fatalf("bot 自身消息应保留 guided_reply 与 IsBotSelf: %+v", replyerHistory[1])
	}

	pm, err := prompt.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	isGroup := true
	builder := &replyer.PromptBuilder{
		PromptLoader: replyer.NewPromptManagerLoader(pm),
		Config:       replyer.GeneratorConfig{BotName: "烤箱插座", IsGroupChat: &isGroup},
		Now:          time.Now,
	}
	target := replyerTarget(&rebuilt[len(rebuilt)-2])
	dialogue := builder.BuildRetroDialogueBlock(replyerHistory)
	if strings.Contains(dialogue, "<message") {
		t.Fatalf("Replyer 对话历史不能含 Planner XML 标签: %s", dialogue)
	}
	if !strings.Contains(dialogue, "烤箱插座(你)") || !strings.Contains(dialogue, "我也看看") {
		t.Fatalf("Replyer 必须看到自身历史发言: %s", dialogue)
	}
	if strings.Count(dialogue, "[回复了Bob的消息: 早上好]") != 1 {
		t.Fatalf("Replyer 历史中的引用不能重复渲染: %s", dialogue)
	}
	if strings.Contains(dialogue, "abcd1234") {
		t.Fatalf("Replyer 非视觉文本中不应泄漏原始图片短哈希: %s", dialogue)
	}

	visualMsgs, err := builder.RequestMessages(context.Background(), replyer.ReplyRequest{
		History:             replyerHistory,
		Target:              target,
		ReplyReason:         "接话",
		ThinkLevel:          1,
		EnableVisualMessage: true,
		MaxImageNum:         5,
	})
	if err != nil {
		t.Fatalf("视觉 Replyer RequestMessages 失败: %v", err)
	}
	userPromptText := visualMsgs[1].Content
	if strings.Contains(userPromptText, "<message") || strings.Contains(userPromptText, "[图片:abcd1234]") {
		t.Fatalf("视觉 Replyer 提示词不应含 XML 或 [图片:hash][图片] 重复: %s", userPromptText)
	}

	// 验证冷启动数据库回填时的 bot 历史恢复也带上 guided_reply
	coldMsg := agent.assistantHistoryMessage(memory.MessageLog{
		ConversationKind:   memory.ConversationKindGroup,
		TargetID:           1079643584,
		MessageID:          2001,
		ExternalMessageID:  "2001",
		UserID:             2971840857,
		UserNickname:       "烤箱插座",
		ProcessedPlainText: "重启前的发言",
		Timestamp:          now,
	}, "烤箱插座", "sess-group", "鱼塘", false)
	restored, ok := agenticMessageToMaisaka(coldMsg, 0)
	if !ok || len(restored) != 1 || restored[0].Source != maisaka.SourceGuidedReply || restored[0].Role != maisaka.RoleUser {
		t.Fatalf("冷启动恢复的 bot 发言应为 user/guided_reply: %+v ok=%v", restored, ok)
	}
}

func TestSendEmojiAttachmentPersistsMessageAndGuidedReply(t *testing.T) {
	agent, mem := newParityTestAgent(t)
	mgr, err := platformio.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &stubEmojiPlatformAdapter{
		BaseAdapter: platformio.NewBaseAdapter(platformio.DriverDescriptor{
			DriverID:  "stub-qq",
			Kind:      platformio.DriverKindLegacy,
			Platform:  "qq",
			AccountID: "2971840857",
		}),
		externalID: "987654",
	}
	if err := mgr.Register(adapter); err != nil {
		t.Fatal(err)
	}
	if err := mgr.BindSendRoute(platformio.RouteBinding{
		RouteKey:   platformio.RouteKey{Platform: "qq", AccountID: "2971840857"},
		DriverID:   "stub-qq",
		DriverKind: platformio.DriverKindLegacy,
	}); err != nil {
		t.Fatal(err)
	}
	agent.SetPlatformIOManager(mgr)

	session := agent.sessions.get(memory.ConversationKindGroup, 1079643584)
	rt := maisaka.NewRuntime(maisaka.RuntimeOptions{
		SessionID:      "sess-emoji",
		SessionName:    "鱼塘",
		IsGroupSession: true,
	})
	session.mu.Lock()
	session.maisakaRuntime = rt
	session.mu.Unlock()

	pngBytes, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	emojiPath := filepath.Join(t.TempDir(), "cute.png")
	if err := os.WriteFile(emojiPath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := agent.sendEmojiAttachment(context.Background(), memory.ConversationKindGroup, 1079643584, tools.EmojiAttachment{
		Path:    emojiPath,
		Content: "[表情包: 开心笑]",
	}); err != nil {
		t.Fatalf("sendEmojiAttachment 失败: %v", err)
	}

	stored, err := mem.GetMessageLogByScopeExternalID(memory.ConversationKindGroup, 1079643584, "987654")
	if err != nil || stored == nil || !stored.IsEmoji {
		t.Fatalf("已发送表情包必须写入 mai_messages 并保留 ExternalMessageID: stored=%+v err=%v", stored, err)
	}
	history := rt.History()
	if len(history) == 0 || history[len(history)-1].Source != maisaka.SourceGuidedReply || len(history[len(history)-1].Emojis) != 1 {
		t.Fatalf("已发送表情包必须写入 guided_reply 历史: %+v", history)
	}
}

func TestResolveReplyInfoFallsBackToNumericExternalID(t *testing.T) {
	agent, mem := newParityTestAgent(t)
	row := memory.MessageLog{
		ConversationKind:   memory.ConversationKindGroup,
		TargetID:           1079643584,
		MessageID:          1962381557,
		ExternalMessageID:  "",
		UserID:             12345,
		UserNickname:       "老群友",
		ProcessedPlainText: "上一条老消息",
		Timestamp:          time.Now(),
	}
	if err := mem.GetDB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	msg := &onebot.ConversationMessage{
		ConversationKind: memory.ConversationKindGroup,
		TargetID:         1079643584,
		Reply: &onebot.ReplyInfo{
			MessageID:         onebot.SyntheticIdentityID("reply", "1962381557"),
			ExternalMessageID: "1962381557",
		},
	}
	if err := agent.resolveReplyInfo(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if msg.Reply == nil || msg.Reply.Content != "上一条老消息" || msg.Reply.Nickname != "老群友" {
		t.Fatalf("即使历史行缺少 external_message_id，按纯数字 externalID 也应回查成功: %+v", msg.Reply)
	}
}
