package runtime

import (
	"MaiGo/internal/person_info"

	"go.uber.org/zap"
)

// wirePersonFactRouter 将 llm.Router 注入 personinfo Registry
func (a *Agent) wirePersonFactRouter() {
	reg := personinfo.DefaultRegistry()
	if a == nil {
		reg.SetFactRouter(nil)
		return
	}
	router := a.llmRouterSnapshot()
	reg.SetFactRouter(router)
	if router == nil {
		return
	}
	zap.L().Debug("人物事实 LLM 写回 Router 已注入")
}

func (a *Agent) personFactWritebackEnabled() bool {
	if a == nil {
		return false
	}
	bot := a.instanceBotConfig()
	if bot == nil {
		return true
	}
	return bot.AMemorix.Integration.PersonFactWritebackEnabled
}
