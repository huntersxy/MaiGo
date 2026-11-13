package expressionvector

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// decodedExpressionVectorIndex 是一次载入后的分组视图，激活分组进入内存检索
type decodedExpressionVectorIndex struct {
	activeMarker string
	profiles     map[string]*expressionVectorProfileState
	activeItems  []persistentExpressionVector
	vectorsFile  string
}

func (s *PersistentExpressionVectorStore) loadLocked() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.resetLocked()
			return s.loadEmbeddingFailuresFileLocked()
		}
		return err
	}
	var payload persistentExpressionVectorFile
	if err := sonic.ConfigStd.Unmarshal(raw, &payload); err != nil {
		zap.L().Warn("表达向量索引文件损坏，按空索引重建",
			zap.String("path", s.path),
			zap.Error(err))
		s.resetLocked()
		return nil
	}
	if payload.Version > expressionVectorIndexFileVersion {
		return fmt.Errorf("表达向量索引版本不匹配: %d", payload.Version)
	}
	legacyInline := persistentExpressionVectorsInline(payload.Expressions) || persistentExpressionVectorsInline(payload.Items)
	decoded, err := decodePersistentExpressionVectorIndex(s.path, payload)
	if err != nil {
		return err
	}
	s.mem.Replace(decoded.activeItems)
	s.generatedAt = strings.TrimSpace(payload.GeneratedAt)
	s.activeMarker = decoded.activeMarker
	s.profiles = decoded.profiles
	s.vectorsFile = decoded.vectorsFile
	s.embeddingFailures = make(map[uint]persistentEmbeddingFailure, len(payload.EmbeddingFailures))
	for _, record := range payload.EmbeddingFailures {
		if record.ExpressionID == 0 {
			continue
		}
		s.embeddingFailures[record.ExpressionID] = record
	}
	s.clusterMaintenance = payload.ClusterMaintenance
	s.embeddingProfile = payload.EmbeddingProfile
	s.pendingClusterChanges = nil
	s.forceClusterRebuild = false
	if err := s.loadEmbeddingFailuresFileLocked(); err != nil {
		return err
	}
	if legacyInline {
		// 旧 Go 索引把向量内联在 JSON 中，载入时立即迁移为 npz，避免继续写入旧格式
		if err := s.saveLocked(); err != nil {
			zap.L().Warn("表达向量索引旧格式迁移失败，保留原文件",
				zap.String("path", s.path),
				zap.Error(err))
		}
	}
	return nil
}

func (s *PersistentExpressionVectorStore) resetLocked() {
	s.mem.Replace(nil)
	s.generatedAt = ""
	s.activeMarker = ""
	s.profiles = make(map[string]*expressionVectorProfileState)
	s.vectorsFile = ""
	s.embeddingFailures = make(map[uint]persistentEmbeddingFailure)
	s.clusterMaintenance = nil
	s.embeddingProfile = nil
	s.pendingClusterChanges = nil
	s.forceClusterRebuild = false
}

