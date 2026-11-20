package expressionvector

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Python expression_vector_index 的聚类维护状态与阈值
const (
	expressionVectorClusterStateBootstrapping = "BOOTSTRAPPING"
	expressionVectorClusterStateStable        = "STABLE"
)

// resolveExpressionVectorClusterState 对齐 Python _resolve_cluster_state
// 读入兼容历史小写状态，非法状态与缺少 profile_marker 直接报错
func resolveExpressionVectorClusterState(maintenance *persistentClusterMaintenance, profileMarker string) (string, error) {
	if maintenance == nil {
		return expressionVectorClusterStateBootstrapping, nil
	}
	rawState := strings.TrimSpace(maintenance.State)
	if rawState == "" {
		return expressionVectorClusterStateBootstrapping, nil
	}
	state := strings.ToUpper(rawState)
	if state != expressionVectorClusterStateBootstrapping && state != expressionVectorClusterStateStable {
		return "", fmt.Errorf("表达向量索引聚类状态非法: '%s'", rawState)
	}
	marker := strings.TrimSpace(maintenance.ProfileMarker)
	if marker == "" {
		return "", fmt.Errorf("表达向量索引聚类状态缺少 profile_marker")
	}
	if marker != profileMarker {
		return expressionVectorClusterStateBootstrapping, nil
	}
	return state, nil
}

// resolveExpressionVectorReclusterReason 对齐 Python _resolve_recluster_reason
func resolveExpressionVectorReclusterReason(force bool, state string, canIncremental bool, changesSinceRecluster, totalCount int) (string, error) {
	if force {
		return "forced", nil
	}
	if !canIncremental {
		if state == expressionVectorClusterStateStable {
			return "", fmt.Errorf("稳定表达向量索引缺少可用聚类中心")
		}
		return "bootstrap", nil
	}
	if state == expressionVectorClusterStateBootstrapping || totalCount <= 0 {
		return "", nil
	}
	if state != expressionVectorClusterStateStable {
		return "", fmt.Errorf("表达向量索引聚类状态非法: '%s'", state)
	}
	if float64(changesSinceRecluster)/float64(totalCount) >= expressionBackfillChangeRatio {
		return "change_ratio", nil
	}
	return "", nil
}

// chooseExpressionVectorClusterCount 对齐 Python _choose_cluster_count
func chooseExpressionVectorClusterCount(sampleCount, previousClusterCount int) int {
	if sampleCount <= 1 {
		return 1
	}
	if previousClusterCount > 0 {
		if previousClusterCount > sampleCount {
			return sampleCount
		}
		if previousClusterCount < 1 {
			return 1
		}
		return previousClusterCount
	}
	if sampleCount > expressionVectorClusterMax {
		return expressionVectorClusterMax
	}
	if sampleCount < 2 {
		return 2
	}
	return sampleCount
}

// selectNearestExpressionVectorClusters 对齐 Python _select_nearest_clusters
func selectNearestExpressionVectorClusters(vectors, centers [][]float32) []int {
	labels := make([]int, len(vectors))
	if len(centers) == 0 {
		return labels
	}
	normalizedCenters := make([][]float32, len(centers))
	for index, center := range centers {
		normalizedCenters[index] = normalizeVectorForClustering(center)
	}
	for index, vector := range vectors {
		normalized := normalizeVectorForClustering(vector)
		bestCluster := 0
		bestScore := float32(-1 << 30)
		for clusterID, center := range normalizedCenters {
			score := dotProduct(normalized, center)
			if score > bestScore {
				bestScore = score
				bestCluster = clusterID
			}
		}
		labels[index] = bestCluster
	}
	return labels
}

// compactExpressionVectorClusterLabels 对齐 numpy unique return_inverse，按原标签升序重排为连续标签
func compactExpressionVectorClusterLabels(labels []int) []int {
	if len(labels) == 0 {
		return labels
	}
	unique := make(map[int]struct{}, len(labels))
	for _, label := range labels {
		unique[label] = struct{}{}
	}
	ordered := make([]int, 0, len(unique))
	for label := range unique {
		ordered = append(ordered, label)
	}
	sort.Ints(ordered)
	positions := make(map[int]int, len(ordered))
	for index, label := range ordered {
		positions[label] = index
	}
	compacted := make([]int, len(labels))
	for index, label := range labels {
		compacted[index] = positions[label]
	}
	return compacted
}

