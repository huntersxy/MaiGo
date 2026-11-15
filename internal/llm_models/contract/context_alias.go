package contract

import (
	payloadcontent "MaiGo/internal/llm_models/payload_content"
)

// ContextItem 是上下文项的别名。
//
// payload_content 子包不依赖本包任何符号，因此 contract -> payload_content
// 是单向的，不会成环。客户端与快照层统一用 contract.ContextItem，
// 保证「同一份上下文项定义」只有一处。
type ContextItem = payloadcontent.ContextItem
