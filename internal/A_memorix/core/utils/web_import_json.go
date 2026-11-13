package utils

// Web 导入 JSON 单元构建与写入，对齐 web_import_manager.py 的 JSON 流程

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// webImportJSONUnit JSON 导入单元
type webImportJSONUnit struct {
	ChunkID       string
	Kind          string
	Content       string
	TimeMeta      map[string]any
	KnowledgeType string
	ChunkType     string
	Source        string
	PersonIDs     []string
	Entities      []string
	Relations     []map[string]string
	Preview       string
	Name          string
	Subject       string
	Predicate     string
	Object        string
}

// processJSONFile 对齐 _process_json_file
func (m *WebImportManager) processJSONFile(ctx context.Context, taskID string, file *WebImportFileRecord, content string, chunkSem chan struct{}) error {
	m.setFileStrategy(taskID, file.FileID, "json")
	m.setFileState(taskID, file.FileID, WebImportFileSplitting, "splitting")
	if err := m.ensureEmbeddingRuntimeReady(ctx); err != nil {
		return err
	}
	var data any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return fmt.Errorf("JSON 解析失败: %v", err)
	}
	schema, err := webImportDetectJSONSchema(data)
	if err != nil {
		return err
	}
	m.webImportSetSchemaDetected(taskID, schema)
	params := m.webImportTaskParams(taskID)
	paragraphMetadata, err := webImportChatMetadataFromParams(params)
	if err != nil {
		return err
	}
	units, warnings := webImportBuildJSONUnits(data, file.FileID, file.Name, schema, m.webImportKnowledgeTypeResolver())
	m.appendFileWarnings(taskID, file.FileID, warnings)
	m.webImportRegisterJSONUnits(taskID, file.FileID, units)
	m.setFileState(taskID, file.FileID, WebImportFileExtracting, "extracting")
	var wg sync.WaitGroup
	errs := make([]error, len(units))
	for index, unit := range units {
		wg.Add(1)
		go func(index int, unit webImportJSONUnit) {
			defer wg.Done()
			errs[index] = m.processJSONUnit(ctx, taskID, file, unit, chunkSem, paragraphMetadata)
		}(index, unit)
	}
	wg.Wait()
	for index, unitErr := range errs {
		if unitErr != nil {
			m.setChunkFailed(taskID, file.FileID, units[index].ChunkID, fmt.Sprintf("JSON 单元处理失败: %v", unitErr))
		}
	}
	if m.isCancelRequested(taskID) {
		m.setFileCancelled(taskID, file.FileID, "任务已取消")
		return nil
	}
	m.setFileState(taskID, file.FileID, WebImportFileSaving, "saving")
	m.storageMu.Lock()
	m.saveRuntimeStoresLocked()
	m.storageMu.Unlock()
	return m.webImportFinalizeFile(taskID, file.FileID)
}

// webImportDetectJSONSchema 对齐 _detect_json_schema
func webImportDetectJSONSchema(data any) (string, error) {
	object, ok := data.(map[string]any)
	if !ok {
		return "", fmt.Errorf("不支持的 JSON 格式：需要 paragraphs 或 docs")
	}
	if docs, ok := object["docs"].([]any); ok {
		_ = docs
		return "lpmm_openie", nil
	}
	paragraphs, ok := object["paragraphs"].([]any)
	if !ok {
		return "", fmt.Errorf("不支持的 JSON 格式：需要 paragraphs 或 docs")
	}
	keys := []string{"entities", "relations", "time_meta", "source", "type", "knowledge_type", "person_ids"}
	for _, paragraph := range paragraphs {
		item, ok := paragraph.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range keys {
			if _, exists := item[key]; exists {
				return "script_json", nil
			}
		}
	}
	return "web_json", nil
}

