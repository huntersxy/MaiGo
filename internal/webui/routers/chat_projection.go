package webuiapi

// 父包与 chat 子包之间的依赖投影。
//
// chat 子包的 Deps 只含聊天域需要的字段；父包 Deps 字段更多且带测试注入项。
// 这里做一次投影，让父包（memory 管理、图片写回等）复用 chat 的历史查询能力，
// 而不必把整份 chat 逻辑复制到父包。

import (
	"MaiGo/internal/webui/routers/chat"
)

// chatDeps 把父包 Deps 投影成 chat 子包的依赖
func (d Deps) chatDeps() chat.Deps {
	return chat.Deps{
		Memory:           d.Memory,
		Agent:            d.Agent,
		PlatformAdapters: d.PlatformAdapters,
		InstanceDir:      d.InstanceDir,
		RequireAuth:      d.requireAuth,
		ValidateBody:     validateRequestBodyForRoute,
	}
}
