package focus

// SessionInfo 判定 Focus 白名单/scope 所需的会话投影
type SessionInfo struct {
	SessionID string
	IsGroup   bool
	Platform  string
	GroupID   string
	UserID    string

	// PrivateReplyPaused 单会话私聊暂停；nil 表示仅看全局 PrivateReplyPausedAll
	PrivateReplyPaused *bool
}