// webImportBuildJSONUnits 对齐 _build_json_units
func webImportBuildJSONUnits(data any, fileID string, filename string, schema string, resolve KnowledgeTypeResolver) ([]webImportJSONUnit, []string) {
	units := []webImportJSONUnit{}
	warnings := []string{}
	paragraphs := []any{}
	entities := []any{}
	relations := []any{}
	object, _ := data.(map[string]any)
	if schema == "web_json" || schema == "script_json" {
		if value, ok := object["paragraphs"].([]any); ok {
			paragraphs = value
		}
		if value, ok := object["entities"].([]any); ok {
			entities = value
		}
		if value, ok := object["relations"].([]any); ok {
			relations = value
		}
	} else if schema == "lpmm_openie" {
		docs, _ := object["docs"].([]any)
		for _, doc := range docs {
			item, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			content := strings.TrimSpace(importValueText(item["passage"]))
			if content == "" {
				continue
			}
			triples, _ := item["extracted_triples"].([]any)
			rels := []any{}
			for _, triple := range triples {
				values, ok := triple.([]any)
				if !ok || len(values) != 3 {
					continue
				}
				rels = append(rels, map[string]any{
					"subject":   importValueText(values[0]),
					"predicate": importValueText(values[1]),
					"object":    importValueText(values[2]),
				})
			}
			paraItem := map[string]any{
				"content":        content,
				"source":         "lpmm_openie:" + filename,
				"entities":       item["extracted_entities"],
				"relations":      rels,
				"knowledge_type": "factual",
			}
			paragraphs = append(paragraphs, paraItem)
		}
	}
	for index, paragraph := range paragraphs {
		item, err := NormalizeParagraphImportItem(paragraph, "web_import:"+filename, resolve)
		if err != nil {
			if validation, ok := err.(*ImportPayloadValidationError); ok {
				warnings = append(warnings, fmt.Sprintf("跳过段落[%d]：%v (code=%s)", index, err, validation.Code))
			} else {
				warnings = append(warnings, fmt.Sprintf("跳过段落[%d]：%v (code=unknown)", index, err))
			}
			continue
		}
		chunkType := item.KnowledgeType
		units = append(units, webImportJSONUnit{
			ChunkID:       fmt.Sprintf("%s_json_%d", fileID, len(units)),
			Kind:          "paragraph",
			Content:       item.Content,
			TimeMeta:      item.TimeMeta,
			KnowledgeType: item.KnowledgeType,
			ChunkType:     chunkType,
			Source:        item.Source,
			PersonIDs:     item.PersonIDs,
			Entities:      item.Entities,
			Relations:     item.Relations,
			Preview:       webImportTruncate(item.Content, 120),
		})
	}
	for index, entity := range entities {
		name, ok := NormalizeEntityImportItem(entity)
		if !ok {
			raw := webImportTruncate(strings.TrimSpace(importValueText(entity)), 80)
			warnings = append(warnings, fmt.Sprintf("跳过实体[%d]：无效名称或疑似哈希值 (%s)", index, raw))
			continue
		}
		units = append(units, webImportJSONUnit{
			ChunkID:   fmt.Sprintf("%s_json_%d", fileID, len(units)),
			Kind:      "entity",
			Name:      name,
			ChunkType: "entity",
			Preview:   webImportTruncate(name, 120),
		})
	}
	for index, relation := range relations {
		normalized, ok := NormalizeRelationImportItem(relation)
		if !ok {
			raw := ""
			if item, isMap := relation.(map[string]any); isMap {
				raw = strings.TrimSpace(importValueText(item["subject"])) + " | " + strings.TrimSpace(importValueText(item["predicate"])) + " | " + strings.TrimSpace(importValueText(item["object"]))
			} else {
				raw = strings.TrimSpace(importValueText(relation))
			}
			warnings = append(warnings, fmt.Sprintf("跳过关系[%d]：无效三元组或疑似哈希值 (%s)", index, webImportTruncate(raw, 120)))
			continue
		}
		units = append(units, webImportJSONUnit{
			ChunkID:   fmt.Sprintf("%s_json_%d", fileID, len(units)),
			Kind:      "relation",
			Subject:   normalized["subject"],
			Predicate: normalized["predicate"],
			Object:    normalized["object"],
			ChunkType: "relation",
			Preview:   webImportTruncate(normalized["subject"]+" "+normalized["predicate"]+" "+normalized["object"], 120),
		})
	}
	return units, warnings
}

