package llm

import (
	"context"
	"testing"

	"MaiGo/internal/services/model_stats"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recordingSinkStub struct {
	modelCalls []modelstats.ModelUsageRecord
	taskCalls  []modelstats.ModelTaskUsageRecord
}

func (s *recordingSinkStub) RecordModelUsage(record modelstats.ModelUsageRecord) {
	s.modelCalls = append(s.modelCalls, record)
}

func (s *recordingSinkStub) RecordModelTaskUsage(record modelstats.ModelTaskUsageRecord) {
	s.taskCalls = append(s.taskCalls, record)
}

type failingAgenticModel struct{ model.AgenticModel }

func (failingAgenticModel) Generate(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.AgenticMessage, error) {
	return nil, &HTTPStatusError{StatusCode: 500, Message: "upstream unavailable"}
}

func TestUsageRecordingModelCountsFailedCall(t *testing.T) {
	sink := &recordingSinkStub{}
	restore := setUsageSinkForTest(sink)
	defer restore()

	client := newUsageRecordingModel(failingAgenticModel{}, ModelConfig{
		Model:            "mai_replier_today",
		ModelDisplayName: "麦麦特调回复（今日版）",
		TaskName:         "replyer",
	})
	if _, err := client.Generate(context.Background(), nil); err == nil {
		t.Fatal("Generate() expected error")
	}
	if len(sink.modelCalls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(sink.modelCalls))
	}
	call := sink.modelCalls[0]
	if !call.Failed {
		t.Fatal("model call Failed = false, want true")
	}
	if call.Task != "replyer" || call.Model != "麦麦特调回复（今日版）" {
		t.Fatalf("unexpected call identity: %+v", call)
	}
	if len(sink.taskCalls) != 1 || sink.taskCalls[0].Succeeded {
		t.Fatalf("unexpected task usage: %+v", sink.taskCalls)
	}
}
