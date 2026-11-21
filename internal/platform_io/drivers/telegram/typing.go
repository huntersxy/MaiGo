package telegram

import (
	"context"
	"time"

	"go.uber.org/zap"
)

type typingTarget struct {
	chatID   int64
	threadID int64
	hasTopic bool
}

type typingTask struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newTypingTarget(chatID int64, threadID *int64) typingTarget {
	target := typingTarget{chatID: chatID}
	if threadID != nil {
		target.threadID, target.hasTopic = *threadID, true
	}
	return target
}

// StartTyping 开始刷新已验证会话的输入状态。
func (a *Adapter) StartTyping(account, scope, targetID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runCtx == nil || a.client == nil || !a.runtime.Ready() || scope != "" || account != a.runtime.AccountID() {
		return nil
	}
	chatID, threadID, err := ParseGroupID(targetID)
	if err != nil {
		return err
	}
	target := newTypingTarget(chatID, threadID)
	if a.typing[target] != nil {
		return nil
	}
	if a.typing == nil {
		a.typing = make(map[typingTarget]*typingTask)
	}
	ctx, cancel := context.WithTimeout(a.runCtx, 3*time.Minute)
	task := &typingTask{cancel: cancel, done: make(chan struct{})}
	a.typing[target] = task
	go a.refreshTyping(ctx, a.client, task, target)
	return nil
}

func (a *Adapter) refreshTyping(ctx context.Context, client *Client, task *typingTask, target typingTarget) {
	defer close(task.done)
	defer task.cancel()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.typing[target] == task {
			delete(a.typing, target)
		}
	}()
	body := map[string]any{"chat_id": target.chatID, "action": "typing"}
	if target.hasTopic {
		body["message_thread_id"] = target.threadID
	}
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := client.Call(requestCtx, "sendChatAction", body, nil)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				zap.L().Warn("Telegram 输入状态发送失败", zap.Int64("chat_id", target.chatID), zap.Error(err))
			}
			return
		}
		timer := time.NewTimer(4 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *Adapter) stopTyping(target typingTarget) {
	a.mu.Lock()
	task := a.typing[target]
	delete(a.typing, target)
	a.mu.Unlock()
	if task != nil {
		task.cancel()
		<-task.done
	}
}
