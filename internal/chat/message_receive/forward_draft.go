package messagereceive

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/database"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// ForwardDraft 一份待落库的合并转发，父级排在嵌套之前
type ForwardDraft struct {
	Handle string
	Parent string
	Nodes  []memory.ForwardNode
}

func buildForwardDrafts(forward messagecomponent.ForwardNodeComponent, parent string, depth int) []ForwardDraft {
	handle := newForwardHandle()
	if handle == "" || depth > onebot.MaxForwardDepth || len(forward.ForwardComponents) == 0 {
		return nil
	}
	drafts := []ForwardDraft{{Handle: handle, Parent: parent}}
	for _, entry := range forward.ForwardComponents {
		node := memory.ForwardNode{Sender: replyDisplayName(entry.UserCardname, &entry.UserNickname, "未知用户")}
		var text strings.Builder
		for _, component := range entry.Content {
			switch item := component.(type) {
			case messagecomponent.TextComponent:
				text.WriteString(item.Text)
			case messagecomponent.AtComponent:
				text.WriteString("@" + replyDisplayName(item.Cardname, item.Nickname, item.TargetUserID))
			case messagecomponent.ReplyComponent:
				if item.TargetContent != nil {
					text.WriteString("[引用: " + *item.TargetContent + "]")
				}
			case messagecomponent.FileComponent:
				text.WriteString(item.ToPlainText())
			case messagecomponent.ImageComponent:
				text.WriteString("[图片]" + item.Content)
			case messagecomponent.EmojiComponent:
				text.WriteString("[表情包]" + item.Content)
			case messagecomponent.VoiceComponent:
				text.WriteString("[语音]" + item.Content)
			case messagecomponent.DictComponent:
				text.WriteString(processDictComponent(item))
			case messagecomponent.ForwardNodeComponent:
				if children := buildForwardDrafts(item, handle, depth+1); len(children) > 0 {
					node.Nested = append(node.Nested, children[0].Handle)
					drafts = append(drafts, children...)
				}
			}
		}
		node.Content = strings.TrimSpace(text.String())
		drafts[0].Nodes = append(drafts[0].Nodes, node)
	}
	return drafts
}

// newForwardHandle 生成一个全局唯一的转发句柄
func newForwardHandle() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	return "f" + hex.EncodeToString(buf[:])
}
