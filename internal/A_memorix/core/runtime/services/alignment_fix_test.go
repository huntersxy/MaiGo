package services

// 外部来源归属校验与后台任务有界关闭的局部回归测试

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestSourceAttributionMatches(t *testing.T) {
	ref := map[string]any{
		"source_type": "chat_summary",
		"metadata": map[string]any{
			"chat_id":    "chat1",
			"person_ids": []any{"p1", "p2"},
		},
	}
	base := IngestTextParams{SourceType: "chat_summary", ChatID: "chat1", PersonIDs: []string{"p2", "p1"}}
	if !ingestSourceAttributionMatches(ref, base) {
		t.Fatal("归属一致时应通过")
	}
	cases := []IngestTextParams{
		{SourceType: "person_fact", ChatID: "chat1", PersonIDs: []string{"p1", "p2"}},
		{SourceType: "chat_summary", ChatID: "chat2", PersonIDs: []string{"p1", "p2"}},
		{SourceType: "chat_summary", ChatID: "chat1", PersonIDs: []string{"p1"}},
	}
	for index, params := range cases {
		if ingestSourceAttributionMatches(ref, params) {
			t.Fatalf("第 %d 组归属不一致未拒绝: %+v", index, params)
		}
	}

	singleRef := map[string]any{
		"source_type": "person_fact",
		"metadata": map[string]any{
			"chat_id":   "chat1",
			"person_id": "p1",
		},
	}
	if !ingestSourceAttributionMatches(singleRef, IngestTextParams{
		SourceType: "person_fact", ChatID: "chat1", PersonIDs: []string{"p1"},
	}) {
		t.Fatal("person_id 单值应与人选一致")
	}
	if ingestSourceAttributionMatches(singleRef, IngestTextParams{
		SourceType: "person_fact", ChatID: "chat1", PersonIDs: []string{"p2"},
	}) {
		t.Fatal("person_id 单值不一致时应拒绝")
	}
}

func TestStopBackgroundTasksWaitsForLoopExit(t *testing.T) {
	var stopping atomic.Bool
	service := NewBackgroundTaskService(nil, BackgroundTaskDeps{
		BackgroundStopping:    stopping.Load,
		SetBackgroundStopping: stopping.Store,
	})
	var started atomic.Bool
	if err := service.EnsureBackgroundTask(context.Background(), "immediate", func(ctx context.Context) error {
		started.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("启动后台任务失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !started.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := service.StopBackgroundTasks(context.Background()); err != nil {
		t.Fatalf("关闭后台任务失败: %v", err)
	}
	if !stopping.Load() {
		t.Fatal("关闭后应保持 stopping 状态")
	}
}
