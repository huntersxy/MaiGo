// Package messagecomponent 移植 MaiBot 的消息组件模型
//
// 对应源码：MaiBot/src/common/data_models/message_component_data_model.py
// 该模型是 MaiBot 落库格式的权威定义：MessageSequence.to_dict() 的结果经
// msgpack 打包后写入 mai_messages.raw_content，读取时再还原成组件对象。
// 因此这里的字典形状、字段缺省值与文本占位必须与 Python 版逐字对齐，
// 否则 Go 侧写出的 raw_content 无法被 Python 侧读取，反之亦然
package messagecomponent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Component 是所有消息组件的公共接口
type Component interface {
	// FormatName 对应 Python 的 format_name 属性
	FormatName() string
	// toDict 对应 Python 的 MessageSequence._item_2_dict
	toDict() map[string]any
}

// ByteComponent 承载二进制内容的组件公共字段
// 对应 Python 的 ByteComponent：Hash 取二进制数据的 SHA256，
// 没有二进制时沿用调用方传入的 hash；Binary 对应 binary_data，
// 只是进程内负载，不参与 to_dict/msgpack 编码，落库后需按 Hash 回查图片缓存
type ByteComponent struct {
	Hash    string
	Content string
	Binary  []byte
}

// newByteComponent 按 Python 语义计算哈希：有二进制数据时以数据为准
func newByteComponent(hash string, content string, binary []byte) ByteComponent {
	if len(binary) > 0 {
		sum := sha256.Sum256(binary)
		hash = hex.EncodeToString(sum[:])
	}
	return ByteComponent{Hash: hash, Content: content, Binary: append([]byte(nil), binary...)}
}

// ensureBinaryContent 对应 Python 的 _ensure_binary_component_content：
// 序列化时为空内容补稳定占位
func ensureBinaryContent(content string, fallback string) string {
	if normalized := strings.TrimSpace(content); normalized != "" {
		return normalized
	}
	return fallback
}

// TextComponent 文本组件
type TextComponent struct {
	Text string
}

// FormatName 返回组件格式名
func (TextComponent) FormatName() string { return "text" }

func (c TextComponent) toDict() map[string]any {
	return map[string]any{"type": "text", "data": c.Text}
}

// ImageComponent 图片组件
type ImageComponent struct {
	ByteComponent
}

// NewImageComponent 构造图片组件，binary 为空表示只有哈希
func NewImageComponent(hash string, content string, binary []byte) ImageComponent {
	return ImageComponent{ByteComponent: newByteComponent(hash, content, binary)}
}

// FormatName 返回组件格式名
func (ImageComponent) FormatName() string { return "image" }

func (c ImageComponent) toDict() map[string]any {
	// Python 侧直接 strip，不补占位
	return map[string]any{"type": "image", "data": strings.TrimSpace(c.Content), "hash": c.Hash}
}

// EmojiComponent 表情组件
type EmojiComponent struct {
	ByteComponent
}

// NewEmojiComponent 构造表情组件
func NewEmojiComponent(hash string, content string, binary []byte) EmojiComponent {
	return EmojiComponent{ByteComponent: newByteComponent(hash, content, binary)}
}

// FormatName 返回组件格式名
func (EmojiComponent) FormatName() string { return "emoji" }

func (c EmojiComponent) toDict() map[string]any {
	return map[string]any{"type": "emoji", "data": ensureBinaryContent(c.Content, "[表情包]"), "hash": c.Hash}
}

// VoiceComponent 语音组件
// Binary/Source 是进程内转写输入，不参与 to_dict/msgpack 编码：
// raw_content 必须保持 Python 的 {type,data,hash} 三键形状
type VoiceComponent struct {
	ByteComponent
	// Source 音频媒体引用（data URI/base64://、URL 或平台文件标识）
	Source string
}

// NewVoiceComponent 构造语音组件，binary 只用于计算 hash 并保留在内存
func NewVoiceComponent(hash string, content string, binary []byte) VoiceComponent {
	return VoiceComponent{ByteComponent: newByteComponent(hash, content, binary)}
}

// NewVoiceComponentFromSource 构造只携带媒体引用的语音组件
func NewVoiceComponentFromSource(source string) VoiceComponent {
	return VoiceComponent{Source: strings.TrimSpace(source)}
}

// FormatName 返回组件格式名
func (VoiceComponent) FormatName() string { return "voice" }

func (c VoiceComponent) toDict() map[string]any {
	return map[string]any{"type": "voice", "data": ensureBinaryContent(c.Content, "[语音消息]"), "hash": c.Hash}
}

// FileComponent 文件组件
type FileComponent struct {
	Name       string
	Size       string
	URL        string
	FileID     string
	MimeType   string
	Base64Data string
	FileRef    string
	Busid      int
}

// FormatName 返回组件格式名
func (FileComponent) FormatName() string { return "file" }

// ToPayload 对应 Python 的 to_payload：只写入非空字段
func (c FileComponent) ToPayload() map[string]any {
	payload := map[string]any{}
	if c.FileRef != "" {
		payload["file_ref"] = c.FileRef
	}
	if c.Busid != 0 {
		payload["busid"] = c.Busid
	}
	if c.Name != "" {
		payload["name"] = c.Name
	}
	if c.Size != "" {
		payload["size"] = c.Size
	}
	if c.URL != "" {
		payload["url"] = c.URL
	}
	if c.FileID != "" {
		payload["file_id"] = c.FileID
	}
	if c.MimeType != "" {
		payload["mime_type"] = c.MimeType
	}
	if c.Base64Data != "" {
		payload["base64"] = c.Base64Data
	}
	return payload
}

