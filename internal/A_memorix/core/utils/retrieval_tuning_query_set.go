package utils

// A_memorix 检索调优测试集构建，对齐 Python core/utils/retrieval_tuning_manager.py
// 覆盖轮次子集、holdout 切分与四类查询构造；随机源见 retrieval_tuning_sampling.go

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// SelectRetrievalTuningRoundCases 对齐 _select_round_eval_cases
func SelectRetrievalTuningRoundCases(cases []*RetrievalQueryCase, intensity string, roundIndex int, seed int) []*RetrievalQueryCase {
	if len(cases) == 0 {
		return []*RetrievalQueryCase{}
	}
	mode := pyLower(pyStrip(intensity))
	if _, ok := RetrievalTuningIntensityRounds(mode); !ok {
		mode = RetrievalTuningIntensityStandard
	}
	if mode == RetrievalTuningIntensityDeep {
		return append([]*RetrievalQueryCase{}, cases...)
	}

	ratio := 0.70
	minTotal := 24
	if mode == RetrievalTuningIntensityQuick {
		ratio = 0.45
		minTotal = 16
	}
	total := len(cases)
	target := int(float64(total) * ratio)
	if target < minTotal {
		target = minTotal
	}
	if target >= total {
		return append([]*RetrievalQueryCase{}, cases...)
	}

	rng := newRetrievalTuningRNG(pyStr(seed) + ":" + pyStr(roundIndex) + ":subset")
	byCategory := map[string][]int{}
	categories := make([]string, 0, len(byCategory))
	for index, item := range cases {
		category := item.Category
		if _, exists := byCategory[category]; !exists {
			categories = append(categories, category)
		}
		byCategory[category] = append(byCategory[category], index)
	}
	sort.Strings(categories)
	known := make([]string, 0, len(categories))
	for _, category := range categories {
		if IsRetrievalTuningCategory(category) {
			known = append(known, category)
		}
	}
	if len(known) == 0 {
		known = categories
	}
	perCategory := target / len(known)
	if perCategory < 1 {
		perCategory = 1
	}

	selected := make([]*RetrievalQueryCase, 0, target)
	selectedIDs := map[string]struct{}{}
	for _, category := range known {
		pool := byCategory[category]
		if len(pool) == 0 {
			continue
		}
		picked := pool
		if len(pool) > perCategory {
			picked = rng.sampleInts(pool, perCategory)
		}
		for _, index := range picked {
			item := cases[index]
			if _, exists := selectedIDs[item.CaseID]; exists {
				continue
			}
			selected = append(selected, item)
			selectedIDs[item.CaseID] = struct{}{}
		}
	}
	if len(selected) < target {
		remain := make([]int, 0, len(cases))
		for index, item := range cases {
			if _, exists := selectedIDs[item.CaseID]; exists {
				continue
			}
			remain = append(remain, index)
		}
		need := target - len(selected)
		if need > len(remain) {
			need = len(remain)
		}
		for _, index := range rng.sampleInts(remain, need) {
			item := cases[index]
			if _, exists := selectedIDs[item.CaseID]; exists {
				continue
			}
			selected = append(selected, item)
			selectedIDs[item.CaseID] = struct{}{}
		}
	}
	if len(selected) > target {
		selected = selected[:target]
	}
	return selected
}

