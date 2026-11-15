package utils

// Episode 聚合与落库服务，对齐 A_memorix core/utils/episode_service.py
// 元数据存储通过函数依赖注入，避免 utils 反向依赖 storage 包

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// EpisodeMaterializationVersion 物化协议版本
const EpisodeMaterializationVersion = "episode_source_revision_v2"

// EpisodeStoreDeps 元数据读取与发布能力
type EpisodeStoreDeps struct {
	GetParagraphEntitiesByHashes     func(hashes []string) (map[string][]map[string]any, error)
	GetLiveParagraphsBySource        func(source string, excludeStale bool) ([]map[string]any, error)
	GetEpisodesBySource              func(source string) ([]map[string]any, error)
	GetEpisodeEmptyGroupFingerprints func(source string) ([]string, error)
	ReplaceEpisodesForSource         func(source string, payloads []map[string]any, emptyGroupFingerprints []string) (map[string]any, error)
}

// EpisodeService Episode 后台物化服务
type EpisodeService struct {
	Deps                      EpisodeStoreDeps
	PluginConfig              map[string]any
	Segmentation              *EpisodeSegmentationService
	FeedbackHardFilterEnabled bool
	Logger                    Logger
}

// NewEpisodeService 构造 Episode 服务
func NewEpisodeService(
	deps EpisodeStoreDeps,
	pluginConfig map[string]any,
	segmentation *EpisodeSegmentationService,
	feedbackHardFilterEnabled bool,
	logger Logger,
) *EpisodeService {
	if pluginConfig == nil {
		pluginConfig = map[string]any{}
	}
	if segmentation == nil {
		segmentation = NewEpisodeSegmentationService(pluginConfig, nil, nil, logger)
	}
	return &EpisodeService{
		Deps:                      deps,
		PluginConfig:              pluginConfig,
		Segmentation:              segmentation,
		FeedbackHardFilterEnabled: feedbackHardFilterEnabled,
		Logger:                    logger,
	}
}

// Cfg 读取点分配置
func (s *EpisodeService) Cfg(key string, defaultValue any) any {
	return episodeConfigValue(s.PluginConfig, key, defaultValue)
}

func (s *EpisodeService) cfgInt(key string, defaultValue int) int {
	return episodeIntOr(s.Cfg(key, defaultValue), defaultValue)
}

func (s *EpisodeService) cfgFloat(key string, defaultValue float64) float64 {
	number, err := pyFloat64(s.Cfg(key, defaultValue))
	if err != nil {
		return defaultValue
	}
	return number
}

func (s *EpisodeService) cfgBool(key string, defaultValue bool) bool {
	value := s.Cfg(key, defaultValue)
	if value == nil {
		return defaultValue
	}
	return isTruthy(value)
}

func episodeOptionalFloat(value any) *float64 {
	if value == nil {
		return nil
	}
	number, err := pyFloat64(value)
	if err != nil {
		return nil
	}
	return &number
}

func episodeClampScoreOr(value any, defaultValue float64) float64 {
	number, err := pyFloat64(value)
	if err != nil {
		number = defaultValue
	}
	if number < 0 {
		return 0
	}
	if number > 1 {
		return 1
	}
	return number
}

// paragraphInterval 对齐 _paragraph_interval
func episodeParagraphInterval(paragraph map[string]any) (float64, float64) {
	createdAt := episodeOptionalFloat(paragraph["created_at"])
	eventTime := episodeOptionalFloat(paragraph["event_time"])
	eventStart := episodeOptionalFloat(paragraph["event_time_start"])
	eventEnd := episodeOptionalFloat(paragraph["event_time_end"])
	start := eventStart
	if start == nil {
		if eventTime != nil {
			start = eventTime
		} else {
			start = eventEnd
		}
	}
	if start == nil {
		if createdAt != nil {
			start = createdAt
		} else {
			zero := 0.0
			start = &zero
		}
	}
	end := eventEnd
	if end == nil {
		if eventTime != nil {
			end = eventTime
		} else {
			end = eventStart
		}
	}
	if end == nil {
		if createdAt != nil {
			end = createdAt
		} else {
			end = start
		}
	}
	startValue, endValue := *start, *end
	if endValue < startValue {
		startValue, endValue = endValue, startValue
	}
	return startValue, endValue
}

