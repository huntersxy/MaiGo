package personinfo

// 人物画像证据心理分类，对齐 person_profile_service._classify_profile_evidence
// LLM 不可用时回退固定规则，永不伪造分类成功

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"MaiGo/internal/llm_models"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// ProfileRelation 关系证据
type ProfileRelation struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// ProfileClassificationEvidence 画像证据
type ProfileClassificationEvidence struct {
	Content       string `json:"content"`
	SourceType    string `json:"source_type,omitempty"`
	IsChatSummary bool   `json:"is_chat_summary,omitempty"`
}

// ProfileClassificationInput 分类输入
type ProfileClassificationInput struct {
	PersonID     string
	PrimaryName  string
	Aliases      []string
	MemoryTraits []string
	Relations    []ProfileRelation
	Evidence     []ProfileClassificationEvidence
}

// ProfileClassification 分类结果
type ProfileClassification struct {
	Buckets map[string][]string `json:"buckets"`
	LLMUsed bool                `json:"llm_used"`
}

// ProfileClassifier 心理画像分类器
type ProfileClassifier struct {
	Router *llm.Router
}

// Classify 优先 LLM 分类，失败回退规则
func (c *ProfileClassifier) Classify(ctx context.Context, input ProfileClassificationInput) ProfileClassification {
	fallback := classifyProfileEvidenceRuleBased(input)
	result := ProfileClassification{Buckets: fallback}
	if c == nil || c.Router == nil {
		return result
	}
	candidates := buildProfileClassificationCandidates(input)
	if len(candidates) == 0 {
		return result
	}
	prompt := buildProfileClassificationPrompt(input, candidates)
	raw, err := c.generate(ctx, prompt)
	if err != nil {
		zap.L().Debug("人物画像证据分类模型调用失败", zap.Error(err))
		return result
	}
	parsed, ok := parseProfileClassificationResponse(raw)
	if !ok {
		return result
	}
	result.Buckets = mergeProfileClassification(fallback, parsed)
	result.LLMUsed = true
	return result
}

// generate 用 fast_model 任务分类，空配置由 Router 回退 utils
func (c *ProfileClassifier) generate(ctx context.Context, prompt string) (string, error) {
	_, client, err := c.Router.NewClientForTask(ctx, llm.TaskFastModel)
	if err != nil {
		return "", err
	}
	msg, err := client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(prompt)})
	if err != nil {
		return "", err
	}
	return agenticPlainText(msg), nil
}

func classifyProfileEvidenceRuleBased(input ProfileClassificationInput) map[string][]string {
	buckets := newProfileBuckets()
	for _, trait := range limitStrings(input.MemoryTraits, 6) {
		appendProfileBucket(buckets, guessProfileBucket(trait), trait)
	}
	for _, relation := range limitRelations(input.Relations, 8) {
		text := formatRelationEvidence(relation)
		if text == "" {
			continue
		}
		bucket := guessProfileBucket(text)
		if bucket == ProfileSectionStableFacts {
			bucket = ProfileSectionRelationshipSettings
		}
		appendProfileBucket(buckets, bucket, text)
	}
	for _, item := range input.Evidence {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		if item.IsChatSummary {
			appendProfileBucket(buckets, ProfileSectionRecentInteractions, content)
			continue
		}
		appendProfileBucket(buckets, guessProfileBucket(content), content)
	}
	return buckets
}

