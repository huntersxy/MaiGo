package runtime

import (
	"context"
	"encoding/base64"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/maisaka/focus"
	"MaiGo/internal/platform_io/drivers/onebot"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

type sessionUser struct {
	LogID        uint
	TranscriptID uint
	Ref          string
	Text         string
	Content      string
	At           time.Time
	ReceivedAt   time.Time
	Mentioned    bool
	SenderID     int64
	Nickname     string
	// GroupCard 群名片，reply 回执优先使用
	GroupCard     string
	ReferencedIDs []int64
	ArrivalSeq    uint64
	Order         uint64
	MessageID     int64
	// QuoteIDs 被回复消息编号，供 Planner/replyer 的 quote 投影
	QuoteIDs     []string
	QuoteSender  string
	QuoteContent string
	Msg          *schema.AgenticMessage
	// MediaRefs 该消息缓存过的媒体引用，供跨会话传话取图
	MediaRefs []media.Ref
	// IsCommand 命令消息标记，启发式记忆召回时排除
	IsCommand      bool
	IsAgentMessage bool
	// IsInternalTask 内部任务消息标记，对齐 Python InternalContextMessage：
	// 它按助手侧写入待处理工作集，但属于模型可见的用户内容，图片可再次被传话引用
	IsInternalTask bool
	IsNotify       bool
	NoticeID       string
	Platform       string
	AtNames        map[int64]string
	BotAtName      string
}

type sessionEntry struct {
	user         *sessionUser
	msg          *schema.AgenticMessage
	transcriptID uint
	visibleSeq   uint64
}

type sessionSegment struct {
	items []sessionEntry
}

type Session struct {
	kind     string
	targetID int64
	memory   *memory.Manager

	mu                      sync.Mutex
	seen                    []sessionEntry
	pending                 []*sessionUser
	media                   *mediaIndex
	injected                uint64
	selfAt                  []time.Time
	notes                   []string
	pendingReferenceReceipt *memory.ReferenceReceipt
	nextOrder               uint64
	tokenScale              float64
	tokenSamples            int
	compactionRetryAfter    time.Time
	focusRuntime            *focus.SessionRuntime
	maisakaRuntime          *maisaka.Runtime
	maisakaLoop             *maisaka.TurnLoop
	maisakaSummary          *maisaka.ContextMessage
	maisakaSummaryPersisted string
	maisakaFocusGroup       *focus.GroupRuntime

	replyTargetsMu sync.Mutex
	replyTargets   *chatsession.ReplyTargetMemory
}

type sessionStore struct {
	mu       sync.Mutex
	memory   *memory.Manager
	sessions map[string]*Session
}

func newSessionStore(mem *memory.Manager) *sessionStore {
	return &sessionStore{memory: mem, sessions: map[string]*Session{}}
}

// all 返回当前已创建的会话快照，供停机时清理后台循环
func (s *sessionStore) all() []*Session {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		out = append(out, session)
	}
	return out
}

func (s *sessionStore) get(kind string, targetID int64) *Session {
	key := chatsession.ConversationKey(kind, targetID)
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[key]
	if !ok {
		session = &Session{
			kind: kind, targetID: targetID, memory: s.memory, media: newMediaIndex(), tokenScale: 1,
			focusRuntime: focus.NewSessionRuntime(),
		}
		s.sessions[key] = session
	}
	return session
}

func (s *Session) AppendUser(msg *onebot.ConversationMessage, logID uint, mediaRefs []media.Ref) *sessionUser {
	if msg == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.newUserLocked(msg, logID, mediaRefs)
	s.pending = append(s.pending, user)
	return user
}

// AppendAgentMessage 把另一个会话投递的内部消息加入待处理工作集
func (s *Session) AppendAgentMessage(message *schema.AgenticMessage, transcriptID uint, at time.Time) *sessionUser {
	if message == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		at = time.Now()
	}
	user := &sessionUser{
		Text: agenticMessageText(message), Msg: message, IsAgentMessage: true,
		TranscriptID: transcriptID, ReceivedAt: time.Now(), At: at, Order: s.nextOrderLocked(),
	}
	s.pending = append(s.pending, user)
	return user
}

