package retrieval

// A_memorix 动态阈值过滤器，对齐 core/retrieval/threshold.py
// 根据结果分布自适应计算阈值，支持百分位/标准差/跳变检测/自适应四种方法

import (
	"fmt"
	"math"
	"sort"
)

// ThresholdMethod 阈值计算方法
type ThresholdMethod string

const (
	// ThresholdPercentile 百分位数
	ThresholdPercentile ThresholdMethod = "percentile"
	// ThresholdStdDev 标准差
	ThresholdStdDev ThresholdMethod = "std_dev"
	// ThresholdGapDetection 跳变检测
	ThresholdGapDetection ThresholdMethod = "gap_detection"
	// ThresholdAdaptive 自适应（综合多种方法）
	ThresholdAdaptive ThresholdMethod = "adaptive"
)

// ThresholdConfig 阈值配置
type ThresholdConfig struct {
	Method        ThresholdMethod
	MinThreshold  float64
	MaxThreshold  float64
	Percentile    float64
	StdMultiplier float64
	MinResults    int
}

// DefaultThresholdConfig 与 Python 默认值一致
func DefaultThresholdConfig() ThresholdConfig {
	return ThresholdConfig{
		Method:        ThresholdAdaptive,
		MinThreshold:  0.29,
		MaxThreshold:  0.95,
		Percentile:    75.0,
		StdMultiplier: 1.5,
		MinResults:    4,
	}
}

// Validate 对齐 __post_init__ 的校验与错误文案
func (c ThresholdConfig) Validate() error {
	if c.MinThreshold < 0 || c.MinThreshold > 1 {
		return fmt.Errorf("min_threshold必须在[0, 1]之间: %v", c.MinThreshold)
	}
	if c.MaxThreshold < 0 || c.MaxThreshold > 1 {
		return fmt.Errorf("max_threshold必须在[0, 1]之间: %v", c.MaxThreshold)
	}
	if c.MinThreshold >= c.MaxThreshold {
		return fmt.Errorf("min_threshold必须小于max_threshold")
	}
	if c.Percentile < 0 || c.Percentile > 100 {
		return fmt.Errorf("percentile必须在[0, 100]之间: %v", c.Percentile)
	}
	if c.StdMultiplier <= 0 {
		return fmt.Errorf("std_multiplier必须大于0: %v", c.StdMultiplier)
	}
	if c.MinResults < 0 {
		return fmt.Errorf("min_results必须大于等于0: %v", c.MinResults)
	}
	return nil
}

// DynamicThresholdFilter 动态阈值过滤器
type DynamicThresholdFilter struct {
	config ThresholdConfig

	totalFiltered    int
	totalProcessed   int
	thresholdCount   int
	thresholdSum     float64
	minThresholdUsed *float64
	maxThresholdUsed *float64
}

// NewDynamicThresholdFilter 构造过滤器并校验配置
func NewDynamicThresholdFilter(config ThresholdConfig) (*DynamicThresholdFilter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &DynamicThresholdFilter{config: config}, nil
}

// Filter 过滤检索结果，不返回阈值
func (f *DynamicThresholdFilter) Filter(results []RetrievalResult) []RetrievalResult {
	filtered, _ := f.FilterWithThreshold(results)
	return filtered
}

