// Package utils 是聊天链路的跨模块工具层。
//
// 对齐 MaiBot src/chat/utils/utils.py：calculate_typing_time、truncate_message、
// is_bot_self 等被 replyer、message_receive、learners 与 A_memorix 多处引用，
// 属于共享工具而非某个子模块私有。
package utils

import (
	"strings"
	"time"
)

// TypingConfig 对齐 MaiBot 打字时长配置
type TypingConfig struct {
	ChineseTimeSec float64
	EnglishTimeSec float64
	IsEmoji        bool
	IsImage        bool
}

// CalculateTypingTime 移植 utils.calculate_typing_time，返回秒
//
// 默认值 0.3/0.15 秒、单字中文三段延时、表情与图片固定 1 秒，
// 均与 Python 保持一致；typingSpeed 在最后统一相乘。
func CalculateTypingTime(input string, cfg TypingConfig, typingSpeed float64) float64 {
	chineseTime := cfg.ChineseTimeSec
	if chineseTime == 0 {
		chineseTime = 0.3
	}
	englishTime := cfg.EnglishTimeSec
	if englishTime == 0 {
		englishTime = 0.15
	}
	trimmed := strings.TrimSpace(input)
	chineseCount := 0
	for _, r := range input {
		if r >= 0x4E00 && r <= 0x9FFF {
			chineseCount++
		}
	}
	// 单字分支直接返回，不乘 typingSpeed：Python 的 typing_speed<=0 判断在函数末尾，
	// 这条提前 return 会绕过它，因此 speed=0 时单字仍返回 chinese_time*3+0.3。
	if !cfg.IsEmoji && !cfg.IsImage && chineseCount == 1 && len([]rune(trimmed)) == 1 {
		return chineseTime*3 + 0.3
	}
	var total float64
	for _, r := range input {
		if r >= 0x4E00 && r <= 0x9FFF {
			total += chineseTime
		} else {
			total += englishTime
		}
	}
	if cfg.IsEmoji || cfg.IsImage {
		total = 1
	}
	if typingSpeed <= 0 {
		return 0
	}
	return total * typingSpeed
}

// TruncateMessage 对齐 truncate_message，按字符（rune）截断。
// Python 默认 max_length=20 只在“省略参数”时生效；显式传入 0/负数时按切片语义处理，不做回退。
func TruncateMessage(message string, max int) string {
	runes := []rune(message)
	if len(runes) <= max {
		return message
	}
	end := max
	if max < 0 {
		end = len(runes) + max
		if end < 0 {
			end = 0
		}
	}
	return string(runes[:end]) + "..."
}

// FloorDuration 将秒转为 time.Duration，负值归零
func FloorDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}
