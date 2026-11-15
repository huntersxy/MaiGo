package scripts

// migrate_maibot_memory 的窗口提交与校验实现

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// migrate 对齐 _migrate
func (r *migrateRunner) migrate(ctx context.Context, startAfter int64) error {
	readBatchSize := max(1, r.options.readBatchSize)
	commitWindowRows := max(1, r.options.commitWindowRows)
	logEvery := max(1, r.options.logEvery)
	var window []*migrateMappedRow
	windowScanned := 0
	lastSeenID := startAfter
	r.logger.Info(fmt.Sprintf("开始迁移: start_after_id=%d, read_batch_size=%d, commit_window_rows=%d", startAfter, readBatchSize, commitWindowRows))
	batches, err := r.source.iterRows(r.selection, readBatchSize, startAfter)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		for index := range batch {
			sourceRow := batch[index]
			lastSeenID = sourceRow.ID
			r.stats["scanned_rows"] = intOrZero(r.stats["scanned_rows"]) + 1
			windowScanned++
			mapped, err := mapSourceRow(sourceRow, r.vectorStore, r.stats, r.logger)
			if err != nil {
				r.stats["bad_rows"] = intOrZero(r.stats["bad_rows"]) + 1
				r.appendBadRow(sourceRow, err.Error())
				if r.options.maxErrors > 0 && intOrZero(r.stats["bad_rows"]) > r.options.maxErrors {
					return fmt.Errorf("坏行数量超过上限 max_errors=%d，已中止。", r.options.maxErrors)
				}
				continue
			}
			r.stats["valid_rows"] = intOrZero(r.stats["valid_rows"]) + 1
			if mapped.existingVector {
				r.stats["skipped_existing_rows"] = intOrZero(r.stats["skipped_existing_rows"]) + 1
			} else {
				r.stats["migrated_rows"] = intOrZero(r.stats["migrated_rows"]) + 1
			}
			window = append(window, mapped)
			if windowScanned >= commitWindowRows {
				if err := r.commitWindow(ctx, window, lastSeenID); err != nil {
					return err
				}
				window = nil
				windowScanned = 0
			}
			if intOrZero(r.stats["scanned_rows"])%logEvery == 0 {
				r.logger.Info(fmt.Sprintf("迁移进度: scanned=%d/%d, valid=%d, bad=%d, last_id=%d",
					intOrZero(r.stats["scanned_rows"]), intOrZero(r.stats["source_matched_total"]),
					intOrZero(r.stats["valid_rows"]), intOrZero(r.stats["bad_rows"]), lastSeenID))
			}
		}
	}
	if windowScanned > 0 || len(window) > 0 {
		if err := r.commitWindow(ctx, window, lastSeenID); err != nil {
			return err
		}
	}
	r.logger.Info(fmt.Sprintf("迁移主流程完成: scanned=%d, valid=%d, bad=%d, last_committed_id=%d",
		intOrZero(r.stats["scanned_rows"]), intOrZero(r.stats["valid_rows"]),
		intOrZero(r.stats["bad_rows"]), intOrZero(r.stats["last_committed_id"])))
	return nil
}

