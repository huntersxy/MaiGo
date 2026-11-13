package focus

// IsEnabled 全局 Focus 实验开关
func IsEnabled(settings Settings) bool {
	return settings.FocusMode
}

// IsEnabledForChat 群聊默认启用；私聊需 focus_on_private
func IsEnabledForChat(settings Settings, isGroupChat bool) bool {
	if !IsEnabled(settings) {
		return false
	}
	if isGroupChat {
		return true
	}
	return settings.FocusOnPrivate
}

// IsPrivateReplyPaused 对齐 ChatConfigUtils.is_private_reply_paused 的简化版
func IsPrivateReplyPaused(settings Settings, session SessionInfo) bool {
	if session.PrivateReplyPaused != nil {
		return *session.PrivateReplyPaused
	}
	return settings.PrivateReplyPausedAll
}

// IsEnabledForSession 会话是否应进入 Focus 共享 Planner
func IsEnabledForSession(settings Settings, session SessionInfo) bool {
	if session.SessionID == "" {
		return false
	}
	if !IsEnabledForChat(settings, session.IsGroup) {
		return false
	}
	if !session.IsGroup && IsPrivateReplyPaused(settings, session) {
		return false
	}
	targets := settings.FocusChatWhitelist
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		if TargetMatchesSessionWithWildcards(target, session) {
			return true
		}
	}
	return false
}
