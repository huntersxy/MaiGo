package llm

// 向 model_client 子包注入父包能力。
//
// 客户端实现（子包）在失败时要落盘快照、在成功时统计缓存命中、在 413 时压缩
// 图片、在记录用量时包装嵌入/音频客户端。这些能力住在父包，子包不能反向
// import，因此按 plugin_runtime/capabilities 的做法：父包 init 时注入。
//
// 注入是显式的：漏注入会在运行期表现为「不做这件事」，而不是编译期报错，
// 所以这里集中在 init 一处，便于审查。

import (
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/llm_models/model_client"
)

func init() {
	model_client.Configure(model_client.Ports{
		NormalizeContextImages:            normalizeContextImages,
		NormalizeContextImagesForModel:    normalizeContextImagesForModel,
		NormalizeAgenticMessageImages:     normalizeAgenticMessageImages,
		CompressContextImages:             compressContextImages,
		ImageCompressionTargetForError:    imageCompressionTargetForError,
		CaptureRequestSnapshot:            captureRequestSnapshot,
		RecordSnapshotAttempt:             recordSnapshotAttempt,
		WrapStreamSnapshot:                wrapAgenticStreamSnapshot,
		RecordGenerationCacheUsage:        recordGenerationCacheUsage,
		WrapStreamCacheStats:              wrapAgenticStreamCacheStats,
		NewUsageRecordingAudioTranscriber: newUsageRecordingAudioTranscriber,
		NewUsageRecordingEmbeddingClient:  newUsageRecordingEmbeddingClient,
		WrapUsageRecordingModel:           newUsageRecordingModel,
		NormalizeTextToolCalls:            NormalizeTextToolResponse,
	})
}

// 编译期断言：注入的函数签名与父包实现一致，改动任一侧都会立刻报错
var (
	_ func([]ContextItem) []ContextItem                                      = normalizeContextImages
	_ func([]ContextItem, int, int, int) []ContextItem                       = normalizeContextImagesForModel
	_ func([]*schema.AgenticMessage, int, int, int) []*schema.AgenticMessage = normalizeAgenticMessageImages
	_ func([]ContextItem, int) []ContextItem                                 = compressContextImages
	_ func(error) (int, bool)                                                = imageCompressionTargetForError
	_ func(requestSnapshotSpec) error                                        = captureRequestSnapshot
	_ func(*snapshotAttemptRecorder, *requestSnapshotSpec, time.Time)        = recordSnapshotAttempt
	_ func(AudioTranscriber, ModelConfig) AudioTranscriber                   = newUsageRecordingAudioTranscriber
	_ func(EmbeddingClient, ModelConfig) EmbeddingClient                     = newUsageRecordingEmbeddingClient
)

var _ func(ModelConfig) (model.AgenticModel, error) = NewClient