// commitWindow 对齐 _commit_window
func (r *migrateRunner) commitWindow(ctx context.Context, rows []*migrateMappedRow, lastSeenID int64) error {
	if len(rows) == 0 {
		if err := r.flushState(lastSeenID); err != nil {
			return err
		}
		r.stats["windows_committed"] = intOrZero(r.stats["windows_committed"]) + 1
		return nil
	}
	hashes := make([]string, 0, len(rows))
	for _, row := range rows {
		hashes = append(hashes, row.paragraphHash)
	}
	existing, err := r.metadataStore.GetParagraphsByHashes(hashes)
	if err != nil {
		return err
	}
	newFlags := make([]bool, len(rows))
	for index, row := range rows {
		_, exists := existing[row.paragraphHash]
		newFlags[index] = !exists
	}

	relationSet := map[string][3]string{}
	for index, row := range rows {
		if _, err := r.metadataStore.AddParagraph(row.content, nil, row.source, map[string]any{}, "narrative", row.timeMeta); err != nil {
			return err
		}
		if !newFlags[index] {
			continue
		}
		if len(row.entities) > 0 {
			if _, err := r.metadataStore.AddEntitiesBatch(row.entities, nil, row.paragraphHash, nil); err != nil {
				return err
			}
		}
		for _, relation := range row.relations {
			relationHash, err := r.metadataStore.AddRelation(relation[0], relation[1], relation[2], nil, 1.0, row.paragraphHash, nil)
			if err != nil {
				return err
			}
			if _, err := r.metadataStore.LinkParagraphRelation(row.paragraphHash, relationHash); err != nil {
				return err
			}
			relationSet[relationHash] = relation
		}
	}
	if len(relationSet) > 0 {
		edges := make([]storage.Edge, 0, len(relationSet))
		relationHashes := make([]string, 0, len(relationSet))
		ordered := make([]string, 0, len(relationSet))
		for hashValue := range relationSet {
			ordered = append(ordered, hashValue)
		}
		sort.Strings(ordered)
		for _, hashValue := range ordered {
			relation := relationSet[hashValue]
			edges = append(edges, storage.Edge{Source: relation[0], Target: relation[2]})
			relationHashes = append(relationHashes, hashValue)
		}
		if err := r.graphStore.BatchUpdate(func() error {
			_, addErr := r.graphStore.AddEdges(edges, nil, relationHashes)
			return addErr
		}); err != nil {
			return err
		}
		r.stats["graph_edges_written"] = intOrZero(r.stats["graph_edges_written"]) + len(edges)
		if r.shouldWriteRelationVectors() && r.relationWrite != nil {
			for _, hashValue := range ordered {
				relation := relationSet[hashValue]
				result, err := r.relationWrite.EnsureRelationVector(ctx, hashValue, relation[0], relation[1], relation[2], 500, false, nil)
				if err != nil {
					r.stats["relation_vectors_failed"] = intOrZero(r.stats["relation_vectors_failed"]) + 1
					continue
				}
				if result.VectorState == "ready" {
					if result.VectorWritten {
						r.stats["relation_vectors_written"] = intOrZero(r.stats["relation_vectors_written"]) + 1
					} else {
						r.stats["relation_vectors_skipped"] = intOrZero(r.stats["relation_vectors_skipped"]) + 1
					}
				} else {
					r.stats["relation_vectors_failed"] = intOrZero(r.stats["relation_vectors_failed"]) + 1
				}
			}
		}
	}
	paragraphTexts := map[string]string{}
	entityTexts := map[string]string{}
	for index, row := range rows {
		if !r.vectorStore.Contains(row.paragraphHash) {
			paragraphTexts[row.paragraphHash] = row.content
		}
		if !newFlags[index] {
			continue
		}
		for _, entity := range row.entities {
			hashValue, err := utils.ComputeHash(strings.ToLower(strings.TrimSpace(entity)))
			if err != nil || hashValue == "" {
				continue
			}
			if !r.vectorStore.Contains(hashValue) {
				entityTexts[hashValue] = entity
			}
		}
	}
	paragraphAdded, err := r.embedAndAddVectors(ctx, paragraphTexts, max(1, r.options.embedBatchSize))
	if err != nil {
		return err
	}
	entityAdded := 0
	if len(entityTexts) > 0 {
		entityAdded, err = r.embedAndAddVectors(ctx, entityTexts, max(1, r.options.entityEmbedBatch))
		if err != nil {
			return err
		}
	}
	r.stats["paragraph_vectors_added"] = intOrZero(r.stats["paragraph_vectors_added"]) + paragraphAdded
	r.stats["entity_vectors_added"] = intOrZero(r.stats["entity_vectors_added"]) + entityAdded
	if err := r.vectorStore.Save("", nil); err != nil {
		return err
	}
	if err := r.graphStore.Save(""); err != nil {
		return err
	}
	r.stats["windows_committed"] = intOrZero(r.stats["windows_committed"]) + 1
	return r.flushState(lastSeenID)
}

