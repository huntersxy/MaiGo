package messagereceive

import (
	"time"

	chatutils "maibot-go/internal/chat/utils"
)

// TypingConfig 对齐 MaiBot 打字时长配置（保留本包名称，供 sender 使用）
type TypingConfig = chatutils.TypingConfig

// CalculateTypingTime 对齐 utils.calculate_typing_time，返回秒
//
// 实现位于 chat/utils（对齐 Python src/chat/utils/utils.py）。
func CalculateTypingTime(input string, cfg TypingConfig, typingSpeed float64) float64 {
	return chatutils.CalculateTypingTime(input, cfg, typingSpeed)
}

// TruncateMessage 对齐 truncate_message，按字符截断
func TruncateMessage(message string, max int) string {
	return chatutils.TruncateMessage(message, max)
}

// floorDuration 将秒转成 time.Duration，避免负值
func floorDuration(seconds float64) time.Duration {
	return chatutils.FloorDuration(seconds)
}
