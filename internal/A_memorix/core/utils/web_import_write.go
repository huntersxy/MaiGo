package utils

// Web 导入元数据、向量与关系写入，对齐 web_import_manager.py 的写入辅助

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/strategies"
)

// sourceLabel 对齐 _source_label
func (m *WebImportManager) sourceLabel(file *WebImportFileRecord) string {
	if strings.TrimSpace(file.SourcePath) != "" {
		return file.SourceKind + ":" + file.SourcePath
	}
	return "web_import:" + file.Name
}

// recordFileSource 对齐 _record_file_source
func (m *WebImportManager) recordFileSource(file *WebImportFileRecord, source string) {
	text := strings.TrimSpace(source)
	if text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range file.ImportedSources {
		if existing == text {
			return
		}
	}
	file.ImportedSources = append(file.ImportedSources, text)
}

// addParagraphMetadata 对齐 _add_paragraph_metadata
func (m *WebImportManager) addParagraphMetadata(file *WebImportFileRecord, content string, source string, metadata map[string]any, knowledgeType string, timeMeta map[string]any) (string, error) {
	if m.deps.Store == nil {
		return "", fmt.Errorf("metadata_store 未初始化")
	}
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	hashValue, err := m.deps.Store.AddParagraph(content, source, metadata, knowledgeType, timeMeta)
	if err != nil {
		return "", err
	}
	m.recordFileSource(file, source)
	return hashValue, nil
}

// enqueueParagraphBackfill 对齐 _enqueue_paragraph_backfill
func (m *WebImportManager) enqueueParagraphBackfill(paragraphHash string, errorText string) {
	if strings.TrimSpace(paragraphHash) == "" {
		return
	}
	if m.deps.EnqueueBackfill != nil {
		if err := m.deps.EnqueueBackfill(paragraphHash, errorText); err == nil {
			return
		} else {
			m.deps.Logger.Warning(fmt.Sprintf("回填入队失败（runtime facade）: %v", err))
		}
	}
	m.deps.Logger.Warning("回填入队失败（metadata_store）: 未注入回填能力")
}

// enqueueParagraphBackfillLocked 对齐 _enqueue_paragraph_backfill_locked
func (m *WebImportManager) enqueueParagraphBackfillLocked(paragraphHash string, errorText string) {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	m.enqueueParagraphBackfill(paragraphHash, errorText)
}

// embeddingDegraded 对齐 _is_embedding_degraded
func (m *WebImportManager) embeddingDegraded() bool {
	if m.deps.EmbeddingDegraded == nil {
		return false
	}
	return m.deps.EmbeddingDegraded()
}

// writeParagraphVectorOrEnqueue 对齐 _write_paragraph_vector_or_enqueue
func (m *WebImportManager) writeParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextName string) (map[string]any, error) {
	token := strings.TrimSpace(paragraphHash)
	text := strings.TrimSpace(content)
	if token == "" || text == "" {
		return map[string]any{"success": false, "vector_written": false, "queued": false, "warning": "", "detail": "invalid_paragraph_input"}, nil
	}
	target := m.paragraphVectorStore()
	if target == nil || m.deps.Embedding == nil {
		if !m.allowMetadataOnlyWrite() {
			return nil, fmt.Errorf("向量写入依赖未初始化")
		}
		m.enqueueParagraphBackfillLocked(token, "vector_runtime_components_missing")
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": "vector_runtime_components_missing"}, nil
	}
	if m.embeddingDegraded() {
		if !m.allowMetadataOnlyWrite() {
			return nil, fmt.Errorf("embedding 处于降级态且 metadata-only 写入被禁用")
		}
		m.enqueueParagraphBackfillLocked(token, "embedding_degraded")
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": "embedding_degraded"}, nil
	}
	if target.Contains(token) {
		return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": "vector_already_exists"}, nil
	}
	vector, err := m.deps.Embedding.Encode(ctx, text)
	if err != nil {
		if !m.allowMetadataOnlyWrite() {
			return nil, err
		}
		m.enqueueParagraphBackfillLocked(token, err.Error())
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": fmt.Sprintf("%s vector write failed: %v", contextName, err)}, nil
	}
	if target.Contains(token) {
		return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": "vector_already_exists_after_encode"}, nil
	}
	added, err := target.Add([][]float32{vector}, []string{token})
	if err != nil {
		if !m.allowMetadataOnlyWrite() {
			return nil, err
		}
		m.enqueueParagraphBackfillLocked(token, err.Error())
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": fmt.Sprintf("%s vector write failed: %v", contextName, err)}, nil
	}
	if added == 0 {
		return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": "vector_already_exists"}, nil
	}
	return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": ""}, nil
}

