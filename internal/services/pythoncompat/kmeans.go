package pythoncompat

import "context"

// KMeansRequest cosine k-means 入参；Vectors 按行优先展平。
//
// InitialCenters 非空时对应 Python _run_kmeans 的 initial_centers：
// 增量重建会沿用上一轮中心，而不是重新做 k-means++ 初始化。
type KMeansRequest struct {
	Rows           int
	Cols           int
	Vectors        []float32
	ClusterCount   int
	Seed           int64
	MaxIter        int
	InitialCenters []float32
	InitialRows    int
	InitialCols    int
}

// KMeansResult 标签与中心向量（中心同样按行优先展平）
type KMeansResult struct {
	Labels      []int
	Centers     []float32
	CentersRows int
	CentersCols int
}

type kmeansRequestPayload struct {
	Rows           int       `json:"rows"`
	Cols           int       `json:"cols"`
	Vectors        []float32 `json:"vectors"`
	ClusterCount   int       `json:"cluster_count"`
	Seed           int64     `json:"seed"`
	MaxIter        int       `json:"max_iter"`
	InitialCenters []float32 `json:"initial_centers,omitempty"`
	InitialRows    int       `json:"initial_rows,omitempty"`
	InitialCols    int       `json:"initial_cols,omitempty"`
}

type kmeansResponse struct {
	Labels      []int     `json:"labels"`
	Centers     []float32 `json:"centers"`
	CentersRows int       `json:"centers_rows"`
	CentersCols int       `json:"centers_cols"`
}

// RunKMeans 调用 Python 原版 expression_vector_index._run_kmeans。
//
// 侧车内部先做 l2_normalize 再聚类，与 Go 侧 buildExpressionVectorClusters
// 的输入约定一致；seed 为 0 时由侧车回落 Python 默认的 20260621。
func (c *Client) RunKMeans(ctx context.Context, req KMeansRequest) (*KMeansResult, error) {
	payload := kmeansRequestPayload{
		Rows:           req.Rows,
		Cols:           req.Cols,
		Vectors:        req.Vectors,
		ClusterCount:   req.ClusterCount,
		Seed:           req.Seed,
		MaxIter:        req.MaxIter,
		InitialCenters: req.InitialCenters,
		InitialRows:    req.InitialRows,
		InitialCols:    req.InitialCols,
	}
	var response kmeansResponse
	if err := c.post(ctx, "/kmeans", payload, &response); err != nil {
		return nil, err
	}
	return &KMeansResult{
		Labels:      response.Labels,
		Centers:     response.Centers,
		CentersRows: response.CentersRows,
		CentersCols: response.CentersCols,
	}, nil
}
