package contract

// 结构化输出上下文注入
// eino 的 model.Options 没有 response_format 字段，MaiBot 业务层则按请求携带 RespFormat。
// Go 侧通过 context 把任务级 response_format 传给具体客户端；优先级：
//   context 注入 > ModelConfig.ResponseFormat > 不发送
// 该层不伪造业务配置：只有调用方显式 WithResponseFormat 才生效

import (
	"context"
)

type responseFormatContextKey struct{}

type responseFormatContextValue struct {
	value any
}

// WithResponseFormat 在请求上下文注入 response_format；value 可为字符串或 MaiBot JsonSchema 包装
func WithResponseFormat(ctx context.Context, value any) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, responseFormatContextKey{}, responseFormatContextValue{value: value})
}

func ResponseFormatFromContext(ctx context.Context) (any, bool) {
	if ctx == nil {
		return nil, false
	}
	value, ok := ctx.Value(responseFormatContextKey{}).(responseFormatContextValue)
	if !ok {
		return nil, false
	}
	return value.value, true
}

// effectiveResponseFormat 合并上下文与配置；返回 nil 表示不发送
func EffectiveResponseFormat(ctx context.Context, configured any) any {
	if value, ok := ResponseFormatFromContext(ctx); ok {
		return value
	}
	return configured
}

// responseFormatSnapshot 序列化为 MaiBot RespFormat.to_dict 形状
func ResponseFormatSnapshot(spec ResponseFormatSpec) map[string]any {
	switch spec.Kind {
	case "text":
		return map[string]any{"format_type": "text"}
	case "json_object":
		return map[string]any{"format_type": "json_object"}
	case "json_schema":
		if spec.Schema == nil {
			return map[string]any{"format_type": "json_schema"}
		}
		return map[string]any{"format_type": "json_schema", "schema": CloneAnyMap(spec.Schema)}
	default:
		return nil
	}
}
