package system_probe

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
)

type fakeCollector struct {
	data platformData
}

func (f fakeCollector) collect(context.Context, *Probe, []Subprocess) platformData {
	return f.data
}

func fakePlatformData() platformData {
	ticks := cpuSample{User: 100, Nice: 10, System: 50, Idle: 800, IOWait: 5, IRQ: 2, SoftIRQ: 2, Steal: 1}
	available := uint64(6 << 30)
	rss := uint64(64 << 20)
	return platformData{
		CPU: &CPUSection{
			LogicalCores: 2,
			Stat:         CPUStat{UserTicks: ticks.User, IdleTicks: ticks.Idle},
			PerCore:      []CPUCore{{Core: 0, UserTicks: 60}, {Core: 1, UserTicks: 40}},
			Load:         CPULoad{Load1: floatPtr(0.5)},
		},
		Memory: &MemorySection{
			System: MemorySystem{MemTotalBytes: 16 << 30, MemAvailableBytes: int64(available)},
			Process: ProcessMemory{
				RSSBytes: int64(rss), Threads: 12,
			},
		},
		Disk: &DiskSection{
			Mounts:  []DiskMount{{Label: "root", Path: "/", TotalBytes: 100, FreeBytes: 40, UsedBytes: 60}},
			Devices: []DiskDevice{{Name: "sda", ReadBytes: 1024}},
		},
		Network: &NetworkSection{
			Interfaces: []NetworkInterface{{Name: "eth0", RXBytes: 10, TXBytes: 20}},
			TCPStates:  TCPStates{Established: 3, Listen: 1},
		},
		Processes: &ProcessSection{
			Limit:   20,
			Scanned: 1,
			List: []ProcessInfo{{
				PID: 123, Name: "worker", Cmdline: "worker --run", RSSBytes: int64(rss), Threads: 12,
			}},
		},
		CPUTicks:        &ticks,
		BTime:           floatPtr(1700000000),
		MemoryAvailable: &available,
		ProcessRSS:      &rss,
	}
}

func floatPtr(value float64) *float64 {
	return &value
}

func TestProbeSnapshotWithFakeCollector(t *testing.T) {
	current := time.Unix(1700000000, 0)
	data := fakePlatformData()
	probe := New(Options{
		InstanceDir:  "C:/instance",
		Version:      "test-version",
		StartedAt:    current.Add(-time.Hour),
		HistoryLimit: 3,
		Now:          func() time.Time { return current },
		collector:    fakeCollector{data: data},
	})
	snapshot := probe.Snapshot(context.Background())
	if snapshot == nil {
		t.Fatal("Snapshot 不应为 nil")
	}
	if snapshot.Meta.Schema != 1 || snapshot.Meta.Version != "test-version" {
		t.Fatalf("meta 字段不符: %+v", snapshot.Meta)
	}
	if snapshot.CPU == nil || snapshot.Memory == nil || snapshot.Disk == nil || snapshot.Network == nil {
		t.Fatal("假平台 section 不应缺失")
	}
	if snapshot.CPU.Usage.TotalPercent != nil || snapshot.Meta.WindowSec != nil {
		t.Fatal("首采样 CPU 占比与窗口应为 nil")
	}
	if snapshot.CPU.LogicalCores != 2 || snapshot.CPU.GOMAXPROCS <= 0 {
		t.Fatalf("CPU 核心字段不符: %+v", snapshot.CPU)
	}
	if snapshot.Runtime == nil || snapshot.App == nil {
		t.Fatal("runtime/app 应始终有值")
	}
	if snapshot.App.Counts != (Counts{}) || snapshot.App.Scheduler.Available {
		t.Fatalf("未接线的 app provider 应为零值: %+v", snapshot.App)
	}
	if snapshot.History.Capacity != 3 || len(snapshot.History.Samples) != 1 {
		t.Fatalf("history 首条不符: %+v", snapshot.History)
	}

	current = current.Add(2 * time.Second)
	nextTicks := cpuSample{User: 200, Nice: 20, System: 100, Idle: 1600, IOWait: 10, IRQ: 4, SoftIRQ: 4, Steal: 2}
	data.CPUTicks = &nextTicks
	probe.collector = fakeCollector{data: data}
	second := probe.Snapshot(context.Background())
	if second.CPU.Usage.TotalPercent == nil || second.Meta.WindowSec == nil {
		t.Fatal("第二次采样应有 CPU 占比与窗口")
	}
	if *second.CPU.Usage.TotalPercent < 0 || *second.CPU.Usage.TotalPercent > 100 {
		t.Fatalf("CPU 占比越界: %v", *second.CPU.Usage.TotalPercent)
	}

	for i := 0; i < 5; i++ {
		current = current.Add(time.Second)
		probe.Snapshot(context.Background())
	}
	if got := probe.state.historyLen(); got != 3 {
		t.Fatalf("history 环形上限应为 3，实际 %d", got)
	}
	history := probe.state.snapshotHistory()
	if len(history.Samples) != 3 || history.Capacity != 3 {
		t.Fatalf("history 快照不符: %+v", history)
	}

	if _, err := json.Marshal(second); err != nil {
		t.Fatalf("encoding/json 编码失败: %v", err)
	}
	if _, err := sonic.Marshal(second); err != nil {
		t.Fatalf("sonic 编码失败: %v", err)
	}

	text := string(RenderText(second))
	if !strings.Contains(text, "MaiGo system resource probe") {
		t.Fatal("文本缺少标题")
	}
	order := []string{"[meta]", "[cpu]", "[memory]", "[disk]", "[network]", "[processes]", "[runtime]", "[app]", "[cgroup]", "[psi]", "[history]", "[errors]"}
	last := -1
	for _, section := range order {
		index := strings.Index(text, section)
		if index < 0 {
			t.Fatalf("文本缺少段 %s", section)
		}
		if index <= last {
			t.Fatalf("文本段序错误: %s", section)
		}
		last = index
	}
}

