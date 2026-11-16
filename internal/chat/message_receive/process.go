package messagereceive

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"MaiGo/internal/common/data_models/messagecomponent"

	"go.uber.org/zap"
)

// ProcessOptions 控制消息组件预处理深度
type ProcessOptions struct {
	EnableHeavyMediaAnalysis bool
	EnableVoiceTranscription bool
}

// ImageRef 图片描述输入；content 为组件已带文本，hash 用于适配器查缓存
type ImageRef struct {
	Hash    string
	Content string
}

// EmojiRef 表情描述输入
type EmojiRef struct {
	Hash    string
	Content string
	// Binary 已加载的表情包字节；用于缓存前 Hook
	Binary []byte
}

// VoiceRef 语音转录输入
type VoiceRef struct {
	Hash    string
	Content string
	// Binary 已加载的音频字节；非空时优先于 Hash/Content
	Binary []byte
	// Source 音频媒体引用，交给宿主 VoiceResolver 解析
	Source string
}

// ImageDescriber 图片描述端口
type ImageDescriber interface {
	DescribeImage(ctx context.Context, ref ImageRef, wait bool) (string, error)
}

// EmojiDescriber 表情描述端口
type EmojiDescriber interface {
	DescribeEmoji(ctx context.Context, sessionID string, ref EmojiRef, wait bool) (string, error)
}

// VoiceTranscriber 语音转录端口
type VoiceTranscriber interface {
	TranscribeVoice(ctx context.Context, ref VoiceRef) (string, bool, error)
}

// Person 人员展示信息
type Person struct {
	UserID     string
	Nickname   string
	Cardname   string
	GroupCards map[string]string
}

// PersonResolver 人员资料端口
type PersonResolver interface {
	LookupPerson(ctx context.Context, platform, userID, groupID string) (*Person, bool)
}

// BotIdentity 机器人自身识别端口
type BotIdentity interface {
	IsBotSelf(platform, userID string) bool
	BotNickname() string
}

// ReplyMessage 引用消息查询结果
type ReplyMessage struct {
	Content string
	Sender  UserInfo
}

// ReplyLookup 引用消息查询端口；count 用于复刻唯一性校验
type ReplyLookup interface {
	LookupReplyMessage(ctx context.Context, platform, sessionID, messageID string) (ReplyMessage, int, error)
}

// Processor 执行 SessionMessage.process() 的组件文本化
type Processor struct {
	Images  ImageDescriber
	Emojis  EmojiDescriber
	Voice   VoiceTranscriber
	Persons PersonResolver
	Replies ReplyLookup
	Bot     BotIdentity
}

type replyMemoryEntry struct {
	Content   string
	User      UserInfo
	ready     chan struct{}
	completed bool
	err       error
}

type processState struct {
	mu      sync.Mutex
	replies map[string]*replyMemoryEntry
}

func newProcessState() *processState {
	return &processState{replies: map[string]*replyMemoryEntry{}}
}

// registerPending 为合并转发节点登记占位，供并发回复组件等待
func (s *processState) registerPending(messageID string) *replyMemoryEntry {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.replies[messageID]; ok {
		return entry
	}
	entry := &replyMemoryEntry{ready: make(chan struct{})}
	s.replies[messageID] = entry
	return entry
}

// completePending 写入转发节点内容并唤醒等待者
func (s *processState) completePending(entry *replyMemoryEntry, content string, user UserInfo, err error) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	if entry.completed {
		s.mu.Unlock()
		return
	}
	entry.Content = content
	entry.User = user
	entry.err = err
	entry.completed = true
	close(entry.ready)
	s.mu.Unlock()
}

func (s *processState) storeReply(messageID string, content string, user UserInfo) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	s.mu.Lock()
	s.replies[messageID] = &replyMemoryEntry{Content: content, User: user, completed: true}
	s.mu.Unlock()
}

func (s *processState) lookupReply(messageID string) (*replyMemoryEntry, bool) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.replies[messageID]
	return entry, ok
}

// registerForwardComponents 递归预登记合并转发节点，避免回复组件与转发组件并发时错过占位
func (s *processState) registerForwardComponents(nodes []messagecomponent.ForwardComponent) {
	for _, node := range nodes {
		s.registerPending(node.MessageID)
		for _, child := range node.Content {
			switch nested := child.(type) {
			case messagecomponent.ForwardNodeComponent:
				s.registerForwardComponents(nested.ForwardComponents)
			case *messagecomponent.ForwardNodeComponent:
				if nested != nil {
					s.registerForwardComponents(nested.ForwardComponents)
				}
			}
		}
	}
}

