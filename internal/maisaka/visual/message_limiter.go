package visual

import (
	"strings"

	maisaka "MaiGo/internal/maisaka"
)

// ImageLimitPlaceholder 超限图片替换文本
const ImageLimitPlaceholder = "[图片]"

// LimitLatestImagesInMessages 限制 prompt 中的图片数量，只保留最新的图片
//
// 对齐 Python limit_latest_images_in_messages：preservedItemIDs 中的工具结果图不占普通图片配额，
// 也不被改写，保证历史前缀稳定。
func LimitLatestImagesInMessages(messages []maisaka.ContextItem, maxImageNum int, placeholder string, preservedItemIDs map[string]struct{}) []maisaka.ContextItem {
	if placeholder == "" {
		placeholder = ImageLimitPlaceholder
	}
	if maxImageNum < 0 {
		maxImageNum = 0
	}
	type position struct{ messageIndex, imageIndex int }
	positions := make([]position, 0, len(messages))
	for messageIndex, message := range messages {
		if isPreservedMessage(message, preservedItemIDs) {
			continue
		}
		if message.Role != maisaka.RoleSystem && message.Role != maisaka.RoleUser && message.Role != maisaka.RoleAssistant {
			continue
		}
		for imageIndex := range message.Images {
			positions = append(positions, position{messageIndex, imageIndex})
		}
	}
	if len(positions) <= maxImageNum {
		return append([]maisaka.ContextItem{}, messages...)
	}
	keep := map[position]struct{}{}
	if maxImageNum > 0 {
		for _, item := range positions[len(positions)-maxImageNum:] {
			keep[item] = struct{}{}
		}
	}
	limited := make([]maisaka.ContextItem, 0, len(messages))
	for messageIndex, message := range messages {
		if isPreservedMessage(message, preservedItemIDs) {
			limited = append(limited, message)
			continue
		}
		if message.Role != maisaka.RoleSystem && message.Role != maisaka.RoleUser && message.Role != maisaka.RoleAssistant {
			limited = append(limited, message)
			continue
		}
		keptImages := make([]maisaka.ImageComponent, 0, len(message.Images))
		placed := make([]string, 0, len(message.Images))
		for imageIndex, image := range message.Images {
			if _, ok := keep[position{messageIndex, imageIndex}]; !ok {
				placed = append(placed, placeholder)
				continue
			}
			keptImages = append(keptImages, image)
		}
		if len(keptImages) == len(message.Images) {
			limited = append(limited, message)
			continue
		}
		message.Images = keptImages
		if len(placed) > 0 {
			message.Text = strings.TrimSpace(message.Text + " " + strings.Join(placed, " "))
		}
		limited = append(limited, message)
	}
	return limited
}

func isPreservedMessage(message maisaka.ContextItem, preservedItemIDs map[string]struct{}) bool {
	if message.ToolCallID == "" {
		return false
	}
	_, preserved := preservedItemIDs[message.ToolCallID]
	return preserved
}
