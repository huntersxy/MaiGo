package runtime

// 回复缓存模式：保留 Planner 请求前缀，在末尾追加完整的创意回复任务，对齐 chat/replyer/reply_cache.py

import (
	"fmt"
	"strings"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/maisaka"
)

// BuildCachedReplyRequest 不重写前缀及工具定义；保留 Replyer 全部文本和图片
func BuildCachedReplyRequest(
	snapshot maisaka.PlannerRequestSnapshot,
	replyMessages []replyer.RequestMessage,
	replyReference string,
	promptText string,
) (maisaka.PlannerRequestSnapshot, error) {
	if len(replyMessages) == 0 {
		return maisaka.PlannerRequestSnapshot{}, fmt.Errorf("回复缓存模式缺少 Replyer 请求内容")
	}
	var builder strings.Builder
	builder.WriteString(promptText)
	images := make([]maisaka.ImageComponent, 0)
	for _, message := range replyMessages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "system" && role != "user" {
			return maisaka.PlannerRequestSnapshot{}, fmt.Errorf("回复缓存模式的 Replyer 请求只允许 system/user 消息")
		}
		builder.WriteString("\n<replyer_" + role + ">\n")
		if len(message.Parts) > 0 {
			for _, part := range message.Parts {
				switch strings.ToLower(strings.TrimSpace(part.Kind)) {
				case "text":
					builder.WriteString(part.Text)
				case "image":
					if part.Image == nil {
						continue
					}
					images = append(images, maisaka.ImageComponent{
						MediaRef:  part.Image.Base64,
						Format:    part.Image.Format,
						HasBinary: true,
					})
				}
			}
		} else {
			builder.WriteString(message.Content)
		}
		builder.WriteString("\n</replyer_" + role + ">\n")
	}
	out := snapshot
	out.Items = append([]maisaka.ContextItem(nil), snapshot.Items...)
	if strings.TrimSpace(replyReference) != "" {
		out.Items = append(out.Items, maisaka.ContextItem{
			Role: maisaka.RoleAssistant,
			Text: replyReference,
		})
	}
	out.Items = append(out.Items, maisaka.ContextItem{
		Role:   maisaka.RoleUser,
		Text:   builder.String(),
		Images: images,
		Visual: len(images) > 0,
	})
	return out, nil
}
