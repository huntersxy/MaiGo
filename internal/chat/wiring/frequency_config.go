package chatwiring

import (
	"strings"

	"MaiGo/internal/chat/message_receive"
	maibotcfg "MaiGo/internal/config/maibot"
)

// ConfigFrequencyFallback 用聊天流配置计算默认回复频率
// 群聊走 talk_value 与时段规则，私聊暂停为 0、否则为 1，对齐 Python get_talk_value
func ConfigFrequencyFallback(timing *maibotcfg.ChatReplyTimingConfig) func(message *messagereceive.SessionMessage) (float64, bool) {
	if timing == nil {
		return nil
	}
	config := *timing
	return func(message *messagereceive.SessionMessage) (float64, bool) {
		if message == nil {
			return 0, false
		}
		platform := strings.TrimSpace(message.Platform)
		if message.IsGroup() {
			return maibotcfg.GroupTalkFrequency(config, platform, message.TargetID()), true
		}
		return maibotcfg.PrivateTalkFrequency(config, platform, message.TargetID()), true
	}
}
