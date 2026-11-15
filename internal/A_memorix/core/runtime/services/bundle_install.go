package services

// 知识包导入的段落与安装记录写入，对齐 bundle_admin_service.py 的 _insert_knowledge_docs 与 _install_bundle_metadata

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// bundleKnowledgeDoc 规范化后的知识包段落，对齐 _knowledge_doc
type bundleKnowledgeDoc struct {
	DocID     string
	Content   string
	Entities  []string
	Triples   [][3]string
	Extension map[string]any
}

// normalizeBundleKnowledgeDocs 过滤空段落并补齐映射键
func normalizeBundleKnowledgeDocs(raw []any) []bundleKnowledgeDoc {
	out := make([]bundleKnowledgeDoc, 0, len(raw))
	for _, value := range raw {
		doc := asMap(value)
		if doc == nil {
			continue
		}
		content := strings.TrimSpace(asString(doc["passage"]))
		if content == "" {
			continue
		}
		entities := make([]string, 0, len(asList(doc["extracted_entities"])))
		seenEntities := map[string]struct{}{}
		for _, entityValue := range asList(doc["extracted_entities"]) {
			name := strings.TrimSpace(asString(entityValue))
			if name == "" {
				continue
			}
			if _, ok := seenEntities[name]; ok {
				continue
			}
			seenEntities[name] = struct{}{}
			entities = append(entities, name)
		}
		triples := make([][3]string, 0, len(asList(doc["extracted_triples"])))
		for _, tripleValue := range asList(doc["extracted_triples"]) {
			parts := asList(tripleValue)
			if len(parts) != 3 {
				continue
			}
			subject := strings.TrimSpace(asString(parts[0]))
			predicate := strings.TrimSpace(asString(parts[1]))
			object := strings.TrimSpace(asString(parts[2]))
			if subject == "" || predicate == "" || object == "" {
				continue
			}
			triples = append(triples, [3]string{subject, predicate, object})
		}
		extension := asMap(doc["a_memorix"])
		if extension == nil {
			extension = map[string]any{}
		}
		docID := strings.TrimSpace(asString(doc["id"]))
		if docID == "" {
			docID = fmt.Sprintf("doc-%d", len(out))
		}
		out = append(out, bundleKnowledgeDoc{
			DocID: docID, Content: content, Entities: entities, Triples: triples, Extension: extension,
		})
	}
	return out
}

// bundleInstallResult 保存元数据安装结果，供图片与向量导入映射目标主键
type bundleInstallResult struct {
	Inserted         map[string]any
	MappedParagraphs int
	ParagraphHashes  []string
	EntityHashes     []string
	RelationHashes   []string
	EpisodeIDs       []string
}