func decodePersistentExpressionVectorIndex(indexPath string, payload persistentExpressionVectorFile) (decodedExpressionVectorIndex, error) {
	expressions := payload.Expressions
	if len(expressions) == 0 {
		expressions = payload.Items
	}
	profiles := make(map[string]*expressionVectorProfileState)
	vectorsPath := expressionVectorResolveVectorsPath(indexPath, payload.VectorsFile)
	arrays, arraysErr := readExpressionVectorNPZ(vectorsPath)
	if arraysErr != nil && !os.IsNotExist(arraysErr) {
		return decodedExpressionVectorIndex{}, fmt.Errorf("表达向量索引 npz 读取失败: %w", arraysErr)
	}
	inline := persistentExpressionVectorsInline(expressions)
	hasProfiles := len(payload.EmbeddingProfiles) > 0

	switch {
	case inline:
		// 旧 Go JSON 把向量内联在 expressions/items 中，载入后由下一次保存迁移为 npz
		if err := decodeInlineExpressionVectorProfile(payload, expressions, profiles); err != nil {
			return decodedExpressionVectorIndex{}, err
		}
	case hasProfiles:
		if arraysErr != nil {
			return decodedExpressionVectorIndex{}, fmt.Errorf("表达向量索引缺少向量文件: %s", vectorsPath)
		}
		if err := decodeNPZExpressionVectorProfiles(payload, arrays, profiles); err != nil {
			return decodedExpressionVectorIndex{}, err
		}
	default:
		if arraysErr == nil {
			if err := decodeLegacyNPZExpressionVectorProfile(payload, arrays, profiles); err != nil {
				return decodedExpressionVectorIndex{}, err
			}
		} else if len(expressions) > 0 {
			return decodedExpressionVectorIndex{}, fmt.Errorf("表达向量索引缺少向量文件: %s", vectorsPath)
		} else {
			marker := firstNonEmptyExpressionVectorText(payload.EmbeddingProfileMarker, expressionVectorLegacyMarker)
			profiles[marker] = newEmptyExpressionVectorProfile(marker)
		}
	}

	activeMarker := strings.TrimSpace(payload.EmbeddingProfileMarker)
	if _, ok := profiles[activeMarker]; !ok || activeMarker == "" {
		activeMarker = expressionVectorFirstProfileMarker(profiles)
	}
	if activeMarker == "" {
		return decodedExpressionVectorIndex{profiles: profiles}, nil
	}
	allItems, err := decodeExpressionVectorItems(payload, expressions, profiles)
	if err != nil {
		return decodedExpressionVectorIndex{}, err
	}
	for marker, profile := range profiles {
		profile.expressions = filterExpressionVectorItems(allItems, marker)
	}
	activeItems := profiles[activeMarker].expressions
	for marker, profile := range profiles {
		profile.metadata.ExpressionCount = len(profile.expressions)
		profile.metadata.ClusterCount = profile.clusterCenters.rows
		if profile.metadata.EmbeddingDimension == 0 {
			profile.metadata.EmbeddingDimension = profile.vectors.cols
		}
		if marker == activeMarker {
			profile.metadata.Marker = activeMarker
		}
	}
	return decodedExpressionVectorIndex{
		activeMarker: activeMarker,
		profiles:     profiles,
		activeItems:  activeItems,
		vectorsFile:  vectorsPath,
	}, nil
}

func persistentExpressionVectorsInline(expressions []persistentExpressionVector) bool {
	for _, item := range expressions {
		if len(item.Vector) > 0 {
			return true
		}
	}
	return false
}

func decodeInlineExpressionVectorProfile(payload persistentExpressionVectorFile, expressions []persistentExpressionVector, profiles map[string]*expressionVectorProfileState) error {
	marker := firstNonEmptyExpressionVectorText(payload.EmbeddingProfileMarker, expressionVectorLegacyMarker)
	profile := newEmptyExpressionVectorProfile(marker)
	dimension := 0
	vectors := make([]float32, 0, len(expressions))
	for index := range expressions {
		item := expressions[index]
		if item.ID == 0 {
			continue
		}
		if len(item.Vector) == 0 {
			return fmt.Errorf("表达向量索引内联向量缺失: id=%d", item.ID)
		}
		if dimension == 0 {
			dimension = len(item.Vector)
		}
		if len(item.Vector) != dimension {
			return fmt.Errorf("表达向量索引维度不一致: id=%d dimension=%d expected=%d", item.ID, len(item.Vector), dimension)
		}
		vectors = append(vectors, item.Vector...)
	}
	rows := 0
	if dimension > 0 {
		rows = len(vectors) / dimension
	}
	profile.vectors = expressionVectorArray{rows: rows, cols: dimension, data: vectors}
	profile.clusterCenters = expressionVectorArray{cols: dimension}
	profiles[marker] = profile
	return nil
}

