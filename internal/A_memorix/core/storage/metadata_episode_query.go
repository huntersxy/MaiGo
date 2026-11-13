package storage

// A_memorix Episode 检索查询，对齐 Python core/storage/metadata_episode.py 的查询侧方法

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"maibot-go/internal/A_memorix/core/utils"
)

// episodeQuerySpanPattern 对齐 Python r"[A-Za-z0-9_]+|[\u4e00-\u9fff]+"
var episodeQuerySpanPattern = regexp.MustCompile(`[A-Za-z0-9_]+|[\x{4e00}-\x{9fff}]+`)

// EpisodeQueryFilter 对齐 Python _build_episode_query_components 的关键字参数
// Source/Person 为空等价于 Python 的 None/空串（不过滤）
type EpisodeQueryFilter struct {
	// TimeFrom 时间下界，nil 表示不限
	TimeFrom *float64
	// TimeTo 时间上界，nil 表示不限
	TimeTo *float64
	// Person 人物名，参与 participants_json 与实体名匹配
	Person string
	// Source 来源过滤
	Source string
}

// buildEpisodeQueryComponents 对齐 Python _build_episode_query_components
// 返回来源表达式、有效起点表达式、有效终点表达式、条件列表与参数
func buildEpisodeQueryComponents(filter EpisodeQueryFilter) (string, string, string, []string, []any) {
	sourceExpr := "TRIM(COALESCE(e.source, ''))"
	effectiveStart := "COALESCE(e.event_time_start, e.event_time_end, e.updated_at)"
	effectiveEnd := "COALESCE(e.event_time_end, e.event_time_start, e.updated_at)"
	conditions := []string{
		sourceExpr + " != ''",
		"COALESCE(e.paragraph_count, 0) > 0",
		`COALESCE(e.paragraph_count, 0) = (
				SELECT COUNT(*)
				FROM episode_paragraphs ep_current
				JOIN paragraphs p_current ON p_current.hash = ep_current.paragraph_hash
				WHERE ep_current.episode_id = e.episode_id
				  AND (p_current.is_deleted IS NULL OR p_current.is_deleted = 0)
			)`,
	}
	params := make([]any, 0, 4)

	if filter.Source != "" {
		token := normalizeEpisodeSource(filter.Source)
		if token == "" {
			return sourceExpr, effectiveStart, effectiveEnd, []string{"1 = 0"}, []any{}
		}
		conditions = append(conditions, sourceExpr+" = ?")
		params = append(params, token)
	}

	person := strings.ToLower(strings.TrimSpace(filter.Person))
	if person != "" {
		likePerson := "%" + person + "%"
		conditions = append(conditions, `(
				LOWER(COALESCE(e.participants_json, '')) LIKE ?
				OR EXISTS (
					SELECT 1
					FROM episode_paragraphs ep_person
					JOIN paragraph_entities pe ON pe.paragraph_hash = ep_person.paragraph_hash
					JOIN entities en ON en.hash = pe.entity_hash
					WHERE ep_person.episode_id = e.episode_id
					  AND LOWER(en.name) LIKE ?
				)
			)`)
		params = append(params, likePerson, likePerson)
	}

	if filter.TimeFrom != nil && filter.TimeTo != nil {
		conditions = append(conditions, fmt.Sprintf("(%s >= ? AND %s <= ?)", effectiveEnd, effectiveStart))
		params = append(params, *filter.TimeFrom, *filter.TimeTo)
	} else if filter.TimeFrom != nil {
		conditions = append(conditions, fmt.Sprintf("(%s >= ?)", effectiveEnd))
		params = append(params, *filter.TimeFrom)
	} else if filter.TimeTo != nil {
		conditions = append(conditions, fmt.Sprintf("(%s <= ?)", effectiveStart))
		params = append(params, *filter.TimeTo)
	}

	return sourceExpr, effectiveStart, effectiveEnd, conditions, params
}

