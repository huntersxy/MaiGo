package emojisystem

import "strings"

// levenshteinDistance 对齐 MaiBot person_info 内编辑距离实现
func levenshteinDistance(a, b string) int {
	left := []rune(a)
	right := []rune(b)
	if len(left) < len(right) {
		left, right = right, left
	}
	if len(right) == 0 {
		return len(left)
	}
	prev := make([]int, len(right)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, c1 := range left {
		cur := make([]int, len(right)+1)
		cur[0] = i + 1
		for j, c2 := range right {
			cost := 1
			if c1 == c2 {
				cost = 0
			}
			ins := prev[j+1] + 1
			del := cur[j] + 1
			sub := prev[j] + cost
			cur[j+1] = min3(ins, del, sub)
		}
		prev = cur
	}
	return prev[len(right)]
}

func min3(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

// emotionSimilarity 返回 0~1，对齐 MaiBot Levenshtein 归一化相似度
func emotionSimilarity(label, candidate string) float64 {
	label = strings.TrimSpace(strings.ToLower(label))
	candidate = strings.TrimSpace(strings.ToLower(candidate))
	if label == "" || candidate == "" {
		return 0
	}
	if label == candidate {
		return 1
	}
	dist := levenshteinDistance(label, candidate)
	maxLen := len([]rune(label))
	if n := len([]rune(candidate)); n > maxLen {
		maxLen = n
	}
	if maxLen == 0 {
		return 0
	}
	return 1 - float64(dist)/float64(maxLen)
}