func episodeParagraphSortKey(paragraph map[string]any) (float64, float64, string) {
	start, end := episodeParagraphInterval(paragraph)
	return start, end, pyStrip(pyStrOrEmpty(paragraph["hash"]))
}

func episodeParagraphLess(left, right map[string]any) bool {
	leftStart, leftEnd, leftHash := episodeParagraphSortKey(left)
	rightStart, rightEnd, rightHash := episodeParagraphSortKey(right)
	if leftStart != rightStart {
		return leftStart < rightStart
	}
	if leftEnd != rightEnd {
		return leftEnd < rightEnd
	}
	return leftHash < rightHash
}

// GenerationSignature 对齐 generation_signature
func (s *EpisodeService) GenerationSignature() map[string]any {
	disabled := make(map[string]struct{})
	for _, item := range ArgumentTokens(s.Cfg("episode.disabled_source_types", []string{"person_fact", "knowledge_pack"})) {
		token := strings.ToLower(strings.TrimSpace(item))
		if token != "" {
			disabled[token] = struct{}{}
		}
	}
	disabled["knowledge_pack"] = struct{}{}
	disabledList := make([]string, 0, len(disabled))
	for token := range disabled {
		disabledList = append(disabledList, token)
	}
	sort.Strings(disabledList)

	segmentation := map[string]any{}
	if s.Segmentation != nil {
		segmentation = s.Segmentation.GenerationSignature()
	}
	return map[string]any{
		"materialization_version": EpisodeMaterializationVersion,
		"segmentation":            segmentation,
		"grouping": map[string]any{
			"max_paragraphs_per_call":    max(1, s.cfgInt("episode.max_paragraphs_per_call", 20)),
			"max_chars_per_call":         max(200, s.cfgInt("episode.max_chars_per_call", 6000)),
			"source_time_window_seconds": max(60.0, s.cfgFloat("episode.source_time_window_hours", 24)*3600.0),
		},
		"source_policy": map[string]any{
			"enabled":                      s.cfgBool("episode.enabled", true),
			"generation_enabled":           s.cfgBool("episode.generation_enabled", true),
			"disabled_source_types":        disabledList,
			"feedback_hard_filter_enabled": s.FeedbackHardFilterEnabled,
		},
	}
}

// GenerationHash 对齐 generation_hash
func (s *EpisodeService) GenerationHash(signature map[string]any) (string, error) {
	payload := signature
	if payload == nil {
		payload = s.GenerationSignature()
	}
	text, err := pyJSONMarshal(payload)
	if err != nil {
		return "", err
	}
	return ComputeHash(text)
}

// enrichParagraphParticipants 写入排序后的 _participant_names
func (s *EpisodeService) enrichParagraphParticipants(paragraphs []map[string]any) ([]map[string]any, error) {
	hashes := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if token := pyStrip(pyStrOrEmpty(paragraph["hash"])); token != "" {
			hashes = append(hashes, token)
		}
	}
	if s.Deps.GetParagraphEntitiesByHashes == nil {
		return nil, fmt.Errorf("episode metadata store unavailable")
	}
	entityMap, err := s.Deps.GetParagraphEntitiesByHashes(hashes)
	if err != nil {
		return nil, err
	}
	enriched := make([]map[string]any, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		item := make(map[string]any, len(paragraph)+1)
		for key, value := range paragraph {
			item[key] = value
		}
		paragraphHash := pyStrip(pyStrOrEmpty(item["hash"]))
		names := make(map[string]struct{})
		for _, entity := range entityMap[paragraphHash] {
			if name := pyStrip(pyStrOrEmpty(entity["name"])); name != "" {
				names[name] = struct{}{}
			}
		}
		ordered := make([]string, 0, len(names))
		for name := range names {
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		item["_participant_names"] = ordered
		enriched = append(enriched, item)
	}
	return enriched, nil
}