// tokenizeEpisodeQuery 将 episode 查询归一化为短语和 token
// 对齐 Python _tokenize_episode_query；未注入分词器时等价于 Python HAS_JIEBA=False
func (s *Store) tokenizeEpisodeQuery(query string) (string, []string) {
	normalized := strings.ToLower(strings.TrimSpace(utils.NormalizeText(query)))
	if normalized == "" {
		return "", []string{}
	}
	tokens := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	push := func(token string) {
		clean := strings.ToLower(strings.TrimSpace(token))
		if utf8.RuneCountInString(clean) < 2 {
			return
		}
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		tokens = append(tokens, clean)
	}

	for _, span := range episodeQuerySpanPattern.FindAllString(normalized, -1) {
		if isASCIIWordSpan(span) {
			push(span)
			continue
		}
		segmented := make([]string, 0, 8)
		if segmenter := s.currentSegmenter(); segmenter != nil {
			for _, item := range segmenter.CutForSearch(span) {
				trimmed := strings.TrimSpace(item)
				if utf8.RuneCountInString(trimmed) < 2 {
					continue
				}
				segmented = append(segmented, strings.ToLower(trimmed))
			}
		}
		if len(segmented) == 0 {
			compact := []rune(strings.TrimSpace(span))
			if len(compact) <= 3 {
				segmented = append(segmented, string(compact))
			} else {
				maxN := 4
				if len(compact) < maxN {
					maxN = len(compact)
				}
				for n := 2; n <= maxN; n++ {
					for start := 0; start+n <= len(compact); start++ {
						segmented = append(segmented, string(compact[start:start+n]))
					}
				}
			}
		}
		for _, token := range segmented {
			push(token)
		}
	}
	if len(tokens) == 0 && utf8.RuneCountInString(normalized) >= 2 {
		tokens = []string{normalized}
	}
	return normalized, tokens
}

