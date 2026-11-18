package system_probe

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/bytedance/sonic"
)

func TestSanitizeSnapshotDropsNonFinite(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	negativeInf := math.Inf(-1)
	snapshot := &Snapshot{
		CPU: &CPUSection{Usage: CPUUsage{
			UserPercent:  &nan,
			TotalPercent: &inf,
		}},
		Memory: &MemorySection{Usage: MemoryUsage{UsedPercent: &negativeInf}},
		Cgroup: &CgroupSection{CPU: &CgroupCPU{CPUs: &inf}},
		Runtime: &RuntimeSection{
			CPUClasses: RuntimeCPUClasses{TotalCPUSeconds: nan},
			GC:         RuntimeGC{PauseP50NS: &nan},
		},
		History: HistorySection{Samples: []HistorySample{{CPUTotalPercent: &inf}}},
	}
	sanitizeSnapshot(snapshot)
	if snapshot.CPU.Usage.UserPercent != nil || snapshot.CPU.Usage.TotalPercent != nil {
		t.Fatal("CPU 中的非有限值应置 nil")
	}
	if snapshot.Memory.Usage.UsedPercent != nil || snapshot.Cgroup.CPU.CPUs != nil {
		t.Fatal("内存/cgroup 中的非有限值应置 nil")
	}
	if snapshot.Runtime.GC.PauseP50NS != nil {
		t.Fatal("运行时浮点指针中的非有限值应置 nil")
	}
	if snapshot.Runtime.CPUClasses.TotalCPUSeconds != 0 {
		t.Fatal("普通浮点字段中的非有限值应置 0")
	}
	if snapshot.History.Samples[0].CPUTotalPercent != nil {
		t.Fatal("history 中的非有限值应置 nil")
	}
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatalf("encoding/json 编码失败: %v", err)
	}
	if _, err := sonic.Marshal(snapshot); err != nil {
		t.Fatalf("sonic 编码失败: %v", err)
	}
}

func TestRuntimeSentinelMapping(t *testing.T) {
	if runtimeGOGCPercent(math.MaxUint64) != nil {
		t.Fatal("GOGC 关闭哨兵应映射为 nil")
	}
	if runtimeGOMEMLimit(math.MaxInt64) != nil {
		t.Fatal("内存上限未设置哨兵应映射为 nil")
	}
	value := runtimeGOGCPercent(100)
	if value == nil || *value != 100 {
		t.Fatalf("普通 GOGC 值应保留: %v", value)
	}
	limit := runtimeGOMEMLimit(1 << 30)
	if limit == nil || *limit != 1<<30 {
		t.Fatalf("普通内存上限应保留: %v", limit)
	}
}
