package expressionvector

import "sort"

// ExpressionVectorDiversityLambda 对齐 expression_vector_index.VECTOR_DIVERSITY_LAMBDA
const ExpressionVectorDiversityLambda = 0.85

// selectExpressionHitsByMMR 在候选命中上做最大边际相关去冗余
//
// 对齐 expression_vector_index._select_by_mmr：首轮按 lambda*relevance 取最大，
// 后续每轮用候选与已选集合的最大余弦相似度做惩罚；返回集合再按原始分数降序。
func selectExpressionHitsByMMR(hits []ExpressionVectorHit, limit int, lambda float64) []ExpressionVectorHit {
	if len(hits) == 0 || limit <= 0 {
		return nil
	}
	if lambda >= 0.999 {
		out := append([]ExpressionVectorHit(nil), hits...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].Score != out[j].Score {
				return out[i].Score > out[j].Score
			}
			return out[i].ID < out[j].ID
		})
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}

	relevance := make([]float64, len(hits))
	penalties := make([]float64, len(hits))
	mmrScores := make([]float64, len(hits))
	selected := make([]bool, len(hits))
	for index, hit := range hits {
		relevance[index] = float64(hit.Score)
		mmrScores[index] = lambda * relevance[index]
	}

	chosen := make([]int, 0, minInt(limit, len(hits)))
	for len(chosen) < limit && len(chosen) < len(hits) {
		best := -1
		bestScore := 0.0
		for index := range hits {
			if selected[index] {
				continue
			}
			if best < 0 || mmrScores[index] > bestScore {
				best = index
				bestScore = mmrScores[index]
			}
		}
		if best < 0 {
			break
		}
		selected[best] = true
		chosen = append(chosen, best)
		for index := range hits {
			if selected[index] {
				continue
			}
			if similarity := expressionVectorCosine(hits[index].Vector, hits[best].Vector); similarity > penalties[index] {
				penalties[index] = similarity
			}
			mmrScores[index] = lambda*relevance[index] - (1-lambda)*penalties[index]
		}
	}

	out := make([]ExpressionVectorHit, 0, len(chosen))
	for _, index := range chosen {
		out = append(out, hits[index])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func expressionVectorCosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	denom := float64(vectorL2Norm(a)) * float64(vectorL2Norm(b))
	if denom == 0 {
		return 0
	}
	return float64(dotProduct(a, b)) / denom
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