// persistProcessedChunk 对齐 _persist_processed_chunk
func (m *WebImportManager) persistProcessedChunk(ctx context.Context, file *WebImportFileRecord, processed strategies.ProcessedChunk, metadata map[string]any, timeMeta map[string]any) error {
	content := processed.Chunk.Text
	if IsProbableHashToken(content) {
		m.deps.Logger.Warning(fmt.Sprintf("跳过疑似哈希段落写入: source=%s preview=%s", m.sourceLabel(file), webImportTruncate(content, 32)))
		return nil
	}
	data, err := webImportCoerceImportDataDict(processed.Data, "分块抽取结果")
	if err != nil {
		return err
	}
	paragraphMetadata := map[string]any{}
	for key, value := range metadata {
		paragraphMetadata[key] = value
	}
	events := NormalizeEntityImportList(data["events"])
	if len(events) > 0 {
		paragraphMetadata["extracted_events"] = events
	}
	paraHash, err := m.addParagraphMetadata(file, content, m.sourceLabel(file), paragraphMetadata, webImportStorageTypeFromStrategy(processed.Type), timeMeta)
	if err != nil {
		return err
	}
	result, err := m.writeParagraphVectorOrEnqueue(ctx, paraHash, content, "web_import_text")
	if err != nil {
		return err
	}
	if strings.TrimSpace(importValueText(result["warning"])) != "" {
		m.deps.Logger.Warning(fmt.Sprintf("web_import text paragraph 向量写入降级: hash=%s detail=%v", webImportHashPrefix(paraHash), result["detail"]))
	}
	entities := []string{}
	relations := [][3]string{}
	for _, triple := range NormalizeRelationImportList(data["triples"]) {
		relations = append(relations, [3]string{triple["subject"], triple["predicate"], triple["object"]})
		entities = append(entities, triple["subject"], triple["object"])
	}
	for _, relation := range NormalizeRelationImportList(data["relations"]) {
		relations = append(relations, [3]string{relation["subject"], relation["predicate"], relation["object"]})
		entities = append(entities, relation["subject"], relation["object"])
	}
	entities = append(entities, NormalizeEntityImportList(data["entities"])...)
	entities = append(entities, NormalizeEntityImportList(data["verbatim_entities"])...)
	uniq := []string{}
	seen := map[string]struct{}{}
	for _, name := range entities {
		text := strings.TrimSpace(name)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		uniq = append(uniq, text)
	}
	if _, err := m.addEntitiesWithVectors(ctx, uniq, paraHash); err != nil {
		return err
	}
	_, err = m.addRelationsWithVectors(ctx, relations, paraHash)
	return err
}

