package test

import (
	"context"
	"fmt"
	"testing"

	"MaiGo/internal/runtime/execution/scheduler"
)

func BenchmarkSchedulerSubmit(b *testing.B) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   8,
		QueueSize: 1024,
		BatchSize: 8,
	})
	defer func() {
		_ = engine.Shutdown(context.Background())
	}()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		index := 0
		for pb.Next() {
			key := fmt.Sprintf("session-%d", index%64)
			index++
			if err := engine.Submit(context.Background(), key, func(context.Context) error {
				return nil
			}); err != nil {
				b.Fatalf("submit failed: %v", err)
			}
		}
	})
}
