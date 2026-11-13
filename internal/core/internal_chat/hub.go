package internalchat

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Sink 单个连接的事件发送函数
type Sink func(event string, data map[string]any) error

// Client 已建立的内部聊天连接
type Client struct {
	SessionID       string
	ClientSessionID string
	UserName        string
	ChannelKey      string
	sink            Sink
}

// Hub 内部聊天连接与历史消息中心
type Hub struct {
	mu             sync.RWMutex
	clients        map[string]*Client
	connSeq        uint64
	botName        string
	history        map[string][]map[string]any
	histSeen       map[string]struct{}
	targetChannels map[string]string
	virtualClients map[string]*VirtualIdentity

	botObservers []func(map[string]any)

	replyTargetLookup ReplyTargetLookup
}

// ReplyTarget 引用卡片所需的被引用消息展示信息
type ReplyTarget struct {
	Content  string
	SenderID string
	Nickname string
	Cardname string
}

// ReplyTargetLookup 按消息 ID 回查被引用消息，供实时推送补全引用卡片
type ReplyTargetLookup func(ctx context.Context, messageID string) (ReplyTarget, bool)

// SetReplyTargetLookup 注入引用目标回查器
func (h *Hub) SetReplyTargetLookup(lookup ReplyTargetLookup) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.replyTargetLookup = lookup
	h.mu.Unlock()
}

// fillReplyTargets 给 reply 段补上被引用消息的发送者与正文
//
// 出站只带 target_message_id，不补全时前端会显示「未知发送者 / 原消息内容不可用」。
func (h *Hub) fillReplyTargets(ctx context.Context, segments []map[string]any) {
	if h == nil || len(segments) == 0 {
		return
	}
	h.mu.RLock()
	lookup := h.replyTargetLookup
	h.mu.RUnlock()
	if lookup == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, segment := range segments {
		if segment == nil {
			continue
		}
		if kind, _ := segment["type"].(string); kind != "reply" {
			continue
		}
		data, ok := segment["data"].(map[string]any)
		if !ok {
			continue
		}
		targetID, _ := data["target_message_id"].(string)
		targetID = strings.TrimSpace(targetID)
		if targetID == "" {
			continue
		}
		target, found := lookup(ctx, targetID)
		if !found {
			continue
		}
		if value, _ := data["target_message_content"].(string); strings.TrimSpace(value) == "" {
			data["target_message_content"] = target.Content
		}
		if value, _ := data["target_message_sender_id"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_id"] = target.SenderID
		}
		if value, _ := data["target_message_sender_nickname"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_nickname"] = target.Nickname
		}
		if value, _ := data["target_message_sender_cardname"].(string); strings.TrimSpace(value) == "" {
			data["target_message_sender_cardname"] = target.Cardname
		}
	}
}

// NewHub 创建内部聊天中心
func NewHub() *Hub {
	return &Hub{
		clients:        make(map[string]*Client),
		botName:        "MaiBot",
		history:        make(map[string][]map[string]any),
		histSeen:       make(map[string]struct{}),
		targetChannels: make(map[string]string),
		virtualClients: make(map[string]*VirtualIdentity),
	}
}

// SetBotIdentity 设置机器人昵称，对齐 global_config.bot.nickname
func (h *Hub) SetBotIdentity(name string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if name != "" {
		h.botName = name
	}
	h.mu.Unlock()
}

// Open 注册连接并返回内部会话 ID，格式为 connection_id:client_session_id
func (h *Hub) Open(clientSessionID, userName, channelKey string, sink Sink) (*Client, error) {
	if h == nil {
		return nil, fmt.Errorf("内部聊天未初始化")
	}
	if clientSessionID == "" {
		return nil, fmt.Errorf("缺少客户端会话 ID")
	}
	if sink == nil {
		return nil, fmt.Errorf("缺少消息发送函数")
	}
	channelKey = strings.TrimSpace(channelKey)
	if channelKey == "" {
		channelKey = OwnerChannelKey()
	}
	h.mu.Lock()
	h.connSeq++
	client := &Client{
		SessionID:       strconv.FormatUint(h.connSeq, 10) + ":" + clientSessionID,
		ClientSessionID: clientSessionID,
		UserName:        userName,
		ChannelKey:      channelKey,
		sink:            sink,
	}
	h.clients[client.SessionID] = client
	h.mu.Unlock()
	return client, nil
}

