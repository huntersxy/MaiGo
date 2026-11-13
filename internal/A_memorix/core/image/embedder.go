package imagememory

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/A_memorix/core/utils"
	"maibot-go/internal/llm_models"
)

// imageEmbeddingRequestType 对齐 Python EmbeddingServiceClient 的 request_type
const imageEmbeddingRequestType = "A_Memorix.ImageEmbedding"

// ImageEmbedder 对齐 Python ImageEmbedder 协议
type ImageEmbedder interface {
	Probe(ctx context.Context) (map[string]any, error)
	Embed(ctx context.Context, imageBytes []byte, mimeType string, sessionID string) (map[string]any, error)
}

// HostImageEmbedderOptions 对齐 HostImageEmbedder 构造参数
type HostImageEmbedderOptions struct {
	Client llm.EmbeddingClient
	// ClientProvider 延迟解析嵌入客户端，供宿主在模型路由就绪后注入
	ClientProvider    func(ctx context.Context) (llm.EmbeddingClient, error)
	TaskName          string
	PreprocessVersion string
	// nil 表示使用默认 60 秒，显式 0 表示失败后立即重试
	ProbeRetrySeconds *float64
	Logger            utils.Logger
}

// HostImageEmbedder 通过真实嵌入客户端探测并嵌入图片
type HostImageEmbedder struct {
	client            llm.EmbeddingClient
	clientProvider    func(ctx context.Context) (llm.EmbeddingClient, error)
	taskName          string
	preprocessVersion string
	probeRetry        time.Duration
	logger            utils.Logger
	now               func() time.Time

	mu          sync.Mutex
	probeReport map[string]any
	nextProbeAt time.Time
}

// NewHostImageEmbedder 使用默认任务名与预处理版本创建嵌入器
func NewHostImageEmbedder(client llm.EmbeddingClient) (*HostImageEmbedder, error) {
	return NewHostImageEmbedderWithOptions(HostImageEmbedderOptions{Client: client})
}

// NewHostImageEmbedderWithOptions 按显式配置创建嵌入器
func NewHostImageEmbedderWithOptions(options HostImageEmbedderOptions) (*HostImageEmbedder, error) {
	if options.Client == nil && options.ClientProvider == nil {
		return nil, validationErrorf("图片嵌入客户端不能为空")
	}
	taskName := options.TaskName
	if taskName == "" {
		taskName = "image_embedding"
	}
	preprocessVersion := options.PreprocessVersion
	if preprocessVersion == "" {
		preprocessVersion = "identity_v1"
	}
	retrySeconds := 60.0
	if options.ProbeRetrySeconds != nil {
		retrySeconds = *options.ProbeRetrySeconds
	}
	if retrySeconds < 0 {
		retrySeconds = 0
	}
	return &HostImageEmbedder{
		client:            options.Client,
		clientProvider:    options.ClientProvider,
		taskName:          taskName,
		preprocessVersion: preprocessVersion,
		probeRetry:        time.Duration(retrySeconds * float64(time.Second)),
		logger:            utils.OrLogger(options.Logger),
		now:               time.Now,
	}, nil
}

// TaskName 返回探测使用的任务名
func (h *HostImageEmbedder) TaskName() string { return h.taskName }

// PreprocessVersion 返回图片预处理版本
func (h *HostImageEmbedder) PreprocessVersion() string { return h.preprocessVersion }

// ProbeReport 返回最近一次探测报告副本
func (h *HostImageEmbedder) ProbeReport() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.probeReport == nil {
		return nil
	}
	return copyAnyMap(h.probeReport)
}

