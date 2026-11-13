package expressionvector

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// ExpressionVectorSpaceDeleteStatus 表达向量空间删除结果
type ExpressionVectorSpaceDeleteStatus string

const (
	// ExpressionVectorSpaceDeleted 分组已删除
	ExpressionVectorSpaceDeleted ExpressionVectorSpaceDeleteStatus = "deleted"
	// ExpressionVectorSpaceInUse 分组仍在使用中，不能删除
	ExpressionVectorSpaceInUse ExpressionVectorSpaceDeleteStatus = "in_use"
	// ExpressionVectorSpaceNotFound 索引或分组不存在
	ExpressionVectorSpaceNotFound ExpressionVectorSpaceDeleteStatus = "not_found"
)

// LoadExpressionVectorSpacePayload 容错读取模型库清单
// 对齐 Python _read_and_parse_index_payload:475-501：文件不存在、JSON 损坏或根节点不是对象时按空索引处理
func LoadExpressionVectorSpacePayload(indexPath string) (map[string]any, error) {
	indexPath = strings.TrimSpace(indexPath)
	if indexPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var decoded any
	if err := sonic.Unmarshal(raw, &decoded); err != nil {
		zap.L().Warn("表达向量索引 JSON 已损坏，按空索引处理", zap.String("path", indexPath))
		return nil, nil
	}
	payload, ok := decoded.(map[string]any)
	if !ok {
		zap.L().Warn("表达向量索引 JSON 根节点不是对象，按空索引处理", zap.String("path", indexPath))
		return nil, nil
	}
	return payload, nil
}

// DeleteExpressionVectorSpaceFile 从索引清单中删除一个未使用的模型分组
// 无 embedding_profiles 的旧格式按实际单空间处理，删除时同步清理 npz 与清单
// currentMarker 为运行期 current profile，对齐 Python 删除时的在用保护
func DeleteExpressionVectorSpaceFile(indexPath, spaceID string, enabled bool, currentMarker string) (ExpressionVectorSpaceDeleteStatus, error) {
	indexPath = strings.TrimSpace(indexPath)
	spaceID = strings.TrimSpace(spaceID)
	currentMarker = strings.TrimSpace(currentMarker)
	if indexPath == "" || spaceID == "" {
		return ExpressionVectorSpaceNotFound, nil
	}
	payload, err := LoadExpressionVectorSpacePayload(indexPath)
	if err != nil {
		return ExpressionVectorSpaceNotFound, err
	}
	if payload == nil {
		return ExpressionVectorSpaceNotFound, nil
	}
	activeMarker := ""
	if enabled {
		activeMarker = firstNonEmptyExpressionVectorText(
			expressionVectorSpaceText(payload["embedding_profile_marker"]),
			expressionVectorLegacyMarker,
		)
	}
	if spaceID == activeMarker || (enabled && currentMarker != "" && spaceID == currentMarker) {
		return ExpressionVectorSpaceInUse, nil
	}
	profiles, err := expressionVectorSpaceProfiles(payload)
	if err != nil {
		return ExpressionVectorSpaceNotFound, err
	}
	found := false
	remaining := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		if expressionVectorSpaceText(profile["marker"]) == spaceID {
			found = true
			continue
		}
		remaining = append(remaining, profile)
	}
	if !found {
		return ExpressionVectorSpaceNotFound, nil
	}
	vectorsPath := expressionVectorResolveVectorsPath(indexPath, expressionVectorSpaceText(payload["vectors_file"]))
	if len(remaining) == 0 {
		// 对齐 Python：先清理可归属的 npz，再删除清单
		if err := removeExpressionVectorSpaceOwnedFile(indexPath, vectorsPath); err != nil {
			return ExpressionVectorSpaceNotFound, err
		}
		if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
			return ExpressionVectorSpaceNotFound, err
		}
		return ExpressionVectorSpaceDeleted, nil
	}

	arrays, arraysErr := readExpressionVectorNPZ(vectorsPath)
	if arraysErr != nil && !os.IsNotExist(arraysErr) {
		return ExpressionVectorSpaceNotFound, fmt.Errorf("表达向量索引 npz 读取失败: %w", arraysErr)
	}
	if os.IsNotExist(arraysErr) && strings.TrimSpace(expressionVectorSpaceText(payload["vectors_file"])) != "" {
		return ExpressionVectorSpaceNotFound, fmt.Errorf("表达向量索引缺少向量文件: %s", vectorsPath)
	}
	nextVectorsPath := ""
	if arraysErr == nil {
		filteredArrays := make(map[string]expressionVectorArray, len(remaining)*2)
		for _, profile := range remaining {
			marker := expressionVectorSpaceText(profile["marker"])
			vectorsKey := expressionVectorSpaceText(profile["vectors_key"])
			centersKey := expressionVectorSpaceText(profile["cluster_centers_key"])
			if marker == "" || vectorsKey == "" || centersKey == "" {
				return ExpressionVectorSpaceNotFound, fmt.Errorf("表达向量索引 profile 缺少数组键: %s", marker)
			}
			vectors, ok := arrays[vectorsKey]
			if !ok {
				return ExpressionVectorSpaceNotFound, fmt.Errorf("表达向量索引缺少数组: %s", vectorsKey)
			}
			centers, ok := arrays[centersKey]
			if !ok {
				return ExpressionVectorSpaceNotFound, fmt.Errorf("表达向量索引缺少数组: %s", centersKey)
			}
			filteredArrays[vectorsKey] = vectors
			filteredArrays[centersKey] = centers
		}
		var err error
		nextVectorsPath, err = expressionVectorVersionedVectorsPath(indexPath)
		if err != nil {
			return ExpressionVectorSpaceNotFound, err
		}
		if err := writeExpressionVectorNPZ(nextVectorsPath, filteredArrays); err != nil {
			_ = os.Remove(nextVectorsPath)
			return ExpressionVectorSpaceNotFound, err
		}
	}
	cleanupVectors := func() {
		if nextVectorsPath != "" {
			_ = os.Remove(nextVectorsPath)
		}
	}

	expressionKey := "expressions"
	if len(expressionVectorSpaceAnyList(payload["expressions"])) == 0 && len(expressionVectorSpaceAnyList(payload["items"])) > 0 {
		expressionKey = "items"
	}
	filteredExpressions := make([]any, 0)
	for _, item := range expressionVectorSpaceAnyList(payload[expressionKey]) {
		rawItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		marker := firstNonEmptyExpressionVectorText(
			expressionVectorSpaceText(rawItem["embedding_profile_marker"]),
			expressionVectorSpaceText(payload["embedding_profile_marker"]),
			expressionVectorLegacyMarker,
		)
		if marker == spaceID {
			continue
		}
		filteredExpressions = append(filteredExpressions, rawItem)
	}
	clusters, err := expressionVectorSpaceClusters(filteredExpressions)
	if err != nil {
		cleanupVectors()
		return ExpressionVectorSpaceNotFound, err
	}
	payload[expressionKey] = filteredExpressions
	payload["embedding_profiles"] = remaining
	payload["sample_count"] = len(filteredExpressions)
	payload["clusters"] = clusters
	payload["updated_at"] = time.Now().Format("2006-01-02T15:04:05")
	if expressionVectorSpaceText(payload["embedding_profile_marker"]) == spaceID {
		delete(payload, "embedding_profile")
		delete(payload, "cluster_maintenance")
		first := remaining[0]
		payload["embedding_profile_marker"] = expressionVectorSpaceText(first["marker"])
		payload["embedding_model"] = expressionVectorSpaceText(first["embedding_model"])
		payload["embedding_dimension"] = expressionVectorSpaceInt(first["embedding_dimension"])
	}
	if nextVectorsPath != "" {
		payload["vectors_file"] = filepath.Base(nextVectorsPath)
	}
	encoded, err := sonic.ConfigStd.MarshalIndent(payload, "", "  ")
	if err != nil {
		cleanupVectors()
		return ExpressionVectorSpaceNotFound, err
	}
	if err := writeExpressionVectorJSONAtomic(indexPath, encoded); err != nil {
		cleanupVectors()
		return ExpressionVectorSpaceNotFound, err
	}
	if nextVectorsPath != "" {
		if err := removeExpressionVectorSpaceOwnedFile(indexPath, vectorsPath); err != nil {
			zap.L().Warn("表达向量索引旧 npz 清理失败，旧文件可安全保留",
				zap.String("path", vectorsPath),
				zap.Error(err))
		}
	}
	return ExpressionVectorSpaceDeleted, nil
}