// installBundleMetadata 在单事务内写入段落、实体关系与安装记录
func (s *BundleAdminService) installBundleMetadata(loaded *loadedBundle, packageInfo map[string]any, installationID, scopeType, chatID, scopeKey, contentLevel, mode, contentDigest string) (bundleInstallResult, error) {
	result := bundleInstallResult{Inserted: map[string]any{}}
	packageID := asString(packageInfo["id"])
	docs := normalizeBundleKnowledgeDocs(asList(loaded.Knowledge["docs"]))
	var mappings [][2]string
	err := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		manifestBytes, _ := canonicalJSON(loaded.Manifest)
		now := float64(time.Now().UnixNano()) / 1e9
		if _, err := tx.Exec(
			`INSERT INTO knowledge_packages (installation_id, package_id, version, name, scope_type, scope_key,
			 chat_id, content_level, content_digest, status, manifest_json, installed_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'installing', ?, ?, ?) ON CONFLICT(installation_id) DO UPDATE SET
			 status = 'installing', content_digest = excluded.content_digest, manifest_json = excluded.manifest_json,
			 updated_at = excluded.updated_at`,
			installationID, asString(packageInfo["id"]), asString(packageInfo["version"]), asString(packageInfo["name"]),
			scopeType, scopeKey, chatID, contentLevel, contentDigest, string(manifestBytes), now, now); err != nil {
			return err
		}
		inserted := map[string]any{}
		if contentLevel == "full" {
			rawTables := asMap(loaded.State["tables"])
			if rawTables == nil {
				return fmt.Errorf("state.json 缺少 tables")
			}
			tables := buildFullStateTables(rawTables)
			if err := s.validateFullStateHashes(tables); err != nil {
				return err
			}
			originalChatIDs := fullStateOriginalChatIDs(tables["paragraphs"])
			tables = s.remapFullState(tables, packageID, installationID, scopeType, chatID, originalChatIDs)
			counts, err := s.insertFullStateTx(tx, tables, installationID)
			if err != nil {
				return err
			}
			for table, count := range counts {
				inserted[table] = count
			}
			for _, doc := range docs {
				mappings = append(mappings, [2]string{doc.DocID, utils.ComputeParagraphHash(doc.Content)})
			}
			result.ParagraphHashes = append(result.ParagraphHashes, tableHashes(tables, "paragraphs")...)
			result.EntityHashes = append(result.EntityHashes, tableHashes(tables, "entities")...)
			result.RelationHashes = append(result.RelationHashes, tableHashes(tables, "relations")...)
			for _, episode := range tables["episodes"] {
				if episodeID := strings.TrimSpace(asString(episode["episode_id"])); episodeID != "" {
					result.EpisodeIDs = append(result.EpisodeIDs, episodeID)
				}
			}
		} else {
			docMappings, docEntities, docRelations, docInserted, err := s.insertKnowledgeDocsTx(tx, docs, packageID, installationID, scopeType, chatID, now)
			if err != nil {
				return err
			}
			mappings = docMappings
			result.ParagraphHashes = append(result.ParagraphHashes, func() []string {
				values := make([]string, 0, len(docMappings))
				for _, mapping := range docMappings {
					values = append(values, mapping[1])
				}
				return values
			}()...)
			result.EntityHashes = docEntities
			result.RelationHashes = docRelations
			for key, count := range docInserted {
				inserted[key] = count
			}
		}
		validMappings := make([][2]string, 0, len(mappings))
		for _, mapping := range mappings {
			rows, err := queryRows(tx, "SELECT 1 AS ok FROM paragraphs WHERE hash = ? LIMIT 1", mapping[1])
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				validMappings = append(validMappings, mapping)
			}
		}
		for _, mapping := range validMappings {
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO knowledge_package_paragraphs (installation_id, doc_id, paragraph_hash)
				 VALUES (?, ?, ?)`, installationID, mapping[0], mapping[1]); err != nil {
				return err
			}
		}
		// 对齐 Python _install_locked：为导入段落维护 tokenized FTS 与 ngram 索引
		for _, paragraphHash := range result.ParagraphHashes {
			if strings.TrimSpace(paragraphHash) == "" {
				continue
			}
			s.Meta().FtsUpsertTokenizedParagraph(paragraphHash, tx.Conn())
			rows, err := queryRows(tx, "SELECT content FROM paragraphs WHERE hash = ? LIMIT 1", paragraphHash)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				continue
			}
			s.Meta().UpsertParagraphNgramIfReady(paragraphHash, asString(rows[0]["content"]), 0, tx.Conn())
		}
		result.Inserted = inserted
		result.MappedParagraphs = len(validMappings)
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

// tableHashes 提取完整包某张表的主键哈希
func tableHashes(tables map[string][]map[string]any, table string) []string {
	hashes := make([]string, 0, len(tables[table]))
	for _, row := range tables[table] {
		if hashValue := strings.TrimSpace(asString(row["hash"])); hashValue != "" {
			hashes = append(hashes, hashValue)
		}
	}
	return hashes
}

// insertKnowledgeDocsTx 写入知识级段落、实体、关系与归属资源
func (s *BundleAdminService) insertKnowledgeDocsTx(tx *storage.Tx, docs []bundleKnowledgeDoc, packageID, installationID, scopeType, chatID string, now float64) ([][2]string, []string, []string, map[string]int, error) {
	mappings := make([][2]string, 0, len(docs))
	entityHashes := make([]string, 0, len(docs))
	relationHashes := make([]string, 0, len(docs))
	inserted := map[string]int{"paragraphs": 0, "entities": 0, "relations": 0}
	source := "knowledge_pack:" + packageID
	for _, doc := range docs {
		content := doc.Content
		paragraphHash := utils.ComputeParagraphHash(content)
		metadata := jsonObjectAny(doc.Extension["metadata"])
		metadata["scope_type"] = scopeType
		metadata["knowledge_package_id"] = packageID
		metadata["knowledge_package_installation_id"] = installationID
		if scopeType == "chat" {
			metadata["chat_id"] = chatID
		} else {
			delete(metadata, "chat_id")
		}
		knowledgeType, err := storage.ResolveStoredKnowledgeType(doc.Extension["knowledge_type"], content, false, nil)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		timeMeta := asMap(doc.Extension["time_meta"])
		timeConfidence := 1.0
		if raw, ok := timeMeta["time_confidence"]; ok {
			if parsed, ok := asFloat(raw); ok && parsed != 0 {
				timeConfidence = parsed
			}
		}
		result, err := tx.Exec(
			`INSERT OR IGNORE INTO paragraphs (
				hash, content, vector_index, created_at, updated_at, metadata, source, word_count,
				event_time, event_time_start, event_time_end, time_granularity, time_confidence,
				knowledge_type, is_deleted
			) VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			paragraphHash, content, now, now, canonicalJSONText(metadata), source,
			len(strings.Fields(utils.NormalizeText(content))),
			timeMeta["event_time"], timeMeta["event_time_start"], timeMeta["event_time_end"],
			timeMeta["time_granularity"], timeConfidence, string(knowledgeType),
		)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		created := rowsCreated(result)
		if err := s.registerPackageResource(tx, installationID, "paragraphs",
			bundleResourceKey("paragraphs", map[string]any{"hash": paragraphHash}), created); err != nil {
			return nil, nil, nil, nil, err
		}
		if created {
			inserted["paragraphs"]++
		}
		mappings = append(mappings, [2]string{doc.DocID, paragraphHash})

		names := make([]string, 0, len(doc.Entities)+len(doc.Triples)*2)
		names = append(names, doc.Entities...)
		for _, triple := range doc.Triples {
			names = append(names, triple[0], triple[2])
		}
		for _, name := range dedupeBundleNames(names) {
			entityHash := utils.ComputeEntityHash(name)
			result, err := tx.Exec(
				`INSERT OR IGNORE INTO entities (hash, name, vector_index, appearance_count, created_at, metadata, is_deleted)
				 VALUES (?, ?, NULL, 1, ?, NULL, 0)`, entityHash, name, now)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			entityCreated := rowsCreated(result)
			entityHashes = append(entityHashes, entityHash)
			if err := s.registerPackageResource(tx, installationID, "entities",
				bundleResourceKey("entities", map[string]any{"hash": entityHash}), entityCreated); err != nil {
				return nil, nil, nil, nil, err
			}
			if entityCreated {
				inserted["entities"]++
			}
			result, err = tx.Exec(
				`INSERT OR IGNORE INTO paragraph_entities (paragraph_hash, entity_hash, mention_count)
				 VALUES (?, ?, 1)`, paragraphHash, entityHash)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			if err := s.registerPackageResource(tx, installationID, "paragraph_entities",
				bundleResourceKey("paragraph_entities", map[string]any{"paragraph_hash": paragraphHash, "entity_hash": entityHash}),
				rowsCreated(result)); err != nil {
				return nil, nil, nil, nil, err
			}
		}
		for _, triple := range doc.Triples {
			relationHash, err := s.Meta().ComputeRelationHash(triple[0], triple[1], triple[2])
			if err != nil {
				return nil, nil, nil, nil, err
			}
			result, err := tx.Exec(
				`INSERT OR IGNORE INTO relations (
					hash, subject, predicate, object, vector_index, confidence, vector_state,
					created_at, source_paragraph, metadata, retention_strength,
					retention_anchor_at, next_lifecycle_at, reinforcement_count,
					lifecycle_revision, is_inactive
				) VALUES (?, ?, ?, ?, NULL, 1.0, 'none', ?, ?, NULL, 1.0, ?, ?, 0, 0, 0)`,
				relationHash, triple[0], triple[1], triple[2], now, paragraphHash, now, now)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			relationCreated := rowsCreated(result)
			relationHashes = append(relationHashes, relationHash)
			if err := s.registerPackageResource(tx, installationID, "relations",
				bundleResourceKey("relations", map[string]any{"hash": relationHash}), relationCreated); err != nil {
				return nil, nil, nil, nil, err
			}
			if relationCreated {
				inserted["relations"]++
			}
			result, err = tx.Exec(
				`INSERT OR IGNORE INTO paragraph_relations (paragraph_hash, relation_hash)
				 VALUES (?, ?)`, paragraphHash, relationHash)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			if err := s.registerPackageResource(tx, installationID, "paragraph_relations",
				bundleResourceKey("paragraph_relations", map[string]any{"paragraph_hash": paragraphHash, "relation_hash": relationHash}),
				rowsCreated(result)); err != nil {
				return nil, nil, nil, nil, err
			}
		}
	}
	return mappings, dedupeStringsFunc(entityHashes), dedupeStringsFunc(relationHashes), inserted, nil
}