// GroupInputFingerprint 对齐 _group_input_fingerprint
func (s *EpisodeService) GroupInputFingerprint(group map[string]any) (string, error) {
	paragraphs := episodeMapSlice(group["paragraphs"])
	ordered := append([]map[string]any{}, paragraphs...)
	sort.SliceStable(ordered, func(i, j int) bool { return episodeParagraphLess(ordered[i], ordered[j]) })

	payloads := make([]any, 0, len(ordered))
	for _, paragraph := range ordered {
		participants := episodeStringList(paragraph["_participant_names"])
		payloads = append(payloads, map[string]any{
			"hash":             pyStrip(pyStrOrEmpty(paragraph["hash"])),
			"content":          pyStrOrEmpty(paragraph["content"]),
			"created_at":       episodeOptionalFloat(paragraph["created_at"]),
			"event_time":       episodeOptionalFloat(paragraph["event_time"]),
			"event_time_start": episodeOptionalFloat(paragraph["event_time_start"]),
			"event_time_end":   episodeOptionalFloat(paragraph["event_time_end"]),
			"time_granularity": pyStrip(pyStrOrEmpty(paragraph["time_granularity"])),
			"time_confidence":  episodeClampScoreOr(paragraph["time_confidence"], 1.0),
			"participants":     participants,
		})
	}
	segmentationGeneration, ok := group["_segmentation_generation"].(map[string]any)
	if !ok {
		if s.Segmentation != nil {
			segmentationGeneration = s.Segmentation.GenerationSignature()
		} else {
			segmentationGeneration = map[string]any{}
		}
	}
	fingerprint := map[string]any{
		"source":                  pyStrip(pyStrOrEmpty(group["source"])),
		"segmentation_generation": segmentationGeneration,
		"grouping": map[string]any{
			"max_paragraphs_per_call":  max(1, s.cfgInt("episode.max_paragraphs_per_call", 20)),
			"max_chars_per_call":       max(200, s.cfgInt("episode.max_chars_per_call", 6000)),
			"source_time_window_hours": max(60.0, s.cfgFloat("episode.source_time_window_hours", 24)*3600.0),
		},
		"paragraphs": payloads,
	}
	text, err := pyJSONMarshal(fingerprint)
	if err != nil {
		return "", err
	}
	return ComputeHash(text)
}

func episodeMapSlice(value any) []map[string]any {
	if typed, ok := value.([]map[string]any); ok {
		return typed
	}
	items, ok := episodeAnySlice(value)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if typed, ok := item.(map[string]any); ok {
			out = append(out, typed)
		}
	}
	return out
}

// ReusableGroupPayloads 对齐 _reusable_group_payloads
func episodeReusableGroupPayloads(group map[string]any, inputFingerprint string, cachedByFingerprint map[string][]map[string]any) []map[string]any {
	cached := cachedByFingerprint[inputFingerprint]
	if len(cached) == 0 {
		return nil
	}
	groupHashes := make(map[string]struct{})
	for _, paragraph := range episodeMapSlice(group["paragraphs"]) {
		if token := pyStrip(pyStrOrEmpty(paragraph["hash"])); token != "" {
			groupHashes[token] = struct{}{}
		}
	}
	for _, episode := range cached {
		for _, rawHash := range episodeStringList(episode["evidence_ids"]) {
			token := pyStrip(rawHash)
			if _, exists := groupHashes[token]; !exists {
				return nil
			}
		}
		if pyStrip(pyStrOrEmpty(episode["title"])) == "" ||
			pyStrip(pyStrOrEmpty(episode["summary"])) == "" ||
			pyStrip(pyStrOrEmpty(episode["segmentation_model"])) == "fallback_rule" {
			return nil
		}
	}
	out := make([]map[string]any, 0, len(cached))
	for _, episode := range cached {
		copied := make(map[string]any, len(episode))
		for key, value := range episode {
			copied[key] = value
		}
		out = append(out, copied)
	}
	return out
}

