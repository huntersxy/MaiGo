package hostadapter

// A_memorix 宿主内核适配器
// Python 对照：host_service.py 直接持有 SDKMemoryKernel 单例；
// Go 侧 memorix 包与 core/runtime 互相引用会形成 import 环，因此适配器单独成包

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"maibot-go/internal/A_memorix"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/utils"
)

// RuntimeKernel 把 core/runtime.Kernel 适配为 memorix.HostKernel
type RuntimeKernel struct {
	kernel *memruntime.Kernel
}

// WrapRuntimeKernel 包装一个已构造的内核
func WrapRuntimeKernel(kernel *memruntime.Kernel) *RuntimeKernel {
	return &RuntimeKernel{kernel: kernel}
}

// NewRuntimeKernel 构造内核并包装；services 为 nil 时保持内核未注入状态
func NewRuntimeKernel(pluginRoot string, config map[string]any, logger utils.Logger, services *memruntime.KernelServices) *RuntimeKernel {
	kernel := memruntime.NewKernel(pluginRoot, config, logger)
	if services != nil {
		kernel.SetServices(*services)
	}
	return WrapRuntimeKernel(kernel)
}

// ServicesFactory 按内核实例装配运行时服务
type ServicesFactory func(kernel *memruntime.Kernel) (memruntime.KernelServices, error)

// NewKernelFactory 构造 memorix.KernelFactory，供 HostServiceOptions 注入
func NewKernelFactory(logger utils.Logger, servicesFactory ServicesFactory) memorix.KernelFactory {
	return func(ctx context.Context, pluginRoot string, config map[string]any) (memorix.HostKernel, error) {
		kernel := memruntime.NewKernel(pluginRoot, config, logger)
		if servicesFactory != nil {
			services, err := servicesFactory(kernel)
			if err != nil {
				return nil, err
			}
			kernel.SetServices(services)
		}
		return WrapRuntimeKernel(kernel), nil
	}
}

// Kernel 返回底层运行时内核
func (r *RuntimeKernel) Kernel() *memruntime.Kernel {
	if r == nil {
		return nil
	}
	return r.kernel
}