// registerPackageResource 登记安装资源归属，created 表示本次安装是否真正新建
func (s *BundleAdminService) registerPackageResource(tx *storage.Tx, installationID, resourceType, resourceKey string, created bool) error {
	createdValue := 0
	if created {
		createdValue = 1
	}
	_, err := tx.Exec(
		`INSERT INTO knowledge_package_resources (installation_id, resource_type, resource_key, created_by_installation)
		 VALUES (?, ?, ?, ?) ON CONFLICT(installation_id, resource_type, resource_key) DO NOTHING`,
		installationID, resourceType, resourceKey, createdValue)
	return err
}

// bundleResourceKey 按资源主键生成归属键
func bundleResourceKey(table string, row map[string]any) string {
	keys := resourcePrimaryKeys[table]
	values := make([]any, 0, len(keys))
	for _, key := range keys {
		values = append(values, row[key])
	}
	return canonicalJSONText(values)
}

// canonicalJSONText 序列化为 Python 风格紧凑 JSON
func canonicalJSONText(value any) string {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// jsonObjectAny 对齐 Python _json_object，接受对象或 JSON 文本
func jsonObjectAny(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	case string:
		return jsonObject(typed)
	case nil:
		return map[string]any{}
	default:
		return jsonObject(asString(value))
	}
}

// jsonListAny 对齐 Python _json_list，接受列表或 JSON 文本
func jsonListAny(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case string:
		var out []any
		if err := json.Unmarshal([]byte(typed), &out); err != nil {
			return nil
		}
		return out
	case nil:
		return nil
	default:
		var out []any
		if err := json.Unmarshal([]byte(asString(value)), &out); err != nil {
			return nil
		}
		return out
	}
}

// rowsCreated 判断 INSERT OR IGNORE 是否真正写入
func rowsCreated(result interface{ RowsAffected() (int64, error) }) bool {
	affected, err := result.RowsAffected()
	return err == nil && affected > 0
}

// dedupeBundleNames 保序去重并清理空白
func dedupeBundleNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// dedupeStringsFunc 保序去重
func dedupeStringsFunc(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
