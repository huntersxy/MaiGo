package contract

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// GenerationHooks 是客户端实现对外暴露的观测回调。
//
// 为什么需要这层：Python 的 openai_client.py 直接 import ..request_snapshot
// 与 ..cache_stats 记录请求快照和缓存命中；Python 是运行期导入，不构成循环。
// Go 编译期不允许——快照层反过来要读客户端的请求构造逻辑与 ContextItem 投影，
// 客户端再 import 回去就是循环。
//
// 解法：客户端只声明「我需要记录什么」，由父包在构造时注入实现。这样
// 依赖方向是「客户端 -> contract <- 父包」，两侧都向下，不会成环。
type GenerationHooks struct {
	// RecordCacheUsage 记录一次非流式生成的缓存用量
	RecordCacheUsage func(ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any, message *schema.AgenticMessage)
	// WrapStreamCacheStats 给流式读取器挂上缓存统计
	WrapStreamCacheStats func(reader *schema.StreamReader[*schema.AgenticMessage], ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any) *schema.StreamReader[*schema.AgenticMessage]
	// WrapStreamSnapshot 给流式读取器挂上失败快照记录
	WrapStreamSnapshot func(reader *schema.StreamReader[*schema.AgenticMessage], wrap func(error) error) *schema.StreamReader[*schema.AgenticMessage]
}

// 本文件里的 ContextItem 由 payload_content 子包提供。
// 为避免 contract 反向依赖 payload_content（那样会把 context 项定义与
// 客户端契约绑死），这里用类型别名在编译期打通：见 context_alias.go。
