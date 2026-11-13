// Package platformio 提供 MaiBot Platform IO 中间层的 Go 抽象，供多适配器入站/出站与路由复用
package platformio

import (
	"strings"
	"time"
)

// DriverKind 底层收发驱动类型，对齐 MaiBot platform_io.types.DriverKind
type DriverKind string

const (
	DriverKindLegacy DriverKind = "legacy"
	DriverKindPlugin DriverKind = "plugin"
	DriverKindLocal  DriverKind = "local"
)

// DeliveryStatus 统一出站回执状态
type DeliveryStatus string

const (
	DeliveryStatusPending DeliveryStatus = "pending"
	DeliveryStatusSent    DeliveryStatus = "sent"
	DeliveryStatusFailed  DeliveryStatus = "failed"
	DeliveryStatusDropped DeliveryStatus = "dropped"
)

// ChatType 会话类型，与 MaiBot 出站 chat_type 字符串一致
type ChatType string

const (
	ChatTypeGroup   ChatType = "group"
	ChatTypePrivate ChatType = "private"
)

// PlatformQQ 是 QQ 平台标识；其余平台名由适配器声明
const PlatformQQ = "qq"

// SegmentKind 消息段类型（与 maim_message / OneBot 段类型对齐的子集）
type SegmentKind string

const (
	SegmentText    SegmentKind = "text"
	SegmentAt      SegmentKind = "at"
	SegmentImage   SegmentKind = "image"
	SegmentFace    SegmentKind = "face"
	SegmentReply   SegmentKind = "reply"
	SegmentFile    SegmentKind = "file"
	SegmentEmoji   SegmentKind = "emoji"
	SegmentVoice   SegmentKind = "voice"
	SegmentVideo   SegmentKind = "video"
	SegmentRecord  SegmentKind = "record"
	SegmentForward SegmentKind = "forward"
	SegmentJSON    SegmentKind = "json"
	SegmentDict    SegmentKind = "dict"
)

// ForwardNode 合并转发中的单个消息节点
// 对齐 MaiBot ForwardNodeComponent：发送者信息 + 该节点内的消息段
type ForwardNode struct {
	UserID    string
	Nickname  string
	Cardname  string
	MessageID string
	Time      int64
	Segments  []Segment
}

// Segment 规范化消息段
type Segment struct {
	Kind     SegmentKind
	Text     string
	AtUserID string
	URL      string
	// Data 保存 dict/custom 段的原始负载，对齐 Python DictComponent.data
	Data         any
	Extra        map[string]string
	ForwardNodes []ForwardNode
}

// ChatMessage  practical Go 层会话消息，对应 MaiBot SessionMessage 的核心路由/展示字段
type ChatMessage struct {
	Platform   string
	ChatType   ChatType
	AccountID  string // 机器人账号 / self_id
	Scope      string
	UserID     string
	GroupID    string // 群聊时非空
	TargetID   string // 群号或私聊对端
	MessageID  string
	Segments   []Segment
	PlainText  string
	ReceivedAt time.Time
	Metadata   map[string]any
}

// OutboundUserInfo 出站消息的发送者信息，对齐 Python UserInfoDict
type OutboundUserInfo struct {
	UserID       string
	UserNickname string
	UserCardname string
}

// OutboundGroupInfo 出站消息的群信息，对齐 Python GroupInfoDict
type OutboundGroupInfo struct {
	GroupID   string
	GroupName string
}

// OutboundMessageInfo 出站消息的 message_info，对齐 Python MessageInfoDict
type OutboundMessageInfo struct {
	UserInfo         OutboundUserInfo
	GroupInfo        *OutboundGroupInfo
	AdditionalConfig map[string]any
}

// RouteKey 用于 Platform IO 路由决策的唯一键
type RouteKey struct {
	Platform  string
	AccountID string // 空表示未指定
	Scope     string
}

// Normalize 规范化路由键字段
func (k RouteKey) Normalize() (RouteKey, error) {
	platform := strings.ToLower(strings.TrimSpace(k.Platform))
	if platform == "" {
		return RouteKey{}, errEmptyRoutePlatform
	}
	return RouteKey{
		Platform:  platform,
		AccountID: strings.TrimSpace(k.AccountID),
		Scope:     strings.TrimSpace(k.Scope),
	}, nil
}

// ResolutionOrder 从最具体到最宽泛的回退匹配顺序
func (k RouteKey) ResolutionOrder() []RouteKey {
	keys := []RouteKey{k}
	hasAccount := k.AccountID != ""
	hasScope := k.Scope != ""

	if hasAccount && hasScope {
		keys = append(keys,
			RouteKey{Platform: k.Platform, AccountID: k.AccountID},
			RouteKey{Platform: k.Platform, Scope: k.Scope},
		)
	} else if hasAccount {
		keys = append(keys, RouteKey{Platform: k.Platform})
	} else if hasScope {
		keys = append(keys, RouteKey{Platform: k.Platform})
	}

	defaultKey := RouteKey{Platform: k.Platform}
	if !routeKeyEqual(defaultKey, keys[len(keys)-1]) {
		keys = append(keys, defaultKey)
	}
	return keys
}

