package learners

import "maibot-go/internal/llm_models"

// RefreshMinerInferencer 在 Agent 注入 Router 后更新已缓存 JargonMiner
func RefreshMinerInferencer(m *JargonMiner, router *llm.Router, botName string) {
	if m == nil {
		return
	}
	if router == nil {
		m.SetMeaningInferencer(nil)
		return
	}
	m.SetMeaningInferencer(&MeaningInferencer{Router: router, BotName: botName})
}

// ResolveInferencer 优先 JargonMiner 上配置的推断器，否则从 Registry 读取 Router
func (m *JargonMiner) ResolveInferencer(regRouter *llm.Router) *MeaningInferencer {
	if m == nil {
		return nil
	}
	if m.inferencer != nil && m.inferencer.Router != nil {
		return m.inferencer
	}
	if regRouter == nil {
		return nil
	}
	inf := &MeaningInferencer{Router: regRouter}
	if m.inferencer != nil {
		inf.BotName = m.inferencer.BotName
	}
	return inf
}
