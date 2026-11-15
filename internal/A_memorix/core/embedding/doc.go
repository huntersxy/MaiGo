// Package embedding 迁移 A_memorix 的嵌入层
//
// 对齐 MaiBot 原版 src/A_memorix/core/embedding 下的 presets.py、manager.py、api_adapter.py
// 本地 SentenceTransformer 加载在 Go 侧无法对等实现，EmbeddingManager 通过 ModelLoader 接口注入
// 未注入时构造即返回 ErrLocalModelUnsupported；EmbeddingAPIAdapter 通过 OpenAI 兼容 HTTP 或
// Gemini REST 直连宿主 embedding 服务，维度控制、重试与缓存语义与 Python 版本保持一致
package embedding
