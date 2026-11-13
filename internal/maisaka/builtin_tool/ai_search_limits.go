package tools

// 对外暴露 AI 搜索工具的限制，供 internal/search 的 Agent 调度复用，避免两处魔法数字漂移。
const (
	AISearchDefaultToolLimit   = aiSearchDefaultToolLimit
	AISearchMaxToolQueryLength = aiSearchMaxToolQueryLength
	OfficialDocsSearchMaxLimit = officialDocsSearchMaxLimit
	OfficialDocsMaxReadCount   = officialDocsMaxReadCount
	LocalConfigMaxReadCount    = localConfigMaxReadCount
)
