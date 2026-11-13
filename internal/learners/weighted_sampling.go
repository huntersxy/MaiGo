package learners

import "math/rand/v2"

// ComputeExpressionWeights 按 count 线性映射到 [1,5] 权重，对齐 weighted_sampling._compute_weights
func ComputeExpressionWeights(counts []int) []float64 {
	if len(counts) == 0 {
		return nil
	}
	values := make([]float64, len(counts))
	minValue := float64(0)
	maxValue := float64(0)
	for i, count := range counts {
		value := float64(count)
		if value < 0 {
			value = 0
		}
		values[i] = value
		if i == 0 || value < minValue {
			minValue = value
		}
		if i == 0 || value > maxValue {
			maxValue = value
		}
	}
	weights := make([]float64, len(values))
	if maxValue == minValue {
		for i := range weights {
			weights[i] = 1
		}
		return weights
	}
	span := maxValue - minValue
	for i, value := range values {
		weights[i] = 1 + (value-minValue)/span*4
	}
	return weights
}

// WeightedSample 按 count 权重不放回抽样，对齐 weighted_sampling.weighted_sample
func WeightedSample[T any](population []T, k int, countOf func(T) int) []T {
	if len(population) == 0 || k <= 0 {
		return nil
	}
	if len(population) <= k {
		out := make([]T, len(population))
		copy(out, population)
		return out
	}
	if countOf == nil {
		countOf = func(T) int { return 1 }
	}

	pool := make([]T, len(population))
	copy(pool, population)
	selected := make([]T, 0, k)
	for range min(k, len(pool)) {
		counts := make([]int, len(pool))
		for i, item := range pool {
			counts[i] = countOf(item)
		}
		weights := ComputeExpressionWeights(counts)
		total := 0.0
		for _, weight := range weights {
			total += weight
		}
		if total <= 0 {
			index := rand.IntN(len(pool))
			selected = append(selected, pool[index])
			pool = append(pool[:index], pool[index+1:]...)
			continue
		}
		threshold := rand.Float64() * total
		cumulative := 0.0
		for index, weight := range weights {
			cumulative += weight
			if threshold <= cumulative {
				selected = append(selected, pool[index])
				pool = append(pool[:index], pool[index+1:]...)
				break
			}
		}
	}
	return selected
}
