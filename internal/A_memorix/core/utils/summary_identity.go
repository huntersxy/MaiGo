package utils

// A_memorix 摘要身份上下文，对齐 Python core/utils/summary_identity.py
// 聊天消息模型与宿主查询能力在本文件定义，数据库与人物主档通过接口注入

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// identityReferencePattern 对齐 IDENTITY_REFERENCE
var identityReferencePattern = regexp.MustCompile(`\[U[0-9]+\]`)

// ChatMessageComponentKind 组件类型
type ChatMessageComponentKind string

const (
	// ComponentText 文本组件
	ComponentText ChatMessageComponentKind = "text"
	// ComponentAt @ 组件
	ComponentAt ChatMessageComponentKind = "at"
	// ComponentReply 引用组件
	ComponentReply ChatMessageComponentKind = "reply"
	// ComponentForward 转发节点组件
	ComponentForward ChatMessageComponentKind = "forward"
	// ComponentOther 其余组件，交由宿主渲染
	ComponentOther ChatMessageComponentKind = "other"
)

// ForwardNode 转发节点，对齐 ForwardNodeComponent 的节点字段
type ForwardNode struct {
	UserID       string
	UserNickname string
	UserCardname string
	Content      []ChatMessageComponent
}

// ChatMessageComponent 聊天消息组件，按 Kind 取用对应字段
type ChatMessageComponent struct {
	Kind ChatMessageComponentKind
	Text string
	// Image 标记图片组件，供摘要图片证据路径生成
	Image bool

	TargetUserID       string
	TargetUserNickname string
	TargetUserCardname string

	TargetMessageID             string
	TargetMessageSenderID       string
	TargetMessageSenderNickname string
	TargetMessageSenderCardname string
	TargetMessageContent        string

	ForwardComponents []ForwardNode
}

// ChatUserInfo 发送者信息
type ChatUserInfo struct {
	UserID       string
	UserNickname string
	UserCardname string
}

// ChatGroupInfo 群信息
type ChatGroupInfo struct {
	GroupID string
}

// ChatMessageInfo 消息依附信息
type ChatMessageInfo struct {
	UserInfo  ChatUserInfo
	GroupInfo *ChatGroupInfo
}

// ChatRawMessage 原始消息
type ChatRawMessage struct {
	Components []ChatMessageComponent
}

// SessionMessage 聊天消息，只保留摘要身份处理需要的字段
type SessionMessage struct {
	Platform    string
	SessionID   string
	MessageID   string
	Timestamp   time.Time
	MessageInfo ChatMessageInfo
	RawMessage  ChatRawMessage
}

// PersonProfileRow 人物主档行，对齐 PersonInfo 使用到的字段
type PersonProfileRow struct {
	PersonName     string
	UserNickname   string
	GroupCardNames []string
}

// IdentityObservation 身份登记输入，对齐 observe_person_identity 参数
type IdentityObservation struct {
	PersonID      string
	Platform      string
	UserID        string
	Aliases       []string
	GroupID       string
	PreferredName string
}

// SummaryIdentityStore 摘要身份所需的存储能力
type SummaryIdentityStore interface {
	// GetPersonIdentity 读取已登记身份，不存在返回 nil
	GetPersonIdentity(personID string) map[string]any
	// GetPersonProfileAliasOverride 读取人物主档称呼覆盖，不存在返回 nil
	GetPersonProfileAliasOverride(personID string) map[string]any
	// ObservePersonIdentity 登记身份并返回最新身份
	ObservePersonIdentity(input IdentityObservation) (map[string]any, error)
}

// SummaryIdentityHost 宿主的身份与消息查询能力
type SummaryIdentityHost interface {
	// PersonID 对齐 get_person_id
	PersonID(platform string, userID string) string
	// PersonProfile 查询人物主档
	PersonProfile(personID string) (PersonProfileRow, bool)
	// HistoricalNicknames 查询历史昵称与群名片
	HistoricalNicknames(platform string, identityPlatform string, userID string) ([]string, error)
	// FindMessages 对齐 find_messages
	FindMessages(platform string, sessionID string, messageID string, limit int) ([]SessionMessage, error)
	// RenderWithoutReplyComponents 对齐 _render_without_reply_components
	RenderWithoutReplyComponents(message SessionMessage, components []ChatMessageComponent) string
	// IsBotSelf 对齐 is_bot_self
	IsBotSelf(platform string, userID string) bool
}

