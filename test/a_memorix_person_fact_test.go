package test

import (
	"context"
	"testing"
	"time"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/native"
)

func TestMemorixPersonFactObservationTimestamp(t *testing.T) {
	root := t.TempDir()
	previousKernel := memruntime.GetRuntimeKernel()
	t.Cleanup(func() { memruntime.SetRuntimeKernel(previousKernel) })
	runtime, err := native.Open(native.Options{
		PluginRoot: root,
		Config: map[string]any{
			"storage":   map[string]any{"data_dir": root},
			"embedding": map[string]any{"dimension": 4},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtime.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := runtime.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}

	timestamp := 1700000000.25
	for _, testCase := range []struct {
		name      string
		timestamp *float64
		wantJSON  any
	}{
		{name: "missing"},
		{name: "explicit", timestamp: &timestamp, wantJSON: timestamp},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			params := memruntime.IngestTextParams{
				ExternalID: "test-person-fact-" + testCase.name,
				SourceType: "person_fact",
				Text:       "Person fact with " + testCase.name + " timestamp",
				ChatID:     "test-chat",
				PersonIDs:  []string{"test-person-" + testCase.name},
				Timestamp:  testCase.timestamp,
				Metadata: map[string]any{
					"platform":             "test",
					"evidence_message_ids": []string{"test-message-" + testCase.name},
					"writeback_source":     "memory_flow_service",
				},
			}
			result, err := runtime.Kernel().IngestText(t.Context(), params)
			if err != nil {
				t.Fatalf("first ingest: %v", err)
			}
			claimIDs, ok := result["fact_claim_ids"].([]string)
			if result["detail"] != "fact_added" || !ok || len(claimIDs) != 1 {
				t.Fatalf("first ingest = %+v, want one new claim", result)
			}
			replayed, err := runtime.Kernel().IngestText(t.Context(), params)
			if err != nil || replayed["detail"] != "dedup_replayed" {
				t.Fatalf("replay = %+v, error = %v", replayed, err)
			}

			evidence, err := runtime.MetadataStore().GetFactEvidence(claimIDs[0])
			if err != nil || len(evidence) != 2 {
				t.Fatalf("evidence = %+v, error = %v, want a paragraph and one observation", evidence, err)
			}
			for _, row := range evidence {
				if row["evidence_type"] != "source" {
					continue
				}
				metadata, _ := row["metadata"].(map[string]any)
				if got, present := metadata["observed_at"]; !present || got != testCase.wantJSON {
					t.Fatalf("metadata observed_at = %v (present: %v), want %v", got, present, testCase.wantJSON)
				}
				if got, ok := row["observed_at"].(float64); !ok || got <= 0 || (testCase.timestamp != nil && got != *testCase.timestamp) {
					t.Fatalf("evidence observed_at = %v, timestamp input = %v", row["observed_at"], testCase.wantJSON)
				}
				return
			}
			t.Fatal("missing source evidence")
		})
	}
}