// buildExpressionVectorClusterCenters 按标签计算归一化簇中心，空簇与零向量报错
func buildExpressionVectorClusterCenters(vectors [][]float32, labels []int, clusterCount int) ([][]float32, error) {
	centers := make([][]float32, clusterCount)
	members := make([]int, clusterCount)
	for index, label := range labels {
		if label < 0 || label >= clusterCount {
			return nil, fmt.Errorf("表达向量聚类标签越界: label=%d cluster_count=%d", label, clusterCount)
		}
		if centers[label] == nil {
			centers[label] = make([]float32, len(vectors[index]))
		}
		members[label]++
		for dimension, value := range vectors[index] {
			centers[label][dimension] += value
		}
	}
	for clusterID := range centers {
		if members[clusterID] == 0 {
			return nil, fmt.Errorf("表达向量聚类中心构建失败: cluster_id=%d 为空簇", clusterID)
		}
		for dimension := range centers[clusterID] {
			centers[clusterID][dimension] /= float32(members[clusterID])
		}
		norm := pythonCenterL2Norm(centers[clusterID])
		if norm == 0 {
			return nil, fmt.Errorf("表达向量聚类中心构建失败: cluster_id=%d 为零向量", clusterID)
		}
		for dimension := range centers[clusterID] {
			centers[clusterID][dimension] /= norm
		}
	}
	return centers, nil
}

func expressionVectorCentersFromArray(array expressionVectorArray) ([][]float32, bool) {
	if array.rows <= 0 || array.cols <= 0 || len(array.data) != array.rows*array.cols {
		return nil, false
	}
	centers := make([][]float32, array.rows)
	for row := 0; row < array.rows; row++ {
		centers[row] = append([]float32(nil), array.data[row*array.cols:(row+1)*array.cols]...)
	}
	return centers, true
}

func expressionVectorClusterVectors(entries []expressionVectorClusterSample) [][]float32 {
	vectors := make([][]float32, len(entries))
	for index, entry := range entries {
		vectors[index] = normalizeVectorForClustering(entry.vector)
	}
	return vectors
}

// rebuildExpressionVectorClusters 在 Go 进程内执行 cosine k-means，按需沿用旧簇数量与中心。
func rebuildExpressionVectorClusters(entries []expressionVectorClusterSample, previousCenters [][]float32, reuse bool) (map[uint]int, [][]float32, error) {
	labels := make(map[uint]int, len(entries))
	if len(entries) == 0 {
		return labels, nil, nil
	}
	vectors := expressionVectorClusterVectors(entries)
	for index, vector := range vectors {
		if pythonRowL2Norm(vector) == 0 {
			return nil, nil, fmt.Errorf("表达向量索引包含零向量: id=%d", entries[index].id)
		}
	}

	// _choose_cluster_count 始终参考上一次簇数。
	clusterCount := chooseExpressionVectorClusterCount(len(entries), len(previousCenters))
	assigned := make([]int, len(vectors))
	var centers [][]float32
	if clusterCount == 1 {
		var err error
		centers, err = buildExpressionVectorClusterCenters(vectors, assigned, clusterCount)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if reuse && len(previousCenters) == clusterCount {
			centers = make([][]float32, 0, clusterCount)
			for _, center := range previousCenters {
				if len(center) != len(vectors[0]) {
					centers = nil
					break
				}
				centers = append(centers, normalizeVectorForClustering(center))
			}
		}
		if len(centers) == 0 {
			centers = initializeClusterCenters(vectors, clusterCount)
		}

		var previousLabels []int
		for iteration := 0; iteration < 100; iteration++ {
			assigned = assignClusterLabels(vectors, centers)
			repairEmptyClusterLabels(assigned, vectors, centers)
			var err error
			centers, err = buildExpressionVectorClusterCenters(vectors, assigned, clusterCount)
			if err != nil {
				return nil, nil, err
			}
			if slices.Equal(previousLabels, assigned) {
				break
			}
			previousLabels = assigned
		}
	}
	for index, entry := range entries {
		labels[entry.id] = assigned[index]
	}
	return labels, centers, nil
}