// embedAndAddVectors 对齐 _embed_and_add_vectors
func (r *migrateRunner) embedAndAddVectors(ctx context.Context, idToText map[string]string, batchSize int) (int, error) {
	if len(idToText) == 0 {
		return 0, nil
	}
	if r.embedManager == nil {
		return 0, fmt.Errorf("embedding_manager 未初始化，无法写入向量")
	}
	ids := make([]string, 0, len(idToText))
	texts := make([]string, 0, len(idToText))
	ordered := make([]string, 0, len(idToText))
	for hashValue := range idToText {
		ordered = append(ordered, hashValue)
	}
	sort.Strings(ordered)
	for _, hashValue := range ordered {
		if r.vectorStore.Contains(hashValue) {
			continue
		}
		ids = append(ids, hashValue)
		texts = append(texts, idToText[hashValue])
	}
	chunkSize := max(1, batchSize)
	totalAdded := 0
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		embeddings, err := r.embedManager.Encode(ctx, texts[start:end], &embedding.EncodeOptions{BatchSize: chunkSize})
		if err != nil {
			return totalAdded, err
		}
		if len(embeddings) != end-start {
			r.logger.Warning(fmt.Sprintf("embedding 返回数量异常: expected=%d, got=%d，跳过该批次", end-start, len(embeddings)))
			continue
		}
		validVectors := make([][]float32, 0, len(embeddings))
		validIDs := make([]string, 0, len(embeddings))
		for offset, vector := range embeddings {
			if len(vector) != r.vectorStore.Dimension() {
				r.logger.Warning(fmt.Sprintf("向量维度不匹配，跳过: id=%s, got=%d, expected=%d", ids[start+offset], len(vector), r.vectorStore.Dimension()))
				continue
			}
			finite := true
			for _, value := range vector {
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					finite = false
					break
				}
			}
			if !finite {
				r.logger.Warning(fmt.Sprintf("向量含 NaN/Inf，跳过: id=%s", ids[start+offset]))
				continue
			}
			if r.vectorStore.Contains(ids[start+offset]) {
				continue
			}
			validVectors = append(validVectors, vector)
			validIDs = append(validIDs, ids[start+offset])
		}
		if len(validVectors) > 0 {
			added, err := r.vectorStore.Add(validVectors, validIDs)
			if err != nil {
				return totalAdded, err
			}
			totalAdded += added
		}
	}
	return totalAdded, nil
}

