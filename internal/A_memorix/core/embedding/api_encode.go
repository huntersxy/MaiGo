package embedding

// 适配器编码管线：维度探测、批量并发、缓存与重试，对齐 api_adapter.py

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/llm_models"

	"golang.org/x/sync/semaphore"
)

// EncodeOptions Encode 的选项，对应 Python 可选参数
// Go 侧不实现 show_progress/normalize，它们对 API 适配器无实际作用
type EncodeOptions struct {
	BatchSize  int
	Dimensions *int
}

// EncodeBatchOptions EncodeBatch 的选项，对应 Python 可选参数
type EncodeBatchOptions struct {
	BatchSize    int
	NumWorkers   int
	ShowProgress bool
	Dimensions   *int
}

// Encode 生成文本嵌入，对齐 EmbeddingAPIAdapter.encode
// 单条文本请用 EncodeOne，对应 Python 传 str 时返回 1D 数组
// 维度探测错误按 Python 行为原样返回，只有批次编码错误才包装为 embedding encode failed
func (a *EmbeddingAPIAdapter) Encode(
	ctx context.Context,
	texts []string,
	opt *EncodeOptions,
) ([][]float32, error) {
	start := time.Now()
	options := opt
	if options == nil {
		options = &EncodeOptions{}
	}
	batchSize := a.desiredBatchSize(options.BatchSize)
	dimensions := normalizeDimensionOverride(options.Dimensions)

	var requestedDimension *int
	if dimensions == nil || a.dimensionRequestMode == "never" {
		if _, detectErr := a.DetectDimension(ctx); detectErr != nil {
			return nil, detectErr
		}
		if a.dimensionRequestMode == "always" {
			value := a.resolveCanonicalDimension(nil)
			requestedDimension = &value
		}
	} else {
		value := a.resolveCanonicalDimension(dimensions)
		requestedDimension = &value
	}

	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	embeddings, err := a.encodeBatchInternal(ctx, texts, batchSize, requestedDimension)
	if err != nil {
		a.totalErrors.Add(1)
		adapterLogger.Error(fmt.Sprintf("编码失败: %v", err))
		return nil, fmt.Errorf("embedding encode failed: %w", err)
	}
	a.totalEncoded.Add(int64(len(texts)))
	elapsed := time.Since(start)
	a.totalTimeNanos.Add(elapsed.Nanoseconds())
	adapterLogger.Debug(fmt.Sprintf(
		"编码完成: %d 个文本, 耗时 %.2fs, 平均 %.3fs/文本",
		len(texts),
		elapsed.Seconds(),
		elapsed.Seconds()/float64(len(texts)),
	))
	return embeddings, nil
}

// EncodeOne 生成单条文本嵌入，对齐 Python 单字符串输入返回 1D 数组的分支
func (a *EmbeddingAPIAdapter) EncodeOne(
	ctx context.Context,
	text string,
	opt *EncodeOptions,
) ([]float32, error) {
	embeddings, err := a.Encode(ctx, []string{text}, opt)
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, errors.New("embedding encode failed: 文本 0 编码失败：embedding 返回为空")
	}
	return embeddings[0], nil
}

// EncodeBatch 批量生成嵌入，对齐 encode_batch 的临时并发调整
func (a *EmbeddingAPIAdapter) EncodeBatch(
	ctx context.Context,
	texts []string,
	opt *EncodeBatchOptions,
) ([][]float32, error) {
	options := opt
	if options == nil {
		options = &EncodeBatchOptions{}
	}
	if options.NumWorkers != 0 {
		workers := options.NumWorkers
		if workers < 1 {
			workers = 1
		}
		previous := a.setMaxConcurrent(workers)
		defer a.setMaxConcurrent(previous)
	}
	return a.Encode(ctx, texts, &EncodeOptions{
		BatchSize:  options.BatchSize,
		Dimensions: options.Dimensions,
	})
}

// pendingText 待编码文本及其全局下标
type pendingText struct {
	localIndex    int
	absoluteIndex int
	text          string
}