// GroupParagraphs 按 source 与时间邻近窗口组批
func (s *EpisodeService) GroupParagraphs(paragraphs []map[string]any) []map[string]any {
	if len(paragraphs) == 0 {
		return []map[string]any{}
	}
	maxParagraphs := max(1, s.cfgInt("episode.max_paragraphs_per_call", 20))
	maxChars := max(200, s.cfgInt("episode.max_chars_per_call", 6000))
	windowSeconds := max(60.0, s.cfgFloat("episode.source_time_window_hours", 24)*3600.0)

	sourceOrder := make([]string, 0)
	bySource := make(map[string][]map[string]any)
	for _, paragraph := range paragraphs {
		source := pyStrip(pyStrOrEmpty(paragraph["source"]))
		if _, exists := bySource[source]; !exists {
			sourceOrder = append(sourceOrder, source)
		}
		bySource[source] = append(bySource[source], paragraph)
	}

	groups := make([]map[string]any, 0)
	for _, source := range sourceOrder {
		ordered := append([]map[string]any{}, bySource[source]...)
		sort.SliceStable(ordered, func(i, j int) bool { return episodeParagraphLess(ordered[i], ordered[j]) })
		components := make([][]map[string]any, 0)
		current := make([]map[string]any, 0)
		var frontierEnd *float64
		for _, paragraph := range ordered {
			intervalStart, intervalEnd := episodeParagraphInterval(paragraph)
			if len(current) > 0 && frontierEnd != nil && intervalStart > *frontierEnd+windowSeconds {
				components = append(components, current)
				current = make([]map[string]any, 0)
				frontierEnd = nil
			}
			current = append(current, paragraph)
			if frontierEnd == nil {
				value := intervalEnd
				frontierEnd = &value
			} else if intervalEnd > *frontierEnd {
				value := intervalEnd
				frontierEnd = &value
			}
		}
		if len(current) > 0 {
			components = append(components, current)
		}
		for _, component := range components {
			chunk := make([]map[string]any, 0)
			currentChars := 0
			for _, paragraph := range component {
				contentLen := len([]rune(pyStrOrEmpty(paragraph["content"])))
				if len(chunk) > 0 && (len(chunk) >= maxParagraphs || currentChars+contentLen > maxChars) {
					groups = append(groups, map[string]any{"source": source, "paragraphs": chunk})
					chunk = make([]map[string]any, 0)
					currentChars = 0
				}
				chunk = append(chunk, paragraph)
				currentChars += contentLen
			}
			if len(chunk) > 0 {
				groups = append(groups, map[string]any{"source": source, "paragraphs": chunk})
			}
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		leftParagraphs := episodeMapSlice(groups[i]["paragraphs"])
		rightParagraphs := episodeMapSlice(groups[j]["paragraphs"])
		leftAnchor, leftHash := 0.0, ""
		rightAnchor, rightHash := 0.0, ""
		if len(leftParagraphs) > 0 {
			leftAnchor, _ = episodeParagraphInterval(leftParagraphs[0])
			leftHash = pyStrip(pyStrOrEmpty(leftParagraphs[0]["hash"]))
		}
		if len(rightParagraphs) > 0 {
			rightAnchor, _ = episodeParagraphInterval(rightParagraphs[0])
			rightHash = pyStrip(pyStrOrEmpty(rightParagraphs[0]["hash"]))
		}
		if leftAnchor != rightAnchor {
			return leftAnchor < rightAnchor
		}
		leftSource := pyStrip(pyStrOrEmpty(groups[i]["source"]))
		rightSource := pyStrip(pyStrOrEmpty(groups[j]["source"]))
		if leftSource != rightSource {
			return leftSource < rightSource
		}
		return leftHash < rightHash
	})
	return groups
}