// SplitRetrievalTuningQueryCases 对齐 _split_query_cases
func SplitRetrievalTuningQueryCases(cases []*RetrievalQueryCase, seed int) ([]*RetrievalQueryCase, []*RetrievalQueryCase, map[string]any) {
	if len(cases) <= 1 {
		shared := append([]*RetrievalQueryCase{}, cases...)
		return shared, shared, map[string]any{
			"strategy": "shared_small_set",
			"train":    len(cases),
			"holdout":  len(cases),
		}
	}

	rng := newRetrievalTuningRNG(pyStr(seed) + ":holdout_split")
	byCategory := map[string][]int{}
	for index, item := range cases {
		byCategory[item.Category] = append(byCategory[item.Category], index)
	}
	holdoutTarget := int(pyRound(float64(len(cases))*0.25, 0))
	if holdoutTarget < 4 {
		holdoutTarget = 4
	}
	if limit := len(cases) - 1; holdoutTarget > limit {
		holdoutTarget = limit
	}
	if holdoutTarget < 1 {
		holdoutTarget = 1
	}

	categories := make([]string, 0, len(byCategory))
	for category := range byCategory {
		categories = append(categories, category)
	}
	sort.Strings(categories)

	holdoutIDs := map[string]struct{}{}
	for _, category := range categories {
		pool := append([]int{}, byCategory[category]...)
		rng.shuffleInts(pool)
		perCategory := 0
		if len(pool) > 1 {
			perCategory = 1
		}
		for _, index := range pool[:perCategory] {
			if len(holdoutIDs) >= holdoutTarget {
				break
			}
			holdoutIDs[cases[index].CaseID] = struct{}{}
		}
	}
	if len(holdoutIDs) < holdoutTarget {
		remain := make([]int, 0, len(cases))
		for index, item := range cases {
			if _, exists := holdoutIDs[item.CaseID]; exists {
				continue
			}
			remain = append(remain, index)
		}
		need := holdoutTarget - len(holdoutIDs)
		if need > len(remain) {
			need = len(remain)
		}
		for _, index := range rng.sampleInts(remain, need) {
			holdoutIDs[cases[index].CaseID] = struct{}{}
		}
	}

	train := make([]*RetrievalQueryCase, 0, len(cases)-len(holdoutIDs))
	holdout := make([]*RetrievalQueryCase, 0, len(holdoutIDs))
	for _, item := range cases {
		if _, exists := holdoutIDs[item.CaseID]; exists {
			holdout = append(holdout, item)
			continue
		}
		train = append(train, item)
	}
	if len(train) == 0 || len(holdout) == 0 {
		shared := append([]*RetrievalQueryCase{}, cases...)
		return shared, shared, map[string]any{
			"strategy": "shared_fallback",
			"train":    len(cases),
			"holdout":  len(cases),
		}
	}
	ratio := pyRound(float64(len(holdout))/float64(max(1, len(cases))), 4)
	return train, holdout, map[string]any{
		"strategy":      "category_balanced_holdout",
		"train":         len(train),
		"holdout":       len(holdout),
		"holdout_ratio": ratio,
	}
}

// RetrievalTuningAnchor 测试集锚点，对齐 _build_query_set 中的 anchor 字典
type RetrievalTuningAnchor struct {
	AnchorID         string
	Subject          string
	Predicate        string
	Object           string
	RelationHash     string
	ParagraphHash    string
	ParagraphExcerpt string
}

// ToMap 输出 Python 同构字典
func (a *RetrievalTuningAnchor) ToMap() map[string]any {
	return map[string]any{
		"anchor_id":         a.AnchorID,
		"subject":           a.Subject,
		"predicate":         a.Predicate,
		"object":            a.Object,
		"relation_hash":     a.RelationHash,
		"paragraph_hash":    a.ParagraphHash,
		"paragraph_excerpt": a.ParagraphExcerpt,
	}
}

