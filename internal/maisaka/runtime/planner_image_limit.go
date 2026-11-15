package runtime

import (
	maisaka "MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/visual"
)

// limitPlannerImages 按 visual.max_image_num 限制 Planner 请求内的图片数量
//
// 对齐 Python limit_latest_images_in_messages：只保留最新的图片，超出的替换为占位文本；
// 表情候选拼图通过 PreserveImages 标记不占配额。
func (a *Agent) limitPlannerImages(items []maisaka.ContextItem, maxImageNum int) []maisaka.ContextItem {
	if maxImageNum <= 0 {
		maxImageNum = defaultPlannerMaxImageNum()
	}
	return visual.LimitLatestImagesInMessages(items, maxImageNum, "", nil)
}

// defaultPlannerMaxImageNum 对齐 visual.max_image_num 默认值 64
func defaultPlannerMaxImageNum() int {
	return 64
}