// RestoreUser 用 transcript 中保存的原始消息体恢复用户条目
func (s *Session) RestoreUser(msg *onebot.ConversationMessage, logID uint, mediaRefs []media.Ref, transcriptID uint, visibleSeq uint64, visible bool, payload *schema.AgenticMessage) *sessionUser {
	if msg == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.newUserLocked(msg, logID, mediaRefs)
	user.TranscriptID = transcriptID
	if payload != nil {
		user.Msg = payload
		if text := agenticMessageText(payload); text != "" {
			user.Text = text
		}
	}
	if visible {
		s.seen = append(s.seen, sessionEntry{user: user, msg: user.Msg, transcriptID: transcriptID, visibleSeq: visibleSeq})
	} else {
		s.pending = append(s.pending, user)
	}
	return user
}

func (s *Session) RestorePendingObservation(payload *schema.AgenticMessage, transcriptID uint) {
	if payload == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, &sessionUser{Msg: payload, TranscriptID: transcriptID, Order: s.nextOrderLocked()})
}

func (s *Session) RestorePendingAgentMessage(payload *schema.AgenticMessage, transcriptID uint) {
	if payload == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, &sessionUser{Msg: payload, Text: agenticMessageText(payload), IsAgentMessage: true, TranscriptID: transcriptID, Order: s.nextOrderLocked()})
}

func (s *Session) RestorePendingReference(payload *schema.AgenticMessage, transcriptID uint, receipt *memory.ReferenceReceipt) {
	if payload == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, sessionEntry{msg: payload, transcriptID: transcriptID})
	s.pendingReferenceReceipt = receipt
}

func agenticMessageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var parts []string
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			parts = append(parts, block.UserInputText.Text)
		}
		if block.AssistantGenText != nil {
			parts = append(parts, block.AssistantGenText.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *Session) AppendSeenMessage(msg *schema.AgenticMessage, transcriptID uint, visibleSeq uint64) {
	if msg == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if isMaisakaSummaryMessage(msg) {
		s.maisakaSummary = &maisaka.ContextMessage{Role: maisaka.RoleAssistant, Content: agenticMessageText(msg), Source: maisaka.ContextSummarySource}
		s.maisakaSummaryPersisted = s.maisakaSummary.Content
		return
	}
	s.seen = append(s.seen, sessionEntry{msg: msg, transcriptID: transcriptID, visibleSeq: visibleSeq})
}

// isMaisakaSummaryMessage 判断一条 transcript 消息是否是 Maisaka 上下文摘要
func isMaisakaSummaryMessage(msg *schema.AgenticMessage) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	value, _ := msg.Extra["maisaka_summary"].(bool)
	return value
}

// RestoreVisibleMessage 恢复一条已经进入可见上下文的历史消息
func (s *Session) RestoreVisibleMessage(msg *schema.AgenticMessage, at time.Time, senderID int64, ref string, visibleSeq uint64) {
	if msg == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user := &sessionUser{
		Ref: ref, Content: agenticMessageText(msg), At: at, ReceivedAt: at,
		SenderID: senderID, Msg: msg, Order: s.nextOrderLocked(),
	}
	s.seen = append(s.seen, sessionEntry{user: user, msg: msg, visibleSeq: visibleSeq})
}

// FindVisibleMessage 按 message_id 在可见工作集里查找原始 schema 消息
func (s *Session) FindVisibleMessage(messageID string) (*schema.AgenticMessage, bool) {
	messageID = strings.TrimSpace(messageID)
	if s == nil || messageID == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.seen) - 1; i >= 0; i-- {
		message := s.seen[i].msg
		if message == nil {
			continue
		}
		if raw, ok := message.Extra["message_id"]; ok {
			if text, ok := raw.(string); ok && strings.TrimSpace(text) == messageID {
				return message, true
			}
		}
	}
	return nil, false
}

