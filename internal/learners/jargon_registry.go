package learners

// NewMinerForRegistry 从 Registry 构造会话 JargonMiner，供 agent/Maisaka 统一入口
func NewJargonMinerForRegistry(reg *Registry, sessionID, sessionName string) *JargonMiner {
	if reg == nil {
		reg = DefaultRegistry()
	}
	miner := NewMiner(sessionID, sessionName, NewJargonService(reg))
	if router := reg.JargonRouter(); router != nil {
		miner.SetMeaningInferencer(&MeaningInferencer{Router: router, BotName: ""})
	}
	return miner
}
