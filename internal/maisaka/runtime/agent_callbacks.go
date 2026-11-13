package runtime

import (
	"context"

	"maibot-go/internal/platform_io/drivers/onebot"
)

// AgentCallbacks 宿主注入的回调端口集合：内部聊天出站、思考通知、文件登记、
// 发送后记忆自动化、外部平台文件发送与机器人账号对。
// 由 bootstrap 在启动期经 Set* 方法注入，运行期只读。
type AgentCallbacks struct {
	WebChat            WebChatSender
	ThinkingNotifier   func(conversationKind string, targetID int64)
	FileIngestor       FileIngestor
	SentMessageHandler func(context.Context, *onebot.ConversationMessage) error
	ExternalFileSender ExternalFileSender
	BotAccountPairs    func(ctx context.Context) ([][2]string, error)
}

// setCallbacks 写入回调集合并返回原值（仅启动期接线使用）
func (a *Agent) setCallbacks(mutate func(*AgentCallbacks)) {
	if a == nil || mutate == nil {
		return
	}
	a.callbacksMu.Lock()
	mutate(&a.callbacks)
	a.callbacksMu.Unlock()
}

// webChatSender 返回当前内部聊天出站通道
func (a *Agent) webChatSender() WebChatSender {
	if a == nil {
		return nil
	}
	a.callbacksMu.RLock()
	defer a.callbacksMu.RUnlock()
	return a.callbacks.WebChat
}

// fileIngestorPort 返回当前入站文件登记器
func (a *Agent) fileIngestorPort() FileIngestor {
	if a == nil {
		return nil
	}
	a.callbacksMu.RLock()
	defer a.callbacksMu.RUnlock()
	return a.callbacks.FileIngestor
}

// externalFileSenderPort 返回当前外部平台文件发送实现
func (a *Agent) externalFileSenderPort() ExternalFileSender {
	if a == nil {
		return nil
	}
	a.callbacksMu.RLock()
	defer a.callbacksMu.RUnlock()
	return a.callbacks.ExternalFileSender
}