// Close 移除连接，连接不存在时返回 false
func (h *Hub) Close(sessionID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	client, ok := h.clients[sessionID]
	delete(h.clients, sessionID)
	delete(h.virtualClients, sessionID)
	if ok && client != nil {
		stillOnline := false
		for _, other := range h.clients {
			if other.ChannelKey == client.ChannelKey {
				stillOnline = true
				break
			}
		}
		if !stillOnline {
			for key, channelKey := range h.targetChannels {
				if channelKey == client.ChannelKey {
					delete(h.targetChannels, key)
				}
			}
		}
	}
	h.mu.Unlock()
	return ok
}

// SetClientVirtualIdentity 记录连接对应的虚拟身份，供文件会话与历史查询复用
func (h *Hub) SetClientVirtualIdentity(sessionID string, identity *VirtualIdentity) {
	if h == nil || strings.TrimSpace(sessionID) == "" || identity == nil || !identity.Enabled {
		return
	}
	copy := *identity
	h.mu.Lock()
	h.virtualClients[sessionID] = &copy
	h.mu.Unlock()
}

// VirtualIdentityForSession 读取连接绑定的虚拟身份
func (h *Hub) VirtualIdentityForSession(sessionID string) *VirtualIdentity {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	h.mu.RLock()
	identity := h.virtualClients[sessionID]
	h.mu.RUnlock()
	if identity == nil {
		return nil
	}
	copy := *identity
	return &copy
}

// BotName 返回机器人昵称
func (h *Hub) BotName() string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	name := h.botName
	h.mu.RUnlock()
	return name
}

// UpdateUserName 更新连接昵称
func (h *Hub) UpdateUserName(sessionID, userName string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if client, ok := h.clients[sessionID]; ok && userName != "" {
		client.UserName = userName
	}
	h.mu.Unlock()
}

// UserName 读取连接昵称
func (h *Hub) UserName(sessionID string) string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if client, ok := h.clients[sessionID]; ok {
		return client.UserName
	}
	return ""
}

// UpdateAllUserNames 更新所有连接昵称，对齐 MaiBot 内部聊天室共享昵称
func (h *Hub) UpdateAllUserNames(userName string) {
	if h == nil || userName == "" {
		return
	}
	h.mu.Lock()
	for _, client := range h.clients {
		if client.ChannelKey == OwnerChannelKey() {
			client.UserName = userName
		}
	}
	h.mu.Unlock()
}

// HasClient 判断会话 ID 是否仍有活动连接
func (h *Hub) HasClient(sessionID string) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	_, ok := h.clients[sessionID]
	h.mu.RUnlock()
	return ok
}

// Online 在线连接数
func (h *Hub) Online() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	count := len(h.clients)
	h.mu.RUnlock()
	return count
}

// Broadcast 向所有连接推送事件
func (h *Hub) Broadcast(event string, data map[string]any) {
	if h == nil {
		return
	}
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()
	h.deliver(clients, "", event, data)
}

// BroadcastToChannel 只向绑定指定频道的连接推送事件
func (h *Hub) BroadcastToChannel(channelKey, event string, data map[string]any) {
	if h == nil {
		return
	}
	channelKey = strings.TrimSpace(channelKey)
	if channelKey == "" {
		channelKey = OwnerChannelKey()
	}
	h.mu.RLock()
	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.ChannelKey == channelKey {
			clients = append(clients, client)
		}
	}
	h.mu.RUnlock()
	h.deliver(clients, channelKey, event, data)
}