// ToDedupeScope 生成跨驱动共享的去重作用域字符串
func (k RouteKey) ToDedupeScope() string {
	accountID := k.AccountID
	if accountID == "" {
		accountID = "*"
	}
	scope := k.Scope
	if scope == "" {
		scope = "*"
	}
	return k.Platform + ":" + accountID + ":" + scope
}

func routeKeyEqual(a, b RouteKey) bool {
	return a.Platform == b.Platform && a.AccountID == b.AccountID && a.Scope == b.Scope
}

// tableKey 路由表内部键
func (k RouteKey) tableKey() string {
	return k.Platform + "\x00" + k.AccountID + "\x00" + k.Scope
}

// DriverDescriptor 已注册驱动的静态描述
type DriverDescriptor struct {
	DriverID  string
	Kind      DriverKind
	Platform  string
	AccountID string
	Scope     string
	PluginID  string
	Metadata  map[string]any
}

// RouteKey 返回该驱动默认代表的路由键
func (d DriverDescriptor) RouteKey() RouteKey {
	return RouteKey{
		Platform:  d.Platform,
		AccountID: d.AccountID,
		Scope:     d.Scope,
	}
}

// RouteBinding 路由键到驱动的绑定
type RouteBinding struct {
	RouteKey   RouteKey
	DriverID   string
	DriverKind DriverKind
	Priority   int
	Metadata   map[string]any
}

// InboundMessageEnvelope 驱动产出的规范化入站封装
type InboundMessageEnvelope struct {
	RouteKey          RouteKey
	DriverID          string
	DriverKind        DriverKind
	ExternalMessageID string
	DedupeKey         string
	ChatMessage       *ChatMessage
	Payload           map[string]any
	Metadata          map[string]any
	ReceivedAt        time.Time
}

// OutboundMessageEnvelope 出站投递载荷（SessionMessage 落地前的 practical 形态）
type OutboundMessageEnvelope struct {
	InternalMessageID        string
	RouteKey                 RouteKey
	ChatType                 ChatType
	TargetID                 string
	Segments                 []Segment
	PlainText                string
	ReplyToMessageID         string
	ExternalReplyToMessageID string // 非 QQ 平台被回复消息的原始字符串 ID
	Metadata                 map[string]any

	// 以下字段对齐 Python SessionMessage，供插件网关消息字典使用
	Timestamp   time.Time
	SessionID   string
	MessageInfo OutboundMessageInfo
	IsMentioned bool
	IsAt        bool
	IsEmoji     bool
	IsPicture   bool
	IsCommand   bool
	IsNotify    bool
	ReceivedAt  *time.Time
}

// DeliveryReceipt 单次出站投递结果
type DeliveryReceipt struct {
	InternalMessageID string
	RouteKey          RouteKey
	Status            DeliveryStatus
	DriverID          string
	DriverKind        DriverKind
	ExternalMessageID string
	Error             string
	Metadata          map[string]any
}

// DeliveryBatch 广播式出站批量结果
type DeliveryBatch struct {
	InternalMessageID string
	RouteKey          RouteKey
	Receipts          []DeliveryReceipt
}

// SentReceipts 返回发送成功的回执
func (b DeliveryBatch) SentReceipts() []DeliveryReceipt {
	out := make([]DeliveryReceipt, 0, len(b.Receipts))
	for _, r := range b.Receipts {
		if r.Status == DeliveryStatusSent {
			out = append(out, r)
		}
	}
	return out
}

// FailedReceipts 返回未成功送达的回执，对齐 Python failed_receipts（含 DROPPED）
func (b DeliveryBatch) FailedReceipts() []DeliveryReceipt {
	out := make([]DeliveryReceipt, 0, len(b.Receipts))
	for _, r := range b.Receipts {
		if r.Status != DeliveryStatusSent {
			out = append(out, r)
		}
	}
	return out
}

// DroppedReceipts 返回被适配器策略丢弃的回执
func (b DeliveryBatch) DroppedReceipts() []DeliveryReceipt {
	out := make([]DeliveryReceipt, 0, len(b.Receipts))
	for _, r := range b.Receipts {
		if r.Status == DeliveryStatusDropped {
			out = append(out, r)
		}
	}
	return out
}

// HasSuccess 是否至少有一条成功回执
func (b DeliveryBatch) HasSuccess() bool {
	return len(b.SentReceipts()) > 0
}
