package services

// 知识包图片导出、清单与卸载辅助，对齐 bundle_admin_service.py 的 _select_image_occurrence_ids 与图片段

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	imagememory "maibot-go/internal/A_memorix/core/image"
	"maibot-go/internal/A_memorix/core/storage"
)

// bundleBoolOr 读取布尔开关，缺省时使用 Python 默认值
func bundleBoolOr(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return pyTruthy(value)
}

// asStrings 把任意列表归一为字符串列表
func asStrings(value any) []string {
	out := []string{}
	for _, item := range asList(value) {
		if token := strings.TrimSpace(asString(item)); token != "" {
			out = append(out, token)
		}
	}
	return out
}

// stringArgs 转换 SQL 参数
func stringArgs(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// selectorImageValues 对齐 Python 对 content_hashes/values 的 or 回退，保留原始顺序与重复
func selectorImageValues(selector map[string]any) []any {
	values := asList(selector["content_hashes"])
	if len(values) == 0 {
		values = asList(selector["values"])
	}
	out := make([]any, 0, len(values))
	return append(out, values...)
}

// selectorStrings 对齐 Python 的 str(item) 过滤，保留原始文本与顺序，不做 trim 与去重
func selectorStrings(values []any) []string {
	out := []string{}
	for _, item := range values {
		text := asString(item)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// selectorContentHashes 对齐 Python 的选择器哈希过滤：优先 content_hashes，空才回退 values，不做去重
func selectorContentHashes(selector map[string]any) []string {
	return selectorStrings(selectorImageValues(selector))
}

// selectImageOccurrenceIDs 对齐 _select_image_occurrence_ids
func (s *BundleAdminService) selectImageOccurrenceIDs(selector map[string]any, paragraphIDs []string) ([]string, error) {
	runtime := s.imageRuntime()
	if runtime == nil {
		return []string{}, nil
	}
	store := runtime.MetadataStore()
	if store == nil {
		return []string{}, nil
	}
	selectorType := strings.ToLower(strings.TrimSpace(asString(selector["type"])))
	if selectorType == "" {
		selectorType = "all"
	}
	switch selectorType {
	case "image", "images":
		occurrenceIDs := selectorStrings(asList(selector["occurrence_ids"]))
		contentHashes := selectorContentHashes(selector)
		ids := []string{}
		if len(occurrenceIDs) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(occurrenceIDs)), ",")
			rows, err := s.Meta().Query(
				"SELECT occurrence_id, status FROM image_occurrences WHERE occurrence_id IN ("+placeholders+")",
				stringArgs(occurrenceIDs)...)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				if asString(row["status"]) == "active" {
					ids = append(ids, asString(row["occurrence_id"]))
				}
			}
		}
		if len(contentHashes) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(contentHashes)), ",")
			rows, err := s.Meta().Query(
				"SELECT occurrence.occurrence_id AS occurrence_id FROM image_occurrences AS occurrence "+
					"JOIN image_assets AS asset ON asset.asset_id = occurrence.asset_id "+
					"WHERE occurrence.status = 'active' AND asset.status = 'active' AND asset.content_hash IN ("+placeholders+")",
				stringArgs(contentHashes)...)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				ids = append(ids, asString(row["occurrence_id"]))
			}
		}
		return dedupeStrings(ids), nil
	case "chat":
		chatID := strings.TrimSpace(asString(firstNonEmpty(selector["chat_id"], selector["value"])))
		rows, err := store.ListVisibleImageOccurrences([]string{chatID}, "")
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			if asString(row["chat_id"]) == chatID {
				ids = append(ids, asString(row["occurrence_id"]))
			}
		}
		return dedupeStrings(ids), nil
	case "all":
		rows, err := store.ListVisibleImageOccurrences(nil, "")
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, asString(row["occurrence_id"]))
		}
		return dedupeStrings(ids), nil
	default:
		if len(paragraphIDs) == 0 {
			return []string{}, nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(paragraphIDs)), ",")
		rows, err := s.Meta().Query(
			"SELECT DISTINCT occurrence_id FROM image_memory_links WHERE status = 'active' "+
				"AND target_type = 'paragraph' AND target_id IN ("+placeholders+")",
			stringArgs(paragraphIDs)...)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, asString(row["occurrence_id"]))
		}
		return dedupeStrings(ids), nil
	}
}

// listImageLinks 读取指定出现记录的有效关联
func (s *BundleAdminService) listImageLinks(occurrenceIDs []string) ([]map[string]any, error) {
	runtime := s.imageRuntime()
	if runtime == nil || len(occurrenceIDs) == 0 {
		return []map[string]any{}, nil
	}
	store := runtime.MetadataStore()
	if store == nil {
		return []map[string]any{}, nil
	}
	links, err := store.ListImageMemoryLinks(occurrenceIDs)
	if err != nil {
		return nil, err
	}
	if links == nil {
		return []map[string]any{}, nil
	}
	return links, nil
}

