package utils

// difflib.SequenceMatcher.ratio 的移植，用于事实去重候选排序
// 只对齐默认参数：isjunk 为 None，autojunk 为 True

// pySequenceRatio 对齐 SequenceMatcher(None, left, right).ratio()
func pySequenceRatio(left string, right string) float64 {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	total := len(leftRunes) + len(rightRunes)
	if total == 0 {
		return 1.0
	}
	matcher := newPySequenceMatcher(leftRunes, rightRunes)
	matches := 0
	for _, block := range matcher.matchingBlocks() {
		matches += block.size
	}
	return 2.0 * float64(matches) / float64(total)
}

type pySequenceMatcher struct {
	a    []rune
	b    []rune
	b2j  map[rune][]int
	junk map[rune]struct{}
}

func newPySequenceMatcher(a []rune, b []rune) *pySequenceMatcher {
	matcher := &pySequenceMatcher{a: a, b: b, b2j: make(map[rune][]int), junk: make(map[rune]struct{})}
	for index, char := range b {
		matcher.b2j[char] = append(matcher.b2j[char], index)
	}
	// 对齐 autojunk：长度不小于 200 时把高频元素移出索引
	if len(b) >= 200 {
		threshold := len(b)/100 + 1
		for char, positions := range matcher.b2j {
			if len(positions) > threshold {
				matcher.junk[char] = struct{}{}
			}
		}
		for char := range matcher.junk {
			delete(matcher.b2j, char)
		}
	}
	return matcher
}

type pySequenceBlock struct {
	a    int
	b    int
	size int
}

// matchingBlocks 对齐 get_matching_blocks，返回按起点排序的匹配块
func (m *pySequenceMatcher) matchingBlocks() []pySequenceBlock {
	queue := [][4]int{{0, len(m.a), 0, len(m.b)}}
	blocks := make([]pySequenceBlock, 0, 8)
	for len(queue) > 0 {
		last := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		alo, ahi, blo, bhi := last[0], last[1], last[2], last[3]
		i, j, size := m.findLongestMatch(alo, ahi, blo, bhi)
		if size > 0 {
			blocks = append(blocks, pySequenceBlock{a: i, b: j, size: size})
			if alo < i && blo < j {
				queue = append(queue, [4]int{alo, i, blo, j})
			}
			if i+size < ahi && j+size < bhi {
				queue = append(queue, [4]int{i + size, ahi, j + size, bhi})
			}
		}
	}
	sortSequenceBlocks(blocks)
	collapsed := make([]pySequenceBlock, 0, len(blocks)+1)
	for _, block := range blocks {
		if block.size == 0 {
			continue
		}
		if len(collapsed) > 0 {
			previous := &collapsed[len(collapsed)-1]
			if previous.a+previous.size == block.a && previous.b+previous.size == block.b {
				previous.size += block.size
				continue
			}
		}
		collapsed = append(collapsed, block)
	}
	return collapsed
}

// findLongestMatch 对齐 find_longest_match
func (m *pySequenceMatcher) findLongestMatch(alo int, ahi int, blo int, bhi int) (int, int, int) {
	besti, bestj, bestsize := alo, blo, 0
	j2len := make(map[int]int)
	for i := alo; i < ahi; i++ {
		newJ2len := make(map[int]int)
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			size := j2len[j-1] + 1
			newJ2len[j] = size
			if size > bestsize {
				besti, bestj, bestsize = i-size+1, j-size+1, size
			}
		}
		j2len = newJ2len
	}
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti--
		bestj--
		bestsize++
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return besti, bestj, bestsize
}

// sortSequenceBlocks 按 a 起点排序，稳定排序保留同等起点顺序
func sortSequenceBlocks(blocks []pySequenceBlock) {
	for i := 1; i < len(blocks); i++ {
		for j := i; j > 0 && blocks[j].a < blocks[j-1].a; j-- {
			blocks[j], blocks[j-1] = blocks[j-1], blocks[j]
		}
	}
}