// webImportRegisterJSONUnits 对齐 _register_json_units
func (m *WebImportManager) webImportRegisterJSONUnits(taskID string, fileID string, units []webImportJSONUnit) {
	records := make([]*WebImportChunkRecord, 0, len(units))
	for index, unit := range units {
		chunkType := unit.ChunkType
		if chunkType == "" {
			chunkType = "json"
		}
		record := NewWebImportChunkRecord(unit.ChunkID, index, chunkType, m.now())
		record.ContentPreview = unit.Preview
		records = append(records, record)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	file.Chunks = records
	file.TotalChunks = len(records)
	file.DoneChunks = 0
	file.FailedChunks = 0
	file.CancelledChunks = 0
	if len(records) == 0 {
		file.Progress = 1
	} else {
		file.Progress = 0
	}
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// processJSONUnit 对齐 _process_json_unit
func (m *WebImportManager) processJSONUnit(ctx context.Context, taskID string, file *WebImportFileRecord, unit webImportJSONUnit, chunkSem chan struct{}, paragraphMetadata map[string]any) error {
	chunkSem <- struct{}{}
	defer func() { <-chunkSem }()
	if m.isCancelRequested(taskID) {
		m.setChunkCancelled(taskID, file.FileID, unit.ChunkID, "任务已取消")
		return nil
	}
	m.setChunkState(taskID, file.FileID, unit.ChunkID, WebImportChunkWriting, "writing", 0.7)
	warnings := []string{}
	skipWrite := false
	switch unit.Kind {
	case "paragraph":
		content := unit.Content
		if strings.TrimSpace(content) == "" {
			warnings = append(warnings, fmt.Sprintf("跳过分块[%s]：段落内容为空", unit.ChunkID))
			skipWrite = true
		} else if IsProbableHashToken(content) {
			warnings = append(warnings, fmt.Sprintf("跳过分块[%s]：段落内容疑似哈希值", unit.ChunkID))
			skipWrite = true
		}
		kType := m.resolveKnowledgeType(unit.KnowledgeType, content)
		source := unit.Source
		if strings.TrimSpace(source) == "" {
			source = "web_import:" + file.Name
		}
		if !skipWrite {
			personIDs := make([]string, 0, len(unit.PersonIDs))
			for _, personID := range unit.PersonIDs {
				if token := strings.TrimSpace(personID); token != "" {
					personIDs = append(personIDs, token)
				}
			}
			unitMetadata := map[string]any{}
			for key, value := range paragraphMetadata {
				unitMetadata[key] = value
			}
			if len(personIDs) > 0 {
				unitMetadata["person_ids"] = personIDs
			}
			paraHash, err := m.addParagraphMetadata(file, content, source, unitMetadata, kType, unit.TimeMeta)
			if err != nil {
				m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: %v", err))
				return nil
			}
			if len(personIDs) > 0 && m.deps.RecordPersonEvidence != nil {
				m.deps.RecordPersonEvidence(personIDs, "web_import_json")
			}
			result, err := m.writeParagraphVectorOrEnqueue(ctx, paraHash, content, "web_import_json")
			if err != nil {
				m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: %v", err))
				return nil
			}
			if warning := strings.TrimSpace(importValueText(result["warning"])); warning != "" {
				m.deps.Logger.Warning(fmt.Sprintf("web_import json paragraph 向量写入降级: hash=%s detail=%v", webImportHashPrefix(paraHash), result["detail"]))
			}
			for _, name := range unit.Entities {
				if strings.TrimSpace(name) == "" {
					continue
				}
				if _, err := m.addEntitiesWithVectors(ctx, []string{strings.TrimSpace(name)}, paraHash); err != nil {
					m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: %v", err))
					return nil
				}
			}
			for _, relation := range unit.Relations {
				subject := strings.TrimSpace(relation["subject"])
				predicate := strings.TrimSpace(relation["predicate"])
				object := strings.TrimSpace(relation["object"])
				if subject == "" || predicate == "" || object == "" {
					continue
				}
				if _, err := m.addRelation(ctx, subject, predicate, object, paraHash); err != nil {
					m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: %v", err))
					return nil
				}
			}
		}
	case "entity":
		name := strings.TrimSpace(unit.Name)
		if name == "" {
			warnings = append(warnings, fmt.Sprintf("跳过分块[%s]：实体名为空", unit.ChunkID))
			skipWrite = true
		}
		if !skipWrite {
			if _, err := m.addEntitiesWithVectors(ctx, []string{name}, ""); err != nil {
				m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: %v", err))
				return nil
			}
		}
	case "relation":
		subject := strings.TrimSpace(unit.Subject)
		predicate := strings.TrimSpace(unit.Predicate)
		object := strings.TrimSpace(unit.Object)
		if subject == "" || predicate == "" || object == "" {
			warnings = append(warnings, fmt.Sprintf("跳过分块[%s]：关系字段不完整", unit.ChunkID))
			skipWrite = true
		}
		if !skipWrite {
			if _, err := m.addRelation(ctx, subject, predicate, object, ""); err != nil {
				m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: %v", err))
				return nil
			}
		}
	default:
		m.setChunkFailed(taskID, file.FileID, unit.ChunkID, fmt.Sprintf("写入失败: 未知 JSON 导入单元类型: %s", unit.Kind))
		return nil
	}
	if len(warnings) > 0 {
		m.appendFileWarnings(taskID, file.FileID, warnings)
	}
	m.setChunkCompleted(taskID, file.FileID, unit.ChunkID)
	return nil
}

// webImportHashPrefix 返回哈希前 8 位
func webImportHashPrefix(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

// webImportKnowledgeTypeResolver 返回知识类型解析器，未注入时按本地规则回落
func (m *WebImportManager) webImportKnowledgeTypeResolver() KnowledgeTypeResolver {
	if m.deps.KnowledgeTypeResolver != nil {
		return m.deps.KnowledgeTypeResolver
	}
	return func(value any, content string) (string, error) {
		text := strings.ToLower(strings.TrimSpace(importValueText(value)))
		switch text {
		case "narrative", "factual", "quote", "mixed", "structured":
			return text, nil
		case "":
			return "narrative", nil
		default:
			return "", fmt.Errorf("非法 knowledge_type: %s", importValueText(value))
		}
	}
}

// resolveKnowledgeType 归一化知识类型，解析失败时回落 narrative
func (m *WebImportManager) resolveKnowledgeType(value string, content string) string {
	resolved, err := m.webImportKnowledgeTypeResolver()(value, content)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return strings.TrimSpace(resolved)
	}
	return "narrative"
}

// webImportSortedStrings 返回排序后的副本
func webImportSortedStrings(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
