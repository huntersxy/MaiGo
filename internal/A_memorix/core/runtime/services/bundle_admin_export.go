package services

// 知识包导出，对齐 Python bundle_admin_service.py 的 _export_bundle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// exportBundle 对齐 _export_bundle
func (s *BundleAdminService) exportBundle(payload map[string]any) map[string]any {
	contentLevel := strings.ToLower(strings.TrimSpace(asString(payload["content_level"])))
	if contentLevel == "" {
		contentLevel = "knowledge"
	}
	if contentLevel != "knowledge" && contentLevel != "full" {
		return map[string]any{"success": false, "error": "content_level 只支持 knowledge 或 full"}
	}
	includeImages := bundleBoolOr(payload["include_images"], true)
	includeImageRelated := bundleBoolOr(payload["include_image_related"], false)
	includeVectors := bundleBoolOr(payload["include_vectors"], true)
	selector := asMap(payload["selector"])
	if selector == nil {
		selector = map[string]any{}
	}
	selectorType := strings.ToLower(strings.TrimSpace(asString(selector["type"])))
	if selectorType == "" {
		selectorType = "all"
	}
	paragraphRows := []map[string]any{}
	resolvedSelector := map[string]any{}
	var err error
	if selectorType == "image" || selectorType == "images" {
		resolvedSelector = map[string]any{
			"type":      "image",
			"values":    selectorImageValues(selector),
			"precision": "exact",
		}
	} else {
		paragraphRows, resolvedSelector, err = s.selectParagraphs(selector)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
	}

	selectedOccurrences := []string{}
	selectedLinks := []map[string]any{}
	if includeImages && s.imageRuntime() != nil {
		selectedOccurrences, err = s.selectImageOccurrenceIDs(selector, bundleParagraphHashes(paragraphRows))
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
		selectedLinks, err = s.listImageLinks(selectedOccurrences)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
	}
	if includeImageRelated && includeImages {
		resolvedSelector["include_image_related"] = true
		relatedIDs, relateErr := s.relatedParagraphIDs(selectedLinks)
		if relateErr != nil {
			return map[string]any{"success": false, "error": relateErr.Error()}
		}
		if len(relatedIDs) > 0 {
			extraRows, queryErr := s.paragraphsByHashes(relatedIDs)
			if queryErr != nil {
				return map[string]any{"success": false, "error": queryErr.Error()}
			}
			paragraphRows = mergeParagraphRows(paragraphRows, extraRows)
		}
	}
	docs := s.buildKnowledgeDocs(paragraphRows)

	knowledgeTables := s.collectKnowledgeTables(paragraphRows)
	stateTables := map[string][]map[string]any{}
	var state any
	if contentLevel == "full" {
		for _, table := range fullStateTableOrder {
			rows, queryErr := s.Meta().Query("SELECT * FROM " + table)
			if queryErr != nil {
				return map[string]any{"success": false, "error": queryErr.Error()}
			}
			stateTables[table] = rows
		}
		state = map[string]any{
			"schema":         "a-memorix-semantic-state",
			"schema_version": 1,
			"tables":         stateTables,
			"excluded":       fullStateExclusions,
		}
	} else {
		for table, rows := range knowledgeTables {
			stateTables[table] = rows
		}
	}

	imageRecords := map[string]any{}
	hasImages := false
	if includeImages && s.imageRuntime() != nil {
		if len(selectedOccurrences) > 0 {
			records, exportErr := s.imageRuntime().ExportRecords(selectedOccurrences)
			if exportErr != nil {
				return map[string]any{"success": false, "error": exportErr.Error()}
			}
			if records != nil {
				imageRecords = records
			}
		} else {
			imageRecords = emptyImageRecords()
		}
		if recordsFingerprint := asMap(imageRecords["fingerprint"]); len(recordsFingerprint) == 0 {
			imageRecords["fingerprint"] = map[string]any{}
		}
		hasImages = len(anyMapList(imageRecords["assets"])) > 0
	}

	targets := bundleExportTargets(paragraphRows, knowledgeTables, contentLevel, stateTables)
	filteredLinks := filterImageLinks(anyMapList(imageRecords["links"]), targets)
	missing := missingImageLinkTargets(anyMapList(imageRecords["links"]), targets)
	if includeImageRelated && len(missing) > 0 {
		return map[string]any{"success": false, "error": "图片关联目标不在可迁移知识闭包内；Episode关联请使用完整包，孤立目标需先补齐来源知识"}
	}
	imageRecords["links"] = filteredLinks

	if len(docs) == 0 && !hasImages {
		return map[string]any{"success": false, "error": "当前选择范围没有可导出的文字或图片记忆"}
	}

	knowledge := map[string]any{"format": "lpmm_openie", "format_version": 1, "docs": docs}
	counts := map[string]any{}
	if contentLevel == "full" {
		for _, table := range fullStateTableOrder {
			counts[table] = len(stateTables[table])
		}
	} else {
		counts["paragraphs"] = len(paragraphRows)
		counts["entities"] = len(knowledgeTables["entities"])
		counts["relations"] = len(knowledgeTables["relations"])
	}

	digestPayload := map[string]any{"knowledge": knowledge, "state": nil}
	if contentLevel == "full" {
		digestPayload["state"] = state
	}
	if hasImages {
		digestPayload["images"] = canonicalImageSemantics(imageRecords)
		counts["image_assets"] = len(anyMapList(imageRecords["assets"]))
		counts["image_occurrences"] = len(anyMapList(imageRecords["occurrences"]))
		counts["image_observations"] = len(anyMapList(imageRecords["observations"]))
		counts["image_links"] = len(anyMapList(imageRecords["links"]))
	}
	digestBytes, err := pythonCanonicalJSON(digestPayload)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	contentDigest := sha256Hex(digestBytes)

	packageInfo := asMap(payload["package"])
	if packageInfo == nil {
		packageInfo = map[string]any{}
	}
	packageName := strings.TrimSpace(asString(packageInfo["name"]))
	if packageName == "" {
		packageName = "A_Memorix 知识包"
	}
	packageVersion := strings.TrimSpace(asString(packageInfo["version"]))
	if packageVersion == "" {
		packageVersion = "1.0.0"
	}
	packageID := strings.TrimSpace(asString(packageInfo["id"]))
	if packageID == "" {
		packageID = strings.TrimSpace(asString(packageInfo["package_id"]))
	}
	if packageID == "" {
		packageID = "amx." + contentDigest[:16]
	}
	now := time.Now().UTC()

	members := map[string][]byte{}
	knowledgeBytes, err := pythonCanonicalJSON(knowledge)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	members["knowledge.json"] = knowledgeBytes
	if contentLevel == "full" {
		stateBytes, err := pythonCanonicalJSON(state)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
		members["state.json"] = stateBytes
	}
	if hasImages {
		portable, assetMembers, portableErr := s.portableImageRecords(imageRecords)
		if portableErr != nil {
			return map[string]any{"success": false, "error": portableErr.Error()}
		}
		for name, content := range assetMembers {
			members[name] = content
		}
		imagesBytes, encodeErr := pythonCanonicalJSON(portable)
		if encodeErr != nil {
			return map[string]any{"success": false, "error": encodeErr.Error()}
		}
		members["images.json"] = imagesBytes
	}
	paragraphIDs := bundleRowHashes(stateTables["paragraphs"])
	entityIDs := bundleRowHashes(stateTables["entities"])
	relationIDs := bundleRowHashes(stateTables["relations"])
	graphIDs := make([]string, 0, len(entityIDs)+len(relationIDs))
	for _, hashValue := range entityIDs {
		graphIDs = append(graphIDs, s.graphVectorID("entity", hashValue))
	}
	for _, hashValue := range relationIDs {
		graphIDs = append(graphIDs, s.graphVectorID("relation", hashValue))
	}
	// 对齐 Python：无已存 embedding 指纹时不导出文本与图谱向量
	var embeddingFingerprint map[string]any
	if includeVectors {
		paragraphStore := s.paragraphVectorStore()
		graphStore := s.graphVectorStore()
		if s.deps.StoredEmbeddingFingerprint != nil {
			embeddingFingerprint = s.deps.StoredEmbeddingFingerprint(paragraphStore)
		}
		if embeddingFingerprint != nil {
			paragraphVectors, vectorErr := exportVectorMember(paragraphStore, paragraphIDs, nil)
			if vectorErr != nil {
				return map[string]any{"success": false, "error": vectorErr.Error()}
			}
			if paragraphVectors != nil {
				members["vectors/paragraphs.npz"] = paragraphVectors
			}
			graphStorageIDs := graphIDs
			if !s.dualVectorPoolsEnabled() {
				graphStorageIDs = append(append([]string{}, entityIDs...), relationIDs...)
			}
			graphVectors, graphErr := exportVectorMember(graphStore, graphStorageIDs, graphIDs)
			if graphErr != nil {
				return map[string]any{"success": false, "error": graphErr.Error()}
			}
			if graphVectors != nil {
				members["vectors/graph.npz"] = graphVectors
			}
		}
		if hasImages && s.imageRuntime() != nil {
			payloadBytes, exportErr := s.imageRuntime().ExportVectors(imageAssetIDs(imageRecords))
			if exportErr != nil {
				return map[string]any{"success": false, "error": exportErr.Error()}
			}
			if payloadBytes != nil {
				members["vectors/images.npz"] = payloadBytes
			}
		}
	}

	manifestComponents := make([]string, 0, len(members))
	for name := range members {
		manifestComponents = append(manifestComponents, name)
	}
	sort.Strings(manifestComponents)

	formatVersion := BundleLegacyFormatVersion
	if hasImages {
		formatVersion = BundleFormatVersion
	}
	var imageFingerprint any
	if hasImages {
		imageFingerprint = asMap(imageRecords["fingerprint"])
	}
	var embeddingFingerprintValue any
	if embeddingFingerprint != nil {
		embeddingFingerprintValue = embeddingFingerprint
	}
	manifest := map[string]any{
		"format":         BundleFormat,
		"format_version": formatVersion,
		"created_at":     now.Format("2006-01-02T15:04:05+00:00"),
		"content_level":  contentLevel,
		"content_digest": contentDigest,
		"package": map[string]any{
			"id": packageID, "version": packageVersion, "name": packageName,
			"description": asString(packageInfo["description"]), "author": asString(packageInfo["author"]),
		},
		"selector":                    resolvedSelector,
		"counts":                      counts,
		"components":                  manifestComponents,
		"embedding_fingerprint":       embeddingFingerprintValue,
		"image_embedding_fingerprint": imageFingerprint,
		"compatibility": map[string]any{
			"knowledge_semantics":              "lpmm_openie",
			"minimum_a_memorix_bundle_version": 1,
		},
	}
	manifestBytes, err := pythonCanonicalJSON(manifest)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	members["manifest.json"] = manifestBytes

	checksums := map[string]string{}
	for name, content := range members {
		if name == "checksums.json" {
			continue
		}
		checksums[name] = sha256Hex(content)
	}
	checksumsPayload := map[string]any{"algorithm": "sha256", "files": checksums}
	checksumsBytes, err := pythonCanonicalJSON(checksumsPayload)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	members["checksums.json"] = checksumsBytes

	uncompressedSize := 0
	for _, content := range members {
		uncompressedSize += len(content)
	}
	if pyTruthy(payload["preview"]) {
		return map[string]any{"success": true, "preview": true, "manifest": manifest, "counts": counts, "uncompressed_size": uncompressedSize}
	}
	if err := os.MkdirAll(s.bundlesRoot(), 0o755); err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	stem := safeFileStem(packageName) + "-" + safeFileStem(packageVersion)
	target := filepath.Join(s.bundlesRoot(), stem+BundleExtension)
	if _, statErr := os.Stat(target); statErr == nil {
		target = filepath.Join(s.bundlesRoot(), stem+"-"+contentDigest[:8]+BundleExtension)
	}
	if err := writeBundleZip(target, members); err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		return map[string]any{"success": false, "error": statErr.Error()}
	}
	return map[string]any{
		"success": true, "file_name": filepath.Base(target), "path": target,
		"manifest": manifest, "counts": counts, "size": info.Size(),
	}
}

