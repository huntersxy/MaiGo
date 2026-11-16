package tooling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"go.uber.org/zap"
)

var toolNameSanitizer = regexp.MustCompile("[^a-zA-Z0-9_-]")

type toolRoute struct {
	provider ToolProvider
	spec     ToolSpec
}

// ToolRegistry 统一工具注册表
type ToolRegistry struct {
	mu        sync.RWMutex
	providers []ToolProvider
}

// NewToolRegistry 构造空工具注册表
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{}
}

// RegisterProvider 注册工具提供者，同名 Provider 替换并保持原顺序
func (r *ToolRegistry) RegisterProvider(provider ToolProvider) {
	if r == nil || provider == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := make([]ToolProvider, 0, len(r.providers)+1)
	replaced := false
	for _, item := range r.providers {
		if item.ProviderName() == provider.ProviderName() {
			if !replaced {
				updated = append(updated, provider)
				replaced = true
			}
			continue
		}
		updated = append(updated, item)
	}
	if !replaced {
		updated = append(updated, provider)
	}
	r.providers = updated
}

// UnregisterProvider 注销指定名称的工具提供者
func (r *ToolRegistry) UnregisterProvider(providerName string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := make([]ToolProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		if provider.ProviderName() != providerName {
			updated = append(updated, provider)
		}
	}
	r.providers = updated
}

// Providers 返回当前 Provider 快照
func (r *ToolRegistry) Providers() []ToolProvider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ToolProvider(nil), r.providers...)
}

// ListTools 按 Provider 顺序列出工具，重名项添加来源前缀并保留原始入口
func (r *ToolRegistry) ListTools(ctx context.Context, availability *ToolAvailabilityContext) ([]ToolSpec, error) {
	routes, err := r.listRoutes(ctx, availability)
	if err != nil {
		return nil, err
	}
	specs := make([]ToolSpec, 0, len(routes))
	for _, route := range routes {
		specs = append(specs, route.spec)
	}
	return specs, nil
}

// listRoutes 声明与执行共用名称分配规则，不修改 Provider 持有的原始声明
func (r *ToolRegistry) listRoutes(ctx context.Context, availability *ToolAvailabilityContext) ([]toolRoute, error) {
	if r == nil {
		return nil, nil
	}
	providers := r.Providers()
	type declared struct {
		provider ToolProvider
		spec     ToolSpec
	}
	entries := make([]declared, 0)
	for _, provider := range providers {
		specs, err := provider.ListTools(ctx, availability)
		if err != nil {
			return nil, fmt.Errorf("列出工具失败：%s: %w", provider.ProviderName(), err)
		}
		for _, spec := range specs {
			if spec.Enabled {
				entries = append(entries, declared{provider: provider, spec: cloneToolSpec(spec)})
			}
		}
	}
	reserved := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		reserved[entry.spec.Name] = struct{}{}
	}
	routes := make([]toolRoute, 0, len(entries))
	byName := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		spec := entry.spec
		originalName := spec.Name
		if spec.Metadata == nil {
			spec.Metadata = make(map[string]any)
		}
		spec.Metadata["registry_original_name"] = originalName
		if _, exists := byName[originalName]; exists {
			source := firstNonEmpty(metadataText(spec.Metadata, "server_name"), metadataText(spec.Metadata, "plugin_id"), entry.provider.ProviderName())
			prefix := entry.provider.ProviderType() + "_" + source
			if source == entry.provider.ProviderType() {
				prefix = source
			}
			candidate := toolNameSanitizer.ReplaceAllString(prefix+"_"+originalName, "_")
			if len(candidate) > 64 || hasToolName(byName, candidate) || hasToolName(reserved, candidate) {
				digest := sha256.Sum256([]byte(entry.provider.ProviderName() + ":" + source + ":" + originalName))
				candidate = candidate[:min(51, len(candidate))] + "_" + hex.EncodeToString(digest[:])[:12]
			}
			if hasToolName(byName, candidate) || hasToolName(reserved, candidate) {
				return nil, fmt.Errorf("工具来源重复，无法分配唯一名称：%s/%s/%s", entry.provider.ProviderName(), source, originalName)
			}
			spec.Name = candidate
		}
		byName[spec.Name] = struct{}{}
		routes = append(routes, toolRoute{provider: entry.provider, spec: spec})
	}
	return routes, nil
}