// FilterWithThreshold 过滤检索结果并返回实际使用的阈值
func (f *DynamicThresholdFilter) FilterWithThreshold(results []RetrievalResult) ([]RetrievalResult, float64) {
	if len(results) == 0 {
		return []RetrievalResult{}, 0.0
	}
	f.totalProcessed += len(results)

	scores := make([]float64, 0, len(results))
	for _, result := range results {
		scores = append(scores, result.Score)
	}
	threshold := f.computeThreshold(scores)

	filtered := make([]RetrievalResult, 0, len(results))
	for _, result := range results {
		if result.Score >= threshold {
			filtered = append(filtered, result)
		}
	}

	if len(filtered) < f.config.MinResults {
		sorted := append([]RetrievalResult(nil), results...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
		limit := f.config.MinResults
		if limit > len(sorted) {
			limit = len(sorted)
		}
		filtered = sorted[:limit]
		if len(filtered) > 0 {
			threshold = filtered[len(filtered)-1].Score
		} else {
			threshold = 0.0
		}
	}

	f.thresholdCount++
	f.thresholdSum += threshold
	if f.minThresholdUsed == nil || threshold < *f.minThresholdUsed {
		value := threshold
		f.minThresholdUsed = &value
	}
	if f.maxThresholdUsed == nil || threshold > *f.maxThresholdUsed {
		value := threshold
		f.maxThresholdUsed = &value
	}
	f.totalFiltered += len(results) - len(filtered)
	return filtered, threshold
}

// computeThreshold 按配置方法计算阈值并裁剪
func (f *DynamicThresholdFilter) computeThreshold(scores []float64) float64 {
	var threshold float64
	switch f.config.Method {
	case ThresholdPercentile:
		threshold = f.percentileThreshold(scores)
	case ThresholdStdDev:
		threshold = f.stdDevThreshold(scores)
	case ThresholdGapDetection:
		threshold = f.gapDetectionThreshold(scores)
	default:
		percentileThreshold := f.percentileThresholdWith(scores, f.config.Percentile)
		stdDevThreshold := f.stdDevThreshold(scores)
		gapThreshold := f.gapDetectionThreshold(scores)
		gapCapThreshold := f.percentileThresholdWith(scores, 40.0)
		adaptiveGapThreshold := math.Min(gapThreshold, gapCapThreshold)
		threshold = medianFloat([]float64{percentileThreshold, stdDevThreshold, adaptiveGapThreshold})
	}
	return math.Max(f.config.MinThreshold, math.Min(f.config.MaxThreshold, threshold))
}

// percentileThreshold 对齐 _percentile_threshold
func (f *DynamicThresholdFilter) percentileThreshold(scores []float64) float64 {
	return f.percentileThresholdWith(scores, f.config.Percentile)
}

// percentileThresholdWith 对齐 numpy.percentile 默认 linear 插值
func (f *DynamicThresholdFilter) percentileThresholdWith(scores []float64, percentile float64) float64 {
	return percentileLinear(scores, percentile)
}

// stdDevThreshold 对齐 _std_dev_threshold，np.std 使用总体标准差
func (f *DynamicThresholdFilter) stdDevThreshold(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	sum := 0.0
	for _, score := range scores {
		sum += score
	}
	mean := sum / float64(len(scores))
	squared := 0.0
	for _, score := range scores {
		delta := score - mean
		squared += delta * delta
	}
	std := math.Sqrt(squared / float64(len(scores)))
	return mean - f.config.StdMultiplier*std
}

// gapDetectionThreshold 对齐 _gap_detection_threshold
func (f *DynamicThresholdFilter) gapDetectionThreshold(scores []float64) float64 {
	sortedScores := append([]float64(nil), scores...)
	sort.Sort(sort.Reverse(sort.Float64Slice(sortedScores)))
	if len(sortedScores) == 0 {
		return 0.0
	}
	if len(sortedScores) < 2 {
		return sortedScores[0]
	}
	maxGapIndex := 0
	maxDrop := sortedScores[0] - sortedScores[1]
	for index := 1; index < len(sortedScores)-1; index++ {
		drop := sortedScores[index] - sortedScores[index+1]
		if drop > maxDrop {
			maxDrop = drop
			maxGapIndex = index
		}
	}
	return (sortedScores[maxGapIndex] + sortedScores[maxGapIndex+1]) / 2.0
}

// FilterByConfidence 对齐 filter_by_confidence
func (f *DynamicThresholdFilter) FilterByConfidence(results []RetrievalResult, minConfidence float64) []RetrievalResult {
	filtered := make([]RetrievalResult, 0, len(results))
	for _, result := range results {
		if result.ResultType == "relation" {
			confidence := 1.0
			if value, ok := result.Metadata["confidence"]; ok {
				if number, ok := numericValue(value); ok {
					confidence = number
				}
			}
			if confidence >= minConfidence {
				filtered = append(filtered, result)
			}
			continue
		}
		if result.Score >= minConfidence {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

// FilterByDiversity 对齐 filter_by_diversity：按 hash 前 8 位贪心去重
func (f *DynamicThresholdFilter) FilterByDiversity(results []RetrievalResult, similarityThreshold float64, topK int) []RetrievalResult {
	if len(results) == 0 {
		return []RetrievalResult{}
	}
	sorted := append([]RetrievalResult(nil), results...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })

	selected := make([]RetrievalResult, 0, topK)
	selectedHashes := make([]string, 0, topK)
	for _, result := range sorted {
		if len(selected) >= topK {
			break
		}
		prefix := result.HashValue
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		duplicate := false
		for _, selectedHash := range selectedHashes {
			selectedPrefix := selectedHash
			if len(selectedPrefix) > 8 {
				selectedPrefix = selectedPrefix[:8]
			}
			if prefix == selectedPrefix {
				duplicate = true
				break
			}
		}
		if !duplicate {
			selected = append(selected, result)
			selectedHashes = append(selectedHashes, result.HashValue)
		}
	}
	return selected
}

// GetStatistics 对齐 get_statistics 的返回结构
func (f *DynamicThresholdFilter) GetStatistics() map[string]any {
	filterRate := 0.0
	if f.totalProcessed > 0 {
		filterRate = float64(f.totalFiltered) / float64(f.totalProcessed)
	}
	avgThreshold := 0.0
	if f.thresholdCount > 0 {
		avgThreshold = f.thresholdSum / float64(f.thresholdCount)
	}
	statistics := map[string]any{
		"config": map[string]any{
			"method":         string(f.config.Method),
			"min_threshold":  f.config.MinThreshold,
			"max_threshold":  f.config.MaxThreshold,
			"percentile":     f.config.Percentile,
			"std_multiplier": f.config.StdMultiplier,
			"min_results":    f.config.MinResults,
		},
		"statistics": map[string]any{
			"total_processed": f.totalProcessed,
			"total_filtered":  f.totalFiltered,
			"filter_rate":     filterRate,
			"avg_threshold":   avgThreshold,
			"threshold_count": f.thresholdCount,
		},
	}
	if f.thresholdCount > 0 {
		inner := statistics["statistics"].(map[string]any)
		minUsed, maxUsed := 0.0, 0.0
		if f.minThresholdUsed != nil {
			minUsed = *f.minThresholdUsed
		}
		if f.maxThresholdUsed != nil {
			maxUsed = *f.maxThresholdUsed
		}
		inner["min_threshold_used"] = minUsed
		inner["max_threshold_used"] = maxUsed
	}
	return statistics
}

// ResetStatistics 重置统计信息
func (f *DynamicThresholdFilter) ResetStatistics() {
	f.totalFiltered = 0
	f.totalProcessed = 0
	f.thresholdCount = 0
	f.thresholdSum = 0.0
	f.minThresholdUsed = nil
	f.maxThresholdUsed = nil
}

// String 对齐 __repr__ 的主要信息
func (f *DynamicThresholdFilter) String() string {
	return fmt.Sprintf("DynamicThresholdFilter(method=%s, min_threshold=%v, filtered=%d/%d)",
		f.config.Method, f.config.MinThreshold, f.totalFiltered, f.totalProcessed)
}

// percentileLinear 复刻 numpy.percentile 的 linear 插值
func percentileLinear(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0.0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := (float64(len(sorted)) - 1.0) * percentile / 100.0
	lowerIndex := int(math.Floor(position))
	upperIndex := int(math.Ceil(position))
	if lowerIndex < 0 {
		lowerIndex = 0
	}
	if upperIndex >= len(sorted) {
		upperIndex = len(sorted) - 1
	}
	lower := sorted[lowerIndex]
	upper := sorted[upperIndex]
	fraction := position - float64(lowerIndex)
	return lower + (upper-lower)*fraction
}

// numericValue 把 JSON 反序列化产生的多种数值类型归一为 float64
func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	}
	return 0, false
}