// encodeBatchInternal 按批次与并发上限编码，对齐 _encode_batch_internal
func (a *EmbeddingAPIAdapter) encodeBatchInternal(
	ctx context.Context,
	texts []string,
	batchSize int,
	dimensions *int,
) ([][]float32, error) {
	if batchSize < 1 {
		batchSize = 1
	}
	configKey := ""
	cacheDimension := a.resolveCanonicalDimension(dimensions)
	if a.enableCache {
		key, err := a.dimensionCacheKey()
		if err != nil {
			return nil, err
		}
		configKey = key
	}

	allEmbeddings := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		batchResults := make([][]float32, len(batch))
		pending := make([]pendingText, 0, len(batch))
		if a.enableCache {
			for offset, text := range batch {
				cacheKey := newEmbeddingTextCacheKey(configKey, cacheDimension, text)
				if vector, ok := lookupGlobalTextEmbedding(cacheKey); ok {
					batchResults[offset] = vector
					continue
				}
				pending = append(pending, pendingText{localIndex: offset, absoluteIndex: start + offset, text: text})
			}
		} else {
			for offset, text := range batch {
				pending = append(pending, pendingText{localIndex: offset, absoluteIndex: start + offset, text: text})
			}
		}
		if len(pending) == 0 {
			allEmbeddings = append(allEmbeddings, batchResults...)
			continue
		}

		concurrency := a.currentMaxConcurrent()
		if concurrency < 1 {
			concurrency = 1
		}
		sem := semaphore.NewWeighted(int64(concurrency))
		vectors := make([][]float32, len(pending))
		errorsByIndex := make([]error, len(pending))
		var waitGroup sync.WaitGroup
		for index := range pending {
			waitGroup.Add(1)
			// 对齐原 channel 信号量：这里只限制并发度，不响应 ctx 取消。
			_ = sem.Acquire(context.Background(), 1)
			go func(index int) {
				defer func() {
					sem.Release(1)
					waitGroup.Done()
				}()
				vector, ok, err := a.getEmbeddingDirect(ctx, pending[index].text, dimensions, true)
				if err != nil {
					errorsByIndex[index] = err
					return
				}
				if !ok || len(vector) == 0 {
					errorsByIndex[index] = fmt.Errorf(
						"文本 %d 编码失败：embedding 返回为空",
						pending[index].absoluteIndex,
					)
					return
				}
				errorsByIndex[index] = validateEmbeddingVector(
					vector,
					fmt.Sprintf("文本 %d", pending[index].absoluteIndex),
				)
				if errorsByIndex[index] == nil {
					vectors[index] = vector
				}
			}(index)
		}
		waitGroup.Wait()

		for index := range pending {
			if errorsByIndex[index] != nil {
				continue
			}
			if a.enableCache {
				storeGlobalTextEmbedding(
					newEmbeddingTextCacheKey(configKey, cacheDimension, pending[index].text),
					vectors[index],
				)
			}
			batchResults[pending[index].localIndex] = cloneVector(vectors[index])
		}
		for index := range errorsByIndex {
			if errorsByIndex[index] != nil {
				return nil, errorsByIndex[index]
			}
		}
		allEmbeddings = append(allEmbeddings, batchResults...)
	}

	dimension := -1
	for index, vector := range allEmbeddings {
		if dimension < 0 {
			dimension = len(vector)
			continue
		}
		if len(vector) != dimension {
			// 对齐 np.array 对不规则数组抛 ValueError 后由 encode 包装为 embedding encode failed
			return nil, fmt.Errorf(
				"批量嵌入维度不一致: 第 %d 条维度为 %d，首条为 %d",
				index,
				len(vector),
				dimension,
			)
		}
	}
	return allEmbeddings, nil
}

