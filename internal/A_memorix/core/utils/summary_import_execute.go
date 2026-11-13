package utils

// A_memorix 摘要写入执行与去重收尾，对齐 SummaryImporter 的 _execute_import/_finish_fact_summary

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// summaryEntityVector 实体向量待写入项
type summaryEntityVector struct {
	hash   string
	name   string
	vector string
}

// ensureEntityVectors 对齐 _ensure_entity_vectors，失败时按 metadata-only 配置处理
func (i *SummaryImporter) ensureEntityVectors(ctx context.Context, hashes []string, names []string) error {
	if len(hashes) == 0 || !i.dualVectorPoolsEnabled() {
		return nil
	}
	target := i.graphVectorTarget()
	if target == nil || i.embedding == nil {
		return nil
	}
	pending := make([]summaryEntityVector, 0, len(hashes))
	for index, hashValue := range hashes {
		if index >= len(names) {
			break
		}
		hashValue = pyStrip(hashValue)
		name := pyStrip(names[index])
		if hashValue == "" || name == "" {
			continue
		}
		vectorID := graphVectorID("entity", hashValue)
		if target.Contains(vectorID) {
			continue
		}
		pending = append(pending, summaryEntityVector{hash: hashValue, name: name, vector: vectorID})
	}
	if len(pending) == 0 {
		return nil
	}
	batchSize := i.embeddingWriteBatchSize()
	for start := 0; start < len(pending); start += batchSize {
		end := start + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		batchNames := make([]string, 0, len(batch))
		batchIDs := make([]string, 0, len(batch))
		for _, item := range batch {
			batchNames = append(batchNames, item.name)
			batchIDs = append(batchIDs, item.vector)
		}
		vectors, err := i.embedding.EncodeBatch(ctx, batchNames)
		if err != nil {
			return i.handleEntityVectorError(len(batch), err)
		}
		if len(vectors) != len(batch) {
			return i.handleEntityVectorError(len(batch), fmt.Errorf("实体批量向量数量不匹配: %d vs %d", len(vectors), len(batch)))
		}
		if _, err := target.Add(vectors, batchIDs); err != nil {
			return i.handleEntityVectorError(len(batch), err)
		}
	}
	return nil
}

// handleEntityVectorError 按 metadata-only 配置吞掉实体向量错误
func (i *SummaryImporter) handleEntityVectorError(count int, err error) error {
	if !i.allowMetadataOnlyWrite() {
		return err
	}
	i.logger.Warning(fmt.Sprintf("总结导入实体批量向量写入失败，保留 metadata/graph: count=%d error=%v", count, err))
	return nil
}

// addEntities 执行图节点与实体入库并补写实体向量
func (i *SummaryImporter) addEntities(ctx context.Context, entities []string, paragraphHash string) error {
	if len(entities) == 0 {
		return nil
	}
	var hashes []string
	run := func() error {
		if i.graphStore != nil {
			if err := i.graphStore.AddNodes(entities); err != nil {
				return err
			}
		}
		var err error
		hashes, err = i.store.AddEntitiesForParagraph(entities, paragraphHash)
		return err
	}
	var err error
	if i.graphStore != nil {
		err = i.graphStore.BatchUpdate(run)
	} else {
		err = run()
	}
	if err != nil {
		return err
	}
	return i.ensureEntityVectors(ctx, hashes, entities)
}

// relationVectorWriteEnabled 对齐 retrieval.relation_vectorization 读取
func (i *SummaryImporter) relationVectorWriteEnabled() bool {
	config := summaryNestedMap(i.pluginConfig, "retrieval", "relation_vectorization")
	enabled := false
	if value, exists := config["enabled"]; exists {
		enabled = isTruthy(value)
	}
	writeOnImport := true
	if value, exists := config["write_on_import"]; exists {
		writeOnImport = isTruthy(value)
	}
	return enabled && writeOnImport
}

// addRelations 写入关系并补向量关系
func (i *SummaryImporter) addRelations(ctx context.Context, relations []map[string]string, paragraphHash string) error {
	if len(relations) == 0 {
		return nil
	}
	tuples := make([][3]string, 0, len(relations))
	for _, relation := range relations {
		tuples = append(tuples, [3]string{relation["subject"], relation["predicate"], relation["object"]})
	}
	writeVector := i.relationVectorWriteEnabled()
	if i.relationWriter != nil {
		return i.relationWriter.UpsertRelationsWithVectors(ctx, tuples, 1.0, paragraphHash, writeVector)
	}
	var hashes []string
	run := func() error {
		var err error
		hashes, err = i.store.AddRelationsForParagraph(tuples, 1.0, paragraphHash)
		if err != nil {
			return err
		}
		if i.graphStore != nil && len(hashes) > 0 {
			edges := make([][3]string, 0, len(tuples))
			for _, tuple := range tuples {
				edges = append(edges, [3]string{tuple[0], tuple[2]})
			}
			return i.graphStore.AddEdges(edges, hashes)
		}
		return nil
	}
	if i.graphStore != nil {
		return i.graphStore.BatchUpdate(run)
	}
	return run()
}

