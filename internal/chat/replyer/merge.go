package replyer

// mergeSegmentsToMaxCount 移植 utils._merge_processed_segments_to_max_count
func mergeSegmentsToMaxCount(segments []Segment, maxCount int) []Segment {
	if len(segments) <= maxCount {
		return segments
	}
	if maxCount <= 0 {
		return nil
	}

	segmentCount := len(segments)
	groupStarts := map[int]struct{}{0: {}}
	for index, segment := range segments {
		if segment.IsCorrection || segment.QuotePrevious {
			groupStarts[index] = struct{}{}
			if index+1 < segmentCount {
				groupStarts[index+1] = struct{}{}
			}
		}
	}

	evenlySpaced := make([]int, 0, maxCount)
	startIndex := 0
	for groupIndex := 0; groupIndex < maxCount; groupIndex++ {
		remainingSegments := segmentCount - startIndex
		remainingGroups := maxCount - groupIndex
		groupSize := (remainingSegments + remainingGroups - 1) / remainingGroups
		evenlySpaced = append(evenlySpaced, startIndex)
		startIndex += groupSize
	}

	for _, candidate := range evenlySpaced {
		if len(groupStarts) >= maxCount {
			break
		}
		groupStarts[candidate] = struct{}{}
	}

	sortedStarts := make([]int, 0, len(groupStarts))
	for start := range groupStarts {
		sortedStarts = append(sortedStarts, start)
	}
	sortInts(sortedStarts)

	merged := make([]Segment, 0, maxCount)
	for groupIndex, groupStart := range sortedStarts {
		groupEnd := segmentCount
		if groupIndex+1 < len(sortedStarts) {
			groupEnd = sortedStarts[groupIndex+1]
		}
		group := segments[groupStart:groupEnd]
		var parts []string
		for position, segment := range group {
			parts = append(parts, segment.Text)
			if position < len(group)-1 {
				parts = append(parts, segment.Separator)
			}
		}
		merged = append(merged, Segment{
			Text:          joinStrings(parts),
			QuotePrevious: group[0].QuotePrevious,
			Separator:     group[len(group)-1].Separator,
			IsCorrection:  group[0].IsCorrection,
		})
	}
	return merged
}

func sortInts(values []int) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func joinStrings(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 0, n)
	for _, p := range parts {
		b = append(b, p...)
	}
	return string(b)
}