// Process 对齐 SessionMessage.process：并发处理组件，单个组件失败只告警并跳过
func (p *Processor) Process(ctx context.Context, msg *SessionMessage, opts ProcessOptions) error {
	if msg == nil {
		return fmt.Errorf("消息为空")
	}
	components := msg.RawMessage
	if components == nil {
		msg.SetPlainText("")
		return nil
	}
	components.Components = removeVoicePlaceholder(components.Components)
	state := newProcessState()
	for _, component := range components.Components {
		switch forward := component.(type) {
		case messagecomponent.ForwardNodeComponent:
			state.registerForwardComponents(forward.ForwardComponents)
		case *messagecomponent.ForwardNodeComponent:
			if forward != nil {
				state.registerForwardComponents(forward.ForwardComponents)
			}
		}
	}
	// 对齐 Python：成功结果（含空串）按序 join；异常组件跳过不占位
	results := make([]*string, len(components.Components))
	updated := make([]messagecomponent.Component, len(components.Components))
	var wg sync.WaitGroup
	for index, component := range components.Components {
		wg.Add(1)
		go func(i int, item messagecomponent.Component) {
			defer wg.Done()
			next, text, err := p.processSingle(ctx, msg, item, opts, state, 0)
			if err != nil {
				zap.L().Error("处理消息组件时发生错误",
					zap.String("component", item.FormatName()),
					zap.Error(err))
				return
			}
			results[i] = &text
			updated[i] = next
		}(index, component)
	}
	wg.Wait()
	parts := make([]string, 0, len(results))
	for index, result := range results {
		if result != nil {
			parts = append(parts, *result)
			// 对齐 Python：处理结果写回组件并清空已消费的二进制负载
			if updated[index] != nil {
				components.Components[index] = updated[index]
			}
		}
	}
	msg.SetPlainText(strings.Join(parts, " "))
	return nil
}

// ProcessSingle 处理单个组件，主要供测试和调试使用
func (p *Processor) ProcessSingle(ctx context.Context, msg *SessionMessage, component messagecomponent.Component, opts ProcessOptions) (string, error) {
	_, text, err := p.processSingle(ctx, msg, component, opts, newProcessState(), 0)
	return text, err
}

func (p *Processor) processSingle(ctx context.Context, msg *SessionMessage, component messagecomponent.Component, opts ProcessOptions, state *processState, depth int) (messagecomponent.Component, string, error) {
	switch item := component.(type) {
	case messagecomponent.TextComponent:
		return component, item.Text, nil
	case *messagecomponent.TextComponent:
		if item == nil {
			return component, "", nil
		}
		return component, item.Text, nil
	case messagecomponent.ImageComponent:
		next, text, err := p.processImage(ctx, item, opts)
		return next, text, err
	case *messagecomponent.ImageComponent:
		if item == nil {
			return component, "", nil
		}
		next, text, err := p.processImage(ctx, *item, opts)
		*item = next
		return item, text, err
	case messagecomponent.EmojiComponent:
		next, text, err := p.processEmoji(ctx, msg, item, opts)
		return next, text, err
	case *messagecomponent.EmojiComponent:
		if item == nil {
			return component, "", nil
		}
		next, text, err := p.processEmoji(ctx, msg, *item, opts)
		*item = next
		return item, text, err
	case messagecomponent.AtComponent:
		next, text, err := p.processAt(ctx, msg, item)
		return next, text, err
	case *messagecomponent.AtComponent:
		if item == nil {
			return component, "", nil
		}
		next, text, err := p.processAt(ctx, msg, *item)
		*item = next
		return item, text, err
	case messagecomponent.VoiceComponent:
		next, text, err := p.processVoice(ctx, item, opts)
		return next, text, err
	case *messagecomponent.VoiceComponent:
		if item == nil {
			return component, "", nil
		}
		next, text, err := p.processVoice(ctx, *item, opts)
		*item = next
		return item, text, err
	case messagecomponent.FileComponent:
		return component, item.ToPlainText(), nil
	case *messagecomponent.FileComponent:
		if item == nil {
			return component, "", nil
		}
		return component, item.ToPlainText(), nil
	case messagecomponent.ReplyComponent:
		next, text, err := p.processReply(ctx, msg, item, state)
		return next, text, err
	case *messagecomponent.ReplyComponent:
		if item == nil {
			return component, "", nil
		}
		next, text, err := p.processReply(ctx, msg, *item, state)
		*item = next
		return item, text, err
	case messagecomponent.ForwardNodeComponent:
		next, text, err := p.processForward(ctx, msg, item, opts, state, depth)
		return next, text, err
	case *messagecomponent.ForwardNodeComponent:
		if item == nil {
			return component, "", nil
		}
		next, text, err := p.processForward(ctx, msg, *item, opts, state, depth)
		*item = next
		return item, text, err
	case messagecomponent.DictComponent:
		return component, processDictComponent(item), nil
	case *messagecomponent.DictComponent:
		if item == nil {
			return component, "", nil
		}
		return component, processDictComponent(*item), nil
	default:
		return component, "", fmt.Errorf("暂时不支持的消息组件类型: %s", component.FormatName())
	}
}

