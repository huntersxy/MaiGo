package utils

// A_memorix 人物画像证据分桶，对齐 Python core/utils/person_profile_service.py 的分类段
// 规则分桶保证 LLM 不可用时行为稳定，LLM 结果全部为空时回退到规则结果

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// personProfileBucketKeys 固定段落键，顺序对齐 Python 字典
var personProfileBucketKeys = []string{
	"identity_settings",
	"relationship_settings",
	"stable_facts",
	"interaction_preferences",
	"recent_interactions",
	"uncertain_notes",
}

// newPersonProfileBuckets 构造六个空分桶
func newPersonProfileBuckets() map[string][]string {
	buckets := make(map[string][]string, len(personProfileBucketKeys))
	for _, key := range personProfileBucketKeys {
		buckets[key] = []string{}
	}
	return buckets
}

// classifyProfileEvidenceRuleBased 对齐 _classify_profile_evidence_rule_based
func (s *PersonProfileService) classifyProfileEvidenceRuleBased(relationEdges []map[string]any, vectorEvidence []map[string]any, memoryTraits []string) map[string][]string {
	buckets := newPersonProfileBuckets()

	traitLimit := len(memoryTraits)
	if traitLimit > 6 {
		traitLimit = 6
	}
	for _, trait := range memoryTraits[:traitLimit] {
		text := pyStrip(trait)
		if text != "" {
			appendPersonProfileBucket(buckets, personProfileGuessBucket(text), text)
		}
	}

	relationLimit := len(relationEdges)
	if relationLimit > 8 {
		relationLimit = 8
	}
	for _, rel := range relationEdges[:relationLimit] {
		text := personProfileRelationEvidenceText(rel)
		if text == "" {
			continue
		}
		bucket := personProfileGuessBucket(text)
		if bucket == "stable_facts" {
			bucket = "relationship_settings"
		}
		appendPersonProfileBucket(buckets, bucket, text)
	}

	for _, item := range vectorEvidence {
		text := pyStrip(personProfileMapText(item, "content"))
		if text == "" {
			continue
		}
		if isChatSummaryEvidence(item) {
			appendPersonProfileBucket(buckets, "recent_interactions", text)
			continue
		}
		appendPersonProfileBucket(buckets, personProfileGuessBucket(text), text)
	}
	return buckets
}

// buildProfileText 对齐 _build_profile_text
func (s *PersonProfileService) buildProfileText(personID string, primaryName string, aliases []string, relationEdges []map[string]any, vectorEvidence []map[string]any, memoryTraits []string, classifiedBuckets map[string][]string) string {
	buckets := classifiedBuckets
	if buckets == nil {
		buckets = s.classifyProfileEvidenceRuleBased(relationEdges, vectorEvidence, memoryTraits)
	}
	aliasLimit := len(aliases)
	if aliasLimit > 8 {
		aliasLimit = 8
	}
	return BuildStructuredProfileText(StructuredProfileInput{
		PersonID:               personID,
		PrimaryName:            primaryName,
		Aliases:                aliases[:aliasLimit],
		IdentitySettings:       buckets["identity_settings"],
		RelationshipSettings:   buckets["relationship_settings"],
		StableFacts:            buckets["stable_facts"],
		InteractionPreferences: buckets["interaction_preferences"],
		RecentInteractions:     buckets["recent_interactions"],
		UncertainNotes:         buckets["uncertain_notes"],
	})
}

// classifyProfileEvidence 对齐 _classify_profile_evidence
// 用 LLM 辅助证据分桶；模型未配置或调用失败时显式记录原因并回退到规则结果
// 模型返回非法 JSON 时返回错误，避免把规则结果伪装成模型结果
func (s *PersonProfileService) classifyProfileEvidence(ctx context.Context, personID string, primaryName string, aliases []string, relationEdges []map[string]any, vectorEvidence []map[string]any, memoryTraits []string) (map[string][]string, error) {
	fallback := s.classifyProfileEvidenceRuleBased(relationEdges, vectorEvidence, memoryTraits)
	candidates := s.buildProfileClassificationCandidates(relationEdges, vectorEvidence, memoryTraits)
	if len(candidates) == 0 {
		return fallback, nil
	}
	if s.llm == nil {
		s.logger.Warning(fmt.Sprintf("人物画像证据分类 LLM 未配置，降级为规则分桶: person_id=%s", personID))
		return fallback, nil
	}
	prompt := buildPersonProfileClassificationPrompt(personID, primaryName, aliases, candidates)
	raw, err := s.llm.GeneratePersonProfileClassification(ctx, prompt, 0.1, s.profileClassificationMaxTokens())
	if err != nil {
		if errors.Is(err, ErrPersonProfileClassificationUnavailable) {
			s.logger.Warning(fmt.Sprintf("人物画像证据分类模型未配置，降级为规则分桶: person_id=%s, err=%v", personID, err))
		} else {
			s.logger.Warning(fmt.Sprintf("人物画像证据分类模型调用失败，降级为规则分桶: person_id=%s, err=%v", personID, err))
		}
		return fallback, nil
	}
	parsed, parseErr := parsePersonProfileClassificationResponse(raw)
	if parseErr != nil {
		return nil, fmt.Errorf("人物画像证据分类响应解析失败: %w", parseErr)
	}
	if len(parsed) == 0 {
		return fallback, nil
	}
	return mergePersonProfileClassification(fallback, parsed), nil
}