// ObserveIdentityParams observe_identity 入参
type ObserveIdentityParams struct {
	Platform       string
	UserID         string
	Aliases        []string
	GroupID        string
	RefreshProfile bool
}

// ObserveIdentity 从真实发送者元数据登记身份，首次接入时补收历史称呼
func ObserveIdentity(store SummaryIdentityStore, host SummaryIdentityHost, params ObserveIdentityParams) (map[string]any, error) {
	if pyStrip(params.Platform) == "" || pyStrip(params.UserID) == "" {
		return nil, errors.New("消息缺少真实的发送者 platform/user_id")
	}
	identityPlatform := params.Platform
	if index := strings.Index(params.Platform, "-"); index >= 0 {
		identityPlatform = params.Platform[index+1:]
	}
	personID := host.PersonID(params.Platform, params.UserID)
	existing := store.GetPersonIdentity(personID)
	aliases := append([]string{}, params.Aliases...)
	preferredName := ""
	if existing == nil || params.RefreshProfile {
		if profile, ok := host.PersonProfile(personID); ok {
			preferredName = profile.PersonName
			aliases = append(aliases, profile.PersonName, profile.UserNickname)
			aliases = append(aliases, profile.GroupCardNames...)
		}
	}
	if existing == nil {
		historical, err := host.HistoricalNicknames(params.Platform, identityPlatform, params.UserID)
		if err != nil {
			return nil, err
		}
		aliases = append(aliases, historical...)
	}
	if params.RefreshProfile {
		if override := store.GetPersonProfileAliasOverride(personID); override != nil {
			aliases = append(aliases, identityAliasList(override["aliases"])...)
		}
	}
	return store.ObservePersonIdentity(IdentityObservation{
		PersonID:      personID,
		Platform:      identityPlatform,
		UserID:        params.UserID,
		Aliases:       aliases,
		GroupID:       params.GroupID,
		PreferredName: preferredName,
	})
}

// identityAliasList 展开覆盖配置里的称呼列表
func identityAliasList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string{}, typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, importValueText(item))
		}
		return out
	default:
		return nil
	}
}

// SummaryIdentityContext 为摘要构造有明确身份与引用边界的聊天窗口
type SummaryIdentityContext struct {
	store        SummaryIdentityStore
	host         SummaryIdentityHost
	messages     []SessionMessage
	identities   map[string]map[string]any
	references   map[string]string
	messageIndex map[string]*SessionMessage
}

// NewSummaryIdentityContext 创建摘要身份上下文
func NewSummaryIdentityContext(store SummaryIdentityStore, host SummaryIdentityHost, messages []SessionMessage) *SummaryIdentityContext {
	index := make(map[string]*SessionMessage, len(messages))
	for position := range messages {
		message := &messages[position]
		index[identityMessageKey(message.Platform, message.SessionID, message.MessageID)] = message
	}
	return &SummaryIdentityContext{
		store:        store,
		host:         host,
		messages:     messages,
		identities:   map[string]map[string]any{},
		references:   map[string]string{},
		messageIndex: index,
	}
}

// identityMessageKey 构造消息索引键
func identityMessageKey(platform string, sessionID string, messageID string) string {
	return platform + "\x00" + sessionID + "\x00" + messageID
}

// identity 为发送者分配或复用身份标记
func (c *SummaryIdentityContext) identity(message SessionMessage, userID string, nickname string, cardname string) (string, error) {
	groupID := ""
	if message.MessageInfo.GroupInfo != nil {
		groupID = message.MessageInfo.GroupInfo.GroupID
	}
	personID := c.host.PersonID(message.Platform, userID)
	_, referenced := c.references[personID]
	identity, err := ObserveIdentity(c.store, c.host, ObserveIdentityParams{
		Platform:       message.Platform,
		UserID:         userID,
		Aliases:        []string{nickname, cardname},
		GroupID:        groupID,
		RefreshProfile: !referenced,
	})
	if err != nil {
		return "", err
	}
	resolvedPersonID := importValueText(identity["person_id"])
	reference, exists := c.references[resolvedPersonID]
	if !exists {
		reference = fmt.Sprintf("[U%d]", len(c.references)+1)
		c.references[resolvedPersonID] = reference
		c.identities[reference] = identity
		return reference, nil
	}
	c.identities[reference] = identity
	return reference, nil
}

// dataText 屏蔽用户正文里伪造的身份标记
func (c *SummaryIdentityContext) dataText(text string) string {
	return identityReferencePattern.ReplaceAllStringFunc(text, func(match string) string {
		return strings.ReplaceAll(strings.ReplaceAll(match, "[", "［"), "]", "］")
	})
}