func buildProfileClassificationCandidates(input ProfileClassificationInput) []map[string]string {
	candidates := make([]map[string]string, 0, 36)
	for index, trait := range limitStrings(input.MemoryTraits, 8) {
		candidates = append(candidates, map[string]string{
			"id": fmt.Sprintf("trait-%d", index+1), "source_type": "memory_trait", "text": trait,
		})
	}
	for index, relation := range limitRelations(input.Relations, 12) {
		text := formatRelationEvidence(relation)
		if text == "" {
			continue
		}
		candidates = append(candidates, map[string]string{
			"id": fmt.Sprintf("relation-%d", index+1), "source_type": "relation", "text": text,
		})
	}
	for index, item := range input.Evidence {
		if index >= 16 {
			break
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		sourceType := strings.TrimSpace(item.SourceType)
		if sourceType == "" {
			if item.IsChatSummary {
				sourceType = "chat_summary"
			} else {
				sourceType = "person_fact"
			}
		}
		candidates = append(candidates, map[string]string{
			"id": fmt.Sprintf("evidence-%d", index+1), "source_type": sourceType,
			"text": truncateRunes(content, 260),
		})
	}
	return candidates
}

func buildProfileClassificationPrompt(input ProfileClassificationInput, candidates []map[string]string) string {
	aliasRaw, _ := json.Marshal(input.Aliases)
	candidateRaw, _ := json.MarshalIndent(candidates, "", "  ")
	return "你要把人物画像证据归类到固定段落。只根据证据归类，不要编造。\n" +
		"人物ID: " + input.PersonID + "\n" +
		"主称呼: " + input.PrimaryName + "\n" +
		"别名: " + string(aliasRaw) + "\n\n" +
		"分类定义：\n" +
		"- identity_settings: 稳定身份、角色、长期自我描述、重要背景。\n" +
		"- relationship_settings: 与麦麦、群友、组织、作品角色等长期关系或称呼关系。\n" +
		"- stable_facts: 长期稳定、证据明确的人物事实。\n" +
		"- interaction_preferences: 互动偏好、雷点、沟通习惯、喜欢/讨厌的相处方式。\n" +
		"- recent_interactions: 最近发生、对当前聊天有帮助但不应上升为长期事实的内容。\n" +
		"- uncertain_notes: 证据不足、推测、玩笑、自嘲、临时状态或疑似偏好。\n\n" +
		"要求：\n" +
		"1. 每条内容必须是简短中文陈述句。\n" +
		"2. 不要输出证据编号、hash 或置信度。\n" +
		"3. chat_summary 来源通常只能归入 recent_interactions 或 uncertain_notes。\n" +
		"4. 临时状态、计划、可能、似乎、玩笑类内容不能归入 stable_facts。\n" +
		"5. 只输出 JSON 对象，键为上述六类，值为字符串数组。\n\n" +
		"证据列表：\n" + string(candidateRaw)
}

func parseProfileClassificationResponse(raw string) (map[string][]string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, false
	}
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}
	var payload map[string]any
	if err := sonic.UnmarshalString(text, &payload); err != nil {
		return nil, false
	}
	parsed := newProfileBuckets()
	for _, key := range ProfileSectionOrder {
		items, ok := payload[key]
		if !ok {
			continue
		}
		list, ok := items.([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			value := strings.TrimSpace(fmt.Sprint(item))
			if value != "" {
				parsed[key] = append(parsed[key], value)
			}
		}
	}
	return parsed, true
}

func mergeProfileClassification(fallback, llmResult map[string][]string) map[string][]string {
	out := newProfileBuckets()
	for _, key := range ProfileSectionOrder {
		source := llmResult[key]
		if len(source) == 0 {
			source = fallback[key]
		}
		for _, value := range source {
			appendProfileBucket(out, key, value)
		}
	}
	return out
}

func newProfileBuckets() map[string][]string {
	out := make(map[string][]string, len(ProfileSectionOrder))
	for _, key := range ProfileSectionOrder {
		out[key] = []string{}
	}
	return out
}

func appendProfileBucket(buckets map[string][]string, bucket, text string) {
	clean := strings.TrimSpace(strings.Trim(text, "- "))
	if clean == "" {
		return
	}
	if _, ok := buckets[bucket]; !ok {
		bucket = ProfileSectionStableFacts
	}
	for _, existing := range buckets[bucket] {
		if existing == clean {
			return
		}
	}
	buckets[bucket] = append(buckets[bucket], clean)
}

func guessProfileBucket(text string) string {
	content := strings.TrimSpace(text)
	if content == "" {
		return ProfileSectionStableFacts
	}
	if looksUncertainOrTemporary(content) {
		return ProfileSectionUncertainNotes
	}
	for _, token := range []string{"身份", "职业", "工作", "学生", "老师", "作者", "画师", "设定", "角色", "来自"} {
		if strings.Contains(content, token) {
			return ProfileSectionIdentitySettings
		}
	}
	for _, token := range []string{"关系", "朋友", "同事", "群友", "主人", "搭档", "称呼", "叫", "认识"} {
		if strings.Contains(content, token) {
			return ProfileSectionRelationshipSettings
		}
	}
	for _, token := range []string{"喜欢", "讨厌", "偏好", "习惯", "不喜欢", "希望", "雷点", "介意", "更愿意"} {
		if strings.Contains(content, token) {
			return ProfileSectionInteractionPreferences
		}
	}
	return ProfileSectionStableFacts
}

func looksUncertainOrTemporary(text string) bool {
	for _, marker := range []string{
		"可能", "似乎", "好像", "大概", "也许", "疑似", "暂时", "今天", "现在",
		"刚刚", "最近", "计划", "打算", "玩笑", "自嘲", "临时",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func formatRelationEvidence(rel ProfileRelation) string {
	subject := strings.TrimSpace(rel.Subject)
	predicate := strings.TrimSpace(rel.Predicate)
	object := strings.TrimSpace(rel.Object)
	if subject == "" || predicate == "" || object == "" {
		return ""
	}
	return subject + predicate + object + "。"
}

func limitStrings(values []string, limit int) []string {
	out := make([]string, 0, limit)
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		out = append(out, text)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func limitRelations(values []ProfileRelation, limit int) []ProfileRelation {
	if len(values) > limit {
		return values[:limit]
	}
	return values
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// ClassifyProfileEvidence 通过 Registry 的 LLM 路由分类画像证据
func (r *Registry) ClassifyProfileEvidence(ctx context.Context, input ProfileClassificationInput) ProfileClassification {
	r.mu.RLock()
	router := r.factRouter
	r.mu.RUnlock()
	classifier := &ProfileClassifier{Router: router}
	return classifier.Classify(ctx, input)
}