// executeImport 对齐 _execute_import
func (i *SummaryImporter) executeImport(
	ctx context.Context,
	summary string,
	entities []string,
	relations []map[string]string,
	streamID string,
	timeMeta map[string]any,
	metadata map[string]any,
) (string, error) {
	externalID := pyStrip(importValueText(metadata["external_id"]))
	personIDs := summaryPersonIDsFromMetadata(metadata)
	if externalID != "" && i.host != nil {
		if host, ok := i.host.(SummaryIngestHost); ok {
			result, err := host.IngestText(ctx, SummaryIngestRequest{
				ExternalID:    externalID,
				SourceType:    "chat_summary",
				Text:          summary,
				ChatID:        streamID,
				PersonIDs:     personIDs,
				TimeStart:     OptionalFloat(timeMeta["event_time_start"]),
				TimeEnd:       OptionalFloat(timeMeta["event_time_end"]),
				Metadata:      metadata,
				Entities:      entities,
				Relations:     relations,
				RespectFilter: false,
			})
			if err != nil {
				return "", err
			}
			ref, err := i.store.GetExternalMemoryRef(externalID)
			if err != nil {
				return "", err
			}
			if ref != nil {
				if hashValue := pyStrip(importValueText(ref["paragraph_hash"])); hashValue != "" {
					return hashValue, nil
				}
			}
			if result != nil {
				if storedIDs, ok := result["stored_ids"].([]any); ok {
					for _, item := range storedIDs {
						if hashValue := pyStrip(importValueText(item)); hashValue != "" {
							return hashValue, nil
						}
					}
				}
				if storedIDs, ok := result["stored_ids"].([]string); ok {
					for _, item := range storedIDs {
						if hashValue := pyStrip(item); hashValue != "" {
							return hashValue, nil
						}
					}
				}
			}
			return "", fmt.Errorf("公共 ingest 未返回摘要段落: external_id=%s", externalID)
		}
	}
	source := "chat_summary:" + streamID
	knowledgeType := i.resolveImportKnowledgeType(summary)
	if knowledgeType == "" {
		return "", fmt.Errorf("摘要知识类型解析失败")
	}
	paragraphHash, err := i.store.AddParagraph(summary, nil, source, metadata, knowledgeType, timeMeta)
	if err != nil {
		return "", err
	}
	if err := i.writeParagraphVector(ctx, paragraphHash, summary); err != nil {
		return "", err
	}
	if err := i.addEntities(ctx, entities, paragraphHash); err != nil {
		return "", err
	}
	if err := i.addRelations(ctx, relations, paragraphHash); err != nil {
		return "", err
	}
	shortHash := paragraphHash
	if len(shortHash) > 8 {
		shortHash = shortHash[:8]
	}
	i.logger.Info(fmt.Sprintf("总结导入完成: hash=%s", shortHash))
	return paragraphHash, nil
}

// summaryPersonIDsFromMetadata 读取 metadata.person_ids
func summaryPersonIDsFromMetadata(metadata map[string]any) []string {
	raw, exists := metadata["person_ids"]
	if !exists || raw == nil {
		return []string{}
	}
	switch typed := raw.(type) {
	case []string:
		return append([]string{}, typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, pyStr(item))
		}
		return out
	default:
		return []string{}
	}
}

// resolveImportKnowledgeType 解析摘要默认知识类型，非法时回退 narrative
func (i *SummaryImporter) resolveImportKnowledgeType(summary string) string {
	raw := summaryConfigValue(i.pluginConfig, "narrative", "summarization", "default_knowledge_type")
	typeText := pyStrip(importValueText(raw))
	if typeText == "" {
		typeText = "narrative"
	}
	resolved, err := resolveImportKnowledgeType(i.knowledgeTypes, typeText, summary)
	if err != nil {
		i.logger.Warning(fmt.Sprintf("非法 summarization.default_knowledge_type=%s，回退 narrative", typeText))
		return "narrative"
	}
	return resolved
}