// emptyImageRecords 空图片记录结构
func emptyImageRecords() map[string]any {
	return map[string]any{
		"assets": []map[string]any{}, "occurrences": []map[string]any{},
		"observations": []map[string]any{}, "links": []map[string]any{},
		"fingerprint": map[string]any{},
	}
}

// bundleParagraphHashes 提取段落哈希
func bundleParagraphHashes(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if hashValue := strings.TrimSpace(rowString(row, "hash")); hashValue != "" {
			out = append(out, hashValue)
		}
	}
	return dedupeStrings(out)
}

// mergeParagraphRows 合并段落行并按创建时间与哈希排序
func mergeParagraphRows(existing, extra []map[string]any) []map[string]any {
	seen := map[string]bool{}
	merged := make([]map[string]any, 0, len(existing)+len(extra))
	for _, group := range [][]map[string]any{existing, extra} {
		for _, row := range group {
			hashValue := rowString(row, "hash")
			if seen[hashValue] {
				continue
			}
			seen[hashValue] = true
			merged = append(merged, row)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		left, right := rowString(merged[i], "created_at"), rowString(merged[j], "created_at")
		if left != right {
			return left < right
		}
		return rowString(merged[i], "hash") < rowString(merged[j], "hash")
	})
	return merged
}

// collectKnowledgeTables 收集知识级闭包表，对齐 _collect_related_rows 的实体与关系部分
func (s *BundleAdminService) collectKnowledgeTables(paragraphRows []map[string]any) map[string][]map[string]any {
	tables := map[string][]map[string]any{
		"paragraphs":          paragraphRows,
		"entities":            {},
		"relations":           {},
		"paragraph_entities":  {},
		"paragraph_relations": {},
	}
	entityHashes := []string{}
	relationHashes := []string{}
	for _, row := range paragraphRows {
		hashValue := rowString(row, "hash")
		if hashValue == "" {
			continue
		}
		if entityRows, err := s.Meta().GetParagraphEntities(hashValue); err == nil {
			for _, entityRow := range entityRows {
				entityHash := strings.TrimSpace(rowString(entityRow, "hash"))
				if entityHash == "" {
					continue
				}
				entityHashes = append(entityHashes, entityHash)
				tables["paragraph_entities"] = append(tables["paragraph_entities"], map[string]any{
					"paragraph_hash": hashValue, "entity_hash": entityHash,
				})
			}
		}
		if relationRows, err := s.Meta().GetParagraphRelations(hashValue); err == nil {
			for _, relationRow := range relationRows {
				relationHash := strings.TrimSpace(rowString(relationRow, "hash"))
				if relationHash == "" {
					continue
				}
				relationHashes = append(relationHashes, relationHash)
				tables["paragraph_relations"] = append(tables["paragraph_relations"], map[string]any{
					"paragraph_hash": hashValue, "relation_hash": relationHash,
				})
			}
		}
	}
	tables["entities"] = s.rowsByHashes("entities", dedupeStrings(entityHashes))
	tables["relations"] = s.rowsByHashes("relations", dedupeStrings(relationHashes))
	return tables
}

