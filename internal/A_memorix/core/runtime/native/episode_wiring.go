package native

// Episode 物化服务装配。
//
// 后台循环按来源领取重建任务，再用 EpisodeService 生成物化计划；
// 分段依赖 LLM 网关与模型任务表，存储侧端口直接复用 metadataStore。

import (
	"context"
	"fmt"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/utils"
)

// ensureEpisodeService 惰性构造 Episode 服务；存储未就绪时返回 nil
func (r *Runtime) ensureEpisodeService() *utils.EpisodeService {
	r.episodeOnce.Do(func() {
		store := r.metadataStore
		if store == nil || r.kernel == nil {
			return
		}
		pluginConfig := r.kernel.Config()
		segmentation := utils.NewEpisodeSegmentationService(pluginConfig, r.llmGateway, r.logger)
		// 对齐 Python：feedback_correction_paragraph_hard_filter_enabled 默认 true
		hardFilter := memruntime.PyTruthy(
			r.kernel.Cfg("integration.feedback_correction_paragraph_hard_filter_enabled", true),
		)
		r.episodeSvc = utils.NewEpisodeService(utils.EpisodeStoreDeps{
			GetParagraphEntitiesByHashes: store.GetParagraphEntitiesByHashes,
			GetLiveParagraphsBySource: func(source string, excludeStale bool) ([]map[string]any, error) {
				return store.GetLiveParagraphsBySource(source, excludeStale, nil)
			},
			GetEpisodesBySource: func(source string) ([]map[string]any, error) {
				return store.GetEpisodesBySource(source, nil)
			},
			GetEpisodeEmptyGroupFingerprints: func(source string) ([]string, error) {
				return store.GetEpisodeEmptyGroupFingerprints(source, nil)
			},
			ReplaceEpisodesForSource: func(source string, payloads []map[string]any, emptyGroupFingerprints []string) (map[string]any, error) {
				return store.ReplaceEpisodesForSource(source, payloads, emptyGroupFingerprints, nil)
			},
		}, pluginConfig, segmentation, hardFilter, r.logger)
	})
	return r.episodeSvc
}

// episodeGenerationSignature 当前物化代际签名
func (r *Runtime) episodeGenerationSignature() any {
	service := r.ensureEpisodeService()
	if service == nil {
		return nil
	}
	return service.GenerationSignature()
}

// episodeGenerationHash 由代际签名算出稳定哈希
func (r *Runtime) episodeGenerationHash(generation any) string {
	service := r.ensureEpisodeService()
	if service == nil {
		return ""
	}
	signature, _ := generation.(map[string]any)
	hash, err := service.GenerationHash(signature)
	if err != nil {
		r.logger.Warning(fmt.Sprintf("[native] 计算 Episode 代际哈希失败: %v", err))
		return ""
	}
	return hash
}

// planEpisodeSourceRebuild 生成单个来源的物化计划
func (r *Runtime) planEpisodeSourceRebuild(ctx context.Context, source string, generation any) (map[string]any, error) {
	service := r.ensureEpisodeService()
	if service == nil {
		return nil, fmt.Errorf("Episode 服务未就绪")
	}
	signature, _ := generation.(map[string]any)
	return service.PlanSourceRebuild(ctx, source, signature)
}