// writeParagraphVector 对齐 write_paragraph_vector_or_enqueue 与本地向量写入分支
func (i *SummaryImporter) writeParagraphVector(ctx context.Context, paragraphHash string, summary string) error {
	if paragraphHash == "" || summary == "" {
		return nil
	}
	if i.host != nil {
		if writer, ok := i.host.(SummaryVectorWriter); ok {
			result, err := writer.WriteParagraphVectorOrEnqueue(ctx, paragraphHash, summary, "summary_import")
			if err != nil {
				return err
			}
			if warning := pyStrip(importValueText(result["warning"])); warning != "" {
				i.logger.Warning(fmt.Sprintf("总结导入段落进入回退写入: %v", result))
			}
			return nil
		}
	}
	if i.vectorStore == nil || i.embedding == nil {
		if i.allowMetadataOnlyWrite() {
			i.logger.Warning("总结导入段落向量写入失败，改为回填队列: 向量依赖未初始化")
			return i.store.EnqueueParagraphVectorBackfill(paragraphHash, "vector_runtime_components_missing")
		}
		return fmt.Errorf("向量写入依赖未初始化")
	}
	vector, err := i.embedding.Encode(ctx, summary)
	if err != nil {
		if !i.allowMetadataOnlyWrite() {
			return err
		}
		i.logger.Warning(fmt.Sprintf("总结导入段落向量写入失败，改为回填队列: %v", err))
		return i.store.EnqueueParagraphVectorBackfill(paragraphHash, err.Error())
	}
	if _, err := i.vectorStore.Add([][]float32{vector}, []string{paragraphHash}); err != nil {
		if !i.allowMetadataOnlyWrite() {
			return err
		}
		i.logger.Warning(fmt.Sprintf("总结导入段落向量写入失败，改为回填队列: %v", err))
		return i.store.EnqueueParagraphVectorBackfill(paragraphHash, err.Error())
	}
	return nil
}