// Probe 对齐 HostImageEmbedder.probe 的缓存、三次真实图片请求与余弦判定
func (h *HostImageEmbedder) Probe(ctx context.Context) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.probeReport != nil && (truthy(h.probeReport["available"]) || now.Before(h.nextProbeAt)) {
		return copyAnyMap(h.probeReport), nil
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	firstPNG, err := probePNG(20, 80, 210)
	if err != nil {
		return nil, err
	}
	first, err := h.requestLocked(ctx, firstPNG, "image/png", "")
	if err != nil {
		return h.failedProbeLocked(ctx, err)
	}
	second, err := h.requestLocked(ctx, firstPNG, "image/png", "")
	if err != nil {
		return h.failedProbeLocked(ctx, err)
	}
	differentPNG, err := probePNG(220, 40, 30)
	if err != nil {
		return nil, err
	}
	third, err := h.requestLocked(ctx, differentPNG, "image/png", "")
	if err != nil {
		return h.failedProbeLocked(ctx, err)
	}
	fingerprint := mapFromAny(first["fingerprint"])
	if !fingerprintEqual(fingerprint, mapFromAny(second["fingerprint"])) || !fingerprintEqual(fingerprint, mapFromAny(third["fingerprint"])) {
		return h.failedProbeLocked(ctx, runtimeErrorf("图片嵌入模型在探测期间返回了不同的嵌入空间"))
	}
	same := cosineSimilarity(embeddingVector(first), embeddingVector(second))
	different := cosineSimilarity(embeddingVector(first), embeddingVector(third))
	if same < 0.95 {
		return h.failedProbeLocked(ctx, runtimeErrorf("同图嵌入不稳定，余弦相似度为 %.4f", same))
	}
	if different >= 0.999 {
		return h.failedProbeLocked(ctx, runtimeErrorf("不同探测图片的向量无法区分"))
	}
	h.probeReport = map[string]any{
		"available":              true,
		"status":                 "ready",
		"fingerprint":            fingerprint,
		"dimension":              len(embeddingVector(first)),
		"same_image_cosine":      same,
		"different_image_cosine": different,
		"message":                "图片嵌入模型已通过真实图片探测",
	}
	return copyAnyMap(h.probeReport), nil
}

// Embed 对齐 HostImageEmbedder.embed，探测不可用或指纹变化时拒绝嵌入
func (h *HostImageEmbedder) Embed(ctx context.Context, imageBytes []byte, mimeType string, sessionID string) (map[string]any, error) {
	report, err := h.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if !truthy(report["available"]) {
		return nil, runtimeErrorf("%s", stringOr(report["message"], "图片嵌入模型不可用"))
	}
	encoded, err := h.requestLocked(ctx, imageBytes, mimeType, sessionID)
	if err != nil {
		return nil, err
	}
	expected := mapFromAny(report["fingerprint"])
	actual := mapFromAny(encoded["fingerprint"])
	if !fingerprintEqual(actual, expected) {
		return nil, runtimeErrorf("图片嵌入空间在运行期间发生变化")
	}
	return encoded, nil
}

// requestLocked 调用真实嵌入客户端并校验向量
func (h *HostImageEmbedder) requestLocked(ctx context.Context, imageBytes []byte, mimeType string, sessionID string) (map[string]any, error) {
	client := h.client
	if client == nil {
		if h.clientProvider == nil {
			return nil, runtimeErrorf("图片嵌入客户端尚未接线")
		}
		resolved, err := h.clientProvider(ctx)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			return nil, runtimeErrorf("图片嵌入客户端尚未就绪")
		}
		client = resolved
	}
	result, err := client.EmbedImage(ctx, llm.ImageEmbeddingInput{
		Bytes:             imageBytes,
		MIMEType:          mimeType,
		PreprocessVersion: h.preprocessVersion,
		TaskName:          h.taskName,
		RequestType:       imageEmbeddingRequestType,
		SessionID:         sessionID,
	})
	if err != nil {
		return nil, err
	}
	vector := result.Embedding
	if len(vector) == 0 || !isFiniteVector64(vector) {
		return nil, runtimeErrorf("图片嵌入模型返回了空向量或非有限数值")
	}
	norm := vectorNorm64(vector)
	if norm <= 1e-12 {
		return nil, runtimeErrorf("图片嵌入模型返回了零向量")
	}
	normalized := make([]float32, len(vector))
	for index, value := range vector {
		normalized[index] = float32(value / norm)
	}
	model := strings.TrimSpace(result.ModelIdentifier)
	if model == "" {
		model = strings.TrimSpace(result.Model)
	}
	fingerprint, err := BuildImageEmbeddingFingerprint(ImageEmbeddingFingerprintInput{
		Model:               model,
		Provider:            result.APIProvider,
		Dimension:           len(normalized),
		PreprocessVersion:   h.preprocessVersion,
		Normalization:       "l2",
		RequestProtocolHash: result.RequestProtocol,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"embedding": normalized, "fingerprint": fingerprint}, nil
}

