package utils

// A_memorix 检索调优随机源与三元组取样，对齐 Python retrieval_tuning_manager.py 的取样段
// 随机序列由字符串种子派生，仅保证同种子可复现

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// retrievalTuningRNG 由字符串种子派生的确定性随机源
// Python 使用 random.Random(str) 的梅森旋转实现，这里用 sha256 + splitmix64，
// 只保证同种子可复现，不保证与 Python 序列逐值相同
type retrievalTuningRNG struct {
	state uint64
}

// newRetrievalTuningRNG 构造随机源
func newRetrievalTuningRNG(seed string) *retrievalTuningRNG {
	digest := sha256.Sum256([]byte(seed))
	return &retrievalTuningRNG{state: binary.BigEndian.Uint64(digest[:8])}
}

// next 返回下一次 64 位随机值
func (r *retrievalTuningRNG) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	value := r.state
	value = (value ^ (value >> 30)) * 0xBF58476D1CE4E5B9
	value = (value ^ (value >> 27)) * 0x94D049BB133111EB
	return value ^ (value >> 31)
}

// intn 返回 [0, n) 的随机整数
func (r *retrievalTuningRNG) intn(n int) int {
	if n <= 1 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// choiceInt 从候选池取一个整数
func (r *retrievalTuningRNG) choiceInt(values []int) int {
	return values[r.intn(len(values))]
}

// choiceFloat 从候选池取一个浮点
func (r *retrievalTuningRNG) choiceFloat(values []float64) float64 {
	return values[r.intn(len(values))]
}

// choiceBool 从候选池取一个布尔
func (r *retrievalTuningRNG) choiceBool(values []bool) bool {
	return values[r.intn(len(values))]
}

// choiceString 从候选池取一个字符串
func (r *retrievalTuningRNG) choiceString(values []string) string {
	return values[r.intn(len(values))]
}

// shuffleInts 原地洗牌
func (r *retrievalTuningRNG) shuffleInts(values []int) {
	for index := len(values) - 1; index > 0; index-- {
		swap := r.intn(index + 1)
		values[index], values[swap] = values[swap], values[index]
	}
}

// sampleInts 从候选池无放回抽取 k 个
func (r *retrievalTuningRNG) sampleInts(values []int, count int) []int {
	pool := make([]int, len(values))
	copy(pool, values)
	if count > len(pool) {
		count = len(pool)
	}
	for index := 0; index < count; index++ {
		swap := index + r.intn(len(pool)-index)
		pool[index], pool[swap] = pool[swap], pool[index]
	}
	return pool[:count]
}

// SampleRetrievalTuningTriples 对齐 _sample_triples_for_query_set
// 第一轮按谓词轮询，剩余名额按低频实体与低频谓词补足
func SampleRetrievalTuningTriples(triples []RetrievalTuningTriple, sampleSize int, seed int) ([]RetrievalTuningTriple, map[string]any) {
	normalized := make([]RetrievalTuningTriple, 0, len(triples))
	for _, row := range triples {
		hash := pyStrip(row.Hash)
		if hash == "" {
			continue
		}
		normalized = append(normalized, RetrievalTuningTriple{
			Subject:   row.Subject,
			Predicate: row.Predicate,
			Object:    row.Object,
			Hash:      hash,
		})
	}
	if len(normalized) == 0 {
		return []RetrievalTuningTriple{}, map[string]any{"error": "no_relations"}
	}

	target := retrievalTuningClampInt(sampleSize, 4, 4, 1<<20)
	if target > len(normalized) {
		target = len(normalized)
	}

	predicateCounter := map[string]int{}
	entityCounter := map[string]int{}
	for _, row := range normalized {
		predicateCounter[retrievalTuningPredicateKey(row.Predicate)]++
		entityCounter[retrievalTuningEntityKey(row.Subject)]++
		entityCounter[retrievalTuningEntityKey(row.Object)]++
	}

	if target >= len(normalized) {
		return normalized, map[string]any{
			"strategy":          "all",
			"sample_size":       target,
			"total_triples":     len(normalized),
			"predicate_total":   len(predicateCounter),
			"predicate_sampled": len(predicateCounter),
		}
	}

	rng := newRetrievalTuningRNG(pyStr(seed) + ":triple_sample")
	byPredicate := map[string][]int{}
	order := make([]string, 0, len(predicateCounter))
	for index, row := range normalized {
		key := retrievalTuningPredicateKey(row.Predicate)
		if _, exists := byPredicate[key]; !exists {
			order = append(order, key)
		}
		byPredicate[key] = append(byPredicate[key], index)
	}
	for key := range byPredicate {
		rng.shuffleInts(byPredicate[key])
	}
	rng.shuffleInts(indexesOfStrings(order))
	sort.SliceStable(order, func(left int, right int) bool {
		return retrievalTuningOrderIndex(right) < retrievalTuningOrderIndex(left)
	})

	selected := make([]int, 0, target)
	selectedSet := map[int]struct{}{}
	predicateOrder := shuffledKeys(byPredicate, rng)
	for len(selected) < target {
		progressed := false
		for _, key := range predicateOrder {
			pool := byPredicate[key]
			if len(pool) == 0 {
				continue
			}
			index := pool[len(pool)-1]
			byPredicate[key] = pool[:len(pool)-1]
			if _, exists := selectedSet[index]; exists {
				continue
			}
			selected = append(selected, index)
			selectedSet[index] = struct{}{}
			progressed = true
			if len(selected) >= target {
				break
			}
		}
		if !progressed {
			break
		}
	}

	if len(selected) < target {
		remain := make([]int, 0, len(normalized)-len(selected))
		for index := range normalized {
			if _, exists := selectedSet[index]; exists {
				continue
			}
			remain = append(remain, index)
		}
		rng.shuffleInts(remain)
		score := func(index int) (int, int) {
			row := normalized[index]
			subjectFreq := entityCounter[retrievalTuningEntityKey(row.Subject)]
			objectFreq := entityCounter[retrievalTuningEntityKey(row.Object)]
			return subjectFreq + objectFreq, predicateCounter[retrievalTuningPredicateKey(row.Predicate)]
		}
		sort.SliceStable(remain, func(left int, right int) bool {
			leftEntity, leftPredicate := score(remain[left])
			rightEntity, rightPredicate := score(remain[right])
			if leftEntity != rightEntity {
				return leftEntity < rightEntity
			}
			return leftPredicate < rightPredicate
		})
		need := target - len(selected)
		if need > len(remain) {
			need = len(remain)
		}
		for _, index := range remain[:need] {
			selected = append(selected, index)
			selectedSet[index] = struct{}{}
		}
	}

	if len(selected) > target {
		selected = selected[:target]
	}
	sampled := make([]RetrievalTuningTriple, 0, len(selected))
	sampledPredicates := map[string]struct{}{}
	for _, index := range selected {
		sampled = append(sampled, normalized[index])
		sampledPredicates[retrievalTuningPredicateKey(normalized[index].Predicate)] = struct{}{}
	}
	return sampled, map[string]any{
		"strategy":          "predicate_round_robin_entity_diversity",
		"sample_size":       target,
		"total_triples":     len(normalized),
		"predicate_total":   len(predicateCounter),
		"predicate_sampled": len(sampledPredicates),
	}
}

// retrievalTuningPredicateKey 对齐 str(predicate or "").strip() or "__empty__"
func retrievalTuningPredicateKey(predicate string) string {
	trimmed := pyStrip(predicate)
	if trimmed == "" {
		return "__empty__"
	}
	return trimmed
}

// retrievalTuningEntityKey 对齐实体计数键，去空白并转小写
func retrievalTuningEntityKey(entity string) string {
	trimmed := pyLower(pyStrip(entity))
	if trimmed == "" {
		return "__empty__"
	}
	return trimmed
}

// indexesOfStrings 生成下标切片，用于占位洗牌
func indexesOfStrings(values []string) []int {
	indexes := make([]int, len(values))
	for index := range values {
		indexes[index] = index
	}
	return indexes
}

// retrievalTuningOrderIndex 占位函数，保持排序语义稳定
func retrievalTuningOrderIndex(value int) int {
	return value
}

// shuffledKeys 返回洗牌后的谓词键
func shuffledKeys(pools map[string][]int, rng *retrievalTuningRNG) []string {
	keys := make([]string, 0, len(pools))
	for key := range pools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for index := len(keys) - 1; index > 0; index-- {
		swap := rng.intn(index + 1)
		keys[index], keys[swap] = keys[swap], keys[index]
	}
	return keys
}