// appendBadRow 对齐 _append_bad_row
func (r *migrateRunner) appendBadRow(row migrateSourceRow, reason string) {
	payload := map[string]any{
		"id": row.ID, "chat_id": strings.TrimSpace(row.ChatID), "start_time": row.StartTime,
		"end_time": row.EndTime, "participants": row.Participants, "theme": normalizeMigrateName(row.Theme),
		"keywords": row.Keywords, "summary": row.Summary, "error": reason, "timestamp": unixNow(),
	}
	text, err := MarshalJSONIndent(payload)
	if err != nil {
		return
	}
	_ = os.MkdirAll(strings.TrimSuffix(r.badRowsFile, string(os.PathSeparator)+migrationBadRowsFileName), 0o755)
	file, err := os.OpenFile(r.badRowsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(text + "\n")
}

// verify 对齐 _verify
func (r *migrateRunner) verify(ctx context.Context, strict bool) error {
	sampleSize := intOrZero(r.stats["source_matched_total"])
	if sampleSize > 2000 {
		sampleSize = 2000
	}
	if sampleSize < 0 {
		sampleSize = 0
	}
	r.stats["verify_sample_size"] = sampleSize
	if sampleSize == 0 {
		r.stats["verify_passed"] = true
		return nil
	}
	rows, err := r.source.sampleRowsForVerify(r.selection, sampleSize)
	if err != nil {
		return err
	}
	paragraphMissing, vectorMissing, relationMissing, edgeMissing, badRows := 0, 0, 0, 0, 0
	for _, sourceRow := range rows {
		mapped, err := mapSourceRow(sourceRow, r.vectorStore, r.stats, r.logger)
		if err != nil {
			badRows++
			continue
		}
		paragraph, err := r.metadataStore.GetParagraph(mapped.paragraphHash)
		if err != nil {
			return err
		}
		if paragraph == nil {
			paragraphMissing++
		}
		if !r.vectorStore.Contains(mapped.paragraphHash) {
			vectorMissing++
		}
		for _, relation := range mapped.relations {
			relationHash, err := utils.ComputeHash(strings.ToLower(strings.TrimSpace(relation[0])) + "|" + strings.ToLower(strings.TrimSpace(relation[1])) + "|" + strings.ToLower(strings.TrimSpace(relation[2])))
			if err != nil {
				continue
			}
			existing, err := r.metadataStore.GetRelation(relationHash, true)
			if err != nil {
				return err
			}
			if existing == nil {
				relationMissing++
			}
			if r.graphStore.GetEdgeWeight(relation[0], relation[2]) <= 0 {
				edgeMissing++
			}
		}
	}
	r.stats["verify_paragraph_missing"] = paragraphMissing
	r.stats["verify_vector_missing"] = vectorMissing
	r.stats["verify_relation_missing"] = relationMissing
	r.stats["verify_edge_missing"] = edgeMissing
	r.stats["verify_bad_rows"] = badRows
	passed := paragraphMissing == 0 && vectorMissing == 0 && relationMissing == 0 && edgeMissing == 0 && badRows == 0
	if strict && !passed {
		r.failed = true
		r.failReason = fmt.Sprintf("严格校验失败: paragraph_missing=%d, vector_missing=%d, relation_missing=%d, edge_missing=%d, verify_bad_rows=%d",
			paragraphMissing, vectorMissing, relationMissing, edgeMissing, badRows)
	}
	r.stats["verify_passed"] = passed
	return nil
}

// finalize 对齐 _finalize
func (r *migrateRunner) finalize() int {
	r.stats["elapsed_seconds"] = time.Since(r.startedAt).Seconds()
	report := map[string]any{
		"success": !r.failed, "fail_reason": r.failReason, "args": migrateArgsPayload(r.options),
		"source_db": r.sourceDBPath, "target_data_dir": r.targetDir,
		"selection": selectionFingerprint(r.selection), "filter_fingerprint": r.filterFP,
		"source_db_fingerprint": r.sourceFP, "state_file": r.stateFile, "bad_rows_file": r.badRowsFile,
		"stats": copyStats(r.stats), "timestamp": unixNow(),
	}
	if text, err := MarshalJSONIndent(report); err == nil {
		_ = os.MkdirAll(strings.TrimSuffix(r.reportFile, string(os.PathSeparator)+migrationReportFileName), 0o755)
		_ = writeFileAtomic(r.reportFile, []byte(text))
	}
	exitCode := 0
	if r.failed {
		exitCode = 1
	} else if intOrZero(r.stats["bad_rows"]) > 0 {
		exitCode = 2
	}
	fmt.Fprintln(r.stdout, "\n=== Migration Report ===")
	fmt.Fprintf(r.stdout, "success: %t\n", !r.failed)
	if r.failReason != "" {
		fmt.Fprintf(r.stdout, "fail_reason: %s\n", r.failReason)
	}
	fmt.Fprintf(r.stdout, "elapsed: %.2fs\n", r.stats["elapsed_seconds"])
	for _, key := range []string{"source_matched_total", "scanned_rows", "valid_rows", "migrated_rows", "skipped_existing_rows", "bad_rows", "coerced_list_fields", "paragraph_vectors_added", "entity_vectors_added", "relations_written", "graph_edges_written", "windows_committed", "last_committed_id"} {
		fmt.Fprintf(r.stdout, "%s: %d\n", key, intOrZero(r.stats[key]))
	}
	fmt.Fprintf(r.stdout, "relation_vectors: written=%d, failed=%d, skipped=%d\n",
		intOrZero(r.stats["relation_vectors_written"]), intOrZero(r.stats["relation_vectors_failed"]), intOrZero(r.stats["relation_vectors_skipped"]))
	fmt.Fprintf(r.stdout, "verify: sample=%d, paragraph_missing=%d, vector_missing=%d, relation_missing=%d, edge_missing=%d, bad_rows=%d, passed=%t\n",
		intOrZero(r.stats["verify_sample_size"]), intOrZero(r.stats["verify_paragraph_missing"]), intOrZero(r.stats["verify_vector_missing"]),
		intOrZero(r.stats["verify_relation_missing"]), intOrZero(r.stats["verify_edge_missing"]), intOrZero(r.stats["verify_bad_rows"]), isTruthy(r.stats["verify_passed"]))
	fmt.Fprintf(r.stdout, "report_file: %s\n", r.reportFile)
	fmt.Fprintln(r.stdout, "========================")
	return exitCode
}

// printPreview 对齐 _print_preview
func (r *migrateRunner) printPreview(preview *migratePreview) {
	fmt.Fprintln(r.stdout, "\n=== Migration Preview ===")
	fmt.Fprintf(r.stdout, "source_db: %s\n", r.sourceDBPath)
	fmt.Fprintf(r.stdout, "target_data_dir: %s\n", r.targetDir)
	if r.selection != nil {
		fmt.Fprintf(r.stdout, "time_window: [%s ~ %s] (ts: %s ~ %s)\n", emptyDash(r.selection.timeFromRaw), emptyDash(r.selection.timeToRaw), formatTS(r.selection.timeFromTS), formatTS(r.selection.timeToTS))
		start, end := "-", "-"
		if r.selection.startID != nil {
			start = fmt.Sprint(*r.selection.startID)
		}
		if r.selection.endID != nil {
			end = fmt.Sprint(*r.selection.endID)
		}
		fmt.Fprintf(r.stdout, "id_window: [%s ~ %s], selected_streams=%d\n", start, end, len(r.selection.streamIDs))
	}
	fmt.Fprintf(r.stdout, "matched_rows: %d\n", preview.total)
	if len(preview.distribution) > 0 {
		fmt.Fprintln(r.stdout, "top_chat_distribution:")
		for _, entry := range preview.distribution {
			fmt.Fprintf(r.stdout, "  - %v: %v\n", entry[0], entry[1])
		}
	} else {
		fmt.Fprintln(r.stdout, "top_chat_distribution: (none)")
	}
	if len(preview.samples) > 0 {
		fmt.Fprintf(r.stdout, "samples (first %d):\n", len(preview.samples))
		for _, row := range preview.samples {
			summary := truncateText(strings.TrimSpace(pyString(row.Summary)), 60)
			theme := truncateText(strings.TrimSpace(pyString(row.Theme)), 30)
			fmt.Fprintf(r.stdout, "  - id=%d chat_id=%s [%s ~ %s] theme=%q summary=%q\n", row.ID, pyString(row.ChatID), formatTS(pythonTimestamp(row.StartTime)), formatTS(pythonTimestamp(row.EndTime)), theme, summary)
		}
	}
	fmt.Fprintln(r.stdout, "=========================")
	fmt.Fprintln(r.stdout)
}

// confirm 对齐 _confirm
func (r *migrateRunner) confirm() bool {
	fmt.Fprint(r.stdout, "确认按以上筛选执行迁移？输入 y 继续 [y/N]: ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// selectionFingerprint 对齐 SelectionFilter.fingerprint_payload
func selectionFingerprint(selection *migrateSelection) map[string]any {
	if selection == nil {
		return map[string]any{}
	}
	streamIDs := append([]string{}, selection.streamIDs...)
	sort.Strings(streamIDs)
	return map[string]any{
		"time_from_ts": selection.timeFromTS, "time_to_ts": selection.timeToTS,
		"time_from_raw": selection.timeFromRaw, "time_to_raw": selection.timeToRaw,
		"stream_ids": streamIDs, "stream_filter_requested": selection.streamFilterUsed,
		"start_id": selection.startID, "end_id": selection.endID,
	}
}

// migrateArgsPayload 对齐 vars(args) 的键名
func migrateArgsPayload(options *migrateOptions) map[string]any {
	return map[string]any{
		"source_db": options.sourceDB, "target_data_dir": options.targetDataDir, "no_resume": options.noResume,
		"reset_state": options.resetState, "start_id": options.startID, "end_id": options.endID,
		"read_batch_size": options.readBatchSize, "commit_window_rows": options.commitWindowRows,
		"embed_batch_size": options.embedBatchSize, "entity_embed_batch_size": options.entityEmbedBatch,
		"embed_workers": options.embedWorkers, "max_errors": options.maxErrors, "log_every": options.logEvery,
		"dry_run": options.dryRun, "verify_only": options.verifyOnly, "time_from": options.timeFrom,
		"time_to": options.timeTo, "stream_id": options.streamIDs, "group_id": options.groupIDs,
		"user_id": options.userIDs, "yes": options.yes, "preview_limit": options.previewLimit,
	}
}

// formatTS 对齐 _format_ts
func formatTS(ts *float64) string {
	if ts == nil {
		return "-"
	}
	seconds := int64(*ts)
	nanos := int64((*ts - float64(seconds)) * 1e9)
	return time.Unix(seconds, nanos).Local().Format("2006-01-02 15:04:05")
}

// unixNow 返回当前 Unix 秒
func unixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// copyStats 复制统计字典
func copyStats(stats map[string]any) map[string]any {
	out := make(map[string]any, len(stats))
	for key, value := range stats {
		out[key] = value
	}
	return out
}

// intOrZero 对齐 _safe_int(value, 0)
func intOrZero(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return utils.PyIntOrZero(value)
	}
}

// writeFileAtomic 对齐 _dump_json_atomic
func writeFileAtomic(path string, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
