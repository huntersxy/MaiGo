package storage

// 段落 metadata 原子合并与图片补偿批次身份的局部回归测试

import (
	"fmt"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(dir, "metadata.db")
	if err := store.Connect(dir, true); err != nil {
		t.Fatalf("连接测试存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// openSecondTestStore 用独立连接打开同一数据库，模拟并发写入方
func openSecondTestStore(t *testing.T, primary *Store) *Store {
	t.Helper()
	store := NewStore(primary.dataDir, primary.dbName)
	if err := store.Connect(primary.dataDir, true); err != nil {
		t.Fatalf("连接第二个测试存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestUpdateParagraphMetadataConcurrentMergeKeepsAllPatches(t *testing.T) {
	store := newTestStore(t)
	const workers = 8
	writers := make([]*Store, 0, workers)
	writers = append(writers, store)
	for index := 1; index < workers; index++ {
		writers = append(writers, openSecondTestStore(t, store))
	}
	hashValue, err := store.AddParagraph("并发 metadata 测试", nil, "test", map[string]any{"seed": float64(1)}, "factual", nil)
	if err != nil {
		t.Fatalf("写入段落失败: %v", err)
	}

	var waitGroup sync.WaitGroup
	errorsByWorker := make([]error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			_, errorsByWorker[index] = writers[index].UpdateParagraphMetadata(
				hashValue, map[string]any{fmt.Sprintf("key_%d", index): float64(index)}, true,
			)
		}(index)
	}
	waitGroup.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("并发补丁 %d 失败: %v", index, err)
		}
	}

	paragraph, err := store.GetParagraph(hashValue)
	if err != nil {
		t.Fatalf("读取段落失败: %v", err)
	}
	metadata, _ := paragraph["metadata"].(map[string]any)
	for index := 0; index < workers; index++ {
		if _, ok := metadata[fmt.Sprintf("key_%d", index)]; !ok {
			t.Fatalf("并发补丁丢失 key_%d: %+v", index, metadata)
		}
	}
}

func TestMergeExistingParagraphMetadataConcurrentMergeKeepsAllPatches(t *testing.T) {
	store := newTestStore(t)
	const workers = 4
	writers := make([]*Store, 0, workers)
	writers = append(writers, store)
	for index := 1; index < workers; index++ {
		writers = append(writers, openSecondTestStore(t, store))
	}
	hashValue, err := store.AddParagraph("并发合并测试", nil, "test", map[string]any{"base": "v"}, "factual", nil)
	if err != nil {
		t.Fatalf("写入段落失败: %v", err)
	}

	var waitGroup sync.WaitGroup
	errorsByWorker := make([]error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			_, errorsByWorker[index] = writers[index].MergeExistingParagraphMetadata(
				hashValue, map[string]any{fmt.Sprintf("merge_%d", index): true}, nil,
			)
		}(index)
	}
	waitGroup.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("并发合并 %d 失败: %v", index, err)
		}
	}

	paragraph, err := store.GetParagraph(hashValue)
	if err != nil {
		t.Fatalf("读取段落失败: %v", err)
	}
	metadata, _ := paragraph["metadata"].(map[string]any)
	for index := 0; index < workers; index++ {
		if _, ok := metadata[fmt.Sprintf("merge_%d", index)]; !ok {
			t.Fatalf("并发合并丢失 merge_%d: %+v", index, metadata)
		}
	}
}

func TestImageDescriptionCompensationBatchIdentityPersistsAcrossRetry(t *testing.T) {
	store := newTestStore(t)
	conn, err := store.ResolveConn(nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	now := nowTimestamp()
	if _, err := conn.Exec(`INSERT INTO image_assets
		(asset_id, content_hash, storage_key, mime_type, byte_size, width, height, status, created_at, updated_at)
		VALUES ('asset1', 'content1', 'key1', 'image/png', 1, 1, 1, 'active', ?, ?)`, now, now); err != nil {
		t.Fatalf("写入图片资产失败: %v", err)
	}
	occurrences := []struct {
		id         string
		messageID  string
		occurredAt float64
	}{
		{"occ1", "msg1", 100},
		{"occ2", "msg1", 100},
		{"occ3", "msg1", 200},
	}
	for _, occurrence := range occurrences {
		if _, err := conn.Exec(`INSERT INTO image_occurrences
			(occurrence_id, asset_id, external_ref, source_kind, scope_type, chat_id, message_id,
			 component_path, occurred_at, installation_id, status, created_at, updated_at)
			VALUES (?, 'asset1', ?, 'chat', 'chat', 'chat1', ?, 'p1', ?, '', 'active', ?, ?)`,
			occurrence.id, occurrence.id, occurrence.messageID, occurrence.occurredAt, now, now); err != nil {
			t.Fatalf("写入图片出现记录失败: %v", err)
		}
		if err := store.EnqueueImageDescriptionCompensation(occurrence.id, "hash-"+occurrence.id, nil); err != nil {
			t.Fatalf("入队补偿失败: %v", err)
		}
	}

	claimed, err := store.ClaimImageDescriptionCompensations("lease1", 120, 3, 10)
	if err != nil {
		t.Fatalf("领取补偿失败: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("应领取 3 条补偿，实际 %d", len(claimed))
	}
	externalByOccurrence := map[string]string{}
	for _, row := range claimed {
		externalByOccurrence[stringFromRow(row, "occurrence_id")] = stringFromRow(row, "summary_external_id")
	}
	if externalByOccurrence["occ1"] == "" || externalByOccurrence["occ1"] != externalByOccurrence["occ2"] {
		t.Fatalf("同消息同时间应共享批次: %+v", externalByOccurrence)
	}
	if externalByOccurrence["occ3"] == externalByOccurrence["occ1"] {
		t.Fatalf("不同时间应使用独立批次: %+v", externalByOccurrence)
	}

	if _, err := conn.Exec(`UPDATE image_description_compensations
		SET status='pending', lease_token='', lease_until=0, attempt_count=0`); err != nil {
		t.Fatalf("重置补偿状态失败: %v", err)
	}
	claimedAgain, err := store.ClaimImageDescriptionCompensations("lease2", 120, 3, 10)
	if err != nil {
		t.Fatalf("重试领取失败: %v", err)
	}
	for _, row := range claimedAgain {
		occurrenceID := stringFromRow(row, "occurrence_id")
		if stringFromRow(row, "summary_external_id") != externalByOccurrence[occurrenceID] {
			t.Fatalf("重试后批次身份发生变化: occurrence=%s before=%s after=%s",
				occurrenceID, externalByOccurrence[occurrenceID], stringFromRow(row, "summary_external_id"))
		}
	}
}
