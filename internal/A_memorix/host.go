package memorix

import "context"

// Host 是 MaiBot 侧长期记忆宿主能力的最小门面
//
// Python 映射（经 memory_service → a_memorix_host_service.invoke）：
//
//   - IngestMessage → component ingest_text（及启动期 ingest_summary 队列；原生路径）
//   - Search        → component search_memory
//   - Read          → 无单一 component；WebUI/工具常经 episode_admin.get、图谱 node_detail 等
//   - AdminList     → 多 component（如 memory_source_admin action=list）
//   - Health        → memory_runtime_admin（status/self_check）+ _startup_status_payload
//
// Go 后端仅 native（AMemorixHostService）；Remote/Bridge/PG 均已移除。
type Host interface {
	IngestMessage(ctx context.Context, req IngestMessageRequest) (IngestResult, error)
	Search(ctx context.Context, req SearchRequest) (SearchResult, error)
	Read(ctx context.Context, req ReadRequest) (ReadResult, error)
	AdminList(ctx context.Context, req AdminListRequest) (AdminListResult, error)
	Health(ctx context.Context) (HealthResult, error)
}

// DisabledHost 对齐 host_service._disabled_response：未启用长期记忆时的安全降级
type DisabledHost struct{}

func (DisabledHost) IngestMessage(context.Context, IngestMessageRequest) (IngestResult, error) {
	return IngestResult{
		Success: true, Disabled: true, Reason: reasonDisabled,
		StoredIDs: []string{}, SkippedIDs: []string{reasonDisabled},
		Detail: reasonDisabled,
	}, nil
}

func (DisabledHost) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{
		Success: true, Disabled: true, Reason: reasonDisabled,
		Summary: "", Hits: nil, Filtered: false,
	}, nil
}

func (DisabledHost) Read(context.Context, ReadRequest) (ReadResult, error) {
	// 对齐 Python _disabled_response 默认分支：episode/source admin 返回 success=false + error 文案
	return ReadResult{
		Success: false, Disabled: true, Reason: reasonDisabled,
		Error:    disabledMessage,
		Metadata: disabledDefaultPayload(),
	}, nil
}

func (DisabledHost) AdminList(context.Context, AdminListRequest) (AdminListResult, error) {
	return AdminListResult{
		Success: false, Disabled: true, Reason: reasonDisabled,
		Error: disabledMessage,
		Items: nil,
	}, nil
}

func (DisabledHost) Health(context.Context) (HealthResult, error) {
	return disabledHealth(), nil
}

// Invoke 对齐 Python host_service 未启用分支：按组件返回可消费的空结果，
// 而不是让 WebUI 落到「服务未接线」的 503
func (DisabledHost) Invoke(_ context.Context, component string, _ map[string]any, _ *int) (any, error) {
	return disabledComponentResponse(component), nil
}

const (
	reasonDisabled  = "a_memorix_disabled"
	disabledMessage = "A_Memorix 未启用，请在长期记忆配置中开启后再使用。"
)

func disabledHealth() HealthResult {
	msg := disabledMessage
	return HealthResult{
		Success: true, Enabled: false, MemoryEnabled: false, Disabled: true,
		Reason: reasonDisabled, RuntimeReady: false, RetrievalReady: false,
		Degraded: false, RetrievalMode: "disabled",
		// Python _disabled_response 的 memory_runtime_admin 快照里两个通道列表都是空
		AvailableChannels: []string{}, UnavailableChannels: []string{},
		VectorHealth: map[string]any{"state": "disabled"},
		Backend:      "none", Message: msg,
	}
}

// disabledDefaultPayload 对齐 Python _disabled_response 默认分支的字段集
func disabledDefaultPayload() map[string]any {
	return map[string]any{
		"success":  false,
		"enabled":  false,
		"disabled": true,
		"reason":   reasonDisabled,
		"error":    disabledMessage,
	}
}