func expressionVectorSpaceProfiles(payload map[string]any) ([]map[string]any, error) {
	rawProfiles := expressionVectorSpaceAnyList(payload["embedding_profiles"])
	profiles := make([]map[string]any, 0, len(rawProfiles))
	for _, item := range rawProfiles {
		raw, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("表达向量索引 embedding_profiles 元素格式异常")
		}
		if _, ok := raw["marker"]; !ok {
			return nil, fmt.Errorf("表达向量索引 embedding_profiles 缺少 marker")
		}
		profiles = append(profiles, raw)
	}
	if len(profiles) > 0 {
		return profiles, nil
	}
	return []map[string]any{expressionVectorSpaceLegacyProfile(payload)}, nil
}

func expressionVectorSpaceLegacyProfile(payload map[string]any) map[string]any {
	expressions := expressionVectorSpaceAnyList(payload["expressions"])
	if len(expressions) == 0 {
		expressions = expressionVectorSpaceAnyList(payload["items"])
	}
	return map[string]any{
		"marker":              firstNonEmptyExpressionVectorText(expressionVectorSpaceText(payload["embedding_profile_marker"]), expressionVectorLegacyMarker),
		"embedding_model":     expressionVectorSpaceText(payload["embedding_model"]),
		"embedding_dimension": expressionVectorSpaceInt(payload["embedding_dimension"]),
		"expression_count":    len(expressions),
		"cluster_count":       len(expressionVectorSpaceAnyList(payload["clusters"])),
		"vectors_key":         "vectors",
		"cluster_centers_key": "cluster_centers",
	}
}

func expressionVectorSpaceClusters(expressions []any) ([]persistentExpressionCluster, error) {
	raw, err := sonic.Marshal(expressions)
	if err != nil {
		return nil, err
	}
	var typed []persistentExpressionVector
	if err := sonic.Unmarshal(raw, &typed); err != nil {
		return nil, err
	}
	return persistentExpressionClusters(typed), nil
}

func expressionVectorSpaceAnyList(value any) []any {
	if value == nil {
		return nil
	}
	if raw, ok := value.([]any); ok {
		return raw
	}
	return nil
}

func expressionVectorSpaceText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return ""
}

func expressionVectorSpaceInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

func removeExpressionVectorSpaceOwnedFile(indexPath, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if filepath.Clean(filepath.Dir(target)) != filepath.Clean(filepath.Dir(indexPath)) {
		return nil
	}
	stem := strings.TrimSuffix(filepath.Base(indexPath), filepath.Ext(indexPath))
	name := filepath.Base(target)
	if name != stem+".npz" && !strings.HasPrefix(name, stem+".vectors-") {
		return nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
