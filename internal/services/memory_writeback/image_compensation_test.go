package memorywriteback

// 图片描述补偿按消息合并的局部回归测试

import (
	"context"
	"sync"
	"testing"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
)

type recordingWritebackKernel struct {
	mu      sync.Mutex
	ingests []memruntime.IngestSummaryParams
}

func (k *recordingWritebackKernel) IngestSummary(_ context.Context, params memruntime.IngestSummaryParams) (map[string]any, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ingests = append(k.ingests, params)
	return map[string]any{"success": true}, nil
}

func (k *recordingWritebackKernel) SummaryCheckpointCount(string) (int, error) { return 0, nil }
func (k *recordingWritebackKernel) ParagraphsBySource(string) ([]map[string]any, error) {
	return nil, nil
}

func TestImageCompensationCoalescesByMessage(t *testing.T) {
	items := []map[string]any{
		{
			"occurrence_id": "occ1", "description_hash": "h1", "chat_id": "chat1",
			"message_id": "msg1", "occurred_at": float64(100), "summary_external_id": "batch-a",
		},
		{
			"occurrence_id": "occ2", "description_hash": "h2", "chat_id": "chat1",
			"message_id": "msg1", "occurred_at": float64(100), "summary_external_id": "batch-a",
		},
		{
			"occurrence_id": "occ3", "description_hash": "h3", "chat_id": "chat1",
			"message_id": "msg2", "occurred_at": float64(200), "summary_external_id": "batch-b",
		},
	}
	completed := make([]map[string]any, 0, len(items))
	imageMemory := func(_ context.Context, action string, payload map[string]any) (map[string]any, error) {
		switch action {
		case "claim_description_compensations":
			return map[string]any{"success": true, "lease_token": "lease-1", "items": items}, nil
		case "complete_description_compensation":
			completed = append(completed, payload)
			return map[string]any{"success": true}, nil
		default:
			return map[string]any{"success": true}, nil
		}
	}
	kernel := &recordingWritebackKernel{}
	service := NewImageWritebackService(ImageWritebackOptions{
		Settings:    func() ImageWritebackSettings { return DefaultImageWritebackSettings() },
		ImageMemory: imageMemory,
		Kernel:      func() ChatSummaryWritebackKernel { return kernel },
	})
	service.processDescriptionCompensations(context.Background())

	if len(kernel.ingests) != 2 {
		t.Fatalf("同一消息应合并为一次摘要，实际 %d 次", len(kernel.ingests))
	}
	externalIDs := map[string]int{}
	for _, ingest := range kernel.ingests {
		externalIDs[ingest.ExternalID]++
	}
	if externalIDs["batch-a"] != 1 || externalIDs["batch-b"] != 1 {
		t.Fatalf("摘要批次不符合预期: %+v", externalIDs)
	}
	if len(completed) != 3 {
		t.Fatalf("每条出现记录都应单独回执，实际 %d", len(completed))
	}
	for _, payload := range completed {
		if payload["lease_token"] != "lease-1" {
			t.Fatalf("回执租约不符: %+v", payload)
		}
	}
}
