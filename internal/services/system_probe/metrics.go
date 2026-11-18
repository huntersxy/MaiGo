package system_probe

import (
	"math"
	goruntime "runtime"
	"runtime/metrics"
)

// collectRuntimeSection 读取 runtime.MemStats 与固定指标集合，分位由 probeState 差分。
func collectRuntimeSection(state *probeState) *RuntimeSection {
	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)

	samples := make([]metrics.Sample, len(runtimeMetricNames))
	for i, name := range runtimeMetricNames {
		samples[i].Name = name
	}
	metrics.Read(samples)
	values := make(map[string]metrics.Sample, len(samples))
	for _, sample := range samples {
		values[sample.Name] = sample
	}

	section := &RuntimeSection{
		Memstats: RuntimeMemstats{
			AllocBytes:        mem.Alloc,
			TotalAllocBytes:   mem.TotalAlloc,
			SysBytes:          mem.Sys,
			HeapAllocBytes:    mem.HeapAlloc,
			HeapSysBytes:      mem.HeapSys,
			HeapIdleBytes:     mem.HeapIdle,
			HeapInuseBytes:    mem.HeapInuse,
			HeapReleasedBytes: mem.HeapReleased,
			HeapObjects:       mem.HeapObjects,
			StackInuseBytes:   mem.StackInuse,
			StackSysBytes:     mem.StackSys,
			MSpanInuseBytes:   mem.MSpanInuse,
			MSpanSysBytes:     mem.MSpanSys,
			MCacheInuseBytes:  mem.MCacheInuse,
			MCacheSysBytes:    mem.MCacheSys,
			BuckHashSysBytes:  mem.BuckHashSys,
			GCSysBytes:        mem.GCSys,
			OtherSysBytes:     mem.OtherSys,
			NextGCBytes:       mem.NextGC,
			NumGC:             mem.NumGC,
			NumForcedGC:       mem.NumForcedGC,
			PauseTotalNS:      mem.PauseTotalNs,
			Mallocs:           mem.Mallocs,
			Frees:             mem.Frees,
			Lookups:           mem.Lookups,
		},
		Goroutines:         goruntime.NumGoroutine(),
		GOMAXPROCS:         goruntime.GOMAXPROCS(0),
		ThreadsTotal:       sampleUint(values["/sched/threads/total:threads"]),
		GoroutinesRunning:  sampleUint(values["/sched/goroutines/running:goroutines"]),
		GoroutinesRunnable: sampleUint(values["/sched/goroutines/runnable:goroutines"]),
		GoroutinesWaiting:  sampleUint(values["/sched/goroutines/waiting:goroutines"]),
		GoroutinesNotInGo:  sampleUint(values["/sched/goroutines/not-in-go:goroutines"]),
		MemoryClasses: RuntimeMemoryClasses{
			HeapObjectsBytes:   sampleUint(values["/memory/classes/heap/objects:bytes"]),
			HeapFreeBytes:      sampleUint(values["/memory/classes/heap/free:bytes"]),
			HeapUnusedBytes:    sampleUint(values["/memory/classes/heap/unused:bytes"]),
			HeapReleasedBytes:  sampleUint(values["/memory/classes/heap/released:bytes"]),
			HeapStacksBytes:    sampleUint(values["/memory/classes/heap/stacks:bytes"]),
			TotalBytes:         sampleUint(values["/memory/classes/total:bytes"]),
			OSStacksBytes:      sampleUint(values["/memory/classes/os-stacks:bytes"]),
			MSpanInuseBytes:    sampleUint(values["/memory/classes/metadata/mspan/inuse:bytes"]),
			MCacheInuseBytes:   sampleUint(values["/memory/classes/metadata/mcache/inuse:bytes"]),
			MetadataOtherBytes: sampleUint(values["/memory/classes/metadata/other:bytes"]),
		},
	}
	if mem.LastGC > 0 {
		seconds := float64(mem.LastGC) / 1e9
		section.Memstats.LastGCUnix = &seconds
	}
	gcFraction := mem.GCCPUFraction
	section.Memstats.GCCPUFraction = &gcFraction

	section.GC.AutomaticCycles = sampleUint(values["/gc/cycles/automatic:gc-cycles"])
	section.GC.ForcedCycles = sampleUint(values["/gc/cycles/forced:gc-cycles"])
	section.GC.HeapGoalBytes = sampleUint(values["/gc/heap/goal:bytes"])
	section.GC.HeapLiveBytes = sampleUint(values["/gc/heap/live:bytes"])
	if value, ok := sampleUintOK(values["/gc/gogc:percent"]); ok {
		section.GC.GOGCPercent = runtimeGOGCPercent(value)
	}
	if value, ok := sampleUintOK(values["/gc/gomemlimit:bytes"]); ok {
		section.GC.GOMEMLimitBytes = runtimeGOMEMLimit(value)
	}

	pauseHist, _ := sampleHistogram(values["/sched/pauses/total/gc:seconds"])
	if state != nil {
		section.GC.PauseP50NS, section.GC.PauseP95NS, section.GC.PauseP99NS, section.GC.PauseMaxNS, section.GC.PauseWindow =
			state.observePause(pauseHist)
	}
	latencyHist, _ := sampleHistogram(values["/sched/latencies:seconds"])
	if state != nil {
		section.Sched.LatencyP50NS, section.Sched.LatencyP95NS, section.Sched.LatencyP99NS, section.Sched.LatencyMaxNS, section.Sched.LatencyWindow =
			state.observeLatency(latencyHist)
	}

	section.CPUClasses.TotalCPUSeconds = sampleFloat(values["/cpu/classes/total:cpu-seconds"])
	section.CPUClasses.UserCPUSeconds = sampleFloat(values["/cpu/classes/user:cpu-seconds"])
	section.CPUClasses.GCCPUSeconds = sampleFloat(values["/cpu/classes/gc/total:cpu-seconds"])
	section.CPUClasses.IdleCPUSeconds = sampleFloat(values["/cpu/classes/idle:cpu-seconds"])
	section.CPUClasses.MutexWaitNS = int64(math.Round(sampleFloat(values["/sync/mutex/wait/total:seconds"]) * 1e9))
	return section
}