func (s *Session) ApplyVisibleSeqs(seqs map[uint]uint64) {
	if len(seqs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.seen {
		if seq, ok := seqs[s.seen[i].transcriptID]; ok {
			s.seen[i].visibleSeq = seq
		}
	}
	kept := s.pending[:0]
	for _, user := range s.pending {
		if seq, ok := seqs[user.TranscriptID]; ok {
			s.seen = append(s.seen, sessionEntry{user: user, msg: user.Msg, transcriptID: user.TranscriptID, visibleSeq: seq})
			continue
		}
		kept = append(kept, user)
	}
	s.pending = kept
}

// annotateSessionUserMessage 把会话消息编号与发送者写入模型消息 Extra，
// 供 Maisaka 历史投影恢复 reply/view_forward 等工具需要的真实元数据
func annotateSessionUserMessage(message *schema.AgenticMessage, user *sessionUser) {
	if message == nil || user == nil {
		return
	}
	if message.Extra == nil {
		message.Extra = map[string]any{}
	}
	if user.MessageID != 0 {
		message.Extra["message_id"] = strconv.FormatInt(user.MessageID, 10)
	} else if user.NoticeID != "" {
		message.Extra["message_id"] = user.NoticeID
	}
	if user.IsNotify {
		message.Extra["is_notify"] = true
	}
	if user.SenderID != 0 {
		message.Extra["sender_id"] = user.SenderID
	}
	if user.Nickname != "" {
		message.Extra["sender_name"] = user.Nickname
		message.Extra["nickname"] = user.Nickname
	}
	if user.GroupCard != "" {
		message.Extra["sender_cardname"] = user.GroupCard
	}
	if !user.At.IsZero() {
		message.Extra["timestamp_unix_ms"] = user.At.UnixMilli()
	}
	if !user.ReceivedAt.IsZero() {
		message.Extra["received_at_unix_ms"] = user.ReceivedAt.UnixMilli()
	}
	if user.Mentioned {
		message.Extra["is_mentioned"] = true
	}
	if user.Platform != "" {
		message.Extra["platform"] = user.Platform
	}
	if user.BotAtName != "" {
		message.Extra["bot_platform_nickname"] = user.BotAtName
	}
	if len(user.QuoteIDs) > 0 {
		message.Extra["quote_ids"] = append([]string(nil), user.QuoteIDs...)
	}
	if user.QuoteSender != "" {
		message.Extra["quote_sender"] = user.QuoteSender
	}
	if user.QuoteContent != "" {
		message.Extra["quote_content"] = user.QuoteContent
	}
}

func (s *Session) nextOrderLocked() uint64 {
	s.nextOrder++
	return s.nextOrder
}

func (s *Session) newUserLocked(msg *onebot.ConversationMessage, logID uint, mediaRefs []media.Ref) *sessionUser {
	ref := ""
	if msg.MessageID != 0 {
		ref = strconv.FormatInt(msg.MessageID, 10)
	}
	referenced := append([]int64(nil), msg.AtList...)
	quoteIDs := make([]string, 0, 1)
	quoteSender := ""
	quoteContent := ""
	if msg.Reply != nil {
		if msg.Reply.SenderID > 0 {
			referenced = append(referenced, msg.Reply.SenderID)
		}
		quoteID := strings.TrimSpace(msg.Reply.ExternalMessageID)
		if quoteID == "" && msg.Reply.MessageID != 0 {
			quoteID = strconv.FormatInt(msg.Reply.MessageID, 10)
		}
		if quoteID != "" {
			quoteIDs = append(quoteIDs, quoteID)
		}
		quoteSender = firstNonEmptyString(strings.TrimSpace(msg.Reply.Nickname), strings.TrimSpace(msg.Reply.GroupCard), strings.TrimSpace(msg.Reply.ExternalSenderID))
		if quoteSender == "" && msg.Reply.SenderID != 0 {
			quoteSender = strconv.FormatInt(msg.Reply.SenderID, 10)
		}
		quoteContent = strings.TrimSpace(msg.Reply.Content)
	}
	user := &sessionUser{
		LogID: logID, Ref: ref, Content: strings.TrimSpace(msg.Content), At: msg.Time,
		ReceivedAt: msg.ReceivedAt, Mentioned: msg.IsMentioned, SenderID: msg.UserID,
		Nickname: msg.Nickname, GroupCard: strings.TrimSpace(msg.GroupCard), ReferencedIDs: referenced, ArrivalSeq: msg.ArrivalSeq,
		MessageID: msg.MessageID, IsNotify: msg.IsNotify, NoticeID: onebot.NoticeIDFromMessage(msg),
		IsCommand: messageIsCommand(msg),
		Order:     s.nextOrderLocked(), Platform: msg.Platform, AtNames: msg.AtNames,
		QuoteIDs: quoteIDs, QuoteSender: quoteSender, QuoteContent: quoteContent,
		MediaRefs: append([]media.Ref(nil), mediaRefs...),
	}
	if msg.AccountID != "" {
		for atID, name := range msg.AtNames {
			if strconv.FormatInt(atID, 10) == msg.AccountID && strings.TrimSpace(name) != "" {
				user.BotAtName = strings.TrimSpace(name)
				break
			}
		}
	}
	user.Text = s.renderUserText(msg, ref, mediaRefs)
	user.Msg = userSchemaMessage(user.Text, mediaRefs)
	annotateSessionUserMessage(user.Msg, user)
	for _, item := range mediaRefs {
		s.media.register(item)
	}
	return user
}

// userSchemaMessage 把用户文本与已缓存媒体组装成含图片块的 schema 消息
func userSchemaMessage(text string, mediaRefs []media.Ref) *schema.AgenticMessage {
	blocks := make([]*schema.ContentBlock, 0, len(mediaRefs)+1)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: text}))
	}
	for _, ref := range mediaRefs {
		if len(blocks) > 8 {
			break
		}
		image := mediaRefImageBlock(ref)
		if image == nil {
			continue
		}
		blocks = append(blocks, schema.NewContentBlock(image))
	}
	if len(blocks) == 0 {
		return schema.UserAgenticMessage(text)
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}
}