// ComputeTimeMeta 返回起止时间、粒度与平均置信度
func episodeComputeTimeMeta(paragraphs []map[string]any) (*float64, *float64, *string, float64) {
	starts := make([]float64, 0)
	ends := make([]float64, 0)
	priority := map[string]int{"minute": 4, "hour": 3, "day": 2, "month": 1, "year": 0}
	var granularity *string
	granularityRank := -1
	confValues := make([]float64, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		start := episodeOptionalFloat(paragraph["event_time_start"])
		end := episodeOptionalFloat(paragraph["event_time_end"])
		timeValue := episodeOptionalFloat(paragraph["event_time"])
		createdAt := episodeOptionalFloat(paragraph["created_at"])
		var startCandidate *float64
		if start != nil {
			startCandidate = start
		} else if timeValue != nil {
			startCandidate = timeValue
		} else if end != nil {
			startCandidate = end
		} else {
			startCandidate = createdAt
		}
		var endCandidate *float64
		if end != nil {
			endCandidate = end
		} else if timeValue != nil {
			endCandidate = timeValue
		} else if start != nil {
			endCandidate = start
		} else {
			endCandidate = createdAt
		}
		if startCandidate != nil && endCandidate != nil && *endCandidate < *startCandidate {
			startCandidate, endCandidate = endCandidate, startCandidate
		}
		if startCandidate != nil {
			starts = append(starts, *startCandidate)
		}
		if endCandidate != nil {
			ends = append(ends, *endCandidate)
		}
		token := strings.ToLower(pyStrip(pyStrOrEmpty(paragraph["time_granularity"])))
		if rank, ok := priority[token]; ok && rank > granularityRank {
			granularityRank = rank
			value := token
			granularity = &value
		}
		confValues = append(confValues, episodeClampScoreOr(paragraph["time_confidence"], 1.0))
	}
	var timeStart *float64
	var timeEnd *float64
	if len(starts) > 0 {
		value := starts[0]
		for _, item := range starts[1:] {
			if item < value {
				value = item
			}
		}
		timeStart = &value
	}
	if len(ends) > 0 {
		value := ends[0]
		for _, item := range ends[1:] {
			if item > value {
				value = item
			}
		}
		timeEnd = &value
	}
	confidence := 1.0
	if len(confValues) > 0 {
		total := 0.0
		for _, item := range confValues {
			total += item
		}
		confidence = total / float64(len(confValues))
	}
	return timeStart, timeEnd, granularity, episodeClampScoreOr(confidence, 1.0)
}

// CollectParticipants 对齐 _collect_participants
func episodeCollectParticipants(paragraphs []map[string]any, limit int) []string {
	seen := make(map[string]struct{})
	participants := make([]string, 0)
	for _, paragraph := range paragraphs {
		for _, rawName := range episodeStringList(paragraph["_participant_names"]) {
			name := pyStrip(rawName)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			participants = append(participants, name)
			if len(participants) >= limit {
				return participants
			}
		}
	}
	return participants
}

var episodeKeywordPattern = regexp.MustCompile("[A-Za-z0-9_\\p{Han}]{2,}")

var episodeStopWords = map[string]struct{}{
	"the": {}, "and": {}, "that": {}, "this": {}, "with": {}, "from": {}, "for": {},
	"have": {}, "will": {}, "your": {}, "you": {},
	"我们": {}, "你们": {}, "他们": {}, "以及": {}, "一个": {}, "这个": {}, "那个": {}, "然后": {}, "因为": {}, "所以": {},
}

// DeriveKeywords 对齐 _derive_keywords
func episodeDeriveKeywords(paragraphs []map[string]any, limit int) []string {
	counts := make(map[string]int)
	order := make([]string, 0)
	for _, paragraph := range paragraphs {
		text := strings.ToLower(pyStrOrEmpty(paragraph["content"]))
		for _, token := range episodeKeywordPattern.FindAllString(text, -1) {
			if _, stop := episodeStopWords[token]; stop {
				continue
			}
			if _, exists := counts[token]; !exists {
				order = append(order, token)
			}
			counts[token]++
		}
	}
	type keyword struct {
		token string
		count int
	}
	items := make([]keyword, 0, len(order))
	for _, token := range order {
		items = append(items, keyword{token: token, count: counts[token]})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].count > items[j].count })
	out := make([]string, 0, len(items))
	for _, item := range items {
		if len(out) >= limit {
			break
		}
		out = append(out, item.token)
	}
	return out
}

