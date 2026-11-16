package native

// 反馈纠错服务依赖装配
// 消息源与 LLM 由宿主注入，未注入时返回真实错误而不是伪造成功

import (
	"context"
	"fmt"
	"strings"
	"time"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/services"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// feedbackServiceDeps 绑定反馈纠错所需的全部原生服务
func (r *Runtime) feedbackServiceDeps(target *memruntime.KernelServices) services.FeedbackDeps {
	var ingest *services.IngestService
	if adapter, ok := target.Ingest.(*ingestAdapter); ok {
		ingest = adapter.inner
	}
	return services.FeedbackDeps{
		Metadata:           func() *storage.Store { return r.metadataStore },
		FeedbackConfig:     func() *utils.FeedbackIntegrationConfig { return feedbackIntegrationConfig(r.kernel) },
		BackgroundStopping: r.stopping.Load,
		Persist:            r.persistKernel,
		DeleteAdmin:        concreteService[*services.DeleteAdminService](target.DeleteAdmin),
		Ingest:             ingest,
		V5Admin:            concreteService[*services.V5AdminService](target.V5Admin),
		ProfileAdmin:       concreteService[*services.ProfileAdminService](target.ProfileAdmin),
		GraphAdmin:         concreteService[*services.GraphAdminService](target.GraphAdmin),
		SearchHit:          concreteService[*services.SearchHitService](target.SearchHit),
		ResolvePersonID: func(ctx context.Context, identifier string) (string, error) {
			if r.personProfile == nil {
				return "", fmt.Errorf("人物画像服务未初始化，无法解析 person_id")
			}
			return r.personProfile.ResolvePersonID(ctx, identifier)
		},
		Messages: func(ctx context.Context, chatID string, startTime float64, endTime float64, limit int, limitMode string) ([]utils.SessionMessage, error) {
			if r.messageSource == nil {
				return nil, fmt.Errorf("反馈消息源未接入，无法读取聊天消息")
			}
			return r.messageSource.GetMessagesByTimeInChat(ctx, chatID, startTime, endTime, limit, limitMode)
		},
		LLMGenerate: func(ctx context.Context, prompt string) (string, error) {
			if r.llmGateway == nil {
				return "", fmt.Errorf("反馈分类 LLM 未接入")
			}
			result, err := r.llmGateway.Generate(ctx, utils.TextGenerationRequest{
				TaskName: "fast_model", RequestType: "memory_feedback_correction", Prompt: prompt,
			})
			if err != nil {
				return "", err
			}
			if !result.Success {
				if message := strings.TrimSpace(result.Error); message != "" {
					return "", fmt.Errorf("%s", message)
				}
				return "", fmt.Errorf("反馈分类 LLM 未返回结果")
			}
			return result.Completion, nil
		},
		ProcessEpisodeSourceRebuildBatch: r.kernel.ProcessEpisodeSourceRebuildBatch,
		Now:                              time.Now,
	}
}

func concreteService[T any](value any) T {
	typed, _ := value.(T)
	return typed
}
