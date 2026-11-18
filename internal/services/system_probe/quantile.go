package system_probe

import (
	"math"

	"runtime/metrics"
)

// histogramCounts 返回与上一采样差分后的桶计数；prev 为空或长度不一致时使用生命周期计数，
// 计数回退时 ok=false（窗口无效，分位值一律为 null）。
func histogramCounts(hist *metrics.Float64Histogram, prev []uint64) ([]uint64, uint64, bool) {
	if hist == nil || len(hist.Counts) == 0 {
		return nil, 0, false
	}
	if len(prev) != len(hist.Counts) {
		counts := append([]uint64(nil), hist.Counts...)
		return counts, sumCounts(counts), true
	}
	counts := make([]uint64, len(hist.Counts))
	for i, current := range hist.Counts {
		if current < prev[i] {
			return nil, 0, false
		}
		counts[i] = current - prev[i]
	}
	return counts, sumCounts(counts), true
}

func sumCounts(counts []uint64) uint64 {
	var total uint64
	for _, count := range counts {
		total += count
	}
	return total
}

// quantileFromCounts 在已差分的计数上取分位桶上界；上界为 +Inf 时回退到有限下界。
func quantileFromCounts(hist *metrics.Float64Histogram, counts []uint64, total uint64, q float64) *float64 {
	if hist == nil || total == 0 || len(counts) == 0 {
		return nil
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	target := q * float64(total)
	if q >= 1 {
		target = float64(total)
	}
	var cumulative uint64
	bucket := -1
	for i, count := range counts {
		cumulative += count
		if count > 0 && float64(cumulative) >= target {
			bucket = i
			break
		}
	}
	if bucket < 0 {
		for i := len(counts) - 1; i >= 0; i-- {
			if counts[i] > 0 {
				bucket = i
				break
			}
		}
	}
	if bucket < 0 {
		return nil
	}
	if upper, ok := finiteBucketBound(hist, bucket+1); ok {
		return upper
	}
	if lower, ok := finiteBucketBound(hist, bucket); ok {
		return lower
	}
	for i := bucket - 1; i >= 0; i-- {
		if upper, ok := finiteBucketBound(hist, i+1); ok {
			return upper
		}
	}
	return nil
}

func finiteBucketBound(hist *metrics.Float64Histogram, index int) (*float64, bool) {
	if index < 0 || index >= len(hist.Buckets) {
		return nil, false
	}
	value := hist.Buckets[index]
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, false
	}
	return &value, true
}

// histogramQuantile 是带上一采样差分的分位入口；窗口无效或无样本时返回 nil。
func histogramQuantile(hist *metrics.Float64Histogram, prevCounts []uint64, q float64) *float64 {
	counts, total, ok := histogramCounts(hist, prevCounts)
	if !ok || total == 0 {
		return nil
	}
	return quantileFromCounts(hist, counts, total, q)
}

// histogramMax 返回窗口内最大值所在桶的有限上界。
func histogramMax(hist *metrics.Float64Histogram, prevCounts []uint64) *float64 {
	return histogramQuantile(hist, prevCounts, 1)
}
