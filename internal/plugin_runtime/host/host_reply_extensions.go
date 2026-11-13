package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ReplyExtensions 返回当前会话启用的回复扩展组件，按 priority 降序
func (h *Host) ReplyExtensions(sessionID string) []*ComponentEntry {
	if h == nil || h.components == nil {
		return nil
	}
	entries, err := h.components.ComponentsByType(ComponentTypeReplyExtension, true, ComponentFilter{SessionID: sessionID})
	if err != nil {
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Priority > entries[j].Priority
	})
	return entries
}

// ValidateReplyExtensionArguments 校验回复扩展参数，对齐 reply_extensions.validate_parameters
//
// Python 每次调用都 deepcopy 参数、按 schema 补默认值、再校验；Go 同样不修改调用方的
// map，返回补齐后的参数副本供后续调用使用。
func ValidateReplyExtensionArguments(entry *ComponentEntry, args map[string]any) (map[string]any, error) {
	if entry == nil {
		return nil, fmt.Errorf("回复扩展不存在")
	}
	if entry.ComponentType != ComponentTypeReplyExtension {
		return nil, fmt.Errorf("组件 %s 不是回复扩展", entry.FullName)
	}
	schema := entry.BuildParametersSchema()
	prepared := make(map[string]any, len(args))
	for key, value := range args {
		prepared[key] = cloneAnyValue(value)
	}
	if len(schema) == 0 {
		return prepared, nil
	}
	fillSchemaDefaults(prepared, schema)
	rawSchema, err := sonic.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("回复扩展参数模型非法: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("inline://reply-extension.json", strings.NewReader(string(rawSchema))); err != nil {
		return nil, fmt.Errorf("回复扩展参数模型非法: %w", err)
	}
	compiled, err := compiler.Compile("inline://reply-extension.json")
	if err != nil {
		return nil, fmt.Errorf("回复扩展参数模型非法: %w", err)
	}
	if err := compiled.Validate(prepared); err != nil {
		return nil, fmt.Errorf("回复扩展参数不符合声明: %w", err)
	}
	return prepared, nil
}

// InvokeReplyExtension 校验并调用回复扩展
func (h *Host) InvokeReplyExtension(ctx context.Context, fullName string, args map[string]any, timeoutMS int64) (map[string]any, error) {
	if h == nil || h.components == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	entry := h.components.GetComponent(strings.TrimSpace(fullName))
	if entry == nil {
		return nil, fmt.Errorf("未找到回复扩展: %s", fullName)
	}
	prepared, err := ValidateReplyExtensionArguments(entry, args)
	if err != nil {
		return nil, err
	}
	envelope, err := h.InvokePlugin(ctx, methodPluginInvokeReplyExtension, entry.PluginID, entry.Name, prepared, timeoutMS)
	if err != nil {
		return nil, err
	}
	if len(envelope.Error) > 0 {
		return nil, fmt.Errorf("%s", stringValue(envelope.Error["message"]))
	}
	return envelope.Payload, nil
}
