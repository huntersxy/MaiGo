package runtime

import (
	"maibot-go/internal/person_info"

	"go.uber.org/zap"
)

// wirePersonFactRouter 将 llm.Router 注入 personinfo Registry（MAIBOT_PERSON_LLM=1）
func (a *Agent) wirePersonFactRouter() {
	reg := personinfo.DefaultRegistry()
	if a == nil {
		reg.SetFactRouter(nil)
		return
	}
	router := a.llmRouterSnapshot()
	reg.SetFactRouter(router)
	if router == nil || !personinfo.PersonLLMEnabled() {
		return
	}
	zap.L().Debug("人物事实 LLM 写回 Router 已注入",
		zap.Bool("enabled", personinfo.PersonLLMEnabled()))
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