func (p *Processor) processImage(ctx context.Context, item messagecomponent.ImageComponent, opts ProcessOptions) (messagecomponent.ImageComponent, string, error) {
	if content := strings.TrimSpace(item.Content); content != "" {
		item.Content = content
		return item, content, nil
	}
	if p.Images == nil {
		return item, "", fmt.Errorf("未配置图片描述端口")
	}
	description, err := p.Images.DescribeImage(ctx, ImageRef{Hash: item.Hash, Content: item.Content}, opts.EnableHeavyMediaAnalysis)
	if err != nil {
		return item, "", err
	}
	description = strings.TrimSpace(description)
	content := ""
	if description != "" {
		content = "[图片：" + description + "]"
	}
	// 对齐 Python：空描述保持空 content，处理完即丢弃二进制数据
	item.Content = content
	item.Binary = nil
	return item, content, nil
}

func (p *Processor) processEmoji(ctx context.Context, msg *SessionMessage, item messagecomponent.EmojiComponent, opts ProcessOptions) (messagecomponent.EmojiComponent, string, error) {
	if content := strings.TrimSpace(item.Content); content != "" {
		item.Content = content
		return item, content, nil
	}
	content := "[表情包]"
	if p.Emojis != nil {
		description, err := p.Emojis.DescribeEmoji(ctx, msg.SessionID, EmojiRef{Hash: item.Hash, Content: item.Content, Binary: item.Binary}, opts.EnableHeavyMediaAnalysis)
		if err == nil {
			if description = strings.TrimSpace(description); description != "" {
				content = "[表情包: " + description + "]"
			}
		}
	}
	item.Content = content
	item.Binary = nil
	return item, content, nil
}

func (p *Processor) processVoice(ctx context.Context, item messagecomponent.VoiceComponent, opts ProcessOptions) (messagecomponent.VoiceComponent, string, error) {
	if content := strings.TrimSpace(item.Content); content != "" {
		item.Content = content
		return item, content, nil
	}
	if !opts.EnableVoiceTranscription {
		item.Content = "[语音消息]"
		return item, item.Content, nil
	}
	if p.Voice == nil {
		item.Content = "[语音消息，转录失败]"
		return item, item.Content, nil
	}
	text, ok, err := p.Voice.TranscribeVoice(ctx, VoiceRef{
		Hash:    item.Hash,
		Content: item.Content,
		Binary:  item.Binary,
		Source:  item.Source,
	})
	// 对齐 Python：text is None → 转录失败；text=="" → "[语音: ]"
	content := "[语音消息，转录失败]"
	if err == nil && ok {
		content = "[语音: " + text + "]"
	}
	item.Content = content
	return item, content, nil
}

func (p *Processor) processAt(ctx context.Context, msg *SessionMessage, item messagecomponent.AtComponent) (messagecomponent.AtComponent, string, error) {
	if item.Cardname != nil && strings.TrimSpace(*item.Cardname) != "" {
		return item, "@" + strings.TrimSpace(*item.Cardname), nil
	}
	if item.Nickname != nil && strings.TrimSpace(*item.Nickname) != "" {
		return item, "@" + strings.TrimSpace(*item.Nickname), nil
	}
	targetID := strings.TrimSpace(item.TargetUserID)
	if p.Bot != nil && p.Bot.IsBotSelf(msg.Platform, targetID) {
		if nickname := strings.TrimSpace(p.Bot.BotNickname()); nickname != "" {
			// 对齐 Python：补全机器人昵称/群名片并标记来源
			item.Nickname = &nickname
			item.Cardname = &nickname
			item.UsesBotNick = true
			return item, "@" + nickname, nil
		}
	}
	if p.Persons != nil && msg != nil {
		if person, ok := p.Persons.LookupPerson(ctx, msg.Platform, targetID, msg.TargetID()); ok && person != nil {
			if item.Nickname == nil || strings.TrimSpace(*item.Nickname) == "" {
				if nickname := strings.TrimSpace(person.Nickname); nickname != "" {
					item.Nickname = &nickname
				}
			}
			if msg.IsGroup() && person.GroupCards != nil {
				if card := strings.TrimSpace(person.GroupCards[msg.TargetID()]); card != "" {
					cardname := card
					item.Cardname = &cardname
				}
			}
			if item.Cardname != nil && strings.TrimSpace(*item.Cardname) != "" {
				return item, "@" + strings.TrimSpace(*item.Cardname), nil
			}
			if item.Nickname != nil && strings.TrimSpace(*item.Nickname) != "" {
				return item, "@" + strings.TrimSpace(*item.Nickname), nil
			}
		}
	}
	return item, "@" + targetID, nil
}

