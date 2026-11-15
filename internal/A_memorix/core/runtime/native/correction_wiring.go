package native

// 修正管理服务装配，对齐 Python SDKMemoryKernel._correction_admin_service

import (
	"context"
	"fmt"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/services"
	"MaiGo/internal/A_memorix/core/utils"
)

// correctionAdminOptions 构造修正管理服务依赖
func (r *Runtime) correctionAdminOptions() services.CorrectionAdminOptions {
	return services.CorrectionAdminOptions{
		Base:           r.base,
		Planner:        correctionPlanner{runtime: r},
		FeedbackConfig: func() *utils.FeedbackIntegrationConfig { return feedbackIntegrationConfig(r.kernel) },
		IngestText: func(ctx context.Context, params services.IngestTextParams) (map[string]any, error) {
			return r.kernel.IngestText(ctx, memruntime.IngestTextParams{
				ExternalID: params.ExternalID, SourceType: params.SourceType, Text: params.Text,
				ChatID: params.ChatID, PersonIDs: params.PersonIDs, Participants: params.Participants,
				Timestamp: params.Timestamp, TimeStart: params.TimeStart, TimeEnd: params.TimeEnd,
				Tags: params.Tags, Metadata: params.Metadata, Entities: params.Entities,
				Relations: params.Relations, RespectFilter: boolPtr(params.RespectFilter),
				UserID: params.UserID, GroupID: params.GroupID,
			})
		},
		RefreshPersonProfile: func(ctx context.Context, personID string) (map[string]any, error) {
			return r.kernel.RefreshPersonProfile(ctx, personID, 10, true)
		},
		DeleteParagraphs: func(ctx context.Context, hashes []string, requestedBy string, reason string) (map[string]any, error) {
			result, err := r.kernel.MemoryDeleteAdmin(ctx, "execute", map[string]any{
				"mode": "paragraph", "selector": map[string]any{"hashes": hashes},
				"requested_by": requestedBy, "reason": reason,
			})
			if err != nil {
				return nil, err
			}
			if mapped, ok := result.(map[string]any); ok {
				return mapped, nil
			}
			return map[string]any{"success": false, "error": "删除服务未返回结果"}, nil
		},
		RebuildGraphFromMetadata: func(ctx context.Context) (map[string]any, error) {
			result, err := r.kernel.RebuildGraphFromMetadata(ctx, nil)
			if err != nil {
				return nil, err
			}
			if mapped, ok := result.(map[string]any); ok {
				return mapped, nil
			}
			return map[string]any{}, nil
		},
		Persist: func(ctx context.Context) error {
			return r.kernel.Persist(ctx, false)
		},
		EnqueuePersonProfileRefresh: func(personID string, reason string) {
			r.kernel.EnqueuePersonProfileRefresh(personID, reason)
		},
		InvalidateEffectiveFilterCache: func() {
			r.kernel.InvalidateEffectiveFilterCache()
		},
		ResolvePersonID: func(ctx context.Context, identifier string) (string, error) {
			if r.personProfile == nil {
				return "", fmt.Errorf("人物画像服务未初始化，无法解析 person_id")
			}
			return r.personProfile.ResolvePersonID(ctx, identifier)
		},
	}
}

// boolPtr 返回 bool 指针
func boolPtr(value bool) *bool {
	return &value
}