func episodeNormalizeHashes(episodeHashes []string, groupHashes []string) []string {
	inGroup := make(map[string]struct{}, len(groupHashes))
	for _, token := range groupHashes {
		inGroup[token] = struct{}{}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(episodeHashes))
	for _, raw := range episodeHashes {
		token := pyStrip(raw)
		if token == "" {
			continue
		}
		if _, ok := inGroup[token]; !ok {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

// BuildEpisodePayloadsForGroup 对齐 _build_episode_payloads_for_group
func (s *EpisodeService) BuildEpisodePayloadsForGroup(ctx context.Context, group map[string]any) (map[string]any, error) {
	paragraphs := episodeMapSlice(group["paragraphs"])
	if len(paragraphs) == 0 {
		return map[string]any{"payloads": []map[string]any{}, "done_hashes": []string{}, "episode_count": 0, "fallback_count": 0}, nil
	}
	source := pyStrip(pyStrOrEmpty(group["source"]))
	inputFingerprint := pyStrip(pyStrOrEmpty(group["_input_fingerprint"]))
	if inputFingerprint == "" {
		value, err := s.GroupInputFingerprint(group)
		if err != nil {
			return nil, err
		}
		inputFingerprint = value
	}
	groupHashes := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if token := pyStrip(pyStrOrEmpty(paragraph["hash"])); token != "" {
			groupHashes = append(groupHashes, token)
		}
	}
	groupStart, groupEnd, _, _ := episodeComputeTimeMeta(paragraphs)
	llmResult, err := s.Segmentation.Segment(ctx, source, groupStart, groupEnd, paragraphs)
	if err != nil {
		return nil, err
	}
	episodes := episodeMapSlice(llmResult["episodes"])
	segmentationModel := pyStrip(pyStrOrEmpty(llmResult["segmentation_model"]))
	if segmentationModel == "" {
		segmentationModel = "auto"
	}
	segmentationVersion := pyStrip(pyStrOrEmpty(llmResult["segmentation_version"]))
	if segmentationVersion == "" {
		segmentationVersion = EpisodeSegmentationVersion
	}
	stored := make([]map[string]any, 0, len(episodes))
	for _, episode := range episodes {
		orderedHashes := episodeNormalizeHashes(episodeStringList(episode["paragraph_hashes"]), groupHashes)
		if len(orderedHashes) == 0 {
			continue
		}
		subParagraphs := make([]map[string]any, 0, len(orderedHashes))
		included := make(map[string]struct{}, len(orderedHashes))
		for _, token := range orderedHashes {
			included[token] = struct{}{}
		}
		for _, paragraph := range paragraphs {
			// 对齐 Python：段落侧取原始字符串（不 strip）与已规范化的 hash 集合比对
			if _, ok := included[pyStrOrEmpty(paragraph["hash"])]; ok {
				subParagraphs = append(subParagraphs, paragraph)
			}
		}
		eventStart, eventEnd, granularity, timeConfDefault := episodeComputeTimeMeta(subParagraphs)
		participants := episodeStringList(episode["participants"])
		keywords := episodeStringList(episode["keywords"])
		if len(participants) == 0 {
			participants = episodeCollectParticipants(subParagraphs, 16)
		}
		if len(keywords) == 0 {
			keywords = episodeDeriveKeywords(subParagraphs, 12)
		}
		title := truncateRunes(pyStrip(pyStrOrEmpty(episode["title"])), 120)
		summary := truncateRunes(pyStrip(pyStrOrEmpty(episode["summary"])), 2000)
		if title == "" || summary == "" {
			continue
		}
		seedText, err := pyJSONMarshal(map[string]any{
			"source":  source,
			"hashes":  orderedHashes,
			"version": segmentationVersion,
			"title":   title,
			"summary": summary,
		})
		if err != nil {
			return nil, err
		}
		episodeID, err := ComputeHash(seedText)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{
			"episode_id":           episodeID,
			"source":               episodeOptionalString(source),
			"title":                title,
			"summary":              summary,
			"event_time_start":     episodeValueOr(episode, "event_time_start", eventStart),
			"event_time_end":       episodeValueOr(episode, "event_time_end", eventEnd),
			"time_granularity":     episodeValueOr(episode, "time_granularity", granularity),
			"time_confidence":      episodeClampScoreOr(episode["time_confidence"], timeConfDefault),
			"participants":         limitStrings(participants, 16),
			"keywords":             limitStrings(keywords, 20),
			"evidence_ids":         orderedHashes,
			"paragraph_count":      len(orderedHashes),
			"llm_confidence":       episodeClampScoreOr(episode["llm_confidence"], 0.6),
			"segmentation_model":   episodeStringOr(episode, "segmentation_model", segmentationModel),
			"segmentation_version": episodeStringOr(episode, "segmentation_version", segmentationVersion),
			"input_fingerprint":    inputFingerprint,
		}
		stored = append(stored, payload)
	}
	storedHashes := make(map[string]struct{})
	for _, payload := range stored {
		for _, token := range episodeStringList(payload["evidence_ids"]) {
			storedHashes[pyStrip(token)] = struct{}{}
		}
	}
	doneHashes := make([]string, 0, len(groupHashes))
	for _, token := range groupHashes {
		if _, ok := storedHashes[token]; ok {
			doneHashes = append(doneHashes, token)
		}
	}
	return map[string]any{
		"payloads":       stored,
		"done_hashes":    doneHashes,
		"episode_count":  len(stored),
		"fallback_count": 0,
	}, nil
}

func episodeOptionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func episodeValueOr(episode map[string]any, key string, fallback any) any {
	if value, ok := episode[key]; ok {
		return value
	}
	return fallback
}

func episodeStringOr(episode map[string]any, key string, fallback string) string {
	if value := pyStrip(pyStrOrEmpty(episode[key])); value != "" {
		return value
	}
	return fallback
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

// PlanSourceRebuild 读取完整来源并生成确定性物化计划
func (s *EpisodeService) PlanSourceRebuild(ctx context.Context, source string, segmentationGeneration map[string]any) (map[string]any, error) {
	token := pyStrip(source)
	if token == "" {
		return map[string]any{
			"source":          "",
			"payloads":        []map[string]any{},
			"episode_count":   0,
			"fallback_count":  0,
			"group_count":     0,
			"paragraph_count": 0,
		}, nil
	}
	generation := segmentationGeneration
	if generation == nil {
		generation = s.GenerationSignature()
	}
	generationHash, err := s.GenerationHash(generation)
	if err != nil {
		return nil, err
	}
	if s.Deps.GetLiveParagraphsBySource == nil {
		return nil, fmt.Errorf("episode metadata store unavailable")
	}
	paragraphs, err := s.Deps.GetLiveParagraphsBySource(token, s.FeedbackHardFilterEnabled)
	if err != nil {
		return nil, err
	}
	if len(paragraphs) == 0 {
		return map[string]any{
			"source":                 token,
			"payloads":               []map[string]any{},
			"episode_count":          0,
			"fallback_count":         0,
			"group_count":            0,
			"paragraph_count":        0,
			"generation_hash":        generationHash,
			"reused_group_count":     0,
			"reused_episode_count":   0,
			"recomputed_group_count": 0,
		}, nil
	}
	paragraphs, err = s.enrichParagraphParticipants(paragraphs)
	if err != nil {
		return nil, err
	}
	groups := s.GroupParagraphs(paragraphs)
	if s.Deps.GetEpisodesBySource == nil || s.Deps.GetEpisodeEmptyGroupFingerprints == nil {
		return nil, fmt.Errorf("episode metadata store unavailable")
	}
	existingEpisodes, err := s.Deps.GetEpisodesBySource(token)
	if err != nil {
		return nil, err
	}
	cachedEmpty, err := s.Deps.GetEpisodeEmptyGroupFingerprints(token)
	if err != nil {
		return nil, err
	}
	cachedEmptySet := make(map[string]struct{}, len(cachedEmpty))
	for _, item := range cachedEmpty {
		cachedEmptySet[item] = struct{}{}
	}
	emptyGroupFingerprints := make([]string, 0)
	cachedByFingerprint := make(map[string][]map[string]any)
	for _, episode := range existingEpisodes {
		fingerprint := pyStrip(pyStrOrEmpty(episode["input_fingerprint"]))
		if fingerprint != "" {
			cachedByFingerprint[fingerprint] = append(cachedByFingerprint[fingerprint], episode)
		}
	}
	payloads := make([]map[string]any, 0)
	fallbackCount := 0
	reusedGroupCount := 0
	reusedEpisodeCount := 0
	recomputedGroupCount := 0
	for _, group := range groups {
		group["_segmentation_generation"] = generation
		fingerprint, err := s.GroupInputFingerprint(group)
		if err != nil {
			return nil, err
		}
		group["_input_fingerprint"] = fingerprint
		if _, ok := cachedEmptySet[fingerprint]; ok {
			emptyGroupFingerprints = append(emptyGroupFingerprints, fingerprint)
			reusedGroupCount++
			continue
		}
		cachedPayloads := episodeReusableGroupPayloads(group, fingerprint, cachedByFingerprint)
		if len(cachedPayloads) > 0 {
			payloads = append(payloads, cachedPayloads...)
			reusedGroupCount++
			reusedEpisodeCount += len(cachedPayloads)
			continue
		}
		result, err := s.BuildEpisodePayloadsForGroup(ctx, group)
		if err != nil {
			return nil, err
		}
		groupPayloads := episodeMapSlice(result["payloads"])
		payloads = append(payloads, groupPayloads...)
		if len(groupPayloads) == 0 {
			emptyGroupFingerprints = append(emptyGroupFingerprints, fingerprint)
		}
		fallbackCount += episodeIntOr(result["fallback_count"], 0)
		recomputedGroupCount++
	}
	return map[string]any{
		"source":                   token,
		"payloads":                 payloads,
		"episode_count":            len(payloads),
		"fallback_count":           fallbackCount,
		"group_count":              len(groups),
		"paragraph_count":          len(paragraphs),
		"reused_group_count":       reusedGroupCount,
		"reused_episode_count":     reusedEpisodeCount,
		"recomputed_group_count":   recomputedGroupCount,
		"generation_hash":          generationHash,
		"empty_group_fingerprints": emptyGroupFingerprints,
	}, nil
}

// RebuildSource 离线重建入口
func (s *EpisodeService) RebuildSource(ctx context.Context, source string) (map[string]any, error) {
	plan, err := s.PlanSourceRebuild(ctx, source, nil)
	if err != nil {
		return nil, err
	}
	token := pyStrip(pyStrOrEmpty(plan["source"]))
	result := make(map[string]any, len(plan))
	for key, value := range plan {
		if key == "payloads" {
			continue
		}
		result[key] = value
	}
	if token == "" {
		return result, nil
	}
	if s.Deps.ReplaceEpisodesForSource == nil {
		return nil, fmt.Errorf("episode metadata store unavailable")
	}
	replaceResult, err := s.Deps.ReplaceEpisodesForSource(token, episodeMapSlice(plan["payloads"]), episodeStringList(plan["empty_group_fingerprints"]))
	if err != nil {
		return nil, err
	}
	result["episode_count"] = episodeIntOr(replaceResult["episode_count"], 0)
	return result, nil
}
