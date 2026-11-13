package services

// 知识包导入，对齐 Python bundle_admin_service.py 的 _import_bundle

import (
	"context"
	"fmt"
	"sort"
	"strings"

	imagememory "maibot-go/internal/A_memorix/core/image"
	"maibot-go/internal/A_memorix/core/storage"
)

// importBundle 对齐 _import_bundle
func (s *BundleAdminService) importBundle(payload map[string]any) map[string]any {
	path, err := s.resolveBundlePath(firstNonEmpty(payload["path"], payload["file_name"]), false)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	loaded, err := s.loadBundle(path)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	packageInfo := asMap(loaded.Manifest["package"])
	if packageInfo == nil {
		packageInfo = map[string]any{}
	}
	packageID := strings.TrimSpace(asString(packageInfo["id"]))
	packageVersion := strings.TrimSpace(asString(packageInfo["version"]))
	packageName := strings.TrimSpace(asString(packageInfo["name"]))
	if packageID == "" || packageVersion == "" || packageName == "" {
		return map[string]any{"success": false, "error": "manifest.package 的 id、version 和 name 不能为空"}
	}
	scopeType, chatID, scopeKey := validateInstallScope(payload)
	if scopeType == "" {
		return map[string]any{"success": false, "error": "安装范围的 scope_type 与 chat_id 不一致"}
	}
	installationID := stableInstallationID(packageID, packageVersion, scopeType, scopeKey)
	contentLevel := strings.ToLower(strings.TrimSpace(asString(loaded.Manifest["content_level"])))
	contentDigest := asString(loaded.Manifest["content_digest"])
	existing, err := s.Meta().Query(
		"SELECT * FROM knowledge_packages WHERE package_id = ? AND version = ? AND scope_type = ? AND scope_key = ? LIMIT 1",
		packageID, packageVersion, scopeType, scopeKey)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	var existingRecord map[string]any
	if len(existing) > 0 {
		existingRecord = existing[0]
		existingDigest := asString(existingRecord["content_digest"])
		if existingDigest != contentDigest {
			return map[string]any{"success": false, "error": "同一包版本在该范围已安装，但内容摘要不同，请提升包版本"}
		}
		existingStatus := strings.ToLower(strings.TrimSpace(asString(existingRecord["status"])))
		if existingStatus == "" {
			existingStatus = "installed"
		}
		if existingStatus == "installed" {
			return map[string]any{
				"success": true, "already_installed": true,
				"installation_id": asString(existingRecord["installation_id"]),
				"package":         packageInfo, "content_level": contentLevel,
			}
		}
	}
	mode := strings.ToLower(strings.TrimSpace(asString(payload["mode"])))
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "restore" {
		return map[string]any{"success": false, "error": "mode 只支持 merge 或 restore"}
	}
	hasImagesJSON := loaded.hasMember("images.json")
	if mode == "restore" && contentLevel != "full" {
		return map[string]any{"success": false, "error": "restore 模式只接受 full 完整记忆包"}
	}
	if existingRecord != nil {
		existingStatus := strings.ToLower(strings.TrimSpace(asString(existingRecord["status"])))
		if existingStatus != "installing" && existingStatus != "failed" {
			label := existingStatus
			if label == "" {
				label = "empty"
			}
			return map[string]any{"success": false, "error": "知识包安装记录状态无法恢复: " + label}
		}
		rollback := s.uninstallBundle(context.Background(), asString(existingRecord["installation_id"]))
		if !pyTruthy(rollback["success"]) {
			message := asString(rollback["error"])
			if message == "" {
				message = "未知错误"
			}
			return map[string]any{"success": false, "error": "无法清理未完成的知识包安装: " + message}
		}
	}
	if mode == "restore" {
		occupancy, occupyErr := s.restoreOccupancy()
		if occupyErr != nil {
			return map[string]any{"success": false, "error": occupyErr.Error()}
		}
		if len(occupancy) > 0 {
			parts := make([]string, 0, len(occupancy))
			for table, total := range occupancy {
				parts = append(parts, fmt.Sprintf("%s=%d", table, total))
			}
			sort.Strings(parts)
			return map[string]any{"success": false, "error": fmt.Sprintf("restore 模式要求目标记忆库为空，当前存在: %s", strings.Join(parts, ", "))}
		}
	}
	docs := asList(loaded.Knowledge["docs"])
	if len(docs) == 0 && !hasImagesJSON {
		return map[string]any{"success": false, "error": "记忆包没有有效知识段落或图片记忆"}
	}
	if hasImagesJSON && s.imageRuntime() == nil {
		return map[string]any{"success": false, "error": "当前运行时未启用图片记忆，无法安装含图片的记忆包"}
	}

	installResult, err := s.installBundleMetadata(loaded, packageInfo, installationID, scopeType, chatID, scopeKey, contentLevel, mode, contentDigest)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}

	imagesImported := 0
	imagesOccurrences := 0
	imageVectorsImported := 0
	assetMap := map[string]string{}
	graph := s.rebuildBundleGraph(context.Background())
	if hasImagesJSON {
		targetIDMap := bundleTargetIDMap(installResult, loaded)
		imported, importErr := s.imageRuntime().ImportRecords(context.Background(), imagememory.ImportRecordsRequest{
			Records: loaded.Images,
			AssetLoader: func(asset map[string]any) ([]byte, error) {
				return s.readValidatedBundleMember(loaded, asString(asset["member"]))
			},
			InstallationID: installationID,
			ScopeType:      scopeType,
			ChatID:         chatID,
			TargetIDMap:    targetIDMap,
		})
		if importErr != nil {
			return s.failBundleImport(installationID, fmt.Errorf("图片记忆导入失败: %w", importErr))
		}
		imagesImported = intOr(imported["assets"], 0, 0)
		imagesOccurrences = intOr(imported["occurrences"], 0, 0)
		if mapped, ok := imported["asset_map"].(map[string]string); ok {
			assetMap = mapped
		} else {
			for key, value := range asMap(imported["asset_map"]) {
				assetMap[key] = asString(value)
			}
		}
		graph = s.rebuildBundleGraph(context.Background())
	}

	packagedVectorIDs := map[string]struct{}{}
	vectorImported := map[string]int{"paragraphs": 0, "graph": 0}
	vectorReused := bundleVectorsCompatible(loaded.Manifest, s.currentEmbeddingFingerprint())
	if vectorReused {
		if loaded.hasMember("vectors/paragraphs.npz") {
			payloadBytes, readErr := s.readValidatedBundleMember(loaded, "vectors/paragraphs.npz")
			if readErr != nil {
				return s.failBundleImport(installationID, readErr)
			}
			count, accepted, importErr := importVectorMember(payloadBytes, s.paragraphVectorStore(), bundleHashSet(installResult.ParagraphHashes), nil)
			if importErr != nil {
				return s.failBundleImport(installationID, importErr)
			}
			vectorImported["paragraphs"] = count
			for archiveID := range accepted {
				packagedVectorIDs[archiveID] = struct{}{}
			}
		}
		if loaded.hasMember("vectors/graph.npz") {
			allowedGraphIDs := make(map[string]struct{}, len(installResult.EntityHashes)+len(installResult.RelationHashes))
			for _, hashValue := range installResult.EntityHashes {
				allowedGraphIDs[s.graphVectorID("entity", hashValue)] = struct{}{}
			}
			if s.relationVectorsEnabled() {
				for _, hashValue := range installResult.RelationHashes {
					allowedGraphIDs[s.graphVectorID("relation", hashValue)] = struct{}{}
				}
			}
			var storeIDByArchiveID map[string]string
			if !s.dualVectorPoolsEnabled() {
				storeIDByArchiveID = make(map[string]string, len(allowedGraphIDs))
				for _, hashValue := range installResult.EntityHashes {
					storeIDByArchiveID[s.graphVectorID("entity", hashValue)] = hashValue
				}
				if s.relationVectorsEnabled() {
					for _, hashValue := range installResult.RelationHashes {
						storeIDByArchiveID[s.graphVectorID("relation", hashValue)] = hashValue
					}
				}
			}
			payloadBytes, readErr := s.readValidatedBundleMember(loaded, "vectors/graph.npz")
			if readErr != nil {
				return s.failBundleImport(installationID, readErr)
			}
			count, accepted, importErr := importVectorMember(payloadBytes, s.graphVectorStore(), allowedGraphIDs, storeIDByArchiveID)
			if importErr != nil {
				return s.failBundleImport(installationID, importErr)
			}
			vectorImported["graph"] = count
			for archiveID := range accepted {
				packagedVectorIDs[archiveID] = struct{}{}
			}
		}
	}

	if hasImagesJSON && loaded.hasMember("vectors/images.npz") {
		payloadBytes, readErr := s.readValidatedBundleMember(loaded, "vectors/images.npz")
		if readErr != nil {
			return s.failBundleImport(installationID, readErr)
		}
		importedVectors, vectorErr := s.imageRuntime().ImportVectors(context.Background(), payloadBytes, assetMap, asMap(loaded.Manifest["image_embedding_fingerprint"]))
		if vectorErr != nil {
			return s.failBundleImport(installationID, fmt.Errorf("图片向量导入失败: %w", vectorErr))
		}
		imageVectorsImported = importedVectors
	}

	generatedVectors, ensureErr := s.ensureImportedVectors(context.Background(), installResult.ParagraphHashes, installResult.EntityHashes, installResult.RelationHashes, packagedVectorIDs)
	if ensureErr != nil {
		return s.failBundleImport(installationID, ensureErr)
	}
	if s.deps.PersistVectors != nil {
		s.deps.PersistVectors()
	}
	if err := s.setInstallationStatus(installationID, "installed"); err != nil {
		return s.failBundleImport(installationID, err)
	}

	result := map[string]any{
		"success": false, "already_installed": false,
		"installation_id": installationID, "package": packageInfo,
		"content_level": contentLevel, "mode": mode, "scope_type": scopeType, "chat_id": chatID,
		"inserted": installResult.Inserted, "mapped_paragraphs": installResult.MappedParagraphs,
		"graph": graph,
	}
	result["success"] = true
	vectors := map[string]any{
		"bundle_compatible": vectorReused,
		"imported":          map[string]any{"paragraphs": vectorImported["paragraphs"], "graph": vectorImported["graph"]},
		"generated": map[string]any{
			"paragraphs": generatedVectors["paragraphs"],
			"entities":   generatedVectors["entities"],
			"relations":  generatedVectors["relations"],
		},
	}
	if hasImagesJSON {
		vectors["images_imported"] = imageVectorsImported
		result["images"] = s.importedImageSummary(imagesImported, imagesOccurrences, imageVectorsImported)
	}
	result["vectors"] = vectors
	return result
}