func TestProbeNilProvidersDoNotPanic(t *testing.T) {
	probe := New(Options{collector: fakeCollector{data: platformData{
		CPU:       &CPUSection{},
		Memory:    &MemorySection{},
		Processes: &ProcessSection{},
	}}})
	snapshot := probe.Snapshot(context.Background())
	if snapshot == nil || snapshot.App == nil {
		t.Fatal("空 provider 应返回完整快照")
	}
	if snapshot.App.DB.Pool.OpenConnections != 0 || len(snapshot.App.Subprocesses) != 0 {
		t.Fatalf("空 provider 应得到零值: %+v", snapshot.App)
	}
}

func TestProbeProviderPanicIsolated(t *testing.T) {
	probe := New(Options{
		collector: fakeCollector{data: fakePlatformData()},
		Counts: func() Counts {
			panic("counts boom")
		},
	})
	snapshot := probe.Snapshot(context.Background())
	if snapshot == nil || snapshot.App == nil {
		t.Fatal("provider panic 不应让 Snapshot 失败")
	}
	found := false
	for _, item := range snapshot.Errors {
		if item.Section == "app" && strings.Contains(item.Error, "panic") {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider panic 应写入 errors: %+v", snapshot.Errors)
	}
}

func TestHistoryDefaultCapacity(t *testing.T) {
	probe := New(Options{collector: fakeCollector{data: fakePlatformData()}})
	for i := 0; i < 65; i++ {
		probe.state.appendHistory(HistorySample{TimestampUnix: int64(i)})
	}
	if got := probe.state.historyLen(); got != 60 {
		t.Fatalf("默认 history 容量应为 60，实际 %d", got)
	}
}

func TestProbeProviderTimeout(t *testing.T) {
	release := make(chan struct{})
	probe := New(Options{
		collector: fakeCollector{data: fakePlatformData()},
		Counts: func() Counts {
			<-release
			return Counts{}
		},
	})
	started := time.Now()
	snapshot := probe.Snapshot(context.Background())
	close(release)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("provider 超时应受独立上限约束: %v", elapsed)
	}
	if snapshot == nil || snapshot.App == nil {
		t.Fatal("provider 超时不应让 Snapshot 失败")
	}
	found := false
	for _, item := range snapshot.Errors {
		if item.Section == "app" && strings.Contains(item.Error, "timeout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider 超时应写入 errors: %+v", snapshot.Errors)
	}
}

func TestProbeOptionClamping(t *testing.T) {
	probe := New(Options{
		Timeout:      10 * time.Second,
		ProcessLimit: 100,
		ScanLimit:    10000,
		HistoryLimit: 100,
		collector:    fakeCollector{},
	})
	if probe.opts.Timeout != 5*time.Second {
		t.Fatalf("Timeout 应夹紧到 5s: %v", probe.opts.Timeout)
	}
	if probe.opts.ProcessLimit != maxProcessShow {
		t.Fatalf("ProcessLimit 应夹紧到 %d: %d", maxProcessShow, probe.opts.ProcessLimit)
	}
	if probe.opts.HistoryLimit != 60 {
		t.Fatalf("HistoryLimit 应夹紧到 60: %d", probe.opts.HistoryLimit)
	}
	if probe.opts.ScanLimit != defaultScanLimit {
		t.Fatalf("ScanLimit 应夹紧到 %d: %d", defaultScanLimit, probe.opts.ScanLimit)
	}
}
