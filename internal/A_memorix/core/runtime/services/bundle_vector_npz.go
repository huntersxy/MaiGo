package services

// 知识包文本与图谱向量 npz 导出导入，对齐 Python _export_vector_member 与 _import_vector_member

import (
	"context"
	"fmt"
	"sort"
	"strings"

	imagememory "MaiGo/internal/A_memorix/core/image"
	"MaiGo/internal/A_memorix/core/storage"
)

// exportVectorMember 对齐 _export_vector_member
// store 为空或 ids 为空时返回 nil；archive_ids 用于双池模式导出类型前缀 ID
func exportVectorMember(store *storage.VectorStore, ids []string, archiveIDs []string) ([]byte, error) {
	if store == nil || len(ids) == 0 {
		return nil, nil
	}
	exportedIDs := ids
	if archiveIDs != nil {
		exportedIDs = archiveIDs
	}
	if len(exportedIDs) != len(ids) {
		return nil, fmt.Errorf("导出向量 ID 映射数量不一致")
	}
	vectorMap, err := store.GetVectors(ids)
	if err != nil {
		return nil, err
	}
	orderedIDs := make([]string, 0, len(ids))
	vectors := make([][]float32, 0, len(ids))
	for index, storedID := range ids {
		vector, ok := vectorMap[storedID]
		if !ok {
			continue
		}
		orderedIDs = append(orderedIDs, exportedIDs[index])
		vectors = append(vectors, vector)
	}
	if len(orderedIDs) == 0 {
		return nil, nil
	}
	return imagememory.EncodeVectorNPZ(orderedIDs, vectors)
}

// importVectorMember 对齐 _import_vector_member
// 只接受 allowedIDs 内的 ID，已存在的 store ID 跳过，缺失的才写入
func importVectorMember(payload []byte, store *storage.VectorStore, allowedIDs map[string]struct{}, storeIDByArchiveID map[string]string) (int, map[string]struct{}, error) {
	accepted := map[string]struct{}{}
	if store == nil {
		return 0, accepted, nil
	}
	decoded, err := imagememory.DecodeVectorNPZ(payload)
	if err != nil {
		return 0, accepted, err
	}
	if !decoded.TwoDimensional || decoded.Rows != len(decoded.IDs) {
		return 0, accepted, fmt.Errorf("记忆包向量数量与 ID 数量不一致")
	}
	storeIDs := make([]string, 0, len(decoded.IDs))
	vectors := make([][]float32, 0, len(decoded.IDs))
	for index, archiveID := range decoded.IDs {
		if _, ok := allowedIDs[archiveID]; !ok {
			continue
		}
		accepted[archiveID] = struct{}{}
		storeID := archiveID
		if storeIDByArchiveID != nil {
			if mapped, ok := storeIDByArchiveID[archiveID]; ok {
				storeID = mapped
			}
		}
		if store.Contains(storeID) {
			continue
		}
		storeIDs = append(storeIDs, storeID)
		vectors = append(vectors, decoded.Vectors[index])
	}
	if len(storeIDs) == 0 {
		return 0, accepted, nil
	}
	if _, err := store.Add(vectors, storeIDs); err != nil {
		return 0, accepted, err
	}
	return len(storeIDs), accepted, nil
}

// bundleVectorsCompatible 对齐 _bundle_vectors_compatible
func bundleVectorsCompatible(manifest map[string]any, current map[string]any) bool {
	packaged := asMap(manifest["embedding_fingerprint"])
	if packaged == nil || current == nil {
		return false
	}
	packagedHash := strings.TrimSpace(asString(packaged["hash"]))
	if packagedHash == "" {
		return false
	}
	return packagedHash == strings.TrimSpace(asString(current["hash"]))
}

// bundleHashSet 把 hash 列表转为集合
func bundleHashSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	return out
}

// bundleRowHashes 按行顺序取出 hash 列
func bundleRowHashes(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		hashValue := strings.TrimSpace(rowString(row, "hash"))
		if hashValue != "" {
			out = append(out, hashValue)
		}
	}
	return out
}

// ensureImportedVectors 为未随包导入的段落、实体、关系补齐向量，对齐 _ensure_imported_vectors
func (s *BundleAdminService) ensureImportedVectors(ctx context.Context, paragraphHashes []string, entityHashes []string, relationHashes []string, packaged map[string]struct{}) (map[string]int, error) {
	generated := map[string]int{"paragraphs": 0, "entities": 0, "relations": 0}
	if s.deps.EnsureParagraphVector != nil {
		rows, err := s.Meta().GetParagraphsByHashes(dedupeStrings(paragraphHashes))
		if err != nil {
			return nil, err
		}
		for _, hashValue := range dedupeStrings(paragraphHashes) {
			if _, ok := packaged[hashValue]; ok {
				continue
			}
			row := rows[hashValue]
			if row == nil {
				continue
			}
			written, ensureErr := s.deps.EnsureParagraphVector(ctx, map[string]any{
				"hash": hashValue, "content": rowString(row, "content"),
			})
			if ensureErr != nil {
				return nil, ensureErr
			}
			if written {
				generated["paragraphs"]++
			}
		}
	}
	if s.deps.EnsureEntityVector != nil {
		rows, err := s.bundleEntityRowsByHashes(dedupeStrings(entityHashes))
		if err != nil {
			return nil, err
		}
		for _, hashValue := range sortedStrings(dedupeStrings(entityHashes)) {
			vectorID := s.graphVectorID("entity", hashValue)
			if _, ok := packaged[vectorID]; ok {
				continue
			}
			row := rows[hashValue]
			if row == nil {
				continue
			}
			written, ensureErr := s.deps.EnsureEntityVector(ctx, map[string]any{
				"hash": hashValue, "name": rowString(row, "name"),
			})
			if ensureErr != nil {
				return nil, ensureErr
			}
			if written {
				generated["entities"]++
			}
		}
	}
	if s.deps.EnsureRelationVector != nil && s.relationVectorsEnabled() {
		rows, err := s.Meta().GetRelationsByHashes(dedupeStrings(relationHashes), true)
		if err != nil {
			return nil, err
		}
		for _, hashValue := range sortedStrings(dedupeStrings(relationHashes)) {
			vectorID := s.graphVectorID("relation", hashValue)
			if _, ok := packaged[vectorID]; ok {
				continue
			}
			row := rows[hashValue]
			if row == nil {
				continue
			}
			written, ensureErr := s.deps.EnsureRelationVector(ctx, map[string]any{
				"hash": hashValue, "subject": rowString(row, "subject"),
				"predicate": rowString(row, "predicate"), "object": rowString(row, "object"),
			})
			if ensureErr != nil {
				return nil, ensureErr
			}
			if written {
				generated["relations"]++
			}
		}
	}
	return generated, nil
}

// bundleEntityRowsByHashes 批量读取实体行，Metadata 接口未暴露实体批量查询
func (s *BundleAdminService) bundleEntityRowsByHashes(hashes []string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	args := make([]any, 0, len(hashes))
	for _, hashValue := range hashes {
		args = append(args, hashValue)
	}
	rows, err := s.Meta().Query("SELECT * FROM entities WHERE hash IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if hashValue := rowString(row, "hash"); hashValue != "" {
			out[hashValue] = row
		}
	}
	return out, nil
}

// sortedStrings 返回排序后的字符串副本
func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
