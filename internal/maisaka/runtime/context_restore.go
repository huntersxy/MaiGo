package runtime

import (
	"fmt"
	"time"

	maisaka "maibot-go/internal/maisaka"

	"github.com/cloudwego/eino/schema"
)

// formatContextRestoreElapsed 对齐 Python _format_context_restore_elapsed
func formatContextRestoreElapsed(elapsedSeconds float64) string {
	totalSeconds := int(elapsedSeconds)
	if totalSeconds < 0 {
		totalSeconds = 0
	}
	if totalSeconds < 60 {
		return "不到 1 分钟"
	}
	totalMinutes := totalSeconds / 60
	if totalMinutes < 60 {
		seconds := totalSeconds % 60
		if seconds == 0 {
			return fmt.Sprintf("%d 分钟", totalMinutes)
		}
		return fmt.Sprintf("%d 分 %d 秒", totalMinutes, seconds)
	}
	totalHours := totalMinutes / 60
	minutes := totalMinutes % 60
	if totalHours < 24 {
		if minutes == 0 {
			return fmt.Sprintf("%d 小时", totalHours)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", totalHours, minutes)
	}
	days := totalHours / 24
	hours := totalHours % 24
	if hours == 0 {
		return fmt.Sprintf("%d 天", days)
	}
	return fmt.Sprintf("%d 天 %d 小时", days, hours)
}

// buildContextRestoreWakeupText 对齐 Python _build_context_restore_wakeup_text
func buildContextRestoreWakeupText(elapsedSeconds float64, elapsedText string) string {
	switch {
	case elapsedSeconds < maisaka.ContextRestoreShortRestartSeconds:
		return "你只是短暂重启了一下，几乎没有明显中断，可以自然接上刚才的话题。"
	case elapsedSeconds < maisaka.ContextRestoreShortOfflineSeconds:
		return "你短暂离线了 " + elapsedText + "，仍然清楚记得刚才的聊天内容。"
	case elapsedSeconds < maisaka.ContextRestoreLongSleepSeconds:
		return "你离线休息了 " + elapsedText + "，醒来后仍记得上次关机前的聊天内容。"
	case elapsedSeconds < maisaka.ContextRestoreDaySeconds:
		return "你沉睡了 " + elapsedText + "，重新上线后仍记得上次关机前的聊天内容。"
	default:
		return "你已经离线了 " + elapsedText + "，像沉睡了一段时间；恢复后仍记得上次关机前的聊天内容。"
	}
}

// buildContextRestoreReferenceMessage 对齐 Python _build_context_restore_reference_message
func buildContextRestoreReferenceMessage(lastContextTime time.Time, now time.Time) *schema.AgenticMessage {
	if lastContextTime.IsZero() {
		return nil
	}
	elapsed := now.Sub(lastContextTime).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	elapsedText := formatContextRestoreElapsed(elapsed)
	wakeupText := buildContextRestoreWakeupText(elapsed, elapsedText)
	content := "这是启动时恢复的历史上下文提醒，不代表当前用户刚刚发来新消息。\n" +
		"距离上次关机前最后一条可恢复聊天记录已经过去 " + elapsedText + "。\n" +
		wakeupText + "\n" +
		"前面恢复出来的历史消息是你上次关机前记得的聊天内容；" +
		"回复时请结合实际间隔，短间隔自然续聊，长间隔可以表现出刚醒来或重新上线后的状态。"
	message := schema.UserAgenticMessage(content)
	message.Extra = map[string]any{
		"source":         maisaka.SourceReference,
		"kind":           "reference",
		"reference_type": maisaka.ReferenceContextRestoreType,
		"display_prefix": "[上下文恢复]",
	}
	return message
}
