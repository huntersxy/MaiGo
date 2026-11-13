package personinfo

import "maibot-go/internal/llm_models"

// SetFactRouter 注入人物事实 LLM 提取 Router（MAIBOT_PERSON_LLM=1）
func (r *Registry) SetFactRouter(router *llm.Router) {
	r.mu.Lock()
	r.factRouter = router
	r.mu.Unlock()
}

// FactExtractorFromRegistry 返回当前 Registry 绑定的提取器
func (r *Registry) FactExtractorFromRegistry() *FactExtractor {
	r.mu.RLock()
	router := r.factRouter
	r.mu.RUnlock()
	if router == nil {
		return nil
	}
	return &FactExtractor{Router: router}
}
