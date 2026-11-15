package messagereceive

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/common/media"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// PendingMedia 待下载缓存的媒体
type PendingMedia struct {
	URL     string
	File    string
	Sticker bool
}

// ContentPiece 展示正文的一段：普通文本或媒体占位
type ContentPiece struct {
	Text     string
	MediaIdx int    // >=0 表示这是待缓存媒体里的第几份，-1 表示纯文本
	Fallback string // 媒体缓存失败时保留的文字占位
}

// MentionNameResolver 按 @ 目标解析显示名，对应 Python SessionMessage 借助 chat stream 的成员名查询
type MentionNameResolver func(ctx context.Context, msg *onebot.ConversationMessage, userID int64) string

// RenderContentPieces 渲染展示正文，媒体占位带上会话内短哈希
func RenderContentPieces(pieces []ContentPiece, refs []media.Ref) string {
	var b strings.Builder
	for _, piece := range pieces {
		if piece.MediaIdx < 0 {
			b.WriteString(piece.Text)
			continue
		}
		if piece.MediaIdx >= len(refs) {
			continue
		}
		ref := refs[piece.MediaIdx]
		if ref.Hash == "" || ref.Short == "" {
			// 缓存失败时保留原文字占位，不伪造图片
			b.WriteString(piece.Fallback)
			continue
		}
		if ref.Sticker {
			if ref.Collected {
				b.WriteString(" [表情包:" + ref.Short + "(已收藏)]")
				continue
			}
			b.WriteString(" [表情包:" + ref.Short + "]")
			continue
		}
		b.WriteString(" [图片:" + ref.Short + "]")
	}
	return strings.TrimSpace(b.String())
}

// ParseMessageContent 解析消息段，产出展示正文片段、待缓存的媒体与待落库的转发。
// 对应 Python SessionMessage.process_single_component 的分段处理
func ParseMessageContent(ctx context.Context, msg *onebot.ConversationMessage, resolveMention MentionNameResolver) ([]ContentPiece, []PendingMedia, []ForwardDraft) {
	var pieces []ContentPiece
	var pending []PendingMedia
	var forwards []ForwardDraft
	for _, part := range msg.MessageParts {
		switch part.Kind {
		case "text":
			pieces = append(pieces, ContentPiece{Text: part.Text, MediaIdx: -1})
		case "reply":
			// 对齐 Python process_reply_component：引用段必须产出可读文本，
			// 否则 Agent 重算 FinalContent 时会把已解析的引用原文丢掉
			if text := renderReplyPart(msg, part); text != "" {
				pieces = append(pieces, ContentPiece{Text: text, MediaIdx: -1})
			}
		case "at":
			if part.AtUserID == onebot.AtAllUserID {
				pieces = append(pieces, ContentPiece{Text: "@全体成员", MediaIdx: -1})
				continue
			}
			if part.AtUserID > 0 {
				pieces = append(pieces, ContentPiece{Text: "@" + resolveMention(ctx, msg, part.AtUserID), MediaIdx: -1})
			}
		case "face":
			if part.Index < 0 || part.Index >= len(msg.Faces) {
				continue
			}
			face := msg.Faces[part.Index]
			switch {
			case face.Name != "":
				pieces = append(pieces, ContentPiece{Text: fmt.Sprintf(" %s%s%s", onebot.QFaceMarker, face.Name, onebot.QFaceMarkerEnd), MediaIdx: -1})
			case face.ID > 0:
				pieces = append(pieces, ContentPiece{Text: fmt.Sprintf(" %s%d%s", onebot.QFaceMarker, face.ID, onebot.QFaceMarkerEnd), MediaIdx: -1})
			default:
				pieces = append(pieces, ContentPiece{Text: " [无法识别的表情]", MediaIdx: -1})
			}
		case "image":
			if part.Index < 0 || part.Index >= len(msg.Images) {
				continue
			}
			image := msg.Images[part.Index]
			sticker := image.SubType == 1
			fallback := " [图片]"
			if sticker {
				fallback = " [表情包]"
			}
			pending = append(pending, PendingMedia{URL: image.URL, File: image.File, Sticker: sticker})
			pieces = append(pieces, ContentPiece{MediaIdx: len(pending) - 1, Fallback: fallback})
		case "video":
			pieces = append(pieces, ContentPiece{Text: " [视频，无法查看]", MediaIdx: -1})
		case "record":
			pieces = append(pieces, ContentPiece{Text: " [语音]", MediaIdx: -1})
		case "file":
			name := ""
			webUIFile := false
			if part.Index >= 0 && part.Index < len(msg.FileNames) {
				name = msg.FileNames[part.Index]
			} else if part.Index >= 0 && part.Index < len(msg.Files) {
				name = msg.Files[part.Index].Name
				webUIFile = true
			}
			if name == "" && !webUIFile {
				continue
			}
			text := " [文件]"
			if webUIFile && name != "" {
				text = " [文件] " + name
			}
			pieces = append(pieces, ContentPiece{Text: text, MediaIdx: -1})
		case "card":
			if part.Index < 0 || part.Index >= len(msg.Cards) {
				continue
			}
			pieces = append(pieces, ContentPiece{Text: " " + msg.Cards[part.Index].Format(), MediaIdx: -1})
		}
	}
	return pieces, pending, forwards
}

// renderReplyPart 构造引用段的展示文本
// 对齐 Python process_reply_component 的四个分支：已有原文+作者 / 已解析原文 / 作者未核验 / 无法访问
func renderReplyPart(msg *onebot.ConversationMessage, part onebot.MessagePart) string {
	if msg == nil {
		return ""
	}
	reply := msg.Reply
	if reply == nil {
		// 只有段文本（适配器直接给出目标 ID）时无法还原原文
		if id := strings.TrimSpace(part.Text); id != "" {
			return "[回复了一条消息，但原消息已无法访问]"
		}
		return ""
	}
	content := strings.TrimSpace(reply.Content)
	if content == "" {
		return "[回复了一条消息，但原消息已无法访问]"
	}
	name := strings.TrimSpace(reply.GroupCard)
	if name == "" {
		name = strings.TrimSpace(reply.Nickname)
	}
	if name == "" {
		name = strings.TrimSpace(reply.ExternalSenderID)
	}
	if name == "" && reply.SenderID > 0 {
		name = strconv.FormatInt(reply.SenderID, 10)
	}
	if name == "" {
		return "[回复了一条作者未核验的消息: " + content + "]"
	}
	return "[回复了" + name + "的消息: " + content + "]"
}
