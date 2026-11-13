package services

// 知识包卸载，对齐 Python bundle_admin_service.py 的 _uninstall_locked

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
)

// bundleMissingOwnershipMessage 对齐 Python 卸载守卫文案
const bundleMissingOwnershipMessage = "该安装记录缺少资源归属信息，无法安全卸载，请重新安装后再试"

// bundleFactEvidenceDetacher 卸载路径需要的事务内事实证据解绑能力
// Metadata 接口保持精简，这里通过类型断言取用真实 storage.Store
type bundleFactEvidenceDetacher interface {
	DetachFactEvidenceForParagraphsOnConn(paragraphHashes []string, reason string, detachedAt *float64, conn *storage.Conn) (map[string]any, error)
}

// uninstallBundle 对齐 Python _uninstall：图片运行时存在时在发布临界区内卸载
func (s *BundleAdminService) uninstallBundle(ctx context.Context, installationID string) map[string]any {
	runtime := s.imageRuntime()
	if runtime == nil {
		return s.uninstallBundleLocked(ctx, installationID)
	}
	var result map[string]any
	if err := runtime.PublicationGuard(func() error {
		result = s.uninstallBundleLocked(ctx, installationID)
		return nil
	}); err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return result
}

// uninstallBundleLocked 对齐 _uninstall_locked，调用方已持有图片发布锁
func (s *BundleAdminService) uninstallBundleLocked(ctx context.Context, installationID string) map[string]any {
	trimmed := strings.TrimSpace(installationID)
	if trimmed == "" {
		return map[string]any{"success": false, "error": "installation_id 不能为空"}
	}
	rows, err := s.Meta().Query("SELECT * FROM knowledge_packages WHERE installation_id = ?", trimmed)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	if len(rows) == 0 {
		return map[string]any{"success": false, "error": "知识包安装记录不存在"}
	}
	resources, err := s.Meta().Query("SELECT * FROM knowledge_package_resources WHERE installation_id = ?", trimmed)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	imageCount, err := s.activeImageOccurrenceCount(trimmed)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	if len(resources) == 0 && imageCount == 0 {
		return map[string]any{"success": false, "error": bundleMissingOwnershipMessage}
	}

	removed := map[string]int{}
	retainedShared := map[string]int{}
	released := []map[string]any{}
	removedVectorIDs := map[string][]string{"paragraphs": {}, "entities": {}, "relations": {}}
	// 对齐 Python：图片引用释放在独立事务内先提交，资源删除失败不回滚已释放的图片
	// 发布锁已由 uninstallBundle 持有，这里用非原子版本避免重复加锁
	if runtime := s.imageRuntime(); runtime != nil && imageCount > 0 {
		releaseErr := s.Meta().Transaction(true, func(tx *storage.Tx) error {
			assets, err := runtime.ReleaseInstallation(trimmed, tx.Conn())
			if err != nil {
				return err
			}
			released = assets
			return nil
		})
		if releaseErr != nil {
			return map[string]any{"success": false, "error": releaseErr.Error()}
		}
	}
	err = s.Meta().Transaction(true, func(tx *storage.Tx) error {
		for index := len(fullStateTableOrder) - 1; index >= 0; index-- {
			table := fullStateTableOrder[index]
			tableResources, queryErr := queryRows(tx,
				"SELECT * FROM knowledge_package_resources WHERE installation_id = ? AND resource_type = ?", trimmed, table)
			if queryErr != nil {
				return queryErr
			}
			for _, resource := range tableResources {
				key := asString(resource["resource_key"])
				if intOr(resource["created_by_installation"], 0, 0) == 0 {
					continue
				}
				others, otherErr := queryRows(tx,
					"SELECT installation_id, created_by_installation FROM knowledge_package_resources WHERE resource_type = ? AND resource_key = ? AND installation_id <> ? ORDER BY installation_id ASC",
					table, key, trimmed)
				if otherErr != nil {
					return otherErr
				}
				if len(others) > 0 {
					hasOwner := false
					for _, other := range others {
						if intOr(other["created_by_installation"], 0, 0) == 1 {
							hasOwner = true
							break
						}
					}
					// Python 仅在其他安装都没有归属标记时把第一个安装提升为创建者
					if !hasOwner {
						if _, updateErr := tx.Exec(
							"UPDATE knowledge_package_resources SET created_by_installation = 1 WHERE resource_type = ? AND resource_key = ? AND installation_id = ?",
							table, key, asString(others[0]["installation_id"])); updateErr != nil {
							return updateErr
						}
					}
					retainedShared[table]++
					continue
				}
				values, valueErr := resourceKeyValues(table, key)
				if valueErr != nil {
					return valueErr
				}
				hashValue := strings.TrimSpace(asString(values[0]))
				if table == "paragraphs" && hashValue != "" {
					// 对齐 Python _uninstall_locked：先解绑事实证据，再清 ngram 与 tokenized FTS
					if detacher, ok := s.Meta().(bundleFactEvidenceDetacher); ok {
						if _, detachErr := detacher.DetachFactEvidenceForParagraphsOnConn(
							[]string{hashValue}, "knowledge_package_uninstalled", nil, tx.Conn()); detachErr != nil {
							return detachErr
						}
					}
					s.Meta().DeleteParagraphNgramsIfReady([]string{hashValue}, -1, tx.Conn())
					s.Meta().FtsDeleteTokenizedParagraph(hashValue, tx.Conn())
				}
				deleted, deleteErr := s.deleteResourceRow(tx, table, key)
				if deleteErr != nil {
					return deleteErr
				}
				removed[table] += deleted
				if deleted > 0 && hashValue != "" {
					switch table {
					case "paragraphs", "entities", "relations":
						removedVectorIDs[table] = append(removedVectorIDs[table], hashValue)
					}
				}
			}
		}
		if _, err := tx.Exec("DELETE FROM knowledge_package_resources WHERE installation_id = ?", trimmed); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM knowledge_package_paragraphs WHERE installation_id = ?", trimmed); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM knowledge_packages WHERE installation_id = ?", trimmed); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}

	removedParagraphVectors := 0
	paragraphStore := s.paragraphVectorStore()
	if paragraphStore != nil && len(removedVectorIDs["paragraphs"]) > 0 {
		deleted, deleteErr := paragraphStore.Delete(removedVectorIDs["paragraphs"])
		if deleteErr != nil {
			return map[string]any{"success": false, "error": deleteErr.Error()}
		}
		removedParagraphVectors = deleted
	}
	removedGraphVectors := 0
	graphStore := s.graphVectorStore()
	graphIDs := s.vectorDeleteIDs(removedVectorIDs["entities"], removedVectorIDs["relations"])
	if graphStore != nil && len(graphIDs) > 0 {
		deleted, deleteErr := graphStore.Delete(graphIDs)
		if deleteErr != nil {
			return map[string]any{"success": false, "error": deleteErr.Error()}
		}
		removedGraphVectors = deleted
	}
	graph := s.rebuildBundleGraph(ctx)
	if _, aliasErr := s.Meta().RebuildRelationHashAliases(nil); aliasErr != nil {
		return map[string]any{"success": false, "error": aliasErr.Error()}
	}
	if s.deps.PersistVectors != nil {
		s.deps.PersistVectors()
	} else {
		if paragraphStore != nil {
			if saveErr := paragraphStore.Save("", nil); saveErr != nil {
				return map[string]any{"success": false, "error": saveErr.Error()}
			}
		}
		if graphStore != nil && graphStore != paragraphStore {
			if saveErr := graphStore.Save("", nil); saveErr != nil {
				return map[string]any{"success": false, "error": saveErr.Error()}
			}
		}
	}
	if runtime := s.imageRuntime(); runtime != nil && len(released) > 0 {
		if finalizeErr := runtime.FinalizeReleasedAssets(released); finalizeErr != nil {
			return map[string]any{"success": false, "error": finalizeErr.Error()}
		}
	}
	removedMemories := false
	for _, count := range removed {
		if count > 0 {
			removedMemories = true
			break
		}
	}
	return map[string]any{
		"success":          true,
		"installation_id":  trimmed,
		"removed_memories": removedMemories,
		"removed":          removed,
		"retained_shared":  retainedShared,
		"removed_vectors":  map[string]any{"paragraphs": removedParagraphVectors, "graph": removedGraphVectors},
		"removed_images":   len(released),
		"graph":            graph,
		"message":          "知识包及其独占记忆已卸载，共享记忆已保留",
	}
}

// activeImageOccurrenceCount 统计安装记录下活跃的图片出现
func (s *BundleAdminService) activeImageOccurrenceCount(installationID string) (int, error) {
	if s.imageRuntime() == nil {
		return 0, nil
	}
	rows, err := s.Meta().Query(
		"SELECT COUNT(*) AS total FROM image_occurrences WHERE installation_id = ? AND status = 'active'", installationID)
	if err != nil {
		return 0, fmt.Errorf("统计图片出现记录失败: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return intOr(rows[0]["total"], 0, 0), nil
}