// addEntitiesWithVectors 对齐 _add_entities_with_vectors
func (m *WebImportManager) addEntitiesWithVectors(ctx context.Context, names []string, sourceParagraph string) (map[string]string, error) {
	normalized := []string{}
	for _, name := range names {
		if token := strings.TrimSpace(name); token != "" {
			normalized = append(normalized, token)
		}
	}
	if len(normalized) == 0 {
		return map[string]string{}, nil
	}
	entityHashes := make([][2]string, 0, len(normalized))
	m.storageMu.Lock()
	var batchErr error
	if m.deps.Graph == nil || m.deps.Store == nil {
		batchErr = fmt.Errorf("graph_store 或 metadata_store 未初始化")
	} else {
		batchErr = m.deps.Graph.BatchUpdate(func() error {
			if err := m.deps.Graph.AddNodes(normalized); err != nil {
				return err
			}
			hashes, err := m.deps.Store.AddEntitiesBatch(normalized, sourceParagraph)
			if err != nil {
				return err
			}
			if len(hashes) != len(normalized) {
				return fmt.Errorf("实体哈希数量不匹配: %d vs %d", len(hashes), len(normalized))
			}
			for index, name := range normalized {
				entityHashes = append(entityHashes, [2]string{name, hashes[index]})
			}
			return nil
		})
	}
	if batchErr != nil {
		m.storageMu.Unlock()
		return nil, batchErr
	}
	target := m.graphVectorStore()
	pending := make([][3]string, 0, len(entityHashes))
	for _, pair := range entityHashes {
		vectorID := m.graphVectorID("entity", pair[1])
		if target != nil && !target.Contains(vectorID) {
			pending = append(pending, [3]string{vectorID, pair[0], pair[1]})
		}
	}
	m.storageMu.Unlock()
	for offset := 0; offset < len(pending); offset += m.embeddingWriteBatchSize() {
		end := offset + m.embeddingWriteBatchSize()
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[offset:end]
		if err := m.writeEntityVectorBatch(ctx, batch, target); err != nil {
			if !m.allowMetadataOnlyWrite() {
				return nil, err
			}
			m.deps.Logger.Warning(fmt.Sprintf("实体批量向量写入降级，保留 metadata/graph: count=%d error=%v", len(batch), err))
		}
	}
	out := make(map[string]string, len(entityHashes))
	for _, pair := range entityHashes {
		out[pair[0]] = pair[1]
	}
	return out, nil
}

// writeEntityVectorBatch 写入一批实体向量
func (m *WebImportManager) writeEntityVectorBatch(ctx context.Context, batch [][3]string, target WebImportVectorStore) error {
	if target == nil {
		return fmt.Errorf("graph_vector_store_missing")
	}
	if m.embeddingDegraded() {
		return fmt.Errorf("embedding_degraded")
	}
	if m.deps.Embedding == nil {
		return fmt.Errorf("embedding_manager_missing")
	}
	names := make([]string, 0, len(batch))
	for _, item := range batch {
		names = append(names, item[1])
	}
	vectors, err := m.deps.Embedding.EncodeBatch(ctx, names)
	if err != nil {
		return err
	}
	if len(vectors) != len(batch) {
		return fmt.Errorf("实体批量向量数量不匹配: %d vs %d", len(vectors), len(batch))
	}
	ids := make([]string, 0, len(batch))
	for _, item := range batch {
		ids = append(ids, item[0])
	}
	_, err = target.Add(vectors, ids)
	return err
}

