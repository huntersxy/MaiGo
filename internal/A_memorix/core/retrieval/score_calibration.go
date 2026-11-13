package retrieval

// A_memorix 查询内分数校准与融合，对齐 core/retrieval/score_calibration.py
// 只处理已召回候选的分数，不改变候选集合

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// ScoreCalibrationMethods 允许的校准方法，与 Python 集合一致
var ScoreCalibrationMethods = map[string]struct{}{
	"none": {}, "minmax": {}, "rank_percentile": {}, "robust_sigmoid": {},
	"semantic_minmax": {}, "semantic_graph_rrf": {}, "semantic_rank_percentile": {},
	"semantic_robust_sigmoid": {}, "semantic_rrf": {}, "semantic_sparse_rrf": {},
	"semantic_softmax": {}, "softmax": {}, "weighted_rrf": {},
}

// rrfMethodChannels 需要走 RRF 融合的方法及其参与校准的通道
// nil 表示所有通道都按 RRF 处理（weighted_rrf）
var rrfMethodChannels = map[string]map[string]struct{}{
	"semantic_graph_rrf":  {"semantic": {}, "graph": {}},
	"semantic_rrf":        {"semantic": {}},
	"semantic_sparse_rrf": {"semantic": {}, "sparse": {}},
	"weighted_rrf":        nil,
}

// NormalizeCalibrationMethod 规范化校准方法，非法配置直接报错
func NormalizeCalibrationMethod(method string) (string, error) {
	token := method
	if token == "" {
		token = "none"
	}
	token = strings.ToLower(strings.TrimSpace(token))
	if _, ok := ScoreCalibrationMethods[token]; !ok {
		return "", fmt.Errorf("不支持的分数校准方法: %s", token)
	}
	return token, nil
}

// finiteScoreMap 过滤 NaN/Inf
func finiteScoreMap(scores map[string]float64) map[string]float64 {
	output := make(map[string]float64, len(scores))
	for key, value := range scores {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			output[key] = value
		}
	}
	return output
}

// averageRankScores 返回并列项共享名次的降序排名分数，首名为 1
func averageRankScores(scores map[string]float64) map[string]float64 {
	if len(scores) == 0 {
		return map[string]float64{}
	}
	seen := make(map[float64]struct{}, len(scores))
	values := make([]float64, 0, len(scores))
	for _, value := range scores {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			values = append(values, value)
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(values)))
	rankByValue := make(map[float64]float64, len(values))
	for index, value := range values {
		rankByValue[value] = float64(index + 1)
	}
	output := make(map[string]float64, len(scores))
	for key, value := range scores {
		output[key] = rankByValue[value]
	}
	return output
}

// medianFloat 对齐 statistics.median：偶数个取中间两数平均
func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2.0
}

// CalibrateScoreMap 将单通道的查询内分数转换到可比较范围
func CalibrateScoreMap(scores map[string]float64, method string) (map[string]float64, error) {
	normalized, err := NormalizeCalibrationMethod(method)
	if err != nil {
		return nil, err
	}
	finite := finiteScoreMap(scores)
	if len(finite) == 0 || normalized == "none" {
		return finite, nil
	}
	if _, ok := rrfMethodChannels[normalized]; ok {
		return nil, fmt.Errorf("RRF 方法需要通过 fuse_score_maps 融合通道")
	}

	values := make([]float64, 0, len(finite))
	lower, upper := math.Inf(1), math.Inf(-1)
	for _, value := range finite {
		values = append(values, value)
		if value < lower {
			lower = value
		}
		if value > upper {
			upper = value
		}
	}
	if upper-lower < 1e-12 {
		output := make(map[string]float64, len(finite))
		for key := range finite {
			output[key] = 1.0
		}
		return output, nil
	}

	switch normalized {
	case "minmax":
		scale := upper - lower
		output := make(map[string]float64, len(finite))
		for key, value := range finite {
			output[key] = (value - lower) / scale
		}
		return output, nil
	case "rank_percentile":
		ranks := averageRankScores(finite)
		distinct := make(map[float64]struct{}, len(values))
		for _, value := range values {
			distinct[value] = struct{}{}
		}
		if len(distinct) <= 1 {
			output := make(map[string]float64, len(finite))
			for key := range finite {
				output[key] = 1.0
			}
			return output, nil
		}
		output := make(map[string]float64, len(finite))
		for key, rank := range ranks {
			output[key] = 1.0 - (rank-1.0)/float64(len(distinct)-1)
		}
		return output, nil
	}

	center := medianFloat(values)
	deviations := make([]float64, 0, len(values))
	for _, value := range values {
		deviations = append(deviations, math.Abs(value-center))
	}
	robustScale := 1.4826 * medianFloat(deviations)
	if robustScale < 1e-12 {
		robustScale = math.Max((upper-lower)/4.0, 1e-12)
	}

	switch normalized {
	case "robust_sigmoid":
		output := make(map[string]float64, len(finite))
		for key, value := range finite {
			zScore := math.Max(-12.0, math.Min(12.0, (value-center)/robustScale))
			output[key] = 1.0 / (1.0 + math.Exp(-zScore))
		}
		return output, nil
	case "softmax":
		temperature := robustScale
		output := make(map[string]float64, len(finite))
		for key, value := range finite {
			output[key] = math.Exp(math.Max(-50.0, math.Min(0.0, (value-upper)/temperature)))
		}
		return output, nil
	}
	return nil, fmt.Errorf("未处理的分数校准方法: %s", normalized)
}