func decodeNPZExpressionVectorProfiles(payload persistentExpressionVectorFile, arrays map[string]expressionVectorArray, profiles map[string]*expressionVectorProfileState) error {
	for _, rawProfile := range payload.EmbeddingProfiles {
		marker := normalizeExpressionVectorText(rawProfile.Marker)
		vectorsKey := normalizeExpressionVectorText(rawProfile.VectorsKey)
		centersKey := normalizeExpressionVectorText(rawProfile.ClusterCentersKey)
		if marker == "" || vectorsKey == "" || centersKey == "" {
			continue
		}
		vectors, ok := arrays[vectorsKey]
		if !ok {
			return fmt.Errorf("表达向量索引缺少数组: %s", vectorsKey)
		}
		centers, ok := arrays[centersKey]
		if !ok {
			return fmt.Errorf("表达向量索引缺少数组: %s", centersKey)
		}
		normalizedVectors, err := normalizeExpressionVectorArray(vectors, "vectors")
		if err != nil {
			return err
		}
		normalizedCenters, err := normalizeExpressionVectorArray(centers, "cluster_centers")
		if err != nil {
			return err
		}
		if normalizedCenters.cols != normalizedVectors.cols {
			return fmt.Errorf("表达向量索引 profile 维度异常: marker=%s vectors=%dx%d cluster_centers=%dx%d",
				marker, normalizedVectors.rows, normalizedVectors.cols, normalizedCenters.rows, normalizedCenters.cols)
		}
		if rawProfile.EmbeddingDimension > 0 && rawProfile.EmbeddingDimension != normalizedVectors.cols {
			return fmt.Errorf("表达向量索引 profile 维度不匹配: marker=%s metadata=%d vectors=%d",
				marker, rawProfile.EmbeddingDimension, normalizedVectors.cols)
		}
		metadata := rawProfile
		metadata.Marker = marker
		metadata.VectorsKey = vectorsKey
		metadata.ClusterCentersKey = centersKey
		metadata.EmbeddingDimension = normalizedVectors.cols
		profiles[marker] = &expressionVectorProfileState{
			metadata:       metadata,
			vectors:        normalizedVectors,
			clusterCenters: normalizedCenters,
		}
	}
	if len(profiles) == 0 {
		return fmt.Errorf("表达向量索引 embedding_profiles 无有效分组")
	}
	return nil
}

func decodeLegacyNPZExpressionVectorProfile(payload persistentExpressionVectorFile, arrays map[string]expressionVectorArray, profiles map[string]*expressionVectorProfileState) error {
	marker := firstNonEmptyExpressionVectorText(payload.EmbeddingProfileMarker, expressionVectorLegacyMarker)
	vectors, ok := arrays["vectors"]
	if !ok {
		return fmt.Errorf("表达向量索引缺少数组: vectors")
	}
	centers, ok := arrays["cluster_centers"]
	if !ok {
		return fmt.Errorf("表达向量索引缺少数组: cluster_centers")
	}
	normalizedVectors, err := normalizeExpressionVectorArray(vectors, "vectors")
	if err != nil {
		return err
	}
	normalizedCenters, err := normalizeExpressionVectorArray(centers, "cluster_centers")
	if err != nil {
		return err
	}
	if normalizedCenters.cols != normalizedVectors.cols {
		return fmt.Errorf("表达向量索引 profile 维度异常: marker=%s vectors=%dx%d cluster_centers=%dx%d",
			marker, normalizedVectors.rows, normalizedVectors.cols, normalizedCenters.rows, normalizedCenters.cols)
	}
	profiles[marker] = &expressionVectorProfileState{
		metadata: persistentEmbeddingProfile{
			Marker:             marker,
			ProfileVersion:     expressionVectorProfileVersion,
			EmbeddingModel:     normalizeExpressionVectorText(payload.EmbeddingModel),
			EmbeddingDimension: normalizedVectors.cols,
			VectorsKey:         "vectors",
			ClusterCentersKey:  "cluster_centers",
		},
		vectors:        normalizedVectors,
		clusterCenters: normalizedCenters,
	}
	return nil
}

