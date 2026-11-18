package system_probe

import (
	"context"
	"sync"
	"testing"
	"time"
)

type funcCollector func() platformData

func (f funcCollector) collect(context.Context, *Probe, []Subprocess) platformData {
	return f()
}

func TestProbeSnapshotConcurrent(t *testing.T) {
	probe := New(Options{
		HistoryLimit: 20,
		Now:          time.Now,
		collector:    funcCollector(fakePlatformData),
	})
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for i := 0; i < 5; i++ {
				snapshot := probe.Snapshot(context.Background())
				if snapshot == nil || snapshot.Runtime == nil {
					t.Errorf("并发 Snapshot 结果不完整")
					return
				}
				probe.state.observeCPU(cpuSample{User: uint64(worker + i), Idle: 100}, time.Now())
				probe.state.snapshotHistory()
			}
		}(worker)
	}
	group.Wait()
	if got := probe.state.historyLen(); got != 20 {
		t.Fatalf("并发后 history 长度应封顶 20，实际 %d", got)
	}
}