func (h *Hub) deliver(clients []*Client, channelKey, event string, data map[string]any) {
	if len(clients) == 0 {
		return
	}
	if channelKey != "" && (event == "user_message" || event == "bot_message") {
		h.appendHistory(channelKey, data)
	}
	if event == "bot_message" {
		h.notifyBotMessage(data)
	}
	for _, client := range clients {
		if err := client.sink(event, data); err != nil {
			zap.L().Debug("内部聊天推送失败", zap.String("session_id", client.SessionID), zap.Error(err))
		}
	}
}

// RegisterConversationChannel 记录 Agent 目标 ID 到内部聊天频道的映射
func (h *Hub) RegisterConversationChannel(kind string, targetID int64, channelKey string) {
	if h == nil || targetID <= 0 {
		return
	}
	channelKey = strings.TrimSpace(channelKey)
	if channelKey == "" {
		return
	}
	key := kind + ":" + strconv.FormatInt(targetID, 10)
	h.mu.Lock()
	h.targetChannels[key] = channelKey
	h.mu.Unlock()
}

// ConversationChannel 读取 Agent 目标 ID 绑定的频道
func (h *Hub) ConversationChannel(kind string, targetID int64) (string, bool) {
	if h == nil || targetID <= 0 {
		return "", false
	}
	if kind == "" || kind == "private" {
		if targetID == OwnerNumericID {
			return OwnerChannelKey(), true
		}
	}
	key := kind + ":" + strconv.FormatInt(targetID, 10)
	h.mu.RLock()
	channelKey, ok := h.targetChannels[key]
	h.mu.RUnlock()
	return channelKey, ok
}

// SubscribeBotMessages 订阅机器人消息，返回取消函数
func (h *Hub) SubscribeBotMessages(handler func(map[string]any)) func() {
	if h == nil || handler == nil {
		return func() {}
	}
	h.mu.Lock()
	index := len(h.botObservers)
	h.botObservers = append(h.botObservers, handler)
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		if index < len(h.botObservers) {
			h.botObservers[index] = nil
		}
		h.mu.Unlock()
	}
}

func (h *Hub) notifyBotMessage(payload map[string]any) {
	h.mu.RLock()
	handlers := make([]func(map[string]any), 0, len(h.botObservers))
	for _, handler := range h.botObservers {
		if handler != nil {
			handlers = append(handlers, handler)
		}
	}
	h.mu.RUnlock()
	for _, handler := range handlers {
		handler(payload)
	}
}

// History 返回普通内部聊天的历史消息副本
func (h *Hub) History() []map[string]any {
	return h.HistoryForChannel(OwnerChannelKey())
}

