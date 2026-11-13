package learners

import "sync"

// learningBatchAcquireResult 对齐 ExpressionLearningBatchGate.acquire 的返回
type learningBatchAcquireResult struct {
	Acquired bool
	Reason   string
	Active   int
	Max      int
}

// expressionLearningBatchGate 全局表达学习并发闸门，session 互斥 + 全局上限
type expressionLearningBatchGate struct {
	mu     sync.Mutex
	active map[string]struct{}
}

var defaultExpressionBatchGate = &expressionLearningBatchGate{active: map[string]struct{}{}}

func (g *expressionLearningBatchGate) acquire(sessionID string, maxCount int) learningBatchAcquireResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		g.active = map[string]struct{}{}
	}
	if maxCount <= 0 {
		return learningBatchAcquireResult{Reason: "max_expression_learner <= 0", Active: len(g.active), Max: maxCount}
	}
	if _, ok := g.active[sessionID]; ok {
		return learningBatchAcquireResult{Reason: "session_busy", Active: len(g.active), Max: maxCount}
	}
	if len(g.active) >= maxCount {
		return learningBatchAcquireResult{Reason: "global_limit", Active: len(g.active), Max: maxCount}
	}
	g.active[sessionID] = struct{}{}
	return learningBatchAcquireResult{Acquired: true, Active: len(g.active), Max: maxCount}
}

func (g *expressionLearningBatchGate) release(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.active, sessionID)
}