// buildPersonProfileClassificationPrompt 对齐 _build_profile_classification_prompt
func buildPersonProfileClassificationPrompt(personID string, primaryName string, aliases []string, candidates []map[string]any) string {
	promptAliases := aliases
	if promptAliases == nil {
		promptAliases = []string{}
	}
	var builder strings.Builder
	builder.WriteString("你要把人物画像证据归类到固定段落。只根据证据归类，不要编造。\n")
	builder.WriteString("人物ID: " + personID + "\n")
	builder.WriteString("主称呼: " + primaryName + "\n")
	builder.WriteString("别名: " + personProfileJSONCompact(promptAliases) + "\n\n")
	builder.WriteString("分类定义：\n")
	builder.WriteString("- identity_settings: 稳定身份、角色、长期自我描述、重要背景。\n")
	builder.WriteString("- relationship_settings: 与麦麦、群友、组织、作品角色等长期关系或称呼关系。\n")
	builder.WriteString("- stable_facts: 长期稳定、证据明确的人物事实。\n")
	builder.WriteString("- interaction_preferences: 互动偏好、雷点、沟通习惯、喜欢/讨厌的相处方式。\n")
	builder.WriteString("- recent_interactions: 最近发生、对当前聊天有帮助但不应上升为长期事实的内容。\n")
	builder.WriteString("- uncertain_notes: 证据不足、推测、玩笑、自嘲、临时状态或疑似偏好。\n\n")
	builder.WriteString("要求：\n")
	builder.WriteString("1. 每条内容必须是简短中文陈述句。\n")
	builder.WriteString("2. 不要输出证据编号、hash 或置信度。\n")
	builder.WriteString("3. chat_summary 来源通常只能归入 recent_interactions 或 uncertain_notes。\n")
	builder.WriteString("4. 临时状态、计划、可能、似乎、玩笑类内容不能归入 stable_facts。\n")
	builder.WriteString("5. 只输出 JSON 对象，键为上述六类，值为字符串数组。\n\n")
	builder.WriteString("证据列表：\n" + personProfileJSONIndent(candidates, 2))
	return builder.String()
}

// personProfileJSONCompact 对齐 json.dumps(value, ensure_ascii=False)
func personProfileJSONCompact(value any) string {
	text, err := pyJSONMarshal(value)
	if err != nil {
		return "[]"
	}
	return text
}

