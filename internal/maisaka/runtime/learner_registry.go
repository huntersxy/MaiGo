package runtime

import (
	"MaiGo/internal/learners"
)

// learnerRegistry 返回会话学习器注册表，按需初始化
func (a *Agent) learnerRegistry() *learners.SessionLearnerRegistry {
	if a == nil {
		return nil
	}
	a.sessionLearnersMu.Lock()
	defer a.sessionLearnersMu.Unlock()
	if a.sessionLearners == nil {
		a.sessionLearners = learners.NewSessionLearnerRegistry()
	}
	return a.sessionLearners
}

// expressionLearnerFor 返回会话的表达学习器，构造与复用逻辑在 learners 包
func (a *Agent) expressionLearnerFor(sessionID string) *learners.ExpressionLearner {
	registry := a.learnerRegistry()
	if registry == nil {
		return nil
	}
	return registry.ExpressionLearner(sessionID)
}

// jargonLearnerFor 返回会话的黑话学习器，并注入 Python 同款的 JargonMiner
// 对齐 src/learners/jargon_learner.py:__init__ 持有 jargon_miner 的语义
func (a *Agent) jargonLearnerFor(sessionID string) *learners.JargonLearner {
	registry := a.learnerRegistry()
	if registry == nil {
		return nil
	}
	return registry.JargonLearner(sessionID)
}

// jargonMinerFor 返回会话的黑话矿工，供 LLM 学习批次写库与缓存查询
// 对齐 Python JargonMiner(session_id, session_name) 的构造与复用
func (a *Agent) jargonMinerFor(sessionID string) *learners.JargonMiner {
	registry := a.learnerRegistry()
	if registry == nil {
		return nil
	}
	return registry.JargonMiner(sessionID)
}