// Initialize 对齐内核 initialize
func (r *RuntimeKernel) Initialize(ctx context.Context) error {
	if r == nil || r.kernel == nil {
		return errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.Initialize(ctx)
}

// Shutdown 对齐内核 shutdown
func (r *RuntimeKernel) Shutdown(ctx context.Context) error {
	if r == nil || r.kernel == nil {
		return errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.Shutdown(ctx)
}

// Close 对齐内核 close
func (r *RuntimeKernel) Close() {
	if r == nil || r.kernel == nil {
		return
	}
	r.kernel.Close()
}

// SearchMemory 对齐内核 search_memory
func (r *RuntimeKernel) SearchMemory(ctx context.Context, req memorix.HostKernelSearchRequest) (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.SearchMemory(ctx, memruntime.KernelSearchRequest{
		Query:         req.Query,
		Limit:         req.Limit,
		Mode:          req.Mode,
		ChatID:        req.ChatID,
		SharedChatIDs: append([]string(nil), req.SharedChatIDs...),
		PersonID:      req.PersonID,
		TimeStart:     req.TimeStart,
		TimeEnd:       req.TimeEnd,
		RespectFilter: hostadapterBoolPtr(req.RespectFilter),
		UserID:        req.UserID,
		GroupID:       req.GroupID,
	})
}

// IngestSummary 对齐内核 ingest_summary
func (r *RuntimeKernel) IngestSummary(ctx context.Context, params memorix.HostIngestSummaryParams) (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.IngestSummary(ctx, memruntime.IngestSummaryParams{
		ExternalID:    params.ExternalID,
		ChatID:        params.ChatID,
		Text:          params.Text,
		Participants:  append([]string(nil), params.Participants...),
		TimeStart:     numericPtr(params.TimeStart),
		TimeEnd:       numericPtr(params.TimeEnd),
		Tags:          append([]string(nil), params.Tags...),
		Metadata:      cloneMap(params.Metadata),
		RespectFilter: hostadapterBoolPtr(params.RespectFilter),
		UserID:        params.UserID,
		GroupID:       params.GroupID,
	})
}

// IngestText 对齐内核 ingest_text
func (r *RuntimeKernel) IngestText(ctx context.Context, params memorix.HostIngestTextParams) (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.IngestText(ctx, memruntime.IngestTextParams{
		ExternalID:    params.ExternalID,
		SourceType:    params.SourceType,
		Text:          params.Text,
		ChatID:        params.ChatID,
		PersonIDs:     append([]string(nil), params.PersonIDs...),
		Participants:  append([]string(nil), params.Participants...),
		Timestamp:     numericPtr(params.Timestamp),
		TimeStart:     numericPtr(params.TimeStart),
		TimeEnd:       numericPtr(params.TimeEnd),
		Tags:          append([]string(nil), params.Tags...),
		Metadata:      cloneMap(params.Metadata),
		Entities:      append([]string(nil), params.Entities...),
		Relations:     cloneRelations(params.Relations),
		RespectFilter: hostadapterBoolPtr(params.RespectFilter),
		UserID:        params.UserID,
		GroupID:       params.GroupID,
	})
}

// GetPersonProfile 对齐内核 get_person_profile
func (r *RuntimeKernel) GetPersonProfile(ctx context.Context, personID string, chatID string, limit int) (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.GetPersonProfile(ctx, personID, chatID, limit)
}

// MaintainMemory 对齐内核 maintain_memory
func (r *RuntimeKernel) MaintainMemory(ctx context.Context, action string, target string, hours any, reason string, limit int) (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.MaintainMemory(ctx, action, target, numericPtr(hours), reason, limit)
}

// MemoryStats 对齐内核 memory_stats
func (r *RuntimeKernel) MemoryStats() (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.MemoryStats()
}

// ImageMemory 对齐内核图片记忆入口
func (r *RuntimeKernel) ImageMemory(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.ImageMemory(ctx, action, payload)
}

// EnqueueFeedbackTask 对齐内核反馈任务入队
func (r *RuntimeKernel) EnqueueFeedbackTask(ctx context.Context, payload map[string]any) (any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	return r.kernel.EnqueueFeedbackTask(ctx, payload)
}

// RuntimeCapabilityStatus 对齐内核运行时能力快照
func (r *RuntimeKernel) RuntimeCapabilityStatus() map[string]any {
	if r == nil || r.kernel == nil {
		return map[string]any{}
	}
	return r.kernel.RuntimeCapabilityStatus()
}

// HostStatusReport 暴露原生装配的降级原因与缺失服务
func (r *RuntimeKernel) HostStatusReport() map[string]any {
	if r == nil || r.kernel == nil {
		return map[string]any{}
	}
	return r.kernel.HostStatusReport()
}

// IsAdminComponent 对齐 admin_contracts.IsAdminComponent
func (r *RuntimeKernel) IsAdminComponent(component string) bool {
	return memruntime.IsAdminComponent(component)
}

// ParseAdminCommand 解析 admin 组件与动作，返回归一化动作名
func (r *RuntimeKernel) ParseAdminCommand(component string, payload any) (string, error) {
	command, err := memruntime.ParseAdminCommand(component, payload)
	if err != nil {
		return "", err
	}
	return command.Action, nil
}

// DispatchAdmin 解析并分发 admin 命令
func (r *RuntimeKernel) DispatchAdmin(ctx context.Context, component string, payload map[string]any) (any, error) {
	if r == nil || r.kernel == nil {
		return nil, errors.New("A_Memorix 运行时内核未初始化")
	}
	command, err := memruntime.ParseAdminCommand(component, payload)
	if err != nil {
		return nil, err
	}
	return memruntime.DispatchAdminCommand(ctx, r.kernel, command)
}

// numericPtr 把宿主输入的数字转成内核使用的 *float64
// 空白字符串与 nil 视为未提供，布尔按 Python float(bool) 语义处理
func numericPtr(value any) *float64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case *float64:
		if typed == nil {
			return nil
		}
		out := *typed
		return &out
	case float64:
		return &typed
	case float32:
		out := float64(typed)
		return &out
	case int:
		out := float64(typed)
		return &out
	case int64:
		out := float64(typed)
		return &out
	case bool:
		out := 0.0
		if typed {
			out = 1.0
		}
		return &out
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		if parsed, ok := parseFloat(text); ok {
			return &parsed
		}
		return nil
	default:
		return nil
	}
}

func parseFloat(text string) (float64, bool) {
	out, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return out, true
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneRelations(source []map[string]any) []map[string]any {
	if source == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(source))
	for _, item := range source {
		out = append(out, cloneMap(item))
	}
	return out
}

// hostadapterBoolPtr 返回 bool 指针，供内核参数表达「显式设置」
func hostadapterBoolPtr(value bool) *bool {
	return &value
}
