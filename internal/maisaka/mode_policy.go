package maisaka

import "strings"

// PromptSource 提供 prompt 模板，对齐 Python load_prompt
type PromptSource interface {
	Get(name string) (string, error)
}

// PromptRenderer 可选实现，按模板变量完整渲染；未实现时调用方保留旧替换逻辑
type PromptRenderer interface {
	Render(name string, vars map[string]string) (string, error)
}

// BuildConsecutiveWaitReminders 连续等待三次后，在每轮请求尾部提醒，直到其他操作重置计数
//
// Python 版本从全局 prompt_i18n 读取；Go 侧显式传入 PromptSource，避免包级全局状态
func BuildConsecutiveWaitReminders(source PromptSource, consecutiveWaitCount int) []string {
	if consecutiveWaitCount < 3 || source == nil {
		return nil
	}
	text, err := source.Get("maisaka_wait_reminder")
	if err != nil {
		return nil
	}
	// 对齐 Python load_prompt(...).strip()：模板两端空白不进入请求
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []string{text}
}
