package system_probe

import (
	"math"
	"runtime/metrics"
	"sync"
	"time"
)

// cpuSample 是 /proc/stat 聚合行的 tick 快照。
type cpuSample struct {
	User      uint64
	Nice      uint64
	System    uint64
	Idle      uint64
	IOWait    uint64
	IRQ       uint64
	SoftIRQ   uint64
	Steal     uint64
	Guest     uint64
	GuestNice uint64
}

func (c cpuSample) total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// probeState 是 Probe 的唯一可变状态；所有方法都要求通过锁访问。
type probeState struct {
	mu           sync.Mutex
	cpuPrev      *cpuSample
	cpuPrevAt    time.Time
	lastCPUUsage CPUUsage
	pausePrev    []uint64
	latencyPrev  []uint64
	history      []HistorySample
	historyLimit int
}

func newProbeState(historyLimit int) *probeState {
	if historyLimit <= 0 || historyLimit > 60 {
		historyLimit = 60
	}
	return &probeState{
		history:      make([]HistorySample, 0, historyLimit),
		historyLimit: historyLimit,
	}
}

func copyCounts(counts []uint64) []uint64 {
	if len(counts) == 0 {
		return nil
	}
	return append([]uint64(nil), counts...)
}

// observeCPU 用相邻采样差分计算占比；首采样、窗口 <1s 或计数回退返回 nil。
func (s *probeState) observeCPU(current cpuSample, now time.Time) *CPUUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cpuPrev == nil {
		prev := current
		s.cpuPrev = &prev
		s.cpuPrevAt = now
		s.lastCPUUsage = CPUUsage{}
		return nil
	}
	window := now.Sub(s.cpuPrevAt)
	if window < time.Second {
		return nil
	}
	prev := *s.cpuPrev
	if current.User < prev.User || current.Nice < prev.Nice || current.System < prev.System ||
		current.Idle < prev.Idle || current.IOWait < prev.IOWait || current.IRQ < prev.IRQ ||
		current.SoftIRQ < prev.SoftIRQ || current.Steal < prev.Steal {
		snapshot := current
		s.cpuPrev = &snapshot
		s.cpuPrevAt = now
		s.lastCPUUsage = CPUUsage{}
		return nil
	}
	total := current.total() - prev.total()
	if total == 0 {
		return nil
	}
	percent := func(delta uint64) *float64 {
		value := math.Max(0, math.Min(100, float64(delta)/float64(total)*100))
		return &value
	}
	user := percent((current.User + current.Nice) - (prev.User + prev.Nice))
	system := percent((current.System + current.IRQ + current.SoftIRQ) - (prev.System + prev.IRQ + prev.SoftIRQ))
	iowait := percent(current.IOWait - prev.IOWait)
	steal := percent(current.Steal - prev.Steal)
	idle := percent(current.Idle - prev.Idle)
	totalPercent := percent(total - (current.Idle - prev.Idle))
	windowSec := window.Seconds()
	usage := CPUUsage{
		UserPercent:   user,
		SystemPercent: system,
		IOWaitPercent: iowait,
		StealPercent:  steal,
		IdlePercent:   idle,
		TotalPercent:  totalPercent,
		WindowSec:     &windowSec,
	}
	s.cpuPrev = &current
	s.cpuPrevAt = now
	s.lastCPUUsage = usage
	return &usage
}

// lastCPUUsageSnapshot 返回最近一次有效占比的副本。
func (s *probeState) lastCPUUsageSnapshot() CPUUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyCPUUsage(s.lastCPUUsage)
}

func copyCPUUsage(usage CPUUsage) CPUUsage {
	return CPUUsage{
		UserPercent:   copyFloat(usage.UserPercent),
		SystemPercent: copyFloat(usage.SystemPercent),
		IOWaitPercent: copyFloat(usage.IOWaitPercent),
		StealPercent:  copyFloat(usage.StealPercent),
		IdlePercent:   copyFloat(usage.IdlePercent),
		TotalPercent:  copyFloat(usage.TotalPercent),
		WindowSec:     copyFloat(usage.WindowSec),
	}
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// observePause 返回 GC 暂停分位（纳秒）与本窗口样本数。
func (s *probeState) observePause(hist *metrics.Float64Histogram) (p50, p95, p99, max *float64, window uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts, total, ok := histogramCounts(hist, s.pausePrev)
	if !ok {
		if hist != nil {
			s.pausePrev = copyCounts(hist.Counts)
		}
		return nil, nil, nil, nil, 0
	}
	if hist != nil {
		s.pausePrev = copyCounts(hist.Counts)
	}
	p50 = scaleQuantile(quantileFromCounts(hist, counts, total, 0.50), 1e9)
	p95 = scaleQuantile(quantileFromCounts(hist, counts, total, 0.95), 1e9)
	p99 = scaleQuantile(quantileFromCounts(hist, counts, total, 0.99), 1e9)
	max = scaleQuantile(quantileFromCounts(hist, counts, total, 1), 1e9)
	return p50, p95, p99, max, total
}

// observeLatency 返回调度延迟分位（纳秒）与本窗口样本数。
func (s *probeState) observeLatency(hist *metrics.Float64Histogram) (p50, p95, p99, max *float64, window uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts, total, ok := histogramCounts(hist, s.latencyPrev)
	if !ok {
		if hist != nil {
			s.latencyPrev = copyCounts(hist.Counts)
		}
		return nil, nil, nil, nil, 0
	}
	if hist != nil {
		s.latencyPrev = copyCounts(hist.Counts)
	}
	p50 = scaleQuantile(quantileFromCounts(hist, counts, total, 0.50), 1e9)
	p95 = scaleQuantile(quantileFromCounts(hist, counts, total, 0.95), 1e9)
	p99 = scaleQuantile(quantileFromCounts(hist, counts, total, 0.99), 1e9)
	max = scaleQuantile(quantileFromCounts(hist, counts, total, 1), 1e9)
	return p50, p95, p99, max, total
}

func scaleQuantile(value *float64, factor float64) *float64 {
	if value == nil {
		return nil
	}
	scaled := *value * factor
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) {
		return nil
	}
	return &scaled
}

// appendHistory 在锁内追加一条摘要，容量固定并丢弃最旧样本。
func (s *probeState) appendHistory(sample HistorySample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.historyLimit <= 0 {
		return
	}
	if len(s.history) < s.historyLimit {
		s.history = append(s.history, sample)
		return
	}
	copy(s.history, s.history[1:])
	s.history[len(s.history)-1] = sample
}

// snapshotHistory 返回 history 的副本。
func (s *probeState) snapshotHistory() HistorySection {
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := make([]HistorySample, len(s.history))
	copy(samples, s.history)
	return HistorySection{Capacity: s.historyLimit, Samples: samples}
}

// historyLen 供测试断言环形缓冲上限。
func (s *probeState) historyLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.history)
}
