package test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/write"
)

func TestMemorixRelationWritesReuseTransaction(t *testing.T) {
	root := t.TempDir()
	store := storage.NewStore(filepath.Join(root, "metadata"), "metadata.db")
	if err := store.Connect("", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	paragraphHash, err := store.AddParagraph("Alice moved to Paris", nil, "chat_summary:room", nil, "narrative", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddEntity("Bob", nil, paragraphHash, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRelation("Bob", "knows", "Alice", nil, 1, paragraphHash, nil); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	graph := storage.NewGraphStore(storage.SparseMatrixFormatCSR, filepath.Join(root, "graph"))
	service := write.NewRelationWriteService(store, graph, nil, nil, write.RelationWriteConfig{})
	batch, err := service.UpsertRelationsWithVectors(ctx, [][3]string{{"Alice", "moved to", "Paris"}}, 1, paragraphHash, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].VectorState != "failed" {
		t.Fatalf("batch result = %+v, want one relation with unavailable vector channel", batch)
	}
	single, err := service.UpsertRelationWithVector(ctx, "Alice", "lives in", "Paris", 1, paragraphHash, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{batch[0].HashValue, single.HashValue} {
		if _, ok := graph.GetRelationHashesForEdge("Alice", "Paris")[hash]; !ok {
			t.Fatalf("graph edge missing relation %s", hash)
		}
	}

	conn, err := store.Conn()
	if err != nil {
		t.Fatal(err)
	}
	var entityLinks, relationLinks int
	if err := conn.QueryRow("SELECT COUNT(*) FROM paragraph_entities WHERE paragraph_hash = ?", paragraphHash).Scan(&entityLinks); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow("SELECT COUNT(*) FROM paragraph_relations WHERE paragraph_hash = ?", paragraphHash).Scan(&relationLinks); err != nil {
		t.Fatal(err)
	}
	if entityLinks != 1 || relationLinks != 3 {
		t.Fatalf("paragraph links = (%d, %d), want (1, 3)", entityLinks, relationLinks)
	}
	var vectorState string
	var retries int
	if err := conn.QueryRow("SELECT vector_state, vector_retry_count FROM relations WHERE hash = ?", batch[0].HashValue).Scan(&vectorState, &retries); err != nil {
		t.Fatal(err)
	}
	if vectorState != "failed" || retries != 1 {
		t.Fatalf("relation vector state = (%q, %d), want (failed, 1)", vectorState, retries)
	}
}

func TestMemorixFailedCommitRollsBackBeforeReusingConnection(t *testing.T) {
	manager := storage.NewManager(filepath.Join(t.TempDir(), "metadata.db"), 100*time.Millisecond)
	t.Cleanup(func() { _ = manager.CloseAll() })
	conn, err := manager.Conn()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		"CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) DEFERRABLE INITIALLY DEFERRED)",
	} {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	var bodyErr error
	commitErr := manager.Transaction(true, func(tx *storage.Tx) error {
		_, err := tx.Exec("INSERT INTO children(parent_id) VALUES (99)")
		bodyErr = err
		return err
	})
	if bodyErr != nil || commitErr == nil || !strings.Contains(commitErr.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("commit error = %v, want deferred foreign-key failure", commitErr)
	}

	if err := manager.Transaction(true, func(tx *storage.Tx) error {
		if _, err := tx.Exec("INSERT INTO parents(id) VALUES (1)"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO children(parent_id) VALUES (1)")
		return err
	}); err != nil {
		t.Fatalf("transaction after failed commit: %v", err)
	}

	var children int
	if err := conn.QueryRow("SELECT COUNT(*) FROM children").Scan(&children); err != nil {
		t.Fatal(err)
	}
	if children != 1 {
		t.Fatalf("child count = %d, want 1", children)
	}
}