// HistoryForChannel 返回指定频道的历史消息副本
func (h *Hub) HistoryForChannel(channelKey string) []map[string]any {
	if h == nil {
		return nil
	}
	channelKey = strings.TrimSpace(channelKey)
	if channelKey == "" {
		channelKey = OwnerChannelKey()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	stored := h.history[channelKey]
	out := make([]map[string]any, len(stored))
	copy(out, stored)
	return out
}

// BroadcastUserMessage 广播主人或插件投递的用户消息
func (h *Hub) BroadcastUserMessage(msg UserMessage) map[string]any {
	payload := h.userMessagePayload(msg)
	channelKey := strings.TrimSpace(msg.ChannelKey)
	if channelKey == "" {
		if groupID, ok := msg.GroupID.(string); ok && strings.TrimSpace(groupID) != "" {
			channelKey = GroupChannel(groupID)
		} else if strings.TrimSpace(msg.SenderUserID) != "" {
			channelKey = PrivateChannel(msg.SenderUserID)
		} else {
			channelKey = OwnerChannelKey()
		}
	}
	h.BroadcastToChannel(channelKey, "user_message", payload)
	return payload
}

func (h *Hub) conversationChannel(conversationKind string, targetID int64) (string, error) {
	if channelKey, ok := h.ConversationChannel(conversationKind, targetID); ok {
		return channelKey, nil
	}
	if strings.TrimSpace(conversationKind) == "" || targetID == 0 || targetID == OwnerNumericID {
		return OwnerChannelKey(), nil
	}
	return "", fmt.Errorf("内部聊天会话未注册")
}

// userMessagePayload 构造 user_message 事件负载，字段对齐 Python handle_chat_message
func (h *Hub) userMessagePayload(msg UserMessage) map[string]any {
	messageID := msg.MessageID
	if messageID == "" {
		messageID = NewMessageID()
	}
	senderName := msg.SenderName
	h.mu.RLock()
	if senderName == "" {
		senderName = h.botName
	}
	h.mu.RUnlock()
	images := msg.Images
	if images == nil {
		images = []any{}
	}
	emojis := msg.Emojis
	if emojis == nil {
		emojis = []any{}
	}
	files := msg.Files
	if files == nil {
		files = []any{}
	}
	voices := msg.Voices
	if voices == nil {
		voices = []any{}
	}
	payload := map[string]any{
		"type":        "user_message",
		"content":     msg.Content,
		"raw_content": msg.RawContent,
		// 前端按顶层 is_bot 决定气泡左右
		"is_bot":      false,
		"images":      images,
		"emojis":      emojis,
		"files":       files,
		"voices":      voices,
		"group_id":    msg.GroupID,
		"message_id":  messageID,
		"timestamp":   timestamp(),
		"sender": map[string]any{
			"name":    senderName,
			"user_id": msg.SenderUserID,
			"is_bot":  false,
		},
		"virtual_mode": msg.VirtualMode,
		"origin":       msg.Origin,
	}
	return payload
}

// botMessagePayload 构造 bot_message 事件负载，字段对齐 uni_message_sender
func (h *Hub) botMessagePayload(channelKey, messageID, content, messageType string, segments []map[string]any) map[string]any {
	h.mu.RLock()
	botName := h.botName
	h.mu.RUnlock()
	var segmentValue any
	if segments != nil {
		segmentValue = segments
	}
	var groupID any
	if value, ok := ChannelGroupID(channelKey); ok {
		groupID = value
	}
	return map[string]any{
		"type":         "bot_message",
		"message_id":   messageID,
		"content":      content,
		"message_type": messageType,
		"segments":     segmentValue,
		"timestamp":    timestamp(),
		"group_id":     groupID,
		// 前端按顶层 is_bot 决定气泡左右，只放 sender 里会被忽略
		"is_bot": true,
		"sender": map[string]any{
			"name":   botName,
			"avatar": nil,
			"is_bot": true,
		},
	}
}

func (h *Hub) appendHistory(channelKey string, payload map[string]any) {
	rawID, ok := payload["message_id"]
	if !ok {
		return
	}
	messageID, ok := rawID.(string)
	if !ok || messageID == "" {
		return
	}
	channelKey = strings.TrimSpace(channelKey)
	if channelKey == "" {
		channelKey = OwnerChannelKey()
	}
	seenKey := channelKey + "\x00" + messageID
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.histSeen[seenKey]; exists {
		return
	}
	h.histSeen[seenKey] = struct{}{}
	stored := append(h.history[channelKey], payload)
	if len(stored) > maxHistory {
		trimmed := stored[0]
		delete(h.histSeen, channelKey+"\x00"+fmt.Sprint(trimmed["message_id"]))
		stored = stored[1:]
	}
	h.history[channelKey] = stored
}

// UserMessage 用户消息字段，空切片统一广播为 []
type UserMessage struct {
	Content      string
	RawContent   string
	Images       []any
	Emojis       []any
	Files        []any
	Voices       []any
	GroupID      any
	MessageID    string
	SenderName   string
	SenderUserID string
	Origin       string
	ChannelKey   string
	VirtualMode  bool
}

func timestamp() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