// GetToolSpec 查询指定工具声明
func (r *ToolRegistry) GetToolSpec(ctx context.Context, toolName string, availability *ToolAvailabilityContext) (ToolSpec, bool, error) {
	specs, err := r.ListTools(ctx, availability)
	if err != nil {
		return ToolSpec{}, false, err
	}
	for _, spec := range specs {
		if spec.Name == toolName {
			return spec, true, nil
		}
	}
	return ToolSpec{}, false, nil
}

// HasTool 判断指定工具是否存在
func (r *ToolRegistry) HasTool(ctx context.Context, toolName string, availability *ToolAvailabilityContext) (bool, error) {
	_, ok, err := r.GetToolSpec(ctx, toolName, availability)
	return ok, err
}

// GetLLMDefinitions 获取供 LLM 使用的工具定义列表
func (r *ToolRegistry) GetLLMDefinitions(ctx context.Context, availability *ToolAvailabilityContext) ([]map[string]any, error) {
	specs, err := r.ListTools(ctx, availability)
	if err != nil {
		return nil, err
	}
	definitions := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		definitions = append(definitions, spec.ToLLMDefinition())
	}
	return definitions, nil
}

// Invoke 执行一次工具调用
func (r *ToolRegistry) Invoke(ctx context.Context, invocation ToolInvocation, execution *ToolExecutionContext) ToolExecutionResult {
	routes, err := r.listRoutes(ctx, execution.AvailabilityContext())
	if err != nil {
		return ToolExecutionResult{
			ToolName:     invocation.ToolName,
			Success:      false,
			ErrorMessage: "工具路由失败：" + err.Error(),
		}
	}
	var matched *toolRoute
	for index := range routes {
		if routes[index].spec.Name == invocation.ToolName {
			matched = &routes[index]
			break
		}
	}
	if matched == nil {
		return ToolExecutionResult{
			ToolName:     invocation.ToolName,
			Success:      false,
			ErrorMessage: "未找到工具：" + invocation.ToolName,
		}
	}
	metadata := cloneMap(invocation.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if matched.provider.ProviderType() == "mcp" {
		metadata["mcp_server_name"] = matched.spec.Metadata["server_name"]
	}
	native := invocation
	native.ToolName = metadataText(matched.spec.Metadata, "registry_original_name")
	native.Metadata = metadata
	native.Arguments = FilterToolArguments(native.Arguments, matched.spec.ParametersSchema)
	result, err := invokeProvider(ctx, matched.provider, native, execution)
	if err != nil {
		message := strings.TrimSpace(err.Error())
		prefix := fmt.Sprintf("工具 %s 调用失败：%s", invocation.ToolName, errorTypeName(err))
		if message != "" {
			prefix += ": " + message
		}
		zap.L().Error("工具调用异常",
			zap.String("tool", invocation.ToolName),
			zap.String("provider", matched.provider.ProviderName()),
			zap.Error(err),
		)
		return ToolExecutionResult{
			ToolName:     invocation.ToolName,
			Success:      false,
			ErrorMessage: prefix,
		}
	}
	result.ToolName = invocation.ToolName
	return result
}

// Close 关闭全部 Provider
func (r *ToolRegistry) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	providers := r.Providers()
	var errs []error
	for _, provider := range providers {
		if err := provider.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("关闭工具提供者 %s 失败: %w", provider.ProviderName(), err))
		}
	}
	return errors.Join(errs...)
}

func invokeProvider(ctx context.Context, provider ToolProvider, invocation ToolInvocation, execution *ToolExecutionContext) (result ToolExecutionResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return provider.Invoke(ctx, invocation, execution)
}

func errorTypeName(err error) string {
	name := fmt.Sprintf("%T", err)
	name = strings.TrimPrefix(name, "*")
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	return name
}

func hasToolName(names map[string]struct{}, name string) bool {
	_, exists := names[name]
	return exists
}

func metadataText(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if text, ok := metadata[key].(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func cloneToolSpec(spec ToolSpec) ToolSpec {
	cloned := spec
	cloned.ParametersSchema = cloneMap(spec.ParametersSchema)
	cloned.OutputSchema = cloneMap(spec.OutputSchema)
	cloned.Icons = append([]ToolIcon(nil), spec.Icons...)
	cloned.Metadata = cloneMap(spec.Metadata)
	if spec.Annotation != nil {
		annotation := *spec.Annotation
		annotation.Audience = append([]string(nil), spec.Annotation.Audience...)
		annotation.Metadata = cloneMap(spec.Annotation.Metadata)
		cloned.Annotation = &annotation
	}
	return cloned
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned, _ := cloneAny(source).(map[string]any)
	return cloned
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneAny(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneAny(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