// getEmbeddingDirect 通过直连 Client 获取单条向量，对齐 _get_embedding_direct
// 返回 (向量, 是否成功, 错误)；错误仅表示配置级失败，逐模型失败返回成功标记 false
func (a *EmbeddingAPIAdapter) getEmbeddingDirect(
	ctx context.Context,
	text string,
	dimensions *int,
	includeDimension bool,
) ([]float32, bool, error) {
	file, err := a.modelConfigFile()
	if err != nil {
		return nil, false, err
	}
	candidates := a.resolveCandidateModelNames(file)
	if len(candidates) == 0 {
		return nil, false, errors.New("embedding 任务未配置模型")
	}
	if _, err := a.dimensionCacheKey(); err != nil {
		return nil, false, err
	}

	var lastError error
	for _, candidate := range candidates {
		modelInfo, err := a.findModelInfo(file, candidate)
		if err != nil {
			adapterLogger.Warn(fmt.Sprintf("embedding 模型 %s 请求失败: %v", candidate, err))
			lastError = err
			continue
		}
		provider, err := a.findProvider(file, modelInfo.ApiProvider)
		if err != nil {
			adapterLogger.Warn(fmt.Sprintf("embedding 模型 %s 请求失败: %v", candidate, err))
			lastError = err
			continue
		}
		include := a.shouldIncludeDimension(dimensions, includeDimension)
		var requestedDimension *int
		if include {
			value := a.resolveCanonicalDimension(dimensions)
			requestedDimension = &value
		}
		extraParams := a.buildRequestExtraParams(provider, modelInfo.ExtraParams, requestedDimension, include)
		vector, err := a.requestWithRetry(ctx, EmbeddingRequest{
			ModelInfo:   modelInfo,
			Provider:    provider,
			Input:       text,
			ExtraParams: extraParams,
		})
		if err == nil && vector == nil {
			err = fmt.Errorf("模型 %s 未返回 embedding", candidate)
		}
		if err == nil {
			err = validateEmbeddingVector(vector, fmt.Sprintf("embedding 模型 %s", candidate))
		}
		if err == nil {
			a.stateMu.Lock()
			a.lastModel = strings.TrimSpace(candidate)
			a.lastPrimary = candidates[0]
			a.lastProvider = strings.TrimSpace(modelInfo.ApiProvider)
			a.lastDimension = len(vector)
			a.stateMu.Unlock()
			return vector, true, nil
		}
		adapterLogger.Warn(fmt.Sprintf("embedding 模型 %s 请求失败: %v", candidate, err))
		lastError = err
	}
	adapterLogger.Error(fmt.Sprintf("通过直接 Client 获取 Embedding 失败: %v", lastError))
	return nil, false, nil
}

// requestWithRetry 执行单次嵌入请求并处理外层重试，对齐 _request_with_retry
func (a *EmbeddingAPIAdapter) requestWithRetry(
	ctx context.Context,
	request EmbeddingRequest,
) ([]float32, error) {
	retry := a.retryConfig
	var lastError error
	for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
		vector, err := a.transport.GetEmbedding(ctx, request)
		if err == nil {
			return vector, nil
		}
		if !isRetriableEmbeddingError(err) || attempt >= retry.MaxAttempts {
			return nil, err
		}
		waitSeconds := retry.MinWaitSeconds * math.Pow(retry.BackoffMultiplier, float64(attempt-1))
		if waitSeconds > retry.MaxWaitSeconds {
			waitSeconds = retry.MaxWaitSeconds
		}
		wait := time.Duration(waitSeconds * float64(time.Second))
		adapterLogger.Warn(fmt.Sprintf(
			"Embedding 请求失败，重试 %d/%d，%.1fs 后重试: %v",
			attempt,
			max(1, retry.MaxAttempts-1),
			wait.Seconds(),
			err,
		))
		lastError = err
		if err := a.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, errors.New("Embedding 请求失败：未知错误")
}