// rowsByHashes 按 hash 批量查询表行
func (s *BundleAdminService) rowsByHashes(table string, hashes []string) []map[string]any {
	if len(hashes) == 0 {
		return []map[string]any{}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	rows, err := s.Meta().Query("SELECT * FROM "+table+" WHERE hash IN ("+placeholders+")", stringArgs(hashes)...)
	if err != nil {
		return []map[string]any{}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rowString(rows[i], "hash") < rowString(rows[j], "hash") })
	return rows
}

// bundleExportTargets 生成可迁移目标闭包
func bundleExportTargets(paragraphRows []map[string]any, knowledgeTables map[string][]map[string]any, contentLevel string, stateTables map[string][]map[string]any) map[string]map[string]struct{} {
	targets := map[string]map[string]struct{}{
		"paragraph": {}, "entity": {}, "relation": {}, "episode": {},
	}
	for _, row := range paragraphRows {
		if hashValue := strings.TrimSpace(rowString(row, "hash")); hashValue != "" {
			targets["paragraph"][hashValue] = struct{}{}
		}
	}
	entities := knowledgeTables["entities"]
	relations := knowledgeTables["relations"]
	if contentLevel == "full" {
		entities = stateTables["entities"]
		relations = stateTables["relations"]
		for _, row := range stateTables["episodes"] {
			if episodeID := strings.TrimSpace(rowString(row, "episode_id")); episodeID != "" {
				targets["episode"][episodeID] = struct{}{}
			}
		}
	}
	for _, row := range entities {
		if hashValue := strings.TrimSpace(rowString(row, "hash")); hashValue != "" {
			targets["entity"][hashValue] = struct{}{}
		}
	}
	for _, row := range relations {
		if hashValue := strings.TrimSpace(rowString(row, "hash")); hashValue != "" {
			targets["relation"][hashValue] = struct{}{}
		}
	}
	return targets
}
