package runtime

import (
	"maibot-go/internal/learners"

	"go.uber.org/zap"
)

// wireLearnersExpressionRouter 将 llm.Router 注入 learners Registry，供表达 LLM 批次使用
func (a *Agent) wireLearnersExpressionRouter() {
	reg := learners.DefaultRegistry()
	if a == nil {
		reg.SetExpressionRouter(nil)
		return
	}
	router := a.llmRouterSnapshot()
	reg.SetExpressionRouter(router)
	if router == nil {
		return
	}
	botName := a.botName()
	a.sessionLearners.RangeExpressionLearners(func(learner *learners.ExpressionLearner) {
		learners.RefreshLearnerBatchRunner(learner, router, botName)
	})
	zap.L().Debug("表达 LLM 批次 Router 已注入")
}