// isASCIIWordSpan 对齐 Python re.fullmatch(r"[A-Za-z0-9_]+", span) 的判定
func isASCIIWordSpan(span string) bool {
	if span == "" {
		return false
	}
	for _, r := range span {
		if r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// GetEpisodeRowsByParagraphHashes 按段落哈希反查 Episode，对齐 Python get_episode_rows_by_paragraph_hashes
func (s *Store) GetEpisodeRowsByParagraphHashes(paragraphHashes []string, filter EpisodeQueryFilter, conn *Conn) ([]map[string]any, error) {
	normalized := normalizeHashSequence(paragraphHashes)
	if len(normalized) == 0 {
		return []map[string]any{}, nil
	}
	_, _, _, conditions, params := buildEpisodeQueryComponents(filter)
	conditions = append(conditions,
		fmt.Sprintf("ep.paragraph_hash IN (%s)", sqlPlaceholders(len(normalized))),
		"(p.is_deleted IS NULL OR p.is_deleted = 0)",
	)
	args := append(params, stringArgs(normalized)...)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(fmt.Sprintf(`SELECT e.*, ep.paragraph_hash AS matched_paragraph_hash
		FROM episodes e
		JOIN episode_paragraphs ep ON ep.episode_id = e.episode_id
		JOIN paragraphs p ON p.hash = ep.paragraph_hash
		WHERE %s
		ORDER BY e.updated_at DESC`, strings.Join(conditions, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string]map[string]any, len(items))
	order := make([]string, 0, len(items))
	for _, row := range items {
		episodeID := strings.TrimSpace(stringFromRow(row, "episode_id"))
		if episodeID == "" {
			continue
		}
		payload, ok := grouped[episodeID]
		if !ok {
			payload = episodeRowToDict(row)
			payload["matched_paragraph_hashes"] = []string{}
			grouped[episodeID] = payload
			order = append(order, episodeID)
		}
		matched := strings.TrimSpace(stringFromRow(row, "matched_paragraph_hash"))
		list, _ := payload["matched_paragraph_hashes"].([]string)
		if matched != "" && !episodeContainsString(list, matched) {
			payload["matched_paragraph_hashes"] = append(list, matched)
		}
	}

	out := make([]map[string]any, 0, len(order))
	for _, episodeID := range order {
		item := grouped[episodeID]
		list, _ := item["matched_paragraph_hashes"].([]string)
		item["matched_paragraph_count"] = len(list)
		out = append(out, item)
	}
	return out, nil
}

// GetEpisodeRowsByRelationHashes 按关系哈希反查 Episode，对齐 Python get_episode_rows_by_relation_hashes
func (s *Store) GetEpisodeRowsByRelationHashes(relationHashes []string, filter EpisodeQueryFilter, conn *Conn) ([]map[string]any, error) {
	normalized := normalizeHashSequence(relationHashes)
	if len(normalized) == 0 {
		return []map[string]any{}, nil
	}
	_, _, _, conditions, params := buildEpisodeQueryComponents(filter)
	conditions = append(conditions,
		fmt.Sprintf("pr.relation_hash IN (%s)", sqlPlaceholders(len(normalized))),
		"(p.is_deleted IS NULL OR p.is_deleted = 0)",
	)
	args := append(params, stringArgs(normalized)...)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(fmt.Sprintf(`SELECT
			e.*,
			p.hash AS matched_paragraph_hash,
			pr.relation_hash AS matched_relation_hash
		FROM episodes e
		JOIN episode_paragraphs ep ON ep.episode_id = e.episode_id
		JOIN paragraphs p ON p.hash = ep.paragraph_hash
		JOIN paragraph_relations pr ON pr.paragraph_hash = p.hash
		WHERE %s
		ORDER BY e.updated_at DESC`, strings.Join(conditions, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string]map[string]any, len(items))
	order := make([]string, 0, len(items))
	for _, row := range items {
		episodeID := strings.TrimSpace(stringFromRow(row, "episode_id"))
		if episodeID == "" {
			continue
		}
		payload, ok := grouped[episodeID]
		if !ok {
			payload = episodeRowToDict(row)
			payload["matched_paragraph_hashes"] = []string{}
			payload["matched_relation_hashes"] = []string{}
			grouped[episodeID] = payload
			order = append(order, episodeID)
		}
		matchedParagraph := strings.TrimSpace(stringFromRow(row, "matched_paragraph_hash"))
		paragraphList, _ := payload["matched_paragraph_hashes"].([]string)
		if matchedParagraph != "" && !episodeContainsString(paragraphList, matchedParagraph) {
			payload["matched_paragraph_hashes"] = append(paragraphList, matchedParagraph)
		}
		matchedRelation := strings.TrimSpace(stringFromRow(row, "matched_relation_hash"))
		relationList, _ := payload["matched_relation_hashes"].([]string)
		if matchedRelation != "" && !episodeContainsString(relationList, matchedRelation) {
			payload["matched_relation_hashes"] = append(relationList, matchedRelation)
		}
	}

	out := make([]map[string]any, 0, len(order))
	for _, episodeID := range order {
		item := grouped[episodeID]
		paragraphList, _ := item["matched_paragraph_hashes"].([]string)
		relationList, _ := item["matched_relation_hashes"].([]string)
		item["matched_paragraph_count"] = len(paragraphList)
		item["matched_relation_count"] = len(relationList)
		out = append(out, item)
	}
	return out, nil
}

// QueryEpisodes 查询 episode 列表，对齐 Python query_episodes
// limit 为 0 时取 20；allowedEpisodeIDs 为 nil 表示不限，空切片表示结果为空
func (s *Store) QueryEpisodes(query string, filter EpisodeQueryFilter, limit int, allowedEpisodeIDs []string, conn *Conn) ([]map[string]any, error) {
	safeLimit := limit
	if safeLimit == 0 {
		safeLimit = 20
	}
	if safeLimit < 1 {
		safeLimit = 1
	}
	_, _, effectiveEnd, conditions, params := buildEpisodeQueryComponents(filter)
	if allowedEpisodeIDs != nil {
		normalized := make([]string, 0, len(allowedEpisodeIDs))
		for _, value := range allowedEpisodeIDs {
			token := strings.TrimSpace(value)
			if token != "" {
				normalized = append(normalized, token)
			}
		}
		if len(normalized) == 0 {
			return []map[string]any{}, nil
		}
		conditions = append(conditions, "e.episode_id IN (SELECT value FROM json_each(?))")
		params = append(params, jsonDumps(normalized))
	}

	phrase, tokens := s.tokenizeEpisodeQuery(query)
	selectScoreSQL := "0.0 AS lexical_score"
	orderSQL := fmt.Sprintf("%s DESC, e.updated_at DESC", effectiveEnd)
	selectParams := make([]any, 0, 4+8*len(tokens))
	queryParams := make([]any, 0, 4*len(tokens))
	effectiveStart := "COALESCE(e.event_time_start, e.event_time_end, e.updated_at)"
	_ = effectiveStart

	if phrase != "" {
		titleExpr := "LOWER(COALESCE(e.title, ''))"
		summaryExpr := "LOWER(COALESCE(e.summary, ''))"
		keywordsExpr := "LOWER(COALESCE(e.keywords_json, ''))"
		participantsExpr := "LOWER(COALESCE(e.participants_json, ''))"

		phraseLike := "%" + phrase + "%"
		scoreParts := []string{
			fmt.Sprintf("CASE WHEN %s LIKE ? THEN 6.0 ELSE 0.0 END", titleExpr),
			fmt.Sprintf("CASE WHEN %s LIKE ? THEN 4.5 ELSE 0.0 END", keywordsExpr),
			fmt.Sprintf("CASE WHEN %s LIKE ? THEN 3.0 ELSE 0.0 END", summaryExpr),
			fmt.Sprintf("CASE WHEN %s LIKE ? THEN 2.0 ELSE 0.0 END", participantsExpr),
		}
		selectParams = append(selectParams, phraseLike, phraseLike, phraseLike, phraseLike)

		tokenPredicates := make([]string, 0, len(tokens))
		for _, token := range tokens {
			like := "%" + token + "%"
			tokenAny := fmt.Sprintf("(%s LIKE ? OR %s LIKE ? OR %s LIKE ? OR %s LIKE ?)",
				titleExpr, summaryExpr, keywordsExpr, participantsExpr)
			tokenPredicates = append(tokenPredicates, tokenAny)
			queryParams = append(queryParams, like, like, like, like)

			scoreParts = append(scoreParts, fmt.Sprintf("(%s + %s)",
				fmt.Sprintf("CASE WHEN %s LIKE ? THEN 3.0 ELSE 0.0 END", titleExpr),
				fmt.Sprintf("CASE WHEN %s LIKE ? THEN 2.5 ELSE 0.0 END + %s + %s + CASE WHEN %s THEN 2.0 ELSE 0.0 END",
					keywordsExpr,
					fmt.Sprintf("CASE WHEN %s LIKE ? THEN 2.0 ELSE 0.0 END", summaryExpr),
					fmt.Sprintf("CASE WHEN %s LIKE ? THEN 1.5 ELSE 0.0 END", participantsExpr),
					tokenAny,
				),
			))
			selectParams = append(selectParams, like, like, like, like, like, like, like, like)
		}
		if len(tokenPredicates) > 0 {
			conditions = append(conditions, "("+strings.Join(tokenPredicates, " OR ")+")")
		}
		selectScoreSQL = fmt.Sprintf("(%s) AS lexical_score", strings.Join(scoreParts, " + "))
		orderSQL = fmt.Sprintf("lexical_score DESC, %s DESC, e.updated_at DESC", effectiveEnd)
	}

	whereSQL := ""
	if len(conditions) > 0 {
		whereSQL = "WHERE " + strings.Join(conditions, " AND ")
	}
	finalParams := make([]any, 0, len(selectParams)+len(params)+len(queryParams)+1)
	finalParams = append(finalParams, selectParams...)
	finalParams = append(finalParams, params...)
	finalParams = append(finalParams, queryParams...)
	finalParams = append(finalParams, safeLimit)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(fmt.Sprintf(`SELECT e.*, %s
		FROM episodes e
		%s
		ORDER BY %s
		LIMIT ?`, selectScoreSQL, whereSQL, orderSQL), finalParams...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, episodeRowToDict(item))
	}
	return out, nil
}

// episodeContainsString 判断字符串切片是否已包含目标值
func episodeContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