// FileComponentFromPayload 对应 Python 的 FileComponent.from_payload：
// 兼容 name/file/file_name/filename 等历史键
func FileComponentFromPayload(payload map[string]any) FileComponent {
	return FileComponent{
		Name:       firstNonEmpty(payload, "name", "file", "file_name", "filename"),
		Size:       firstNonEmpty(payload, "size", "file_size"),
		URL:        firstNonEmpty(payload, "url", "file_url"),
		FileID:     firstNonEmpty(payload, "file_id", "id"),
		MimeType:   firstNonEmpty(payload, "mime_type", "mimeType"),
		Base64Data: firstNonEmpty(payload, "base64"),
		FileRef:    firstNonEmpty(payload, "file_ref"),
		Busid:      intValue(payload["busid"]),
	}
}

func (c FileComponent) toDict() map[string]any {
	return map[string]any{"type": "file", "data": c.ToPayload()}
}

// ToPlainText 对应 Python 的 to_plain_text
func (c FileComponent) ToPlainText() string {
	parts := make([]string, 0, 4)
	if c.Name != "" {
		parts = append(parts, c.Name)
	}
	if c.Size != "" {
		parts = append(parts, "大小: "+c.Size)
	}
	if c.MimeType != "" {
		parts = append(parts, "类型: "+c.MimeType)
	}
	if c.FileRef != "" {
		parts = append(parts, "file_ref: "+c.FileRef)
	}
	if len(parts) == 0 {
		return "[文件]"
	}
	return "[文件] " + strings.Join(parts, "，")
}

// AtComponent @ 组件
type AtComponent struct {
	TargetUserID string
	Nickname     *string
	Cardname     *string
	UsesBotNick  bool
}

// FormatName 返回组件格式名
func (AtComponent) FormatName() string { return "at" }

func (c AtComponent) toDict() map[string]any {
	return map[string]any{
		"type": "at",
		"data": map[string]any{
			"target_user_id":               c.TargetUserID,
			"target_user_nickname":         c.Nickname,
			"target_user_cardname":         c.Cardname,
			"uses_configured_bot_nickname": c.UsesBotNick,
		},
	}
}

// ReplyComponent 回复组件
type ReplyComponent struct {
	TargetMessageID string
	TargetContent   *string
	SenderID        *string
	SenderNickname  *string
	SenderCardname  *string
}

// FormatName 返回组件格式名
func (ReplyComponent) FormatName() string { return "reply" }

func (c ReplyComponent) toDict() map[string]any {
	// 原消息可能不在本地历史中，摘要重读时仍需要已确认的引用作者和正文
	return map[string]any{
		"type":                           "reply",
		"data":                           c.TargetMessageID,
		"target_message_content":         c.TargetContent,
		"target_message_sender_id":       c.SenderID,
		"target_message_sender_nickname": c.SenderNickname,
		"target_message_sender_cardname": c.SenderCardname,
	}
}

// ForwardComponent 是转发节点里的一条消息
type ForwardComponent struct {
	UserNickname string
	MessageID    string
	Content      []Component
	UserID       *string
	UserCardname *string
}

// ForwardNodeComponent 合并转发的一个节点
type ForwardNodeComponent struct {
	ForwardComponents []ForwardComponent
}

// FormatName 返回组件格式名
func (ForwardNodeComponent) FormatName() string { return "forward_node" }

func (c ForwardNodeComponent) toDict() map[string]any {
	nodes := make([]any, 0, len(c.ForwardComponents))
	for _, comp := range c.ForwardComponents {
		content := make([]any, 0, len(comp.Content))
		for _, inner := range comp.Content {
			content = append(content, inner.toDict())
		}
		nodes = append(nodes, map[string]any{
			"user_id":       comp.UserID,
			"user_nickname": comp.UserNickname,
			"user_cardname": comp.UserCardname,
			"message_id":    comp.MessageID,
			"content":       content,
		})
	}
	return map[string]any{"type": "forward", "data": nodes}
}

// DictComponent 未知组件的兜底载体；Type 保留原始段类型（如 mention_bot）
type DictComponent struct {
	Type string
	Data map[string]any
}

// FormatName 返回组件格式名；未知类型优先回传原始 Type
func (c DictComponent) FormatName() string {
	if name := strings.TrimSpace(c.Type); name != "" {
		return name
	}
	return "dict"
}

func (c DictComponent) toDict() map[string]any {
	typeName := strings.TrimSpace(c.Type)
	if typeName == "" {
		typeName = "dict"
	}
	return map[string]any{"type": typeName, "data": c.Data}
}

func firstNonEmpty(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key]; ok && value != nil {
			if text, ok := value.(string); ok && text != "" {
				return text
			}
		}
	}
	return ""
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		result := 0
		for _, ch := range typed {
			if ch < '0' || ch > '9' {
				break
			}
			result = result*10 + int(ch-'0')
		}
		return result
	default:
		return 0
	}
}