// BuildQuerySet 对齐 _build_query_set
func (m *RetrievalTuningManager) BuildQuerySet(ctx context.Context, sampleSize int, seed int, llmEnabled bool) ([]*RetrievalQueryCase, map[string]any) {
	if m.querySet == nil {
		return []*RetrievalQueryCase{}, map[string]any{"error": "metadata_store_unavailable"}
	}
	triples, err := m.querySet.GetAllTriples()
	if err != nil {
		m.logger.Warning("检索调优读取三元组失败: " + err.Error())
		return []*RetrievalQueryCase{}, map[string]any{"error": "metadata_store_unavailable"}
	}
	if len(triples) == 0 {
		return []*RetrievalQueryCase{}, map[string]any{"error": "no_relations"}
	}

	sampled, sampleInfo := SampleRetrievalTuningTriples(triples, sampleSize, seed)
	if len(sampled) == 0 {
		return []*RetrievalQueryCase{}, map[string]any{"error": "no_relations"}
	}

	anchors := make([]*RetrievalTuningAnchor, 0, len(sampled))
	for index, row := range sampled {
		anchor := &RetrievalTuningAnchor{
			AnchorID:     "a" + padNumber(index+1, 4),
			Subject:      row.Subject,
			Predicate:    row.Predicate,
			Object:       row.Object,
			RelationHash: row.Hash,
		}
		paragraphs, paraErr := m.querySet.GetParagraphsByRelation(row.Hash)
		if paraErr != nil {
			m.logger.Warning("检索调优读取关系段落失败: " + paraErr.Error())
		}
		if len(paragraphs) > 0 {
			anchor.ParagraphHash = pyStrip(pyStr(paragraphs[0]["hash"]))
			anchor.ParagraphExcerpt = retrievalTuningTruncateRunes(pyStr(paragraphs[0]["content"]), 300)
		}
		anchors = append(anchors, anchor)
	}
	if len(anchors) == 0 {
		return []*RetrievalQueryCase{}, map[string]any{"error": "no_anchors"}
	}

	predicateGroups := map[string][]*RetrievalTuningAnchor{}
	for _, anchor := range anchors {
		predicateGroups[anchor.Predicate] = append(predicateGroups[anchor.Predicate], anchor)
	}

	nlQueries := m.generateNLQueriesWithLLM(ctx, anchors, llmEnabled)
	cases := make([]*RetrievalQueryCase, 0, len(anchors)*4)
	seq := 0
	for _, anchor := range anchors {
		seq++
		expected := []string{anchor.RelationHash}
		if anchor.ParagraphHash != "" {
			expected = append(expected, anchor.ParagraphHash)
		}
		commonMeta := map[string]any{
			"anchor_id":      anchor.AnchorID,
			"relation_hash":  anchor.RelationHash,
			"paragraph_hash": anchor.ParagraphHash,
			"subject":        anchor.Subject,
			"predicate":      anchor.Predicate,
			"object":         anchor.Object,
		}

		relationCase := NewRetrievalQueryCase("spo_relation_"+padNumber(seq, 4), RetrievalTuningCategorySPORelation,
			anchor.Subject+"|"+anchor.Predicate+"|"+anchor.Object)
		relationCase.ExpectedHashes = []string{anchor.RelationHash}
		relationCase.ExpectedSPO = map[string]string{
			"subject":   anchor.Subject,
			"predicate": anchor.Predicate,
			"object":    anchor.Object,
		}
		relationCase.Metadata = commonMeta
		cases = append(cases, relationCase)

		searchCase := NewRetrievalQueryCase("spo_search_"+padNumber(seq, 4), RetrievalTuningCategorySPOSearch,
			BuildRetrievalTuningSPOSearchQuery(anchor, seq, predicateGroups))
		searchCase.ExpectedHashes = append([]string{}, expected...)
		searchCase.Metadata = commonMeta
		cases = append(cases, searchCase)

		keywordCase := NewRetrievalQueryCase("query_kw_"+padNumber(seq, 4), RetrievalTuningCategoryQueryKW,
			BuildRetrievalTuningKeywordQuery(anchor, seq, predicateGroups))
		keywordCase.ExpectedHashes = append([]string{}, expected...)
		keywordCase.Metadata = commonMeta
		cases = append(cases, keywordCase)

		query, ok := nlQueries[anchor.AnchorID]
		if !ok || query == "" {
			query = BuildRetrievalTuningNLTemplate(anchor, seq, predicateGroups)
		}
		nlCase := NewRetrievalQueryCase("query_nl_"+padNumber(seq, 4), RetrievalTuningCategoryQueryNL, query)
		nlCase.ExpectedHashes = append([]string{}, expected...)
		nlCase.Metadata = commonMeta
		cases = append(cases, nlCase)
	}

	categoryCounts := map[string]any{}
	for _, item := range cases {
		count, _ := categoryCounts[item.Category].(int)
		categoryCounts[item.Category] = count + 1
	}
	sampledSize, ok := sampleInfo["sample_size"].(int)
	if !ok {
		sampledSize = len(anchors)
	}
	stats := map[string]any{
		"anchors":          len(anchors),
		"case_total":       len(cases),
		"category_counts":  categoryCounts,
		"seed":             seed,
		"sample_size":      sampledSize,
		"sampling":         retrievalTuningDeepCopy(sampleInfo),
		"llm_nl_enabled":   llmEnabled,
		"llm_nl_generated": len(nlQueries),
	}
	return cases, stats
}

// padNumber 对齐 f"{value:04d}"
func padNumber(value int, width int) string {
	text := pyStr(value)
	for len(text) < width {
		text = "0" + text
	}
	return text
}