var runtimeMetricNames = []string{
	"/gc/gogc:percent",
	"/gc/gomemlimit:bytes",
	"/gc/heap/goal:bytes",
	"/gc/heap/live:bytes",
	"/gc/cycles/automatic:gc-cycles",
	"/gc/cycles/forced:gc-cycles",
	"/sched/pauses/total/gc:seconds",
	"/sched/latencies:seconds",
	"/sched/threads/total:threads",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/free:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/released:bytes",
	"/memory/classes/heap/stacks:bytes",
	"/memory/classes/total:bytes",
	"/memory/classes/os-stacks:bytes",
	"/memory/classes/metadata/mspan/inuse:bytes",
	"/memory/classes/metadata/mcache/inuse:bytes",
	"/memory/classes/metadata/other:bytes",
	"/sched/goroutines:goroutines",
	"/sched/gomaxprocs:threads",
	"/sched/goroutines/running:goroutines",
	"/sched/goroutines/runnable:goroutines",
	"/sched/goroutines/waiting:goroutines",
	"/sched/goroutines/not-in-go:goroutines",
	"/cpu/classes/total:cpu-seconds",
	"/cpu/classes/user:cpu-seconds",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/idle:cpu-seconds",
	"/sync/mutex/wait/total:seconds",
}

func sampleUintOK(sample metrics.Sample) (uint64, bool) {
	if sample.Value.Kind() != metrics.KindUint64 {
		return 0, false
	}
	return sample.Value.Uint64(), true
}

func sampleUint(sample metrics.Sample) uint64 {
	value, _ := sampleUintOK(sample)
	return value
}

func sampleFloat(sample metrics.Sample) float64 {
	if sample.Value.Kind() != metrics.KindFloat64 {
		return 0
	}
	value := sample.Value.Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func sampleHistogram(sample metrics.Sample) (*metrics.Float64Histogram, bool) {
	if sample.Value.Kind() != metrics.KindFloat64Histogram {
		return nil, false
	}
	return sample.Value.Float64Histogram(), true
}

// runtimeGOGCPercent 把关闭 GC 的哨兵值映射为 null。
func runtimeGOGCPercent(value uint64) *uint64 {
	if value == math.MaxUint64 {
		return nil
	}
	return &value
}

// runtimeGOMEMLimit 把未设置内存上限的哨兵值映射为 null。
func runtimeGOMEMLimit(value uint64) *uint64 {
	if value == math.MaxInt64 {
		return nil
	}
	return &value
}