// paragraphsByHashes 按哈希批量读取段落，过滤已删除
func (s *BundleAdminService) paragraphsByHashes(hashes []string) ([]map[string]any, error) {
	if len(hashes) == 0 {
		return []map[string]any{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	rows, err := s.Meta().Query(
		"SELECT * FROM paragraphs WHERE hash IN ("+placeholders+") AND COALESCE(is_deleted, 0) = 0",
		stringArgs(hashes)...)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rowString(rows[i], "hash") < rowString(rows[j], "hash") })
	return rows, nil
}

// relatedParagraphIDs 按图片关联目标扩展段落范围，对齐 _export_bundle 的 include_image_related
func (s *BundleAdminService) relatedParagraphIDs(links []map[string]any) ([]string, error) {
	ids := map[string]struct{}{}
	for _, link := range links {
		targetType := strings.ToLower(strings.TrimSpace(asString(link["target_type"])))
		targetID := strings.TrimSpace(asString(link["target_id"]))
		if targetID == "" {
			continue
		}
		switch targetType {
		case "paragraph":
			ids[targetID] = struct{}{}
		case "entity", "relation", "episode":
			table, column := "", ""
			switch targetType {
			case "entity":
				table, column = "paragraph_entities", "entity_hash"
			case "relation":
				table, column = "paragraph_relations", "relation_hash"
			default:
				table, column = "episode_paragraphs", "episode_id"
			}
			rows, err := s.Meta().Query("SELECT paragraph_hash FROM "+table+" WHERE "+column+" = ?", targetID)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				hashValue := strings.TrimSpace(asString(row["paragraph_hash"]))
				if hashValue != "" {
					ids[hashValue] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// filterImageLinks 只保留目标在可迁移闭包内的关联
func filterImageLinks(links []map[string]any, targets map[string]map[string]struct{}) []map[string]any {
	out := make([]map[string]any, 0, len(links))
	for _, link := range links {
		targetType := strings.ToLower(strings.TrimSpace(asString(link["target_type"])))
		if _, ok := targets[targetType][strings.TrimSpace(asString(link["target_id"]))]; ok {
			out = append(out, link)
		}
	}
	return out
}

// missingImageLinkTargets 返回不在闭包内的关联目标
func missingImageLinkTargets(links []map[string]any, targets map[string]map[string]struct{}) []map[string]any {
	out := make([]map[string]any, 0)
	for _, link := range links {
		targetType := strings.ToLower(strings.TrimSpace(asString(link["target_type"])))
		if _, ok := targets[targetType][strings.TrimSpace(asString(link["target_id"]))]; !ok {
			out = append(out, link)
		}
	}
	return out
}

// portableImageRecords 生成 images.json 与图片资产成员，对齐 _export_bundle 的 portable 段
func (s *BundleAdminService) portableImageRecords(records map[string]any) (map[string]any, map[string][]byte, error) {
	runtime := s.imageRuntime()
	if runtime == nil {
		return nil, nil, fmt.Errorf("当前运行时未启用图片记忆")
	}
	portable := map[string]any{
		"assets":       anyMapList(records["assets"]),
		"occurrences":  anyMapList(records["occurrences"]),
		"observations": anyMapList(records["observations"]),
		"links":        anyMapList(records["links"]),
		"fingerprint":  asMap(records["fingerprint"]),
	}
	if portable["fingerprint"] == nil {
		portable["fingerprint"] = map[string]any{}
	}
	assetStore := runtime.AssetStore()
	if assetStore == nil {
		return nil, nil, fmt.Errorf("图片资产存储未初始化")
	}
	members := map[string][]byte{}
	for _, asset := range portable["assets"].([]map[string]any) {
		storageKey := strings.TrimSpace(asString(asset["storage_key"]))
		contentHash := strings.TrimSpace(asString(asset["content_hash"]))
		memberName := "images/assets/" + contentHash + imageAssetSuffix(storageKey)
		payload, err := assetStore.ReadBytes(storageKey)
		if err != nil {
			return nil, nil, err
		}
		asset["member"] = memberName
		members[memberName] = payload
	}
	return portable, members, nil
}

// imageAssetSuffix 返回资产存储键的小写扩展名，缺省 .bin
func imageAssetSuffix(storageKey string) string {
	suffix := strings.ToLower(filepath.Ext(storageKey))
	if suffix == "" {
		suffix = ".bin"
	}
	return suffix
}

// imageAssetIDs 提取资产 ID
func imageAssetIDs(records map[string]any) []string {
	out := make([]string, 0)
	for _, asset := range anyMapList(records["assets"]) {
		if assetID := strings.TrimSpace(asString(asset["asset_id"])); assetID != "" {
			out = append(out, assetID)
		}
	}
	return out
}

// imageRuntimeStatus 返回图片运行时状态，未接入时为 unavailable
func (s *BundleAdminService) imageRuntimeStatus() map[string]any {
	runtime := s.imageRuntime()
	if runtime == nil {
		return map[string]any{"status": "unavailable", "message": ""}
	}
	status, err := runtime.Status()
	if err != nil || status == nil {
		return map[string]any{"status": "unavailable", "message": ""}
	}
	return status
}

// imageInspectSummary 对齐 inspect 的图片摘要
func (s *BundleAdminService) imageInspectSummary(loaded *loadedBundle) map[string]any {
	runtime := s.imageRuntime()
	current := map[string]any{}
	if runtime != nil {
		current = s.imageRuntimeStatus()
	}
	declared := asMap(loaded.Manifest["image_embedding_fingerprint"])
	compatible := false
	if runtime != nil && len(declared) > 0 {
		compatible = imagememory.FingerprintHash(declared) == imagememory.FingerprintHash(runtime.Fingerprint())
	}
	runtimeStatus := strings.TrimSpace(asString(current["status"]))
	if runtimeStatus == "" {
		runtimeStatus = "disabled"
	}
	return map[string]any{
		"vector_compatible": compatible,
		"has_vectors":       loaded.hasMember("vectors/images.npz"),
		"runtime_status":    runtimeStatus,
		"missing_resources": 0,
	}
}

// installationImageCounts 查询安装记录的图片资产、出现与就绪数量
func (s *BundleAdminService) installationImageCounts(installationID string) (map[string]any, string, error) {
	runtime := s.imageRuntime()
	if runtime == nil {
		return map[string]any{"assets": 0, "occurrences": 0, "ready": 0}, "disabled", nil
	}
	runtimeStatus := strings.TrimSpace(asString(s.imageRuntimeStatus()["status"]))
	if runtimeStatus == "" {
		runtimeStatus = "unavailable"
	}
	fingerprintHash := imagememory.FingerprintHash(runtime.Fingerprint())
	rows, err := s.Meta().Query(
		"SELECT COUNT(DISTINCT o.asset_id) AS assets, COUNT(DISTINCT o.occurrence_id) AS occurrences, "+
			"COUNT(DISTINCT e.asset_id) AS ready FROM image_occurrences o "+
			"LEFT JOIN image_embeddings e ON e.asset_id = o.asset_id AND e.fingerprint_hash = ? AND e.status = 'ready' "+
			"WHERE o.installation_id = ? AND o.status = 'active'", fingerprintHash, installationID)
	if err != nil {
		return nil, runtimeStatus, err
	}
	counts := map[string]any{"assets": 0, "occurrences": 0, "ready": 0}
	if len(rows) > 0 {
		counts["assets"] = intOr(rows[0]["assets"], 0, 0)
		counts["occurrences"] = intOr(rows[0]["occurrences"], 0, 0)
		counts["ready"] = intOr(rows[0]["ready"], 0, 0)
	}
	return counts, runtimeStatus, nil
}

// rebuildBundleGraph 调用可选图重建依赖，未接入时如实返回零计数
func (s *BundleAdminService) rebuildBundleGraph(ctx context.Context) map[string]any {
	if s.deps.RebuildGraph == nil {
		return map[string]any{"node_count": 0, "edge_count": 0}
	}
	result, err := s.deps.RebuildGraph(ctx)
	if err != nil || result == nil {
		return map[string]any{"node_count": 0, "edge_count": 0}
	}
	return result
}

// setInstallationStatus 更新安装记录状态
func (s *BundleAdminService) setInstallationStatus(installationID, status string) error {
	return s.Meta().Transaction(true, func(tx *storage.Tx) error {
		_, err := tx.Exec("UPDATE knowledge_packages SET status = ? WHERE installation_id = ?", status, installationID)
		return err
	})
}

// vectorDeleteIDs 生成待删除的图向量 ID，双池开启时带类型前缀
func (s *BundleAdminService) vectorDeleteIDs(entityHashes, relationHashes []string) []string {
	if s.dualVectorPoolsEnabled() && s.deps.GraphVectorID != nil {
		out := make([]string, 0, len(entityHashes)+len(relationHashes))
		for _, hashValue := range entityHashes {
			out = append(out, s.deps.GraphVectorID("entity", hashValue))
		}
		for _, hashValue := range relationHashes {
			out = append(out, s.deps.GraphVectorID("relation", hashValue))
		}
		return dedupeStrings(out)
	}
	return dedupeStrings(append(append([]string{}, entityHashes...), relationHashes...))
}

// releaseImagesForInstallation 在元数据事务内释放图片出现记录
func (s *BundleAdminService) releaseImagesForInstallation(tx *storage.Tx, installationID string) ([]map[string]any, error) {
	runtime := s.imageRuntime()
	if runtime == nil {
		return []map[string]any{}, nil
	}
	return runtime.ReleaseInstallation(installationID, tx.Conn())
}