func decodeExpressionVectorItems(payload persistentExpressionVectorFile, expressions []persistentExpressionVector, profiles map[string]*expressionVectorProfileState) ([]persistentExpressionVector, error) {
	items := make([]persistentExpressionVector, 0, len(expressions))
	for index, item := range expressions {
		if item.ID == 0 {
			continue
		}
		situation := normalizeExpressionVectorText(item.Situation)
		style := normalizeExpressionVectorText(item.Style)
		marker := firstNonEmptyExpressionVectorText(item.EmbeddingProfileMarker, payload.EmbeddingProfileMarker, expressionVectorLegacyMarker)
		profile, ok := profiles[marker]
		if !ok {
			continue
		}
		vectorIndex := item.VectorIndex
		if len(profile.vectors.data) > 0 && (vectorIndex < 0 || vectorIndex >= profile.vectors.rows) {
			return nil, fmt.Errorf("表达向量索引 vector_index 越界: id=%d, marker=%s, vector_index=%d, vectors=%d",
				item.ID, marker, vectorIndex, profile.vectors.rows)
		}
		vector := item.Vector
		if len(vector) == 0 && profile.vectors.cols > 0 {
			vector = profile.vectors.row(vectorIndex)
		}
		if len(vector) == 0 {
			continue
		}
		dimension := item.EmbeddingDimension
		if dimension == 0 {
			dimension = len(vector)
		}
		if dimension != len(vector) {
			return nil, fmt.Errorf("表达向量索引维度不匹配: id=%d metadata=%d vector=%d", item.ID, dimension, len(vector))
		}
		fingerprint := normalizeExpressionVectorText(item.Fingerprint)
		actualFingerprint := expressionVectorFingerprint(item.ID, situation, style)
		if fingerprint != "" && fingerprint != actualFingerprint {
			return nil, fmt.Errorf("表达向量索引指纹不匹配: id=%d stored=%s actual=%s", item.ID, fingerprint, actualFingerprint)
		}
		item.Situation = situation
		item.Style = style
		item.Vector = append([]float32(nil), vector...)
		item.VectorIndex = vectorIndex
		item.EmbeddingProfileMarker = marker
		item.EmbeddingDimension = dimension
		itemModel := normalizeExpressionVectorText(item.EmbeddingModel)
		profileModel := normalizeExpressionVectorText(profile.metadata.EmbeddingModel)
		if itemModel != "" && profileModel != "" && itemModel != profileModel {
			return nil, fmt.Errorf("表达向量索引 embedding model 不匹配: id=%d item=%s profile=%s", item.ID, itemModel, profileModel)
		}
		item.EmbeddingModel = firstNonEmptyExpressionVectorText(item.EmbeddingModel, payload.EmbeddingModel, profile.metadata.EmbeddingModel)
		item.Fingerprint = actualFingerprint
		if item.ClusterID < 0 {
			item.ClusterID = 0
		}
		items = append(items, item)
		_ = index
	}
	return items, nil
}

func filterExpressionVectorItems(items []persistentExpressionVector, marker string) []persistentExpressionVector {
	out := make([]persistentExpressionVector, 0, len(items))
	for _, item := range items {
		if item.EmbeddingProfileMarker == marker {
			out = append(out, item)
		}
	}
	return out
}

func newEmptyExpressionVectorProfile(marker string) *expressionVectorProfileState {
	return &expressionVectorProfileState{
		metadata: persistentEmbeddingProfile{
			Marker:            marker,
			ProfileVersion:    expressionVectorProfileVersion,
			EmbeddingModel:    expressionVectorEmbeddingModel,
			VectorsKey:        "vectors_0",
			ClusterCentersKey: "cluster_centers_0",
		},
	}
}

func expressionVectorFirstProfileMarker(profiles map[string]*expressionVectorProfileState) string {
	markers := make([]string, 0, len(profiles))
	for marker := range profiles {
		if strings.TrimSpace(marker) != "" {
			markers = append(markers, marker)
		}
	}
	sort.Strings(markers)
	if len(markers) == 0 {
		return ""
	}
	return markers[0]
}