// reply 渲染引用组件
func (c *SummaryIdentityContext) reply(message SessionMessage, component ChatMessageComponent, depth int) (string, error) {
	original, err := c.resolveReplyTarget(message, component)
	if err != nil {
		return "", err
	}
	author := ""
	content := ""
	dateNote := ""
	if original != nil {
		sender := original.MessageInfo.UserInfo
		author, err = c.identity(*original, sender.UserID, sender.UserNickname, sender.UserCardname)
		if err != nil {
			return "", err
		}
		content, err = c.components(*original, original.RawMessage.Components, depth+1, false)
		if err != nil {
			return "", err
		}
		if !sameDate(original.Timestamp, message.Timestamp) {
			dateNote = fmt.Sprintf(" [原消息日期：%s]", formatIdentityDate(original.Timestamp))
		}
	} else if component.TargetMessageSenderID != "" {
		author, err = c.identity(message, component.TargetMessageSenderID, component.TargetMessageSenderNickname, component.TargetMessageSenderCardname)
		if err != nil {
			return "", err
		}
		content = c.dataText(component.TargetMessageContent)
		dateNote = " [原消息日期未知]"
	} else {
		author = "作者未核验"
		content = c.dataText(component.TargetMessageContent)
		if content == "" {
			content = c.dataText("原消息不可用")
		}
		dateNote = " [原消息日期未知]"
	}
	encoded, err := summaryJSONMarshal(content)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("引用 %s%s：%s", author, dateNote, encoded), nil
}

// resolveReplyTarget 在当前窗口或宿主消息库中定位被引用消息
func (c *SummaryIdentityContext) resolveReplyTarget(message SessionMessage, component ChatMessageComponent) (*SessionMessage, error) {
	key := identityMessageKey(message.Platform, message.SessionID, component.TargetMessageID)
	if original, ok := c.messageIndex[key]; ok {
		return original, nil
	}
	matches, err := c.host.FindMessages(message.Platform, message.SessionID, component.TargetMessageID, 2)
	if err != nil {
		return nil, err
	}
	if len(matches) > 1 {
		return nil, errors.New("引用消息 ID 在当前聊天流中不唯一")
	}
	if len(matches) == 0 {
		return nil, nil
	}
	matched := matches[0]
	return &matched, nil
}

// components 渲染一组组件
func (c *SummaryIdentityContext) components(message SessionMessage, components []ChatMessageComponent, depth int, includeReply bool) (string, error) {
	if depth > 5 {
		return "", errors.New("记忆输入中的引用或转发嵌套超过 5 层")
	}
	parts := make([]string, 0, len(components))
	for _, component := range components {
		switch component.Kind {
		case ComponentReply:
			if includeReply {
				rendered, err := c.reply(message, component, depth)
				if err != nil {
					return "", err
				}
				parts = append(parts, rendered)
			}
		case ComponentText:
			parts = append(parts, c.dataText(component.Text))
		case ComponentAt:
			reference, err := c.identity(message, component.TargetUserID, component.TargetUserNickname, component.TargetUserCardname)
			if err != nil {
				return "", err
			}
			parts = append(parts, "@"+reference)
		case ComponentForward:
			nodes := make([]string, 0, len(component.ForwardComponents))
			for _, node := range component.ForwardComponents {
				author := "作者未核验"
				if node.UserID != "" {
					reference, err := c.identity(message, node.UserID, node.UserNickname, node.UserCardname)
					if err != nil {
						return "", err
					}
					author = reference
				}
				content, err := c.components(message, node.Content, depth+1, true)
				if err != nil {
					return "", err
				}
				encoded, err := summaryJSONMarshal(content)
				if err != nil {
					return "", err
				}
				nodes = append(nodes, fmt.Sprintf("%s：%s", author, encoded))
			}
			parts = append(parts, "转发资料，非当前作者自述："+strings.Join(nodes, " | "))
		default:
			parts = append(parts, c.dataText(c.host.RenderWithoutReplyComponents(message, []ChatMessageComponent{component})))
		}
	}
	return pyStrip(strings.Join(parts, " ")), nil
}