// personProfileJSONIndent 对齐 json.dumps(value, ensure_ascii=False, indent=2)
func personProfileJSONIndent(value any, indent int) string {
	raw, err := pyJSONMarshal(value)
	if err != nil {
		return ""
	}
	var decoded any
	if unmarshalErr := json.Unmarshal([]byte(raw), &decoded); unmarshalErr != nil {
		return raw
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", strings.Repeat(" ", indent))
	if encodeErr := encoder.Encode(decoded); encodeErr != nil {
		return raw
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// parsePersonProfileClassificationResponse 对齐 _parse_profile_classification_response 的字段语义
// 非法 JSON、空响应或非对象响应返回错误，由调用方显式暴露，不伪装成空分类
func parsePersonProfileClassificationResponse(raw string) (map[string][]string, error) {
	text := pyStrip(raw)
	if text == "" {
		return nil, fmt.Errorf("人物画像分类响应为空")
	}
	repaired := repairJSONText(text)
	var payload any
	decoder := json.NewDecoder(strings.NewReader(repaired))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("人物画像分类响应不是合法 JSON: %w", err)
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("人物画像分类响应必须是 JSON 对象，实际为 %T", payload)
	}
	parsed := make(map[string][]string, len(personProfileBucketKeys))
	for _, key := range personProfileBucketKeys {
		values, ok := object[key].([]any)
		if !ok {
			parsed[key] = []string{}
			continue
		}
		items := make([]string, 0, len(values))
		for _, item := range values {
			text := pyStrip(pyStr(item))
			if text != "" {
				items = append(items, text)
			}
		}
		parsed[key] = items
	}
	return parsed, nil
}

// mergePersonProfileClassification 对齐 _merge_profile_classification
func mergePersonProfileClassification(fallback map[string][]string, llmResult map[string][]string) map[string][]string {
	buckets := newPersonProfileBuckets()
	for _, key := range personProfileBucketKeys {
		values := llmResult[key]
		if len(values) == 0 {
			values = fallback[key]
		}
		for _, value := range values {
			appendPersonProfileBucket(buckets, key, value)
		}
	}
	return buckets
}

// buildProfileClassificationCandidates 对齐 _build_profile_classification_candidates
func (s *PersonProfileService) buildProfileClassificationCandidates(relationEdges []map[string]any, vectorEvidence []map[string]any, memoryTraits []string) []map[string]any {
	candidates := make([]map[string]any, 0, 8+12+16)

	traitLimit := len(memoryTraits)
	if traitLimit > 8 {
		traitLimit = 8
	}
	for index, trait := range memoryTraits[:traitLimit] {
		text := pyStrip(trait)
		if text != "" {
			candidates = append(candidates, map[string]any{
				"id":          fmt.Sprintf("trait-%d", index+1),
				"source_type": "memory_trait",
				"text":        text,
			})
		}
	}

	relationLimit := len(relationEdges)
	if relationLimit > 12 {
		relationLimit = 12
	}
	for index, rel := range relationEdges[:relationLimit] {
		text := personProfileRelationEvidenceText(rel)
		if text != "" {
			candidates = append(candidates, map[string]any{
				"id":          fmt.Sprintf("relation-%d", index+1),
				"source_type": "relation",
				"text":        text,
			})
		}
	}

	vectorLimit := len(vectorEvidence)
	if vectorLimit > 16 {
		vectorLimit = 16
	}
	for index, item := range vectorEvidence[:vectorLimit] {
		text := pyStrip(personProfileMapText(item, "content"))
		if text == "" {
			continue
		}
		metadata := CoerceMetadataDict(item["metadata"])
		sourceType := pyStrip(personProfileMapText(metadata, "source_type"))
		if sourceType == "" {
			if isChatSummaryEvidence(item) {
				sourceType = "chat_summary"
			} else {
				sourceType = "person_fact"
			}
		}
		candidates = append(candidates, map[string]any{
			"id":          fmt.Sprintf("evidence-%d", index+1),
			"source_type": sourceType,
			"text":        personProfileTruncateRunes(text, 260),
		})
	}
	return candidates
}

// personProfileRelationEvidenceText 对齐 _format_relation_evidence_text
func personProfileRelationEvidenceText(rel map[string]any) string {
	subject := pyStrip(personProfileMapText(rel, "subject"))
	predicate := pyStrip(personProfileMapText(rel, "predicate"))
	object := pyStrip(personProfileMapText(rel, "object"))
	if subject == "" || predicate == "" || object == "" {
		return ""
	}
	return subject + predicate + object + "。"
}

// personProfileGuessBucket 对齐 _guess_profile_bucket
func personProfileGuessBucket(text string) string {
	text = pyStrip(text)
	if text == "" {
		return "stable_facts"
	}
	if personProfileLooksUncertainOrTemporary(text) {
		return "uncertain_notes"
	}
	for _, token := range []string{"身份", "职业", "工作", "学生", "老师", "作者", "画师", "设定", "角色", "来自"} {
		if strings.Contains(text, token) {
			return "identity_settings"
		}
	}
	for _, token := range []string{"关系", "朋友", "同事", "群友", "主人", "搭档", "称呼", "叫", "认识"} {
		if strings.Contains(text, token) {
			return "relationship_settings"
		}
	}
	for _, token := range []string{"喜欢", "讨厌", "偏好", "习惯", "不喜欢", "希望", "雷点", "介意", "更愿意"} {
		if strings.Contains(text, token) {
			return "interaction_preferences"
		}
	}
	return "stable_facts"
}

// personProfileLooksUncertainOrTemporary 对齐 _looks_uncertain_or_temporary
func personProfileLooksUncertainOrTemporary(text string) bool {
	text = pyStrip(text)
	if text == "" {
		return false
	}
	for _, marker := range []string{"可能", "似乎", "好像", "大概", "也许", "疑似", "暂时", "今天", "现在", "刚刚", "最近", "计划", "打算", "玩笑", "自嘲", "临时"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
