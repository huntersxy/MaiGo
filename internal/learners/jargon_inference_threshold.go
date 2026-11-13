package learners

// inferenceThresholds 对齐 MaiBot JARGON_INFERENCE_THRESHOLDS
var inferenceThresholds = []int{4, 8, 25, 100}

// ShouldInferAtCount 在 count 达到阈值且超过 last_inference_count 时推断
func ShouldInferAtCount(count, lastInferenceCount int) bool {
	if count < inferenceThresholds[0] {
		return false
	}
	if count <= lastInferenceCount {
		return false
	}
	next := nextInferenceThreshold(lastInferenceCount)
	if next == 0 {
		return false
	}
	return count >= next
}

func nextInferenceThreshold(lastInferenceCount int) int {
	for _, th := range inferenceThresholds {
		if th > lastInferenceCount {
			return th
		}
	}
	return 0
}
