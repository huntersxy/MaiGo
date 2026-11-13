package hostports

import (
	"context"
	"fmt"
	"strings"
)

// DocumentReplyGenerator 由主程序 Agent 实现（Agent.GenerateDocumentReply）
type DocumentReplyGenerator interface {
	GenerateDocumentReply(ctx context.Context, streamID, reference, formatInstruction, modelName string) (map[string]any, error)
}

// MaisakaContextRuntime 由主程序 Agent 实现，提供插件上下文注入与图片解析
type MaisakaContextRuntime interface {
	AppendPluginContext(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error)
	ResolvePluginContextImage(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error)
	TriggerPluginProactive(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error)
}

// Maisaka 提供插件侧 Maisaka 长文生成与上下文能力
type Maisaka struct {
	generator func() DocumentReplyGenerator
	context   func() MaisakaContextRuntime
}

// NewMaisaka 创建 Maisaka 端口；generator 延迟取 Agent，避免启动顺序依赖
func NewMaisaka(generator func() DocumentReplyGenerator) *Maisaka {
	return &Maisaka{generator: generator}
}

// SetContextRuntime 注入插件上下文运行时，Agent 启动后调用
func (m *Maisaka) SetContextRuntime(runtime func() MaisakaContextRuntime) {
	if m == nil {
		return
	}
	m.context = runtime
}

// Invoke 分发 maisaka.* 能力
func (m *Maisaka) Invoke(ctx context.Context, capability string, pluginID string, args map[string]any) (any, error) {
	switch capability {
	case "maisaka.reply.generate":
		return m.replyGenerate(ctx, args)
	case "maisaka.context.append", "maisaka.context.resolve_image", "maisaka.proactive.trigger":
		runtime := m.contextRuntime()
		if runtime == nil {
			return nil, fmt.Errorf("%s 端口未接线：主程序尚未注入 Maisaka 运行时", capability)
		}
		switch capability {
		case "maisaka.context.append":
			return runtime.AppendPluginContext(ctx, pluginID, args)
		case "maisaka.context.resolve_image":
			return runtime.ResolvePluginContextImage(ctx, pluginID, args)
		default:
			return runtime.TriggerPluginProactive(ctx, pluginID, args)
		}
	default:
		return nil, fmt.Errorf("未注册的能力: %s", capability)
	}
}

func (m *Maisaka) contextRuntime() MaisakaContextRuntime {
	if m == nil || m.context == nil {
		return nil
	}
	return m.context()
}

func (m *Maisaka) replyGenerate(ctx context.Context, args map[string]any) (any, error) {
	streamID := stringArg(args, "stream_id")
	if streamID == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 stream_id"}, nil
	}
	reference := stringArg(args, "reference")
	if reference == "" || len([]rune(reference)) > 500000 {
		return map[string]any{"success": false, "error": "reference 必须是 1–500000 字的非空文本"}, nil
	}
	formatInstruction := stringArg(args, "format_instruction")
	if len([]rune(formatInstruction)) > 8000 {
		return map[string]any{"success": false, "error": "format_instruction 必须是不超过 8000 字的文本"}, nil
	}
	modelName := stringArg(args, "model_name")
	if len([]rune(modelName)) > 200 {
		return map[string]any{"success": false, "error": "model_name 必须是不超过 200 字的宿主模型名称"}, nil
	}
	if m == nil || m.generator == nil {
		return map[string]any{"success": false, "error": "maisaka.reply.generate 端口未接线：主程序尚未注入 Replyer 生成器"}, nil
	}
	generator := m.generator()
	if generator == nil {
		return map[string]any{"success": false, "error": "maisaka.reply.generate 端口未接线：Replyer 生成器尚未就绪"}, nil
	}
	result, err := generator.GenerateDocumentReply(ctx, streamID, reference, formatInstruction, modelName)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	text := strings.TrimSpace(stringValue(result["text"]))
	if text == "" {
		return map[string]any{"success": false, "error": "Replyer 返回了空文本"}, nil
	}
	out := map[string]any{"success": true, "text": text}
	if ids, ok := result["selected_expression_ids"]; ok {
		out["selected_expression_ids"] = ids
	}
	return out, nil
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func stringValue(raw any) string {
	if raw == nil {
		return ""
	}
	value, _ := raw.(string)
	return value
}