// FuseScoreMaps 校准并融合多个候选分数通道
func FuseScoreMaps(scoreMaps map[string]map[string]float64, weights map[string]float64, method string, rrfK int) (map[string]float64, map[string]map[string]float64, error) {
	normalized, err := NormalizeCalibrationMethod(method)
	if err != nil {
		return nil, nil, err
	}
	finiteMaps := make(map[string]map[string]float64, len(scoreMaps))
	candidateIds := map[string]struct{}{}
	for name, scores := range scoreMaps {
		finite := finiteScoreMap(scores)
		finiteMaps[name] = finite
		for id := range finite {
			candidateIds[id] = struct{}{}
		}
	}

	calibratedMaps := make(map[string]map[string]float64, len(finiteMaps))
	if channels, ok := rrfMethodChannels[normalized]; ok {
		safeK := rrfK
		if safeK < 1 {
			safeK = 1
		}
		for channel, scores := range finiteMaps {
			if channels != nil {
				if _, allowed := channels[channel]; !allowed {
					calibratedMaps[channel] = cloneScoreMap(scores)
					continue
				}
			}
			ranks := averageRankScores(scores)
			converted := make(map[string]float64, len(ranks))
			for key, rank := range ranks {
				converted[key] = (float64(safeK) + 1.0) / (float64(safeK) + rank)
			}
			calibratedMaps[channel] = converted
		}
	} else if strings.HasPrefix(normalized, "semantic_") {
		semanticMethod := normalized[len("semantic_"):]
		for channel, scores := range finiteMaps {
			usedMethod := "none"
			if channel == "semantic" {
				usedMethod = semanticMethod
			}
			calibrated, calibrateErr := CalibrateScoreMap(scores, usedMethod)
			if calibrateErr != nil {
				return nil, nil, calibrateErr
			}
			calibratedMaps[channel] = calibrated
		}
	} else {
		for channel, scores := range finiteMaps {
			calibrated, calibrateErr := CalibrateScoreMap(scores, normalized)
			if calibrateErr != nil {
				return nil, nil, calibrateErr
			}
			calibratedMaps[channel] = calibrated
		}
	}

	finalScores := make(map[string]float64, len(candidateIds))
	for id := range candidateIds {
		finalScores[id] = 0.0
	}
	for channel, calibrated := range calibratedMaps {
		weight := weights[channel]
		for id, score := range calibrated {
			finalScores[id] += weight * score
		}
	}
	return finalScores, calibratedMaps, nil
}

// OrderedIDs 按分数降序和 ID 升序返回稳定排序
func OrderedIDs(scores map[string]float64) []string {
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := scores[ids[i]], scores[ids[j]]
		if left != right {
			return left > right
		}
		return ids[i] < ids[j]
	})
	return ids
}

func cloneScoreMap(scores map[string]float64) map[string]float64 {
	output := make(map[string]float64, len(scores))
	for key, value := range scores {
		output[key] = value
	}
	return output
}