// incrementalExpressionVectorClusters 对齐 Python _update_profile_arrays_incrementally
func incrementalExpressionVectorClusters(entries []expressionVectorClusterSample, previousExpressions []persistentExpressionVector, previousCenters [][]float32, changed map[uint]struct{}) (map[uint]int, [][]float32, error) {
	labels := make(map[uint]int, len(entries))
	if len(entries) == 0 {
		return labels, nil, nil
	}
	previousLabels := make(map[uint]int, len(previousExpressions))
	for _, expression := range previousExpressions {
		previousLabels[expression.ID] = expression.ClusterID
	}
	rows := len(previousCenters)
	assigned := make([]int, len(entries))
	for index, entry := range entries {
		label, ok := previousLabels[entry.id]
		reassign := !ok || label < 0 || label >= rows
		if _, changedID := changed[entry.id]; changedID {
			reassign = true
		}
		if reassign {
			nearest := selectNearestExpressionVectorClusters([][]float32{entry.vector}, previousCenters)
			if len(nearest) > 0 {
				label = nearest[0]
			}
		}
		assigned[index] = label
	}
	assigned = compactExpressionVectorClusterLabels(assigned)
	clusterCount := 0
	for _, label := range assigned {
		if label+1 > clusterCount {
			clusterCount = label + 1
		}
	}
	vectors := expressionVectorClusterVectors(entries)
	centers, err := buildExpressionVectorClusterCenters(vectors, assigned, clusterCount)
	if err != nil {
		return nil, nil, err
	}
	for index, entry := range entries {
		labels[entry.id] = assigned[index]
	}
	return labels, centers, nil
}

// rebuildInactiveExpressionVectorProfilesLocked 全量重建未激活 profile 的向量矩阵与聚类
// Python _rebuild_profile_arrays 按 marker 分组重建全部 profile，finalize 使用 reuse_previous_centers=False
func (s *PersistentExpressionVectorStore) rebuildInactiveExpressionVectorProfilesLocked(activeMarker, now string, reuse bool) error {
	markers := make([]string, 0, len(s.profiles))
	for marker := range s.profiles {
		markers = append(markers, marker)
	}
	sort.Strings(markers)
	for _, marker := range markers {
		if marker == activeMarker {
			continue
		}
		profile := s.profiles[marker]
		if profile == nil {
			continue
		}
		if err := s.rebuildExpressionVectorProfileLocked(marker, profile, now, reuse); err != nil {
			return err
		}
	}
	return nil
}