// addRelationsWithVectors 对齐 _add_relations_with_vectors
func (m *WebImportManager) addRelationsWithVectors(ctx context.Context, relations [][3]string, sourceParagraph string) ([]string, error) {
	normalized := make([][3]string, 0, len(relations))
	for _, relation := range relations {
		subject, predicate, object := strings.TrimSpace(relation[0]), strings.TrimSpace(relation[1]), strings.TrimSpace(relation[2])
		if subject == "" || predicate == "" || object == "" {
			continue
		}
		normalized = append(normalized, [3]string{subject, predicate, object})
	}
	if len(normalized) == 0 {
		return []string{}, nil
	}
	if m.deps.RelationWriter == nil || m.embeddingDegraded() {
		hashes := make([]string, 0, len(normalized))
		for _, relation := range normalized {
			hashValue, err := m.addRelation(ctx, relation[0], relation[1], relation[2], sourceParagraph)
			if err != nil {
				return nil, err
			}
			hashes = append(hashes, hashValue)
		}
		return hashes, nil
	}
	entityNames := make([]string, 0, len(normalized)*2)
	for _, relation := range normalized {
		entityNames = append(entityNames, relation[0], relation[2])
	}
	if _, err := m.addEntitiesWithVectors(ctx, entityNames, sourceParagraph); err != nil {
		return nil, err
	}
	writeVector := m.relationVectorWriteEnabled()
	if err := m.deps.RelationWriter.UpsertRelationsWithVectors(ctx, normalized, 1.0, sourceParagraph, writeVector); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// relationVectorWriteEnabled 对齐 relation_vectorization 配置
func (m *WebImportManager) relationVectorWriteEnabled() bool {
	raw := m.cfg("retrieval.relation_vectorization", map[string]any{})
	config, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	return webImportCoerceBool(config["enabled"], false) && webImportCoerceBool(config["write_on_import"], true)
}

// buildRelationVectorText 对齐 RelationWriteService.build_relation_vector_text
func buildRelationVectorText(subject string, predicate string, object string) string {
	return fmt.Sprintf("%s %s %s\n%s和%s的关系是%s", subject, predicate, object, subject, object, predicate)
}

// addRelation 对齐 _add_relation
func (m *WebImportManager) addRelation(ctx context.Context, subject string, predicate string, object string, sourceParagraph string) (string, error) {
	subjectToken := strings.TrimSpace(subject)
	predicateToken := strings.TrimSpace(predicate)
	objectToken := strings.TrimSpace(object)
	if subjectToken == "" || predicateToken == "" || objectToken == "" {
		return "", nil
	}
	if _, err := m.addEntitiesWithVectors(ctx, []string{subjectToken}, sourceParagraph); err != nil {
		return "", err
	}
	if _, err := m.addEntitiesWithVectors(ctx, []string{objectToken}, sourceParagraph); err != nil {
		return "", err
	}
	writeVector := m.relationVectorWriteEnabled()
	m.storageMu.Lock()
	if m.deps.Store == nil || m.deps.Graph == nil {
		m.storageMu.Unlock()
		return "", fmt.Errorf("metadata_store 或 graph_store 未初始化")
	}
	relationHash, err := m.deps.Store.AddRelation(subjectToken, predicateToken, objectToken, 1.0, sourceParagraph)
	if err != nil {
		m.storageMu.Unlock()
		return "", err
	}
	if err := m.deps.Graph.AddEdges([][3]string{{subjectToken, predicateToken, objectToken}}, []string{relationHash}); err != nil {
		m.storageMu.Unlock()
		return "", err
	}
	target := m.graphVectorStore()
	vectorID := m.graphVectorID("relation", relationHash)
	vectorExists := target != nil && target.Contains(vectorID)
	state := "pending"
	if vectorExists {
		state = "ready"
	}
	_ = m.deps.Store.SetRelationVectorState(relationHash, state, "", false)
	m.storageMu.Unlock()
	if !writeVector || vectorExists {
		return relationHash, nil
	}
	if err := m.writeRelationVector(ctx, relationHash, vectorID, subjectToken, predicateToken, objectToken, target); err != nil {
		return relationHash, nil
	}
	return relationHash, nil
}

// writeRelationVector 写入单条关系向量并维护状态
func (m *WebImportManager) writeRelationVector(ctx context.Context, relationHash string, vectorID string, subject string, predicate string, object string, target WebImportVectorStore) error {
	var writeErr error
	if target == nil {
		writeErr = fmt.Errorf("graph_vector_store_missing")
	} else if m.deps.Embedding == nil {
		writeErr = fmt.Errorf("embedding_manager_missing")
	} else {
		vector, err := m.deps.Embedding.Encode(ctx, buildRelationVectorText(subject, predicate, object))
		if err == nil {
			if target.Contains(vectorID) {
				return m.setRelationVectorState(relationHash, "ready", "", false)
			}
			added, addErr := target.Add([][]float32{vector}, []string{vectorID})
			if addErr != nil {
				writeErr = addErr
			} else if added == 0 && !target.Contains(vectorID) {
				writeErr = fmt.Errorf("relation vector add returned 0 without existing vector")
			} else {
				return m.setRelationVectorState(relationHash, "ready", "", false)
			}
		} else {
			writeErr = err
		}
	}
	if target != nil && target.Contains(vectorID) {
		return m.setRelationVectorState(relationHash, "ready", "", false)
	}
	_ = m.setRelationVectorState(relationHash, "failed", writeErr.Error(), true)
	m.deps.Logger.Warning(fmt.Sprintf("关系向量写入降级，保留 metadata/graph: relation=%s error=%v", webImportTruncate(relationHash, 16), writeErr))
	return nil
}

// setRelationVectorState 对齐 _set_relation_vector_state_locked
func (m *WebImportManager) setRelationVectorState(relationHash string, state string, errorText string, bumpRetry bool) error {
	if m.deps.Store == nil {
		return nil
	}
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	if err := m.deps.Store.SetRelationVectorState(relationHash, state, errorText, bumpRetry); err != nil && state != "none" {
		return err
	}
	return nil
}
