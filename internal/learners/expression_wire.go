package learners

import "maibot-go/internal/llm_models"

// RefreshLearnerBatchRunner 在 Agent 注入 Router 后更新已缓存 ExpressionLearner
func RefreshLearnerBatchRunner(l *ExpressionLearner, router *llm.Router, botName string) {
	if l == nil {
		return
	}
	if router == nil {
		l.SetBatchRunner(nil)
		return
	}
	l.SetBatchRunner(&BatchRunner{Router: router, BotName: botName})
}

// ResolveBatchRunner 优先 ExpressionLearner 上配置的批次器，否则从 Registry 读取 Router
func (l *ExpressionLearner) ResolveBatchRunner(regRouter *llm.Router) *BatchRunner {
	if l == nil {
		return nil
	}
	if l.batchRunner != nil && l.batchRunner.Router != nil {
		return l.batchRunner
	}
	if regRouter == nil {
		return nil
	}
	runner := &BatchRunner{Router: regRouter}
	if l.batchRunner != nil {
		runner.BotName = l.batchRunner.BotName
	}
	return runner
}