// mediaRefImageBlock 读取本地媒体并转成模型可用的 base64 图片块
func mediaRefImageBlock(ref media.Ref) *schema.UserInputImage {
	if strings.TrimSpace(ref.Path) == "" {
		return nil
	}
	raw, err := os.ReadFile(ref.Path)
	if err != nil || len(raw) == 0 || len(raw) > 12<<20 {
		return nil
	}
	mime := strings.TrimSpace(ref.Mime)
	if mime == "" {
		mime = "image/png"
	}
	return &schema.UserInputImage{
		Base64Data: base64.StdEncoding.EncodeToString(raw),
		MIMEType:   mime,
	}
}

// renderUserText 构造 Planner 可见的用户消息文本
// 对齐 Python src/maisaka/context/planner_messages.py:17-84：
// 前缀携带 msg_id/quote/time/user/group_card，正文不再内联回复标记（引用走 quote 属性）
func (s *Session) renderUserText(msg *onebot.ConversationMessage, ref string, _ []media.Ref) string {
	var b strings.Builder
	if notes := s.takeNotesLocked(); len(notes) > 0 {
		b.WriteString(strings.Join(notes, "\n"))
		b.WriteString("\n")
	}
	sessionID, sessionName, focusActive := s.maisakaSessionInfoLocked()
	b.WriteString(plannerPrefixForMessage(msg, sessionID, sessionName, focusActive, false))
	body := strings.TrimSpace(msg.FinalContent)
	if body == "" {
		body = strings.TrimSpace(msg.Content)
	}
	b.WriteString(body)
	return strings.TrimSpace(b.String())
}

// maisakaSessionInfoLocked 返回已缓存运行时的会话编号、名称与 Focus 状态，调用方必须已持有 s.mu
func (s *Session) maisakaSessionInfoLocked() (string, string, bool) {
	runtime := s.maisakaRuntime
	if runtime == nil {
		return "", "", false
	}
	return runtime.SessionID(), runtime.SessionName(), runtime.FocusModeActive()
}

func (s *Session) MediaShortHash(ref media.Ref) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.media.register(ref)
}

func (s *Session) takeNotesLocked() []string {
	if len(s.notes) == 0 {
		return nil
	}
	notes := s.notes
	s.notes = nil
	return notes
}

func (s *Session) PendingMessages() []*sessionUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*sessionUser(nil), s.pending...)
}

func (s *Session) MarkInjected(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq > s.injected {
		s.injected = seq
	}
}