// restoreOccupancy 统计 restore 前的占用，对齐 _assert_restore_target_empty
func (s *BundleAdminService) restoreOccupancy() (map[string]int, error) {
	occupancy := map[string]int{}
	tables := []string{"paragraphs", "entities", "relations", "episodes", "fact_claims", "person_profile_snapshots"}
	if s.imageRuntime() != nil {
		tables = append(tables, "image_assets")
	}
	for _, table := range tables {
		if table == "image_assets" {
			stats, err := s.imageRuntime().MetadataStore().ImageMemoryStats()
			if err != nil {
				return nil, err
			}
			if stats["asset_count"] > 0 {
				occupancy[table] = stats["asset_count"]
			}
			continue
		}
		rows, err := s.Meta().Query("SELECT COUNT(*) AS total FROM " + table)
		if err != nil {
			return nil, err
		}
		total := 0
		if len(rows) > 0 {
			total = intOr(rows[0]["total"], 0, 0)
		}
		if total > 0 {
			occupancy[table] = total
		}
	}
	return occupancy, nil
}

// bundleTargetIDMap 生成图片关联目标映射
func bundleTargetIDMap(result bundleInstallResult, loaded *loadedBundle) map[string]map[string]string {
	targets := map[string]map[string]string{
		"paragraph": {}, "entity": {}, "relation": {}, "episode": {},
	}
	for _, hashValue := range result.ParagraphHashes {
		targets["paragraph"][hashValue] = hashValue
	}
	for _, hashValue := range result.EntityHashes {
		targets["entity"][hashValue] = hashValue
	}
	for _, hashValue := range result.RelationHashes {
		targets["relation"][hashValue] = hashValue
	}
	for _, episodeID := range result.EpisodeIDs {
		targets["episode"][episodeID] = episodeID
	}
	if loaded.State != nil {
		for _, episode := range asList(asMap(loaded.State["tables"])["episodes"]) {
			episodeMap := asMap(episode)
			episodeID := strings.TrimSpace(asString(episodeMap["episode_id"]))
			if episodeID != "" {
				targets["episode"][episodeID] = episodeID
			}
		}
	}
	return targets
}