// failedProbeLocked 记录失败报告与下次探测时间，取消错误继续上抛
func (h *HostImageEmbedder) failedProbeLocked(ctx context.Context, cause error) (map[string]any, error) {
	if errorIsCancellation(cause) || errors.Is(contextError(ctx), context.Canceled) {
		return nil, cause
	}
	report := map[string]any{
		"available":   false,
		"status":      "unavailable",
		"fingerprint": map[string]any{},
		"dimension":   0,
		"reason":      errorReason(cause),
		"message":     cause.Error(),
	}
	h.probeReport = report
	h.nextProbeAt = h.now().Add(h.probeRetry)
	h.logger.Warning("图片嵌入探测失败: " + cause.Error())
	return copyAnyMap(report), nil
}

// probePNG 生成 24x24 纯色探测图
func probePNG(red uint8, green uint8, blue uint8) ([]byte, error) {
	probe := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			probe.SetRGBA(x, y, color.RGBA{R: red, G: green, B: blue, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, probe); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// cosineSimilarity 对齐 Python _cosine，分母为 0 返回 0
func cosineSimilarity(left []float32, right []float32) float64 {
	if len(left) != len(right) || len(left) == 0 {
		return 0
	}
	var dot float64
	var leftNorm float64
	var rightNorm float64
	for index := range left {
		leftValue := float64(left[index])
		rightValue := float64(right[index])
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	denominator := math.Sqrt(leftNorm) * math.Sqrt(rightNorm)
	if denominator <= 0 {
		return 0
	}
	return dot / denominator
}

// embeddingVector 读取编码结果中的向量，兼容常见数值切片类型
func embeddingVector(encoded map[string]any) []float32 {
	if encoded == nil {
		return nil
	}
	switch vector := encoded["embedding"].(type) {
	case []float32:
		return vector
	case []float64:
		converted := make([]float32, len(vector))
		for index, value := range vector {
			converted[index] = float32(value)
		}
		return converted
	case []int:
		converted := make([]float32, len(vector))
		for index, value := range vector {
			converted[index] = float32(value)
		}
		return converted
	case []any:
		converted := make([]float32, len(vector))
		for index, value := range vector {
			number, ok := numberValue(value)
			if !ok {
				return nil
			}
			converted[index] = float32(number)
		}
		return converted
	}
	return nil
}

// vectorNorm 计算向量 L2 范数
func vectorNorm(vector []float32) float64 {
	var sum float64
	for _, value := range vector {
		number := float64(value)
		sum += number * number
	}
	return math.Sqrt(sum)
}

func vectorNorm64(vector []float64) float64 {
	var sum float64
	for _, value := range vector {
		sum += value * value
	}
	return math.Sqrt(sum)
}

func isFiniteVector64(vector []float64) bool {
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

// contextError 返回 context 已结束时的原因
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// errorIsCancellation 只把取消视为不可缓存，超时按 Python TimeoutError 记录失败报告
func errorIsCancellation(err error) bool {
	return errors.Is(err, context.Canceled)
}

// copyAnyMap 复制字符串键对象
func copyAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	copied := make(map[string]any, len(source))
	for key, value := range source {
		copied[key] = value
	}
	return copied
}