func (s *Session) PendingAfter(seq uint64) []tools.PendingMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []tools.PendingMessage
	for _, user := range s.pending {
		if user.ArrivalSeq <= seq || user.ArrivalSeq <= s.injected {
			continue
		}
		list = append(list, tools.PendingMessage{ArrivalSeq: user.ArrivalSeq, Order: user.Order, Msg: user.Msg, TranscriptID: user.TranscriptID})
	}
	return list
}

// PendingAgentMessages 返回尚未被当前模型请求包含的会话间消息。
func (s *Session) PendingAgentMessages() []tools.PendingMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]tools.PendingMessage, 0)
	for _, user := range s.pending {
		if user != nil && user.IsAgentMessage {
			list = append(list, tools.PendingMessage{ArrivalSeq: user.ArrivalSeq, Order: user.Order, Msg: user.Msg, TranscriptID: user.TranscriptID})
		}
	}
	return list
}

func (s *Session) MessagesAfter(seq uint64) []*sessionUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []*sessionUser
	for _, entry := range s.seen {
		if entry.user != nil && entry.user.ArrivalSeq > seq {
			list = append(list, entry.user)
		}
	}
	for _, user := range s.pending {
		if user.ArrivalSeq > seq {
			list = append(list, user)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].ArrivalSeq < list[j].ArrivalSeq })
	return list
}

func (s *Session) MarkRecalledByMessageID(messageID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.pending {
		if user.MessageID == messageID {
			s.notes = append(s.notes, systemNote("消息撤回", user.Ref+" 已撤回，不要再回应或引用其内容"))
			return
		}
	}
	for _, entry := range s.seen {
		if entry.user != nil && entry.user.MessageID == messageID {
			s.notes = append(s.notes, systemNote("消息撤回", entry.user.Ref+" 已撤回，不要再回应或引用其内容"))
			return
		}
	}
}

func (s *Session) NoteSelfSpoke(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfAt = append(s.selfAt, at)
}

func (s *Session) RegisterMediaRefs(refs []media.Ref) {
	if len(refs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range refs {
		s.media.register(ref)
	}
}

func (s *Session) ResolveMediaByHash(hash string) (media.Ref, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.media.resolve(hash)
}

// LatestLogID 返回当前会话已经进入工作集的最新消息日志编号，用作工具读取快照
func (s *Session) LatestLogID() uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest uint
	for _, entry := range s.seen {
		if entry.user != nil && entry.user.LogID > latest {
			latest = entry.user.LogID
		}
	}
	for _, user := range s.pending {
		if user.LogID > latest {
			latest = user.LogID
		}
	}
	return latest
}

func (s *Session) RecentWindow(now time.Time, window time.Duration) (total int, self int, lastSelf time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := now.Add(-window)
	for _, entry := range s.seen {
		if entry.user != nil && !entry.user.IsAgentMessage && !entry.user.At.Before(since) {
			total++
		}
	}
	for _, user := range s.pending {
		if !user.IsAgentMessage && !user.At.Before(since) {
			total++
		}
	}
	for _, at := range s.selfAt {
		if at.Before(since) {
			continue
		}
		self++
		if lastSelf.IsZero() || at.After(lastSelf) {
			lastSelf = at
		}
	}
	return total, self, lastSelf
}

