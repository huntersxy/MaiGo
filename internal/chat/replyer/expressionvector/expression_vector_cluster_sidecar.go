package expressionvector

// 聚类重建的 Python 侧车接入。
//
// k-means++ 初始化复刻了 numpy PCG64 与 rng.choice 的逐位行为，一旦 numpy
// 升级就要跟着改。这里优先调用 Python 原版 expression_vector_index._run_kmeans，
// 失败再回落到本地实现；侧车未配置时完全走本地路径，行为与之前一致。

import (
	"context"
	"time"

	"maibot-go/internal/services/pythoncompat"

	"go.uber.org/zap"
)

// expressionVectorSidecarTimeout 聚类是离线重建，给足批量向量上传的时间
const expressionVectorSidecarTimeout = 60 * time.Second

// sidecarRebuildExpressionVectorClusters 通过侧车执行全量重聚类。
// 返回 ok=false 表示应回落到本地实现（未配置、调用失败或结果不可用）。
func sidecarRebuildExpressionVectorClusters(
	entries []expressionVectorClusterSample,
	vectors [][]float32,
	previousCenters [][]float32,
	clusterCount int,
	reuse bool,
) (map[uint]int, [][]float32, bool) {
	client := pythoncompat.DefaultClient()
	if !client.Configured() || len(vectors) == 0 || clusterCount <= 0 {
		return nil, nil, false
	}
	dimension := len(vectors[0])
	flat := make([]float32, 0, len(vectors)*dimension)
	for _, vector := range vectors {
		if len(vector) != dimension {
			return nil, nil, false
		}
		flat = append(flat, vector...)
	}

	request := pythoncompat.KMeansRequest{
		Rows:         len(vectors),
		Cols:         dimension,
		Vectors:      flat,
		ClusterCount: clusterCount,
		MaxIter:      100,
	}
	// 复用上一轮中心：Python 要求 initial_centers 形状等于 (cluster_count, dim)，
	// 与本地实现「簇数一致且维度匹配才复用」的判定保持一致。
	if reuse && len(previousCenters) == clusterCount {
		initial := make([]float32, 0, clusterCount*dimension)
		matched := true
		for _, center := range previousCenters {
			if len(center) != dimension {
				matched = false
				break
			}
			initial = append(initial, normalizeVectorForClustering(center)...)
		}
		if matched {
			request.InitialCenters = initial
			request.InitialRows = clusterCount
			request.InitialCols = dimension
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), expressionVectorSidecarTimeout)
	defer cancel()
	result, err := client.RunKMeans(ctx, request)
	if err != nil {
		zap.L().Warn("表达向量聚类侧车不可用，回落到本地实现", zap.Error(err))
		return nil, nil, false
	}
	if len(result.Labels) != len(entries) || result.CentersRows <= 0 || result.CentersCols != dimension {
		zap.L().Warn("表达向量聚类侧车返回尺寸异常，回落到本地实现",
			zap.Int("labels", len(result.Labels)),
			zap.Int("entries", len(entries)),
			zap.Int("centers_rows", result.CentersRows),
			zap.Int("centers_cols", result.CentersCols),
		)
		return nil, nil, false
	}
	centers := make([][]float32, result.CentersRows)
	for row := 0; row < result.CentersRows; row++ {
		centers[row] = append([]float32(nil), result.Centers[row*result.CentersCols:(row+1)*result.CentersCols]...)
	}
	labels := make(map[uint]int, len(entries))
	for index, entry := range entries {
		label := result.Labels[index]
		if label < 0 || label >= result.CentersRows {
			zap.L().Warn("表达向量聚类侧车返回越界标签，回落到本地实现", zap.Int("label", label))
			return nil, nil, false
		}
		labels[entry.id] = label
	}
	return labels, centers, true
}
