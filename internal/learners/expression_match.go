package learners

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

const (
	// MaxSelectedExpressions 对齐 maisaka_expression_selector.MAX_SELECTED_EXPRESSIONS
	MaxSelectedExpressions = 5
	// heuristicExpressionPoolSize 无向量索引时从库内拉取的近期表达上限
	heuristicExpressionPoolSize = 64
)

// BuildExpressionHabitsBlock 对齐 MaisakaExpressionSelector._build_expression_habits_block
func BuildExpressionHabitsBlock(picks []ExpressionPick) string {
	if len(picks) == 0 {
		return ""
	}
	lines := make([]string, 0, len(picks))
	for _, pick := range picks {
		situation := strings.TrimSpace(pick.Situation)
		style := strings.TrimSpace(pick.Style)
		if situation == "" || style == "" {
			continue
		}
		lines = append(lines, "- 当\""+situation+"\"时，可以用\""+style+"\"来表达。")
	}
	if len(lines) == 0 {
		return ""
	}
	return "你可以视情况自由使用以下句式：\n" + strings.Join(lines, "\n")
}

type scoredExpression struct {
	rec   ExpressionRecord
	score int
}

// selectExpressionsHeuristic 在无 FAISS/embedding 时的 DB 回退策略
//
// 该函数只做无向量索引时的 DB 回退；候选数门槛、向量候选池与 LLM 精选在 MatchExpressions 中完成
// 用 target/reply 文本对 situation/style 做关键词重合打分，否则按 last_active_time 取近期
func selectExpressionsHeuristic(candidates []ExpressionRecord, query string) []ExpressionRecord {
	valid := normalizeExpressionCandidates(candidates)
	if len(valid) == 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return pickRecentExpressions(valid, MaxSelectedExpressions)
	}

	terms := expressionQueryTerms(query)
	if len(terms) == 0 {
		return pickRecentExpressions(valid, MaxSelectedExpressions)
	}

	scored := make([]scoredExpression, 0, len(valid))
	for _, rec := range valid {
		score := scoreExpressionRecord(rec, terms)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredExpression{rec: rec, score: score})
	}
	if len(scored) == 0 {
		return pickRecentExpressions(valid, MaxSelectedExpressions)
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if !scored[i].rec.LastActiveTime.Equal(scored[j].rec.LastActiveTime) {
			return scored[i].rec.LastActiveTime.After(scored[j].rec.LastActiveTime)
		}
		if scored[i].rec.Count != scored[j].rec.Count {
			return scored[i].rec.Count > scored[j].rec.Count
		}
		return scored[i].rec.ID > scored[j].rec.ID
	})

	limit := MaxSelectedExpressions
	if len(scored) < limit {
		limit = len(scored)
	}
	out := make([]ExpressionRecord, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, scored[i].rec)
	}
	return out
}

// sampleLegacyExpressionCandidates 对齐 maisaka_expression_selector._sample_legacy_expression_candidates
//
// 高命中候选与全量候选各加权抽样一次后按 id 去重，候选不足 10 条时返回 nil
func sampleLegacyExpressionCandidates(all []ExpressionRecord) []ExpressionRecord {
	if len(all) < 10 {
		return nil
	}
	highCount := make([]ExpressionRecord, 0, len(all))
	for _, rec := range all {
		if rec.Count > 1 {
			highCount = append(highCount, rec)
		}
	}
	var selectedHigh []ExpressionRecord
	if len(highCount) >= 10 {
		selectedHigh = WeightedSample(highCount, min(len(highCount), MaxSelectedExpressions), func(rec ExpressionRecord) int {
			return rec.Count
		})
	}
	selectedRandom := WeightedSample(all, min(len(all), MaxSelectedExpressions), func(rec ExpressionRecord) int {
		return rec.Count
	})
	pool := make([]ExpressionRecord, 0, len(selectedHigh)+len(selectedRandom))
	seen := map[uint]struct{}{}
	for _, rec := range append(selectedHigh, selectedRandom...) {
		if _, ok := seen[rec.ID]; ok {
			continue
		}
		seen[rec.ID] = struct{}{}
		pool = append(pool, rec)
	}
	return pool
}

func normalizeExpressionCandidates(rows []ExpressionRecord) []ExpressionRecord {
	out := make([]ExpressionRecord, 0, len(rows))
	for _, row := range rows {
		situation := strings.TrimSpace(row.Situation)
		style := NormalizeExpressionStyleForLearning(row.Style)
		if row.ID == 0 || situation == "" || style == "" || IsPromptExampleExpressionStyle(style) {
			continue
		}
		row.Situation = situation
		row.Style = style
		out = append(out, row)
	}
	return out
}

func pickRecentExpressions(rows []ExpressionRecord, limit int) []ExpressionRecord {
	if limit <= 0 || len(rows) == 0 {
		return nil
	}
	sorted := append([]ExpressionRecord(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].LastActiveTime.Equal(sorted[j].LastActiveTime) {
			return sorted[i].LastActiveTime.After(sorted[j].LastActiveTime)
		}
		if sorted[i].Count != sorted[j].Count {
			return sorted[i].Count > sorted[j].Count
		}
		return sorted[i].ID > sorted[j].ID
	})
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted
}

func expressionQueryTerms(query string) []string {
	normalized := NormalizeMatchText(query)
	if normalized == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var terms []string
	add := func(term string) {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 2 {
			return
		}
		key := NormalizeMatchText(term)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		terms = append(terms, key)
	}

	for _, field := range strings.Fields(normalized) {
		add(field)
	}
	runes := []rune(normalized)
	for i := 0; i+1 < len(runes); i++ {
		if unicode.IsSpace(runes[i]) || unicode.IsSpace(runes[i+1]) {
			continue
		}
		add(string(runes[i : i+2]))
	}
	return terms
}

func scoreExpressionRecord(rec ExpressionRecord, terms []string) int {
	haystack := NormalizeMatchText(rec.Situation + " " + rec.Style)
	if haystack == "" || len(terms) == 0 {
		return 0
	}
	score := 0
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			score++
		}
	}
	return score
}

func recordsToExpressionPicks(rows []ExpressionRecord) []ExpressionPick {
	out := make([]ExpressionPick, 0, len(rows))
	for _, row := range rows {
		out = append(out, ExpressionPick{
			ID:        row.ID,
			Situation: row.Situation,
			Style:     row.Style,
			Count:     row.Count,
		})
	}
	return out
}

func touchExpressionLastActive(ctx context.Context, store Store, ids []uint) {
	if len(ids) == 0 {
		return
	}
	_ = store.TouchExpressionLastActive(ctx, ids)
}