func (p *Processor) processReply(ctx context.Context, msg *SessionMessage, item messagecomponent.ReplyComponent, state *processState) (messagecomponent.ReplyComponent, string, error) {
	// 对齐 Python：content/sender_id 按真值判断（空串为假，空白串为真），文案保留原始空白
	if item.TargetContent != nil && item.SenderID != nil && *item.TargetContent != "" && *item.SenderID != "" {
		return item, "[回复了" + replyDisplayName(item.SenderCardname, item.SenderNickname, *item.SenderID) + "的消息: " + *item.TargetContent + "]", nil
	}
	targetID := strings.TrimSpace(item.TargetMessageID)
	if entry, ok := state.lookupReply(targetID); ok && entry != nil {
		if entry.ready != nil {
			select {
			case <-entry.ready:
			case <-ctx.Done():
				return item, "", ctx.Err()
			}
		}
		if entry.err != nil {
			return item, "", entry.err
		}
		item = replyComponentWithTarget(item, entry.Content, entry.User)
		return item, formatReplyText(entry.User, entry.Content), nil
	}
	if p.Replies != nil && msg != nil {
		message, count, err := p.Replies.LookupReplyMessage(ctx, msg.Platform, msg.SessionID, targetID)
		switch {
		case err != nil:
			zap.L().Error("查询回复消息时发生错误: " + err.Error())
		case count > 1:
			zap.L().Error("查询回复消息时发生错误: 引用消息 ID 在当前聊天流中不唯一")
		case count == 1:
			state.storeReply(targetID, message.Content, message.Sender)
			item = replyComponentWithTarget(item, message.Content, message.Sender)
			return item, formatReplyText(message.Sender, message.Content), nil
		}
	}
	if item.TargetContent != nil && *item.TargetContent != "" {
		return item, "[回复了一条作者未核验的消息: " + *item.TargetContent + "]", nil
	}
	return item, "[回复了一条消息，但原消息已无法访问]", nil
}

// replyComponentWithTarget 对齐 Python：命中引用后回填目标正文与作者字段
func replyComponentWithTarget(item messagecomponent.ReplyComponent, content string, user UserInfo) messagecomponent.ReplyComponent {
	item.TargetContent = &content
	userID := user.UserID
	item.SenderID = &userID
	nickname := user.UserNickname
	item.SenderNickname = &nickname
	item.SenderCardname = user.UserCardname
	return item
}

func (p *Processor) processForward(ctx context.Context, msg *SessionMessage, item messagecomponent.ForwardNodeComponent, opts ProcessOptions, state *processState, depth int) (messagecomponent.ForwardNodeComponent, string, error) {
	state.registerForwardComponents(item.ForwardComponents)
	type forwardNodeResult struct {
		line string
		node messagecomponent.ForwardComponent
		err  error
	}
	results := make([]forwardNodeResult, len(item.ForwardComponents))
	var wg sync.WaitGroup
	for index, node := range item.ForwardComponents {
		wg.Add(1)
		go func(i int, current messagecomponent.ForwardComponent) {
			defer wg.Done()
			user := UserInfo{UserID: "未知用户", UserNickname: "未知用户"}
			if current.UserID != nil && strings.TrimSpace(*current.UserID) != "" {
				user.UserID = strings.TrimSpace(*current.UserID)
			}
			if nickname := strings.TrimSpace(current.UserNickname); nickname != "" {
				user.UserNickname = nickname
			}
			if current.UserCardname != nil {
				user.UserCardname = current.UserCardname
			}
			texts := make([]string, 0, len(current.Content))
			children := make([]messagecomponent.Component, len(current.Content))
			for childIndex, child := range current.Content {
				next, text, err := p.processSingle(ctx, msg, child, opts, state, depth+1)
				children[childIndex] = next
				if err != nil {
					zap.L().Error("处理消息组件时发生错误", zap.Error(err))
					continue
				}
				if text != "" {
					texts = append(texts, text)
				}
			}
			current.Content = children
			content := strings.Join(texts, " ")
			state.completePending(state.registerPending(current.MessageID), content, user, nil)
			name := user.UserNickname
			if user.UserCardname != nil && strings.TrimSpace(*user.UserCardname) != "" {
				name = strings.TrimSpace(*user.UserCardname)
			}
			if name == "" {
				name = user.UserID
			}
			results[i] = forwardNodeResult{
				line: strings.Repeat("-", depth*2) + " 【" + name + "】: " + content,
				node: current,
			}
		}(index, node)
	}
	wg.Wait()
	lines := make([]string, 0, len(results))
	for index, result := range results {
		if result.err != nil {
			zap.L().Error("处理转发消息组件时发生错误", zap.Error(result.err))
			continue
		}
		lines = append(lines, result.line)
		item.ForwardComponents[index] = result.node
	}
	return item, "【合并转发消息: " + "\n" + strings.Join(lines, "\n") + "\n】", nil
}

