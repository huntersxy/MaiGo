package context

import (
	"fmt"
	"strings"

	maisaka "maibot-go/internal/maisaka"
)

// ImageReference 返回错误文案里的图片引用描述，对齐 Python image_attachment.py:30-31
func ImageReference(sourceID string) string {
	normalized := strings.TrimSpace(sourceID)
	if strings.HasPrefix(normalized, "tool_result:") {
		return "media_index=" + normalized
	}
	return "msg_id=" + maisaka.ToDisplayMessageID(normalized)
}

// ExpandContextMessageID 按当前上下文消息编号展开模型可见短编号，新消息优先
func ExpandContextMessageID(history []maisaka.ContextMessage, sourceID string) string {
	normalized := strings.TrimSpace(sourceID)
	if normalized == "" {
		return ""
	}
	messageIDs := make([]string, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != maisaka.RoleUser || strings.TrimSpace(message.MessageID) == "" {
			continue
		}
		messageIDs = append(messageIDs, message.MessageID)
	}
	if expanded, ok := maisaka.BuildAliasMap(messageIDs)[normalized]; ok {
		return expanded
	}
	return normalized
}

// ResolveContextImage 按当前上下文消息编号或工具媒体索引读取图片，保留原始图片序号
// 错误文案与判定顺序对齐 Python src/maisaka/context/image_attachment.py:14-58
func ResolveContextImage(history []maisaka.ContextMessage, sourceID string, imageIndex int) (maisaka.ImageComponent, error) {
	if strings.TrimSpace(sourceID) == "" {
		return maisaka.ImageComponent{}, fmt.Errorf("图片附件需要提供 msg_id 或 media_index。")
	}
	if imageIndex < 0 {
		return maisaka.ImageComponent{}, fmt.Errorf("图片序号必须是非负整数。")
	}
	normalized := ExpandContextMessageID(history, sourceID)
	reference := ImageReference(normalized)
	var target *maisaka.ContextMessage
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != maisaka.RoleUser {
			continue
		}
		// Go 的 focus 媒体消息只写 MediaIndex，这里同时匹配以保持既有可解析性
		if message.MessageID != normalized && message.MediaIndex != normalized {
			continue
		}
		target = &history[index]
		break
	}
	if target == nil {
		if strings.HasPrefix(normalized, "tool_result:") {
			return maisaka.ImageComponent{}, fmt.Errorf("没有找到工具媒体：%s。请使用上下文实际提供的图片索引；文本或 SVG 源码不是图片附件。", reference)
		}
		return maisaka.ImageComponent{}, fmt.Errorf("没有找到消息：%s", reference)
	}
	// 保留原始图片序号；不能过滤加载失败的图片，否则会发送另一张图
	if imageIndex >= len(target.Images) {
		return maisaka.ImageComponent{}, fmt.Errorf("图片序号超出范围：index=%d，该消息共有 %d 张图片。", imageIndex, len(target.Images))
	}
	return target.Images[imageIndex], nil
}