// importedImageSummary 生成导入响应中的图片段
func (s *BundleAdminService) importedImageSummary(assets, occurrences, vectorsImported int) map[string]any {
	status := s.imageRuntimeStatus()
	retrievalStatus := strings.TrimSpace(asString(status["status"]))
	if retrievalStatus == "" {
		retrievalStatus = "unavailable"
	}
	if retrievalStatus == "ready" && vectorsImported < assets {
		retrievalStatus = "pending"
	}
	return map[string]any{
		"assets": assets, "occurrences": occurrences,
		"content_status": "installed", "retrieval_status": retrievalStatus,
		"retrieval_message": asString(status["message"]),
	}
}

// failBundleImport 回滚已写入的安装并返回复合错误
func (s *BundleAdminService) failBundleImport(installationID string, cause error) map[string]any {
	rollback := s.uninstallBundle(context.Background(), installationID)
	if pyTruthy(rollback["success"]) {
		return map[string]any{"success": false, "error": cause.Error()}
	}
	rollbackError := asString(rollback["error"])
	// 纯图片包在写入任何资源归属前失败时，Python 的卸载守卫会拒绝清理并遗留 installing
	// 这里对「无归属资源」的空安装记录做兜底清理，避免后续重装被阻塞
	if rollbackError == bundleMissingOwnershipMessage {
		if cleanupErr := s.cleanupEmptyInstallation(installationID); cleanupErr != nil {
			return map[string]any{
				"success": false,
				"error":   fmt.Sprintf("%s；回滚失败: %s", cause.Error(), cleanupErr.Error()),
			}
		}
		return map[string]any{"success": false, "error": cause.Error()}
	}
	return map[string]any{
		"success": false,
		"error":   fmt.Sprintf("%s；回滚失败: %s", cause.Error(), rollbackError),
	}
}

// cleanupEmptyInstallation 清理没有任何资源归属的安装记录
// 仅在卸载守卫判定「缺少资源归属信息」后调用，此时该安装未写入任何归属资源
func (s *BundleAdminService) cleanupEmptyInstallation(installationID string) error {
	trimmed := strings.TrimSpace(installationID)
	if trimmed == "" {
		return fmt.Errorf("installation_id 不能为空")
	}
	return s.Meta().Transaction(true, func(tx *storage.Tx) error {
		for _, statement := range []string{
			"DELETE FROM knowledge_package_resources WHERE installation_id = ?",
			"DELETE FROM knowledge_package_paragraphs WHERE installation_id = ?",
			"DELETE FROM knowledge_packages WHERE installation_id = ?",
		} {
			if _, err := tx.Exec(statement, trimmed); err != nil {
				return err
			}
		}
		return nil
	})
}