// isRetriableEmbeddingError 判断是否为可重试的网络类异常
func isRetriableEmbeddingError(err error) bool {
	if err == nil {
		return false
	}
	var networkError *llm.NetworkError
	if errors.As(err, &networkError) {
		return true
	}
	var netError net.Error
	if errors.As(err, &netError) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// sleep 等待重试间隔，可注入测试实现
func (a *EmbeddingAPIAdapter) sleep(ctx context.Context, duration time.Duration) error {
	if a.sleepFunc != nil {
		return a.sleepFunc(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DetectDimension 探测嵌入维度，对齐 _detect_dimension
func (a *EmbeddingAPIAdapter) DetectDimension(ctx context.Context) (int, error) {
	file, err := a.modelConfigFile()
	if err != nil {
		return 0, err
	}
	candidates := a.resolveCandidateModelNames(file)
	configKey, err := a.dimensionCacheKey()
	if err != nil {
		return 0, err
	}

	a.stateMu.Lock()
	if configKey != a.lastConfigKey {
		a.detected = false
		a.dimension = 0
		a.lastModel = ""
		a.lastProvider = ""
		a.lastDimension = 0
		a.lastConfigKey = configKey
	}
	if a.detected {
		dimension := a.dimension
		a.stateMu.Unlock()
		return dimension, nil
	}
	a.stateMu.Unlock()

	if entry, ok := getGlobalDimensionCache(configKey); ok {
		a.stateMu.Lock()
		a.dimension = entry.dimension
		a.detected = true
		a.lastModel = entry.modelName
		a.lastProvider = entry.providerName
		if entry.modelName != "" {
			a.lastDimension = entry.dimension
		} else {
			a.lastDimension = 0
		}
		if len(candidates) > 0 {
			a.lastPrimary = candidates[0]
		}
		a.stateMu.Unlock()
		adapterLogger.Debug(fmt.Sprintf("嵌入维度命中进程缓存: %d", entry.dimension))
		return entry.dimension, nil
	}

	adapterLogger.Info("检测嵌入维度...")
	if a.dimensionRequestMode == "always" {
		target := a.defaultDimension
		adapterLogger.Debug(fmt.Sprintf("尝试请求指定维度: %d", target))
		vector, ok, err := a.getEmbeddingDirect(ctx, "test", &target, true)
		if err == nil && ok && len(vector) > 0 {
			detected := len(vector)
			if detected == target {
				adapterLogger.Info(fmt.Sprintf("嵌入维度: %d", detected))
			} else {
				adapterLogger.Warn(fmt.Sprintf(
					"requested_dimension=%d 但模型返回 detected_dimension=%d，将使用真实输出维度",
					target,
					detected,
				))
			}
			a.storeDetectedDimension(configKey, detected, candidates)
			return detected, nil
		}
		if err != nil {
			adapterLogger.Debug(fmt.Sprintf("带维度参数探测失败: %v，尝试不带维度参数探测", err))
		}
	}

	vector, ok, err := a.getEmbeddingDirect(ctx, "test", nil, false)
	if err != nil {
		adapterLogger.Error(fmt.Sprintf(
			"嵌入维度检测异常: %v，使用 configured_dimension: %d",
			err,
			a.defaultDimension,
		))
		return a.fallbackDimension(configKey), nil
	}
	if ok && len(vector) > 0 {
		detected := len(vector)
		adapterLogger.Info(fmt.Sprintf("嵌入维度: %d (自然输出)", detected))
		a.storeDetectedDimension(configKey, detected, candidates)
		return detected, nil
	}
	adapterLogger.Warn(fmt.Sprintf("嵌入维度检测失败，使用 configured_dimension: %d", a.defaultDimension))
	return a.fallbackDimension(configKey), nil
}

// storeDetectedDimension 记录探测结果并写入进程级缓存
func (a *EmbeddingAPIAdapter) storeDetectedDimension(configKey string, detected int, candidates []string) {
	a.stateMu.Lock()
	a.dimension = detected
	a.detected = true
	a.lastDimension = detected
	if len(candidates) > 0 {
		a.lastPrimary = candidates[0]
	}
	modelName := a.lastModel
	providerName := a.lastProvider
	a.stateMu.Unlock()
	storeGlobalDimensionCache(configKey, dimensionCacheEntry{
		dimension:    detected,
		modelName:    modelName,
		providerName: providerName,
	})
}

// fallbackDimension 探测失败时回落到配置维度
func (a *EmbeddingAPIAdapter) fallbackDimension(configKey string) int {
	a.stateMu.Lock()
	a.dimension = a.defaultDimension
	a.detected = true
	a.stateMu.Unlock()
	storeGlobalDimensionCache(configKey, dimensionCacheEntry{dimension: a.defaultDimension})
	return a.defaultDimension
}