// Render 渲染聊天窗口正文与身份目录
func (c *SummaryIdentityContext) Render() (string, string, error) {
	lines := make([]string, 0, len(c.messages)*2)
	currentDate := ""
	for _, message := range c.messages {
		sender := message.MessageInfo.UserInfo
		author, err := c.identity(message, sender.UserID, sender.UserNickname, sender.UserCardname)
		if err != nil {
			return "", "", err
		}
		date := formatIdentityDate(message.Timestamp)
		if date != currentDate {
			lines = append(lines, "日期："+date)
			currentDate = date
		}
		own := make([]ChatMessageComponent, 0, len(message.RawMessage.Components))
		quotes := make([]string, 0, 2)
		for _, component := range message.RawMessage.Components {
			if component.Kind == ComponentReply {
				quote, err := c.reply(message, component, 0)
				if err != nil {
					return "", "", err
				}
				quotes = append(quotes, quote)
				continue
			}
			own = append(own, component)
		}
		body, err := c.components(message, own, 0, true)
		if err != nil {
			return "", "", err
		}
		encoded, err := summaryJSONMarshal(body)
		if err != nil {
			return "", "", err
		}
		line := fmt.Sprintf("[%s] %s：%s", message.Timestamp.Format("15:04:05"), author, encoded)
		if len(quotes) > 0 {
			line += " | " + strings.Join(quotes, " | ")
		}
		lines = append(lines, line)
	}
	history := strings.Join(lines, "\n")
	catalog := make([]IdentityCatalogItem, 0, len(c.identities))
	for _, reference := range c.referenceOrder() {
		item := c.identities[reference]
		aliases := make([]string, 0, 4)
		for _, alias := range identityAliasList(item["aliases"]) {
			if alias != "" && strings.Contains(history, alias) {
				aliases = append(aliases, alias)
			}
		}
		catalog = append(catalog, IdentityCatalogItem{
			Ref:     reference,
			Name:    importValueText(item["canonical_name"]),
			Aliases: aliases,
			IsBot:   c.host.IsBotSelf(importValueText(item["platform"]), importValueText(item["user_id"])),
		})
	}
	encodedCatalog, err := summaryJSONMarshal(catalog)
	if err != nil {
		return "", "", err
	}
	return history, encodedCatalog, nil
}

// referenceOrder 按标记编号返回身份顺序
func (c *SummaryIdentityContext) referenceOrder() []string {
	ordered := make([]string, 0, len(c.identities))
	for position := 1; position <= len(c.identities); position++ {
		reference := fmt.Sprintf("[U%d]", position)
		if _, ok := c.identities[reference]; ok {
			ordered = append(ordered, reference)
		}
	}
	return ordered
}

// RestoreText 把模型返回的短标记恢复为稳定称呼
func (c *SummaryIdentityContext) RestoreText(text string) (string, error) {
	var failure error
	restored := identityReferencePattern.ReplaceAllStringFunc(text, func(match string) string {
		item, ok := c.identities[match]
		if !ok {
			if failure == nil {
				failure = fmt.Errorf("摘要返回了未登记的身份标记: %s", match)
			}
			return match
		}
		return importValueText(item["canonical_name"])
	})
	if failure != nil {
		return "", failure
	}
	return restored, nil
}

// Metadata 汇总窗口内实际使用的身份
func (c *SummaryIdentityContext) Metadata(texts []string) (map[string]any, error) {
	used := map[string]struct{}{}
	for _, text := range texts {
		for _, match := range identityReferencePattern.FindAllString(text, -1) {
			used[match] = struct{}{}
		}
	}
	for reference := range used {
		if _, ok := c.identities[reference]; !ok {
			return nil, errors.New("摘要包含未登记的身份标记")
		}
	}
	personIDs := make([]string, 0, len(used))
	bindings := make(map[string]any, len(used))
	for _, reference := range c.referenceOrder() {
		if _, ok := used[reference]; !ok {
			continue
		}
		item := c.identities[reference]
		personIDs = append(personIDs, importValueText(item["person_id"]))
		bindings[importValueText(item["canonical_name"])] = item
	}
	return map[string]any{"person_ids": personIDs, "identity_bindings": bindings}, nil
}

// IdentityCatalogItem 身份目录条目，对齐 render 输出的字典
type IdentityCatalogItem struct {
	Ref     string   `json:"ref"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	IsBot   bool     `json:"is_bot"`
}

// summaryJSONMarshal 对齐 json.dumps(ensure_ascii=False, separators=(",", ":"))
func summaryJSONMarshal(value any) (string, error) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

// formatIdentityDate 对齐 %Y-%m-%d
func formatIdentityDate(value time.Time) string {
	return value.Format("2006-01-02")
}

// sameDate 判断两个时间是否同一天
func sameDate(left time.Time, right time.Time) bool {
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}
