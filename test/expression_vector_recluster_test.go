package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"MaiGo/internal/chat/replyer/expressionvector"
)

func TestExpressionVectorReclusterPersistsNativeLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	store, err := expressionvector.NewPersistentExpressionVectorStore(path)
	if err != nil {
		t.Fatal(err)
	}

	profile := expressionvector.ExpressionEmbeddingProfile{Marker: "test-profile", ModelName: "test-embedding", Dimension: 2}
	if err := store.SetActiveEmbeddingProfile(profile); err != nil {
		t.Fatal(err)
	}
	vectors := [][]float32{{1, 0}, {2, 0}, {0, 1}, {0, 2}}
	batch := make([]expressionvector.ExpressionBackfillSuccess, len(vectors))
	for index, vector := range vectors {
		batch[index] = expressionvector.ExpressionBackfillSuccess{
			Meta:   expressionvector.ExpressionVectorMetadata{ID: uint(index + 1)},
			Vector: vector,
		}
	}
	now := time.Now()
	if _, err := store.ApplyExpressionBackfillBatch(profile, batch, nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeExpressionBackfill(profile, now); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Expressions []struct {
			ID      uint `json:"id"`
			Cluster int  `json:"cluster_id"`
		} `json:"expressions"`
		Maintenance struct {
			State string `json:"state"`
		} `json:"cluster_maintenance"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	labels := make(map[uint]int, len(saved.Expressions))
	for _, expression := range saved.Expressions {
		labels[expression.ID] = expression.Cluster
	}
	// The Python reference repairs the two empty clusters caused by the duplicate directions.
	want := map[uint]int{1: 2, 2: 1, 3: 3, 4: 0}
	if !reflect.DeepEqual(labels, want) || saved.Maintenance.State != "STABLE" {
		t.Fatalf("saved labels = %v, state = %q; want %v, STABLE", labels, saved.Maintenance.State, want)
	}

	reloaded, err := expressionvector.NewPersistentExpressionVectorStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Len(); got != len(vectors) {
		t.Fatalf("reloaded %d vectors, want %d", got, len(vectors))
	}
}