func formatReplyText(user UserInfo, content string) string {
	name := strings.TrimSpace(user.UserNickname)
	if user.UserCardname != nil && strings.TrimSpace(*user.UserCardname) != "" {
		name = strings.TrimSpace(*user.UserCardname)
	}
	if name == "" {
		name = strings.TrimSpace(user.UserID)
	}
	return "[回复了" + name + "的消息: " + content + "]"
}

func replyDisplayName(cardname, nickname *string, fallback string) string {
	if cardname != nil && strings.TrimSpace(*cardname) != "" {
		return strings.TrimSpace(*cardname)
	}
	if nickname != nil && strings.TrimSpace(*nickname) != "" {
		return strings.TrimSpace(*nickname)
	}
	return strings.TrimSpace(fallback)
}

func removeVoicePlaceholder(components []messagecomponent.Component) []messagecomponent.Component {
	hasVoice := false
	for _, component := range components {
		switch component.(type) {
		case messagecomponent.VoiceComponent, *messagecomponent.VoiceComponent:
			hasVoice = true
		}
	}
	if !hasVoice {
		return components
	}
	out := make([]messagecomponent.Component, 0, len(components))
	for _, component := range components {
		switch item := component.(type) {
		case messagecomponent.TextComponent:
			if strings.ToLower(strings.TrimSpace(item.Text)) == "[voice]" {
				continue
			}
		case *messagecomponent.TextComponent:
			if strings.ToLower(strings.TrimSpace(item.Text)) == "[voice]" {
				continue
			}
		}
		out = append(out, component)
	}
	return out
}

func processDictComponent(item messagecomponent.DictComponent) string {
	rawType, _ := item.Data["type"].(string)
	rawType = strings.ToLower(strings.TrimSpace(rawType))
	if rawType == "" {
		rawType = "dict"
	}
	payload := item.Data
	if nested, ok := item.Data["data"]; ok {
		if mapping, ok := nested.(map[string]any); ok {
			payload = mapping
		}
	}
	switch rawType {
	case "file":
		return buildFileComponentText(payload)
	case "json":
		for _, key := range []string{"text", "prompt"} {
			if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
		return "[json]"
	}
	// Python 的 raw_type 缺省为 dict，因此任何非空类型都会进入这里
	return "[" + rawType + "]"
}

func buildFileComponentText(payload map[string]any) string {
	name := firstMapString(payload, "name", "file", "file_name", "filename")
	size := firstMapString(payload, "size", "file_size")
	url := firstMapString(payload, "url", "file_url")
	if name == "" && size == "" && url == "" {
		return "[文件]"
	}
	textParts := make([]string, 0, 2)
	if name != "" {
		textParts = append(textParts, name)
	}
	if size != "" {
		textParts = append(textParts, "大小: "+size)
	}
	text := "[文件]"
	if len(textParts) > 0 {
		text = "[文件] " + strings.Join(textParts, "，")
	}
	if url != "" {
		text = text + "，链接: " + url
	}
	return text
}

func firstMapString(mapping map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := mapping[key]; ok && value != nil {
			// 对齐 Python str(payload.get(...) or "")：数字 size 等也要转字符串
			if text := strings.TrimSpace(anyString(value)); text != "" {
				return text
			}
		}
	}
	return ""
}
