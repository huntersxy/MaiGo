package system_probe

import (
	"math"
	"runtime/metrics"
	"testing"
)

func makeHistogram(counts []uint64, buckets []float64) *metrics.Float64Histogram {
	return &metrics.Float64Histogram{Counts: counts, Buckets: buckets}
}

func TestHistogramQuantileZeroSamples(t *testing.T) {
	hist := makeHistogram([]uint64{0, 0, 0}, []float64{-1, 0, 1, 2})
	if value := histogramQuantile(hist, nil, 0.5); value != nil {
		t.Fatalf("零样本应返回 nil: %v", *value)
	}
	if value := histogramMax(hist, nil); value != nil {
		t.Fatalf("零样本最大值应返回 nil: %v", *value)
	}
}

func TestHistogramQuantileInfFallback(t *testing.T) {
	hist := makeHistogram([]uint64{0, 9, 0, 1}, []float64{-1, 1, 2, math.Inf(1)})
	value := histogramQuantile(hist, nil, 1)
	if value == nil || *value != 2 {
		t.Fatalf("末桶 +Inf 应回退到有限下界 2，实际 %v", value)
	}
	if math.IsInf(*value, 0) || math.IsNaN(*value) {
		t.Fatal("返回值不得为 Inf/NaN")
	}
}

func TestHistogramQuantileAllInf(t *testing.T) {
	hist := makeHistogram([]uint64{5}, []float64{math.Inf(1), math.Inf(1)})
	if value := histogramQuantile(hist, nil, 0.5); value != nil {
		t.Fatalf("全 Inf 桶应返回 nil: %v", *value)
	}
}

func TestHistogramQuantileValues(t *testing.T) {
	hist := makeHistogram(
		[]uint64{5, 3, 1, 1},
		[]float64{math.Inf(-1), 10, 20, 30, math.Inf(1)},
	)
	cases := []struct {
		q    float64
		want float64
	}{
		{0.50, 10},
		{0.95, 30},
		{0.99, 30},
		{1, 30},
	}
	for _, item := range cases {
		value := histogramQuantile(hist, nil, item.q)
		if value == nil {
			t.Fatalf("q=%v 不应为 nil", item.q)
		}
		if *value != item.want {
			t.Fatalf("q=%v 期望 %v 实际 %v", item.q, item.want, *value)
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			t.Fatalf("q=%v 返回非有限值", item.q)
		}
	}
}

func TestHistogramQuantileDiffEmpty(t *testing.T) {
	hist := makeHistogram([]uint64{1, 2, 3}, []float64{0, 1, 2, 3})
	prev := []uint64{1, 2, 3}
	if value := histogramQuantile(hist, prev, 0.5); value != nil {
		t.Fatalf("差分后无样本应返回 nil: %v", *value)
	}
}

func TestHistogramQuantileCounterRegression(t *testing.T) {
	hist := makeHistogram([]uint64{4, 1}, []float64{0, 10, 20})
	if value := histogramQuantile(hist, []uint64{10, 10}, 0.5); value != nil {
		t.Fatalf("计数回退应返回 nil: %v", *value)
	}
}

func TestHistogramQuantileDiffWindow(t *testing.T) {
	hist := makeHistogram([]uint64{2, 8}, []float64{0, 10, 20})
	prev := []uint64{2, 2}
	value := histogramQuantile(hist, prev, 0.5)
	if value == nil || *value != 20 {
		t.Fatalf("差分窗口分位不符: %v", value)
	}
	counts, total, ok := histogramCounts(hist, prev)
	if !ok || total != 6 || len(counts) != 2 || counts[0] != 0 || counts[1] != 6 {
		t.Fatalf("差分计数不符: %+v total=%d", counts, total)
	}
}