// retrievalTuningTruncateRunes 按码点截断，对齐 Python 字符串切片
func retrievalTuningTruncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// PickRetrievalTuningContrastAnchor 对齐 _pick_contrast_anchor
func PickRetrievalTuningContrastAnchor(anchor *RetrievalTuningAnchor, groups map[string][]*RetrievalTuningAnchor, seq int) *RetrievalTuningAnchor {
	pool := groups[anchor.Predicate]
	if len(pool) == 0 {
		return nil
	}
	candidates := make([]*RetrievalTuningAnchor, 0, len(pool))
	for _, item := range pool {
		if item == anchor || item.Object == anchor.Object {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[seq%len(candidates)]
}

// BuildRetrievalTuningSPOSearchQuery 对齐 _build_spo_search_query
func BuildRetrievalTuningSPOSearchQuery(anchor *RetrievalTuningAnchor, seq int, groups map[string][]*RetrievalTuningAnchor) string {
	contrast := PickRetrievalTuningContrastAnchor(anchor, groups, seq)
	contrastObject := ""
	if contrast != nil {
		contrastObject = pyStrip(contrast.Object)
	}
	variants := []string{
		anchor.Subject + " " + anchor.Predicate + " " + anchor.Object,
		anchor.Subject + " " + anchor.Object + " relation " + anchor.Predicate,
		anchor.Predicate + " " + anchor.Subject + " " + anchor.Object + " evidence",
		pyStrip(anchor.Subject + " " + anchor.Predicate + " " + anchor.Object + " not " + contrastObject),
	}
	return pyStrip(variants[seq%len(variants)])
}

// retrievalTuningKeywordTokenPattern 对齐 re.findall(r"[A-Za-z0-9_\u4e00-\u9fff]{2,}", ...)
var retrievalTuningKeywordTokenPattern = regexp.MustCompile(`[A-Za-z0-9_\x{4e00}-\x{9fff}]{2,}`)

// BuildRetrievalTuningKeywordQuery 对齐 _build_keyword_query
func BuildRetrievalTuningKeywordQuery(anchor *RetrievalTuningAnchor, seq int, groups map[string][]*RetrievalTuningAnchor) string {
	tokens := retrievalTuningKeywordTokenPattern.FindAllString(anchor.ParagraphExcerpt, -1)
	excluded := map[string]struct{}{
		pyLower(anchor.Subject):   {},
		pyLower(anchor.Predicate): {},
		pyLower(anchor.Object):    {},
	}
	extras := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, token := range tokens {
		key := pyLower(token)
		if _, exists := seen[key]; exists {
			continue
		}
		if _, exists := excluded[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		extras = append(extras, token)
		if len(extras) >= 2 {
			break
		}
	}
	contrast := PickRetrievalTuningContrastAnchor(anchor, groups, seq)
	contrastObject := ""
	if contrast != nil {
		contrastObject = pyStrip(contrast.Object)
	}
	firstExtra := ""
	if len(extras) > 0 {
		firstExtra = extras[0]
	}
	twoExtras := append([]string{}, extras...)
	if len(twoExtras) > 2 {
		twoExtras = twoExtras[:2]
	}
	variants := [][]string{
		append([]string{anchor.Subject, anchor.Object}, twoExtras...),
		append([]string{anchor.Predicate, anchor.Object}, twoExtras...),
		append([]string{anchor.Subject, anchor.Predicate}, twoExtras...),
		{anchor.Subject, anchor.Object, anchor.Predicate, contrastObject, firstExtra},
	}
	parts := variants[seq%len(variants)]
	filtered := make([]string, 0, len(parts))
	for _, item := range parts {
		if item == "" {
			continue
		}
		filtered = append(filtered, item)
	}
	return pyStrip(strings.Join(filtered, " "))
}

// BuildRetrievalTuningNLTemplate 对齐 _build_nl_template
func BuildRetrievalTuningNLTemplate(anchor *RetrievalTuningAnchor, seq int, groups map[string][]*RetrievalTuningAnchor) string {
	contrast := PickRetrievalTuningContrastAnchor(anchor, groups, seq)
	contrastObject := ""
	if contrast != nil {
		contrastObject = pyStrip(contrast.Object)
	}
	templates := []string{
		"请问 " + anchor.Subject + " 与 " + anchor.Object + " 的关系是什么，是否是“" + anchor.Predicate + "”？",
		"在当前知识库中，哪条信息说明 " + anchor.Subject + " 对应的是 " + anchor.Object + "，关系词接近“" + anchor.Predicate + "”？",
		"我想确认：" + anchor.Subject + " 和 " + anchor.Object + " 之间是不是“" + anchor.Predicate + "”这层关系，而不是 " + contrastObject + "？",
		"帮我查一下关于 " + anchor.Subject + " 与 " + anchor.Object + " 的证据，重点看 " + anchor.Predicate + " 相关描述。",
	}
	return templates[seq%len(templates)]
}
