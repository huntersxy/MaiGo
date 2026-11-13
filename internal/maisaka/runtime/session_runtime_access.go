package runtime

import (
	"fmt"
	"strings"

	"maibot-go/internal/common/media"

	"maibot-go/internal/maisaka"
)

// SessionRuntime 按 MaiBot session_id 查找已创建的 Maisaka 运行时
// 只读：不会创建会话或运行时；未创建时返回真实错误，由调用方决定降级
func (a *Agent) SessionRuntime(sessionID string) (*maisaka.Runtime, error) {
	sessionID = strings.TrimSpace(sessionID)
	if a == nil || a.sessions == nil {
		return nil, fmt.Errorf("Agent 未初始化")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session_id 不能为空")
	}
	for _, session := range a.sessions.all() {
		if session == nil {
			continue
		}
		runtime := session.MaisakaRuntime()
		if runtime == nil || runtime.SessionID() != sessionID {
			continue
		}
		return runtime, nil
	}
	return nil, fmt.Errorf("会话运行时尚未创建: %s", sessionID)
}

// SessionSourceImages 返回指定会话可传给其他聊天的图片来源索引
//
// 对齐 Python agent/messaging.py source_images 经由 runtime._chat_history 取图：
// 会话未创建时返回空表，不视为错误。
func (a *Agent) SessionSourceImages(sessionID string) map[string][]media.Ref {
	sessionID = strings.TrimSpace(sessionID)
	if a == nil || a.sessions == nil || sessionID == "" {
		return map[string][]media.Ref{}
	}
	for _, session := range a.sessions.all() {
		if session == nil {
			continue
		}
		runtime := session.MaisakaRuntime()
		if runtime == nil || runtime.SessionID() != sessionID {
			continue
		}
		return session.SourceImages()
	}
	return map[string][]media.Ref{}
}