// Assemble 装配本轮上下文：本代头部（人格、群说明、摘要）已在 transcript 中，这里只按顺序展开
func (s *Session) Assemble(ctx context.Context, mem *memory.Manager, instruction, groupExtra string) ([]*schema.AgenticMessage, error) {
	if err := s.ensureGenerationHead(ctx, mem, instruction, groupExtra); err != nil {
		return nil, err
	}
	s.mu.Lock()
	entries := append([]sessionEntry(nil), s.seen...)
	pending := append([]*sessionUser(nil), s.pending...)
	s.mu.Unlock()

	messages := make([]*schema.AgenticMessage, 0, len(entries)+len(pending)+2)
	for _, entry := range orderEntries(entries) {
		if entry.msg != nil {
			messages = append(messages, entry.msg)
		}
	}
	visibleIDs := make([]uint, 0)
	for _, entry := range entries {
		if entry.transcriptID != 0 && entry.visibleSeq == 0 {
			visibleIDs = append(visibleIDs, entry.transcriptID)
		}
	}
	var referenceID uint
	if mem != nil {
		pendingTexts := make([]string, 0, len(pending))
		for _, user := range pending {
			if user != nil {
				pendingTexts = append(pendingTexts, user.Text)
			}
		}
		var pendingEntry *sessionEntry
		for i := range entries {
			if entries[i].transcriptID != 0 && entries[i].visibleSeq == 0 && entries[i].msg != nil && entries[i].msg.Extra["kind"] == "reference" {
				pendingEntry = &entries[i]
				break
			}
		}
		if pendingEntry != nil {
			referenceID = pendingEntry.transcriptID
		} else {
			snapshot, refErr := mem.BuildAutoReference(ctx, s.kind, s.targetID, s.autoReferenceUserIDs(), pendingTexts)
			if refErr != nil {
				return nil, refErr
			}
			seen, seenErr := mem.ReferenceSeenForSession(ctx, s.kind, s.targetID)
			if seenErr != nil {
				return nil, seenErr
			}
			delta, deltaErr := mem.BuildReferenceDelta(ctx, snapshot, seen)
			if deltaErr != nil {
				return nil, deltaErr
			}
			if delta.Text != "" {
				refMsg := schema.UserAgenticMessage(systemNote("内部参考", delta.Text))
				refMsg.Extra = map[string]any{"source": "internal_context", "kind": "reference", "reference_receipts": delta.Receipt}
				payload, _ := sonic.Marshal(refMsg)
				entry := &memory.TranscriptEntry{ConversationKind: s.kind, TargetID: s.targetID, EntryKind: "internal_reference", Payload: payload}
				if err := mem.AppendTranscript(ctx, entry); err != nil {
					return nil, err
				}
				referenceID = entry.ID
				messages = append(messages, refMsg)
				s.mu.Lock()
				s.seen = append(s.seen, sessionEntry{msg: refMsg, transcriptID: entry.ID})
				s.pendingReferenceReceipt = &delta.Receipt
				s.mu.Unlock()
			}
		}
	}

	if referenceID != 0 {
		visibleIDs = append(visibleIDs, referenceID)
	}
	for _, user := range pending {
		if user.Msg != nil {
			messages = append(messages, user.Msg)
		}
		if user.TranscriptID != 0 {
			visibleIDs = append(visibleIDs, user.TranscriptID)
		}
	}

	if tc := tools.GetToolContext(ctx); tc != nil {
		tc.VisibleTranscriptIDs = append(tc.VisibleTranscriptIDs, visibleIDs...)
		if seq := maxPendingSeq(pending); seq > tc.InjectedArrivalSeq {
			tc.InjectedArrivalSeq = seq
		}
	}
	return messages, nil
}

func (s *Session) autoReferenceUserIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kind == memory.ConversationKindPrivate {
		return []int64{s.targetID}
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, 3)
	add := func(id int64) {
		if id > 0 && !seen[id] && len(ids) < 3 {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for i := len(s.pending) - 1; i >= 0; i-- {
		add(s.pending[i].SenderID)
	}
	for i := len(s.pending) - 1; i >= 0; i-- {
		for _, id := range s.pending[i].ReferencedIDs {
			add(id)
		}
	}
	return ids
}

func maxPendingSeq(pending []*sessionUser) uint64 {
	var seq uint64
	for _, user := range pending {
		if user.ArrivalSeq > seq {
			seq = user.ArrivalSeq
		}
	}
	return seq
}

func (s *Session) lastUserMessageText() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pick := func(user *sessionUser) string {
		if user == nil || user.IsAgentMessage {
			return ""
		}
		text := strings.TrimSpace(user.Content)
		if text == "" {
			text = strings.TrimSpace(user.Text)
		}
		return text
	}
	for i := len(s.pending) - 1; i >= 0; i-- {
		if text := pick(s.pending[i]); text != "" {
			return text
		}
	}
	for i := len(s.seen) - 1; i >= 0; i-- {
		if text := pick(s.seen[i].user); text != "" {
			return text
		}
	}
	return ""
}