func firstNonEmptyExpressionVectorText(values ...string) string {
	for _, value := range values {
		if normalized := normalizeExpressionVectorText(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizeExpressionVectorArray(array expressionVectorArray, label string) (expressionVectorArray, error) {
	out := expressionVectorArray{rows: array.rows, cols: array.cols, data: append([]float32(nil), array.data...)}
	for row := 0; row < array.rows; row++ {
		start := row * array.cols
		norm := float64(0)
		for _, value := range out.data[start : start+array.cols] {
			norm += float64(value) * float64(value)
		}
		norm = math.Sqrt(norm)
		if norm <= 0 {
			return expressionVectorArray{}, fmt.Errorf("表达向量索引 %s 包含零向量，无法用于余弦检索", label)
		}
		for column := 0; column < array.cols; column++ {
			out.data[start+column] = float32(float64(out.data[start+column]) / norm)
		}
	}
	return out, nil
}

func (s *PersistentExpressionVectorStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	items := s.mem.snapshotItems()
	dimension := 0
	if len(items) > 0 {
		dimension = len(items[0].Vector)
	}
	for _, item := range items {
		if len(item.Vector) != dimension {
			return fmt.Errorf("表达向量索引存在不同维度向量: id=%d dimension=%d expected=%d", item.ID, len(item.Vector), dimension)
		}
	}
	activeMarker := s.ensureActiveMarker(dimension)
	activeProfile := s.profiles[activeMarker]
	if activeProfile == nil {
		activeProfile = newEmptyExpressionVectorProfile(activeMarker)
		s.profiles[activeMarker] = activeProfile
	}
	now := time.Now().Format(time.RFC3339)
	changed := s.collectClusterChangedIDsLocked(items, activeProfile)
	labels, centers, maintenance, rebuildReason, err := s.prepareClusterMaintenanceLocked(activeMarker, activeProfile, items, changed, now)
	if err != nil {
		return err
	}
	if rebuildReason != "" {
		// 全量重聚类必须覆盖全部 embedding profile，否则未激活分组会长期保留旧簇
		if err := s.rebuildInactiveExpressionVectorProfilesLocked(activeMarker, now, rebuildReason != "forced"); err != nil {
			return err
		}
		if maintenance != nil {
			total := len(items)
			for marker, profile := range s.profiles {
				if marker != activeMarker {
					total += len(profile.expressions)
				}
			}
			maintenance.LastReclusterSampleCount = total
		}
	}
	s.clusterMaintenance = maintenance
	activeModel := expressionVectorActiveEmbeddingModel(activeProfile, items)
	itemVectors := make([][]float32, len(items))
	for index := range items {
		itemVectors[index] = append([]float32(nil), items[index].Vector...)
		items[index].EmbeddingProfileMarker = activeMarker
		items[index].EmbeddingModel = activeModel
		items[index].EmbeddingDimension = dimension
		items[index].Fingerprint = expressionVectorFingerprint(items[index].ID, items[index].Situation, items[index].Style)
		items[index].VectorIndex = index
		items[index].ClusterID = labels[items[index].ID]
		items[index].Vector = nil
	}
	activeProfile.metadata = persistentEmbeddingProfile{
		Marker:               activeMarker,
		EmbeddingFingerprint: activeProfile.metadata.EmbeddingFingerprint,
		ProfileVersion:       expressionVectorProfileVersion,
		EmbeddingModel:       activeModel,
		EmbeddingDimension:   dimension,
		ExpressionCount:      len(items),
		ClusterCount:         len(centers),
		UpdatedAt:            now,
		LastUsedAt:           now,
		VectorsKey:           activeProfile.metadata.VectorsKey,
		ClusterCentersKey:    activeProfile.metadata.ClusterCentersKey,
	}
	if activeProfile.metadata.VectorsKey == "" {
		activeProfile.metadata.VectorsKey = "vectors_0"
	}
	if activeProfile.metadata.ClusterCentersKey == "" {
		activeProfile.metadata.ClusterCentersKey = "cluster_centers_0"
	}
	activeProfile.vectors = expressionVectorArray{rows: len(items), cols: dimension}
	for _, vector := range itemVectors {
		activeProfile.vectors.data = append(activeProfile.vectors.data, vector...)
	}
	activeProfile.clusterCenters = expressionVectorArray{rows: len(centers), cols: dimension}
	for _, center := range centers {
		if len(center) != dimension {
			return fmt.Errorf("表达向量索引聚类中心维度不匹配: center=%d dimension=%d", len(center), dimension)
		}
		activeProfile.clusterCenters.data = append(activeProfile.clusterCenters.data, center...)
	}
	activeProfile.expressions = items
	if err := s.writeExpressionVectorIndexLocked(activeMarker, now); err != nil {
		return err
	}
	s.pendingClusterChanges = nil
	s.forceClusterRebuild = false
	return nil
}

func expressionVectorActiveEmbeddingModel(profile *expressionVectorProfileState, items []persistentExpressionVector) string {
	if model := normalizeExpressionVectorText(profile.metadata.EmbeddingModel); model != "" {
		return model
	}
	for _, item := range items {
		if model := normalizeExpressionVectorText(item.EmbeddingModel); model != "" {
			return model
		}
	}
	return expressionVectorEmbeddingModel
}

// uniqueExpressionVectorArrayKey 为每个 profile 分配互不冲突的 npz 数组名
func uniqueExpressionVectorArrayKey(candidate, prefix string, index int, used map[string]struct{}) string {
	candidate = strings.TrimSpace(candidate)
	if candidate != "" {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
	}
	for suffix := index; ; suffix++ {
		next := fmt.Sprintf("%s_%d", prefix, suffix)
		if _, exists := used[next]; exists {
			continue
		}
		used[next] = struct{}{}
		return next
	}
}

func (s *PersistentExpressionVectorStore) ensureActiveMarker(dimension int) string {
	if marker := strings.TrimSpace(s.activeMarker); marker != "" {
		return marker
	}
	marker := ""
	for candidate, profile := range s.profiles {
		if profile.vectors.cols == dimension || dimension == 0 {
			marker = candidate
			break
		}
	}
	if marker == "" {
		marker = expressionVectorProfileMarker
	}
	// 记住本次落盘使用的分组，运行期 current profile 与清单保持一致
	s.activeMarker = marker
	return marker
}

func (s *PersistentExpressionVectorStore) writeExpressionVectorIndexLocked(activeMarker, now string) error {
	markers := make([]string, 0, len(s.profiles))
	for marker := range s.profiles {
		markers = append(markers, marker)
	}
	sort.Strings(markers)
	arrays := make(map[string]expressionVectorArray, len(markers)*2)
	profiles := make([]persistentEmbeddingProfile, 0, len(markers))
	allExpressions := make([]persistentExpressionVector, 0)
	usedArrayKeys := make(map[string]struct{}, len(markers)*2)
	for index, marker := range markers {
		profile := s.profiles[marker]
		if profile == nil {
			continue
		}
		profile.metadata.VectorsKey = uniqueExpressionVectorArrayKey(profile.metadata.VectorsKey, "vectors", index, usedArrayKeys)
		profile.metadata.ClusterCentersKey = uniqueExpressionVectorArrayKey(profile.metadata.ClusterCentersKey, "cluster_centers", index, usedArrayKeys)
		if profile.vectors.cols == 0 {
			profile.vectors.cols = profile.metadata.EmbeddingDimension
		}
		arrays[profile.metadata.VectorsKey] = profile.vectors
		arrays[profile.metadata.ClusterCentersKey] = profile.clusterCenters
		profile.metadata.Marker = marker
		profile.metadata.ExpressionCount = len(profile.expressions)
		profile.metadata.ClusterCount = profile.clusterCenters.rows
		if marker != activeMarker {
			profile.metadata.EmbeddingDimension = profile.vectors.cols
		}
		profiles = append(profiles, profile.metadata)
		for expressionIndex := range profile.expressions {
			profile.expressions[expressionIndex].Vector = nil
		}
		allExpressions = append(allExpressions, profile.expressions...)
	}
	if len(profiles) == 0 {
		return fmt.Errorf("表达向量索引没有可写入的 embedding profile")
	}
	if s.generatedAt == "" {
		s.generatedAt = now
	}
	dimension := s.profiles[activeMarker].vectors.cols
	nextVectorsPath, err := expressionVectorVersionedVectorsPath(s.path)
	if err != nil {
		return err
	}
	if err := writeExpressionVectorNPZ(nextVectorsPath, arrays); err != nil {
		return err
	}
	activeModel := firstNonEmptyExpressionVectorText(s.profiles[activeMarker].metadata.EmbeddingModel, expressionVectorEmbeddingModel)
	payload := persistentExpressionVectorFile{
		Version:                 expressionVectorIndexFileVersion,
		GeneratedAt:             s.generatedAt,
		UpdatedAt:               now,
		EmbeddingModel:          activeModel,
		EmbeddingDimension:      dimension,
		EmbeddingProfileMarker:  activeMarker,
		EmbeddingProfileVersion: expressionVectorProfileVersion,
		VectorsFile:             filepath.Base(nextVectorsPath),
		SampleCount:             len(allExpressions),
		Expressions:             allExpressions,
		Clusters:                persistentExpressionClusters(allExpressions),
		EmbeddingProfiles:       profiles,
		EmbeddingFailures:       s.sortedEmbeddingFailuresLocked(),
		ClusterMaintenance:      s.clusterMaintenance,
		EmbeddingProfile:        s.embeddingProfile,
	}
	encoded, err := sonic.ConfigStd.MarshalIndent(payload, "", "  ")
	if err != nil {
		_ = os.Remove(nextVectorsPath)
		return err
	}
	if err := writeExpressionVectorJSONAtomic(s.path, encoded); err != nil {
		_ = os.Remove(nextVectorsPath)
		return err
	}
	s.removeEmbeddingFailuresFileLocked()
	oldVectorsPath := s.vectorsFile
	s.vectorsFile = nextVectorsPath
	if oldVectorsPath != "" && filepath.Clean(oldVectorsPath) != filepath.Clean(nextVectorsPath) {
		s.removeObsoleteExpressionVectorFile(oldVectorsPath)
	}
	return nil
}
