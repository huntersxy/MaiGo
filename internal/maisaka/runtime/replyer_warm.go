package runtime

import (
	"maibot-go/internal/chat/replyer"

	"go.uber.org/zap"
)

// warmReplyerTypoData 按实例配置异步预热错别字数据；不阻塞启动与热重载
func (a *Agent) warmReplyerTypoData() {
	bot := a.instanceBotConfig()
	if bot == nil || !bot.ReplyPostProcessSettings().TypoEnable {
		return
	}
	go func() {
		if err := replyer.WarmTypoData(); err != nil {
			zap.L().Warn("预热错别字数据失败", zap.Error(err))
			return
		}
		zap.L().Debug("错别字数据已预热")
	}()
}
