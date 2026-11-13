package learners

import "sync"

// SessionLearnerRegistry 按会话缓存表达/黑话学习器与黑话矿工
//
// 对齐 Python Maisaka runtime 持有 `_expression_learner` / `_jargon_learner`
// 的按会话复用语义；本类型只做缓存与构造，不持有 Agent 状态。
type SessionLearnerRegistry struct {
	expression sync.Map // session_id -> *ExpressionLearner
	jargon     sync.Map // session_id -> *JargonLearner
	miners     sync.Map // session_id -> *JargonMiner
}

// NewSessionLearnerRegistry 创建空注册表
func NewSessionLearnerRegistry() *SessionLearnerRegistry {
	return &SessionLearnerRegistry{}
}

// ExpressionLearner 返回会话的表达学习器，按需构造并复用
func (r *SessionLearnerRegistry) ExpressionLearner(sessionID string) *ExpressionLearner {
	if r == nil || sessionID == "" {
		return nil
	}
	if value, ok := r.expression.Load(sessionID); ok {
		if learner, ok := value.(*ExpressionLearner); ok {
			return learner
		}
	}
	learner := NewExpressionLearner(sessionID)
	actual, _ := r.expression.LoadOrStore(sessionID, learner)
	if stored, ok := actual.(*ExpressionLearner); ok {
		return stored
	}
	return learner
}

// JargonMiner 返回会话的黑话矿工，按需构造并复用
func (r *SessionLearnerRegistry) JargonMiner(sessionID string) *JargonMiner {
	if r == nil || sessionID == "" {
		return nil
	}
	if value, ok := r.miners.Load(sessionID); ok {
		if miner, ok := value.(*JargonMiner); ok {
			return miner
		}
	}
	miner := NewJargonMinerForRegistry(DefaultRegistry(), sessionID, sessionID)
	actual, _ := r.miners.LoadOrStore(sessionID, miner)
	if stored, ok := actual.(*JargonMiner); ok {
		return stored
	}
	return miner
}

// JargonLearner 返回会话的黑话学习器，并注入该会话的矿工
func (r *SessionLearnerRegistry) JargonLearner(sessionID string) *JargonLearner {
	if r == nil || sessionID == "" {
		return nil
	}
	if value, ok := r.jargon.Load(sessionID); ok {
		if learner, ok := value.(*JargonLearner); ok {
			return learner
		}
	}
	learner := NewJargonLearnerForRegistry(DefaultRegistry(), sessionID)
	if miner := r.JargonMiner(sessionID); miner != nil {
		learner.SetMiner(miner)
	}
	actual, _ := r.jargon.LoadOrStore(sessionID, learner)
	if stored, ok := actual.(*JargonLearner); ok {
		return stored
	}
	return learner
}

// RangeExpressionLearners 遍历已缓存的表达学习器，用于 Router 热更新
func (r *SessionLearnerRegistry) RangeExpressionLearners(visit func(*ExpressionLearner)) {
	if r == nil || visit == nil {
		return
	}
	r.expression.Range(func(_, value any) bool {
		if learner, ok := value.(*ExpressionLearner); ok {
			visit(learner)
		}
		return true
	})
}

// RangeJargonMiners 遍历已缓存的黑话矿工，用于 Router 热更新
func (r *SessionLearnerRegistry) RangeJargonMiners(visit func(*JargonMiner)) {
	if r == nil || visit == nil {
		return
	}
	r.miners.Range(func(_, value any) bool {
		if miner, ok := value.(*JargonMiner); ok {
			visit(miner)
		}
		return true
	})
}

// Forget 丢弃会话的全部学习器缓存；会话销毁或重建时调用
func (r *SessionLearnerRegistry) Forget(sessionID string) {
	if r == nil || sessionID == "" {
		return
	}
	r.expression.Delete(sessionID)
	r.jargon.Delete(sessionID)
	r.miners.Delete(sessionID)
}

// PrimeExpressionLearner 预置表达学习器，供测试或外部注入替换默认构造
func (r *SessionLearnerRegistry) PrimeExpressionLearner(sessionID string, learner *ExpressionLearner) {
	if r == nil || sessionID == "" || learner == nil {
		return
	}
	r.expression.Store(sessionID, learner)
}

// PrimeJargonLearner 预置黑话学习器
func (r *SessionLearnerRegistry) PrimeJargonLearner(sessionID string, learner *JargonLearner) {
	if r == nil || sessionID == "" || learner == nil {
		return
	}
	r.jargon.Store(sessionID, learner)
}

// PrimeJargonMiner 预置黑话矿工
func (r *SessionLearnerRegistry) PrimeJargonMiner(sessionID string, miner *JargonMiner) {
	if r == nil || sessionID == "" || miner == nil {
		return
	}
	r.miners.Store(sessionID, miner)
}
