package runtime

import (
	"MaiGo/internal/learners"

	"go.uber.org/zap"
)

// wireLearnersJargonRouter 将 llm.Router 注入 learners Registry，供黑话含义推断使用
func (a *Agent) wireLearnersJargonRouter() {
	reg := learners.DefaultRegistry()
	if a == nil {
		reg.SetJargonRouter(nil)
		return
	}
	router := a.llmRouterSnapshot()
	reg.SetJargonRouter(router)
	if router == nil {
		return
	}
	botName := a.botName()
	a.sessionLearners.RangeJargonMiners(func(miner *learners.JargonMiner) {
		learners.RefreshMinerInferencer(miner, router, botName)
	})
	zap.L().Debug("黑话 LLM 推断 Router 已注入")
}
