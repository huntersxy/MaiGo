package learners

import "sync"

// jargonBatchAcquireResult 对齐 JargonLearningBatchGate.acquire 的返回
type jargonBatchAcquireResult struct {
	Acquired bool
	Reason   string
	Active   int
	Max      int
}

// jargonLearningBatchGate 全局黑话学习并发闸门，session 互斥 + 全局上限
type jargonLearningBatchGate struct {
	mu     sync.Mutex
	active map[string]struct{}
}

var defaultJargonBatchGate = &jargonLearningBatchGate{active: map[string]struct{}{}}

func (g *jargonLearningBatchGate) acquire(sessionID string, maxCount int) jargonBatchAcquireResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		g.active = map[string]struct{}{}
	}
	if maxCount <= 0 {
		return jargonBatchAcquireResult{Reason: "max_expression_learner <= 0", Active: len(g.active), Max: maxCount}
	}
	if _, ok := g.active[sessionID]; ok {
		return jargonBatchAcquireResult{Reason: "session_busy", Active: len(g.active), Max: maxCount}
	}
	if len(g.active) >= maxCount {
		return jargonBatchAcquireResult{Reason: "global_limit", Active: len(g.active), Max: maxCount}
	}
	g.active[sessionID] = struct{}{}
	return jargonBatchAcquireResult{Acquired: true, Active: len(g.active), Max: maxCount}
}

func (g *jargonLearningBatchGate) release(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.active, sessionID)
}