// rebuildExpressionVectorProfileLocked 重建单个 profile 的数组、标签与元数据
func (s *PersistentExpressionVectorStore) rebuildExpressionVectorProfileLocked(marker string, profile *expressionVectorProfileState, now string, reuse bool) error {
	expressions := profile.expressions
	dimension := profile.metadata.EmbeddingDimension
	if dimension == 0 {
		dimension = profile.vectors.cols
	}
	vectors := make([][]float32, len(expressions))
	for index, item := range expressions {
		vector := item.Vector
		if len(vector) == 0 && profile.vectors.rows > 0 {
			vectorIndex := item.VectorIndex
			if vectorIndex < 0 || vectorIndex >= profile.vectors.rows {
				return fmt.Errorf("表达向量 profile %s 向量下标越界: id=%d vector_index=%d rows=%d", marker, item.ID, vectorIndex, profile.vectors.rows)
			}
			vector = profile.vectors.row(vectorIndex)
		}
		if len(vector) == 0 {
			return fmt.Errorf("表达向量 profile %s 缺少向量: id=%d", marker, item.ID)
		}
		if dimension == 0 {
			dimension = len(vector)
		}
		if len(vector) != dimension {
			return fmt.Errorf("表达向量 profile %s 维度不匹配: id=%d dimension=%d expected=%d", marker, item.ID, len(vector), dimension)
		}
		vectors[index] = append([]float32(nil), vector...)
	}
	if len(expressions) == 0 {
		profile.vectors = expressionVectorArray{cols: dimension}
		profile.clusterCenters = expressionVectorArray{cols: dimension}
		profile.metadata.Marker = marker
		profile.metadata.ExpressionCount = 0
		profile.metadata.ClusterCount = 0
		profile.metadata.UpdatedAt = now
		return nil
	}
	samples := make([]expressionVectorClusterSample, len(expressions))
	for index, item := range expressions {
		samples[index] = expressionVectorClusterSample{id: item.ID, vector: vectors[index]}
	}
	previousCenters, _ := expressionVectorCentersFromArray(profile.clusterCenters)
	labels, centers, err := rebuildExpressionVectorClusters(samples, previousCenters, reuse)
	if err != nil {
		return err
	}
	normalized := expressionVectorClusterVectors(samples)
	profile.vectors = expressionVectorArray{rows: len(vectors), cols: dimension}
	for _, vector := range normalized {
		profile.vectors.data = append(profile.vectors.data, vector...)
	}
	profile.clusterCenters = expressionVectorArray{rows: len(centers), cols: dimension}
	for _, center := range centers {
		if len(center) != dimension {
			return fmt.Errorf("表达向量 profile %s 聚类中心维度不匹配: center=%d dimension=%d", marker, len(center), dimension)
		}
		profile.clusterCenters.data = append(profile.clusterCenters.data, center...)
	}
	model := normalizeExpressionVectorText(profile.metadata.EmbeddingModel)
	if model == "" && len(expressions) > 0 {
		model = normalizeExpressionVectorText(expressions[0].EmbeddingModel)
	}
	if model == "" {
		model = expressionVectorEmbeddingModel
	}
	for index := range expressions {
		expressions[index].EmbeddingProfileMarker = marker
		expressions[index].EmbeddingModel = model
		expressions[index].EmbeddingDimension = dimension
		expressions[index].Fingerprint = expressionVectorFingerprint(expressions[index].ID, expressions[index].Situation, expressions[index].Style)
		expressions[index].VectorIndex = index
		expressions[index].ClusterID = labels[expressions[index].ID]
		expressions[index].Vector = nil
	}
	profile.expressions = expressions
	profile.metadata.Marker = marker
	profile.metadata.ProfileVersion = expressionVectorProfileVersion
	profile.metadata.EmbeddingModel = model
	profile.metadata.EmbeddingDimension = dimension
	profile.metadata.ExpressionCount = len(expressions)
	profile.metadata.ClusterCount = len(centers)
	profile.metadata.UpdatedAt = now
	return nil
}

