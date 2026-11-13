package imagesystem

import "context"

// Describer 图片描述生成端口，对应 Python 的 vlm LLMServiceClient
type Describer interface {
	// Available 是否配置了可用的视觉模型任务
	Available() bool
	// Describe 生成图片描述，data 为原图字节，format 为小写格式名
	Describe(ctx context.Context, data []byte, format string) (string, error)
}

// MemorySync 图片描述写入 A_Memorix 的端口，对应 memory_service.image_memory(describe)
type MemorySync interface {
	SyncImageDescription(ctx context.Context, imageHash, text string) error
}

// RecognitionListener 图片识别完成通知，对应 log_tracked_image_recognition_completed
type RecognitionListener func(imageHash string)