// finishFactSummary 对齐 _finish_fact_summary
func (i *SummaryImporter) finishFactSummary(ctx context.Context, payload summaryImportPayload) (SummaryImportResult, error) {
	plan := summaryPlanFromPayload(payload)
	streamID := pyStrip(plan.ScopeID)
	metadata := summaryMetadata(payload.Metadata)
	externalID := pyStrip(importValueText(metadata["external_id"]))
	decisions := make(map[string]summaryPayloadDecision, len(payload.Plan.Decisions))
	for _, decision := range payload.Plan.Decisions {
		decisions[decision.FactID] = decision
	}
	kept := make([]summaryPayloadFact, 0, len(payload.Plan.Facts))
	for _, fact := range payload.Plan.Facts {
		decision, exists := decisions[fact.FactID]
		if !exists {
			return SummaryImportResult{}, fmt.Errorf("事实去重计划缺少决定: %s", fact.FactID)
		}
		if decision.Action == "reuse" {
			continue
		}
		kept = append(kept, fact)
	}
	paragraphHash := ""
	if len(kept) > 0 {
		texts := make([]string, 0, len(kept))
		personIDSet := map[string]struct{}{}
		personIDs := make([]string, 0, len(kept))
		factTexts := make([]string, 0, len(kept))
		for _, fact := range kept {
			texts = append(texts, fact.Text)
			factTexts = append(factTexts, fact.Text)
			for _, personID := range fact.PersonIDs {
				if _, exists := personIDSet[personID]; exists {
					continue
				}
				personIDSet[personID] = struct{}{}
				personIDs = append(personIDs, personID)
			}
		}
		sort.Strings(personIDs)
		text := strings.Join(texts, "\n")
		mergedMetadata := summaryMetadata(metadata)
		mergedMetadata["person_ids"] = personIDs
		mergedMetadata["identity_bindings"] = filterIdentityBindings(metadata["identity_bindings"], personIDSet)
		mergedMetadata["dedup_managed"] = true
		mergedMetadata["dedup_phase"] = "pending"
		mergedMetadata["dedup_fact_texts"] = factTexts
		keptIDs := make(map[string]struct{}, len(kept))
		for _, fact := range kept {
			keptIDs[fact.FactID] = struct{}{}
		}
		entities := make([]string, 0, len(payload.Entities))
		for _, name := range payload.Entities {
			if strings.Contains(text, name) {
				entities = append(entities, name)
			}
		}
		relations := make([]map[string]string, 0, len(payload.Relations))
		for index, relation := range payload.Relations {
			support, exists := plan.RelationSupport[strconv.Itoa(index)]
			if !exists || !summaryIDsIntersect(keptIDs, support) {
				continue
			}
			relations = append(relations, relation)
		}
		hashValue, err := i.executeImport(ctx, text, entities, relations, streamID, payload.TimeMeta, mergedMetadata)
		if err != nil {
			return SummaryImportResult{}, err
		}
		paragraphHash = hashValue
	}
	allowed := summaryAllowedImageSet(payload)
	if _, err := i.linkSummaryImageEvidence(streamID, paragraphHash, kept, allowed); err != nil {
		return SummaryImportResult{}, err
	}
	dedup := i.newDeduplicator("")
	if dedup == nil {
		return SummaryImportResult{}, fmt.Errorf("事实去重器未初始化")
	}
	claimIDs, err := dedup.Commit(ctx, plan, paragraphHash, nil)
	if err != nil {
		return SummaryImportResult{}, err
	}
	for index, decision := range payload.Plan.Decisions {
		if decision.Action != "reuse" {
			continue
		}
		if index >= len(claimIDs) {
			return SummaryImportResult{}, fmt.Errorf("事实去重提交结果数量与决定不一致: %d vs %d", len(claimIDs), len(payload.Plan.Decisions))
		}
		evidence, err := i.store.GetFactEvidence(claimIDs[index])
		if err != nil {
			return SummaryImportResult{}, err
		}
		target := ""
		for _, item := range evidence {
			if pyStrip(pyStr(item["evidence_type"])) == "paragraph" {
				target = pyStrip(importValueText(item["evidence_id"]))
				break
			}
		}
		fact := summaryPayloadFact{FactID: decision.FactID}
		for _, candidate := range payload.Plan.Facts {
			if candidate.FactID == decision.FactID {
				fact = candidate
				break
			}
		}
		if _, err := i.linkSummaryImageEvidence(streamID, target, []summaryPayloadFact{fact}, allowed); err != nil {
			return SummaryImportResult{}, err
		}
	}
	if err := i.persistVectorStore(i.vectorStore); err != nil {
		return SummaryImportResult{}, err
	}
	if i.graphStore != nil {
		if err := i.graphStore.Save(); err != nil {
			return SummaryImportResult{}, err
		}
	}
	if paragraphHash != "" {
		if err := i.completeDedupBookkeeping(metadata, payload, plan, kept, claimIDs, streamID, externalID, paragraphHash, i.personProfileEnabled()); err != nil {
			return SummaryImportResult{}, err
		}
	}
	triggerCount := summaryIntValue(metadata["trigger_message_count"], 0)
	if externalID != "" {
		if err := i.store.RecordSummaryCheckpoint(externalID, streamID, triggerCount, "complete", nil); err != nil {
			return SummaryImportResult{}, err
		}
	}
	detail := "该窗口事实均已存在，已追加来源证据"
	if len(kept) > 0 {
		detail = "事实去重后总结写入完成"
	}
	return SummaryImportResult{
		Success:       true,
		Detail:        detail,
		ParagraphHash: paragraphHash,
		Source:        "chat_summary:" + streamID,
		Skipped:       len(kept) == 0,
	}, nil
}

// newDeduplicator 通过工厂或默认实现创建去重器
func (i *SummaryImporter) newDeduplicator(taskName string) *FactDeduplicator {
	if i.dedupFactory != nil {
		return i.dedupFactory(taskName)
	}
	return NewFactDeduplicator(i.store, nil, nil, nil, nil, taskName)
}

// filterIdentityBindings 对齐按 kept person_ids 过滤身份绑定
func filterIdentityBindings(raw any, personIDs map[string]struct{}) map[string]any {
	out := map[string]any{}
	items, ok := importObjectMap(raw)
	if !ok {
		return out
	}
	for name, value := range items {
		item, ok := importObjectMap(value)
		if !ok {
			continue
		}
		personID := pyStrip(importValueText(item["person_id"]))
		if personID == "" {
			continue
		}
		if _, exists := personIDs[personID]; exists {
			out[name] = value
		}
	}
	return out
}

// summaryIDsIntersect 判断两个 fact_id 集合是否相交
func summaryIDsIntersect(ids map[string]struct{}, values []string) bool {
	for _, value := range values {
		if _, exists := ids[value]; exists {
			return true
		}
	}
	return false
}

// personProfileEnabled 对齐 person_profile.enabled 默认 True
func (i *SummaryImporter) personProfileEnabled() bool {
	value := summaryConfigValue(i.pluginConfig, true, "person_profile", "enabled")
	return isTruthy(value)
}