func sortedExpressionVectorChangeIDs(ids map[uint]struct{}) []uint {
	out := make([]uint, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *PersistentExpressionVectorStore) markClusterChangeLocked(id uint) {
	if id == 0 {
		return
	}
	if s.pendingClusterChanges == nil {
		s.pendingClusterChanges = make(map[uint]struct{})
	}
	s.pendingClusterChanges[id] = struct{}{}
}

func (s *PersistentExpressionVectorStore) collectClusterChangedIDsLocked(items []persistentExpressionVector, activeProfile *expressionVectorProfileState) map[uint]struct{} {
	changed := make(map[uint]struct{})
	for id := range s.pendingClusterChanges {
		changed[id] = struct{}{}
	}
	previous := make(map[uint]persistentExpressionVector)
	if activeProfile != nil {
		for _, expression := range activeProfile.expressions {
			previous[expression.ID] = expression
		}
	}
	current := make(map[uint]struct{}, len(items))
	for _, item := range items {
		current[item.ID] = struct{}{}
		old, ok := previous[item.ID]
		if !ok {
			changed[item.ID] = struct{}{}
			continue
		}
		fingerprint := expressionVectorFingerprint(item.ID, item.Situation, item.Style)
		if strings.TrimSpace(old.Fingerprint) != fingerprint {
			changed[item.ID] = struct{}{}
		}
	}
	for id := range previous {
		if _, ok := current[id]; !ok {
			changed[id] = struct{}{}
		}
	}
	return changed
}

// prepareClusterMaintenanceLocked 计算本次落盘的簇标签与 cluster_maintenance
func (s *PersistentExpressionVectorStore) prepareClusterMaintenanceLocked(activeMarker string, profile *expressionVectorProfileState, items []persistentExpressionVector, changed map[uint]struct{}, now string) (map[uint]int, [][]float32, *persistentClusterMaintenance, string, error) {
	labels := make(map[uint]int)
	if len(items) == 0 {
		return labels, nil, s.clusterMaintenance, "", nil
	}
	dimension := len(items[0].Vector)
	prior := s.clusterMaintenance
	state, err := resolveExpressionVectorClusterState(prior, activeMarker)
	if err != nil {
		return nil, nil, nil, "", err
	}
	priorChanges := 0
	priorChangedIDs := map[uint]struct{}{}
	priorLastReclusterAt := ""
	priorLastReclusterCount := 0
	priorStabilizedAt := ""
	if prior != nil {
		priorChanges = prior.ChangesSinceRecluster
		for _, id := range prior.ChangedExpressionIDs {
			priorChangedIDs[id] = struct{}{}
		}
		priorLastReclusterAt = prior.LastReclusterAt
		priorLastReclusterCount = prior.LastReclusterSampleCount
		priorStabilizedAt = prior.StabilizedAt
	}

	allChanged := make(map[uint]struct{}, len(priorChangedIDs)+len(changed))
	for id := range priorChangedIDs {
		allChanged[id] = struct{}{}
	}
	changesSinceRecluster := priorChanges
	for id := range changed {
		if _, seen := allChanged[id]; !seen {
			changesSinceRecluster++
		}
		allChanged[id] = struct{}{}
	}

	previousCenters, centersValid := expressionVectorCentersFromArray(profile.clusterCenters)
	canIncremental := centersValid && profile.clusterCenters.cols == dimension && profile.clusterCenters.rows > 0
	reason, err := resolveExpressionVectorReclusterReason(s.forceClusterRebuild, state, canIncremental, changesSinceRecluster, len(items))
	if err != nil {
		return nil, nil, nil, "", err
	}

	samples := make([]expressionVectorClusterSample, 0, len(items))
	for _, item := range items {
		if len(item.Vector) != dimension {
			return nil, nil, nil, "", fmt.Errorf("表达向量索引存在不同维度向量: id=%d dimension=%d expected=%d", item.ID, len(item.Vector), dimension)
		}
		samples = append(samples, expressionVectorClusterSample{id: item.ID, vector: item.Vector})
	}
	var centers [][]float32
	if reason != "" {
		labels, centers, err = rebuildExpressionVectorClusters(samples, previousCenters, reason != "forced")
		if err != nil {
			return nil, nil, nil, "", err
		}
		changesSinceRecluster = 0
		allChanged = map[uint]struct{}{}
	} else {
		labels, centers, err = incrementalExpressionVectorClusters(samples, profile.expressions, previousCenters, changed)
		if err != nil {
			return nil, nil, nil, "", err
		}
	}

	maintenance := &persistentClusterMaintenance{
		State:                    state,
		ProfileMarker:            activeMarker,
		ChangesSinceRecluster:    changesSinceRecluster,
		ChangedExpressionIDs:     sortedExpressionVectorChangeIDs(allChanged),
		ChangeRatioThreshold:     expressionBackfillChangeRatio,
		LastReclusterAt:          priorLastReclusterAt,
		LastReclusterSampleCount: priorLastReclusterCount,
	}
	if reason != "" {
		maintenance.LastReclusterAt = now
		maintenance.LastReclusterSampleCount = len(items)
	}
	if state == expressionVectorClusterStateStable {
		maintenance.StabilizedAt = priorStabilizedAt
		if maintenance.StabilizedAt == "" {
			maintenance.StabilizedAt = now
		}
	}
	return labels, centers, maintenance, reason, nil
}
