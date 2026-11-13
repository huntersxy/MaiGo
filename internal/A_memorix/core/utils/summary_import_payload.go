package utils

// A_memorix 摘要导入负载与归一化，对齐 Python core/utils/summary_importer.py 的模块级辅助

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// summaryImageEvidence 事实图片证据，对齐 image_evidence 列表项
type summaryImageEvidence struct {
	MessageID     string `json:"message_id"`
	ComponentPath string `json:"component_path"`
}

// summaryPayloadFact 去重计划中的事实，保留图片证据以便检查点续跑
type summaryPayloadFact struct {
	FactID        string                 `json:"fact_id"`
	Text          string                 `json:"text"`
	PersonIDs     []string               `json:"person_ids,omitempty"`
	ImageEvidence []summaryImageEvidence `json:"image_evidence"`
}

// summaryPayloadTarget 去重目标，对齐 plan decision 中的 target
type summaryPayloadTarget struct {
	ID             string           `json:"id,omitempty"`
	Text           string           `json:"text,omitempty"`
	ClaimID        string           `json:"claim_id,omitempty"`
	ParagraphHash  string           `json:"paragraph_hash,omitempty"`
	InputFactID    string           `json:"input_fact_id,omitempty"`
	ValidFrom      *float64         `json:"valid_from,omitempty"`
	ValidTo        *float64         `json:"valid_to,omitempty"`
	EventTimeStart *float64         `json:"event_time_start,omitempty"`
	EventTimeEnd   *float64         `json:"event_time_end,omitempty"`
	ObservedAt     *float64         `json:"observed_at,omitempty"`
	PersonIDs      []string         `json:"person_ids,omitempty"`
	Observations   []map[string]any `json:"observations,omitempty"`
}

// summaryPayloadDecision 单条去重决定
type summaryPayloadDecision struct {
	FactID       string                `json:"fact_id"`
	Action       string                `json:"action"`
	Target       *summaryPayloadTarget `json:"target,omitempty"`
	CandidateIDs []string              `json:"candidate_ids,omitempty"`
}

// summaryPayloadPlan 去重计划，对齐 Python plan 字典
type summaryPayloadPlan struct {
	ScopeType       string                   `json:"scope_type"`
	ScopeID         string                   `json:"scope_id"`
	Facts           []summaryPayloadFact     `json:"facts"`
	Source          map[string]any           `json:"source"`
	Decisions       []summaryPayloadDecision `json:"decisions"`
	RelationSupport map[string][]string      `json:"relation_support"`
}

// summaryImportPayload 检查点续跑负载，对齐 Python payload 字典
type summaryImportPayload struct {
	Plan          summaryPayloadPlan  `json:"plan"`
	Metadata      map[string]any      `json:"metadata"`
	Entities      []string            `json:"entities"`
	Relations     []map[string]string `json:"relations"`
	TimeMeta      map[string]any      `json:"time_meta"`
	AllowedImages [][2]string         `json:"allowed_images"`
}

// normalizeSummaryEntityItems 对齐 _normalize_entity_items
func normalizeSummaryEntityItems(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]string); typedOK {
			items = make([]any, 0, len(typed))
			for _, item := range typed {
				items = append(items, item)
			}
		} else {
			return []string{}
		}
	}
	entities := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		name := ""
		switch typed := item.(type) {
		case string:
			name = pyStrip(typed)
		case map[string]any:
			name = importValueText(summaryFirstValue(typed, "name", "label", "entity"))
		}
		if name == "" {
			continue
		}
		key := pyLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entities = append(entities, name)
	}
	return entities
}

// summaryFirstValue 依次取第一个可用键，对齐 item.get(a) or item.get(b) or item.get(c)
func summaryFirstValue(item map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := item[key]; exists && isTruthy(value) {
			return value
		}
	}
	return ""
}

// normalizeSummaryRelationItems 对齐 _normalize_relation_items
func normalizeSummaryRelationItems(raw any) []map[string]string {
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]map[string]string); typedOK {
			return append([]map[string]string{}, typed...)
		}
		return []map[string]string{}
	}
	relations := make([]map[string]string, 0, len(items))
	for _, item := range items {
		mapping, ok := importObjectMap(item)
		if !ok {
			continue
		}
		subject := importValueText(mapping["subject"])
		predicate := importValueText(mapping["predicate"])
		object := importValueText(mapping["object"])
		if subject == "" || predicate == "" || object == "" {
			continue
		}
		relations = append(relations, map[string]string{"subject": subject, "predicate": predicate, "object": object})
	}
	return relations
}

// normalizeSummaryFacts 对齐 _normalize_summary_facts
func normalizeSummaryFacts(raw any) []summaryPayloadFact {
	items, ok := raw.([]any)
	if !ok {
		return []summaryPayloadFact{}
	}
	facts := make([]summaryPayloadFact, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		mapping, ok := importObjectMap(item)
		if !ok {
			continue
		}
		factID := importValueText(mapping["fact_id"])
		text := importValueText(mapping["text"])
		if factID == "" || text == "" {
			continue
		}
		if _, exists := seen[factID]; exists {
			continue
		}
		evidence := make([]summaryImageEvidence, 0, 2)
		rawEvidence, _ := mapping["image_evidence"].([]any)
		for _, entry := range rawEvidence {
			evidenceMap, ok := importObjectMap(entry)
			if !ok {
				continue
			}
			messageID := importValueText(evidenceMap["message_id"])
			componentPath := importValueText(evidenceMap["component_path"])
			if messageID == "" || componentPath == "" {
				continue
			}
			evidence = append(evidence, summaryImageEvidence{MessageID: messageID, ComponentPath: componentPath})
		}
		seen[factID] = struct{}{}
		facts = append(facts, summaryPayloadFact{FactID: factID, Text: text, ImageEvidence: evidence})
	}
	return facts
}

// summaryMetadata 对齐 coerce_metadata_dict
func summaryMetadata(value any) map[string]any {
	mapping, ok := importObjectMap(value)
	if !ok {
		return map[string]any{}
	}
	out := make(map[string]any, len(mapping))
	for key, item := range mapping {
		out[key] = item
	}
	return out
}

// summaryNestedMap 读取嵌套配置字典，缺失或类型不符返回空字典
func summaryNestedMap(root map[string]any, path ...string) map[string]any {
	current := root
	for _, key := range path {
		value, exists := current[key]
		if !exists {
			return map[string]any{}
		}
		next, ok := importObjectMap(value)
		if !ok {
			return map[string]any{}
		}
		current = next
	}
	return current
}

// summaryConfigValue 读取嵌套配置值
func summaryConfigValue(root map[string]any, defaultValue any, path ...string) any {
	if len(path) == 0 {
		return defaultValue
	}
	parent := summaryNestedMap(root, path[:len(path)-1]...)
	value, exists := parent[path[len(path)-1]]
	if !exists {
		return defaultValue
	}
	return value
}

// summaryMapFromItems 合并两个字典，后者覆盖前者，对齐 {**left, **right}
func summaryMapFromItems(left map[string]any, right map[string]any) map[string]any {
	out := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		out[key] = value
	}
	for key, value := range right {
		out[key] = value
	}
	return out
}

// summarySortedPersonIDs 对齐 sorted({...})
func summarySortedPersonIDs(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		unique[value] = struct{}{}
	}
	out := make([]string, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// paragraphCreatedAt 对齐 _paragraph_created_at
func paragraphCreatedAt(paragraph map[string]any) float64 {
	value, exists := paragraph["summary_observed_at"]
	if !exists {
		value = paragraph["created_at"]
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return parsed
}

// messageTimestamp 对齐 _message_timestamp，零值表示无时间
func messageTimestamp(message SessionMessage) (float64, bool) {
	if message.Timestamp.IsZero() {
		return 0, false
	}
	return float64(message.Timestamp.UnixNano()) / 1e9, true
}

// summaryPyEmptyList 判断是否是空列表，对齐 value == []
func summaryPyEmptyList(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	case []map[string]string:
		return len(typed) == 0
	default:
		return false
	}
}

// imageComponentPaths 对齐 iter_message_image_components，按组件树下标生成路径
func imageComponentPaths(components []ChatMessageComponent) []string {
	paths := make([]string, 0, len(components))
	walkImageComponents(components, "", &paths)
	return paths
}

// walkImageComponents 深度优先遍历组件树
func walkImageComponents(components []ChatMessageComponent, prefix string, out *[]string) {
	for index, component := range components {
		path := fmt.Sprintf("%d", index)
		if prefix != "" {
			path = prefix + "." + path
		}
		if component.Image {
			*out = append(*out, path)
		}
		for nodeIndex, node := range component.ForwardComponents {
			nodePath := fmt.Sprintf("%s.%d", path, nodeIndex)
			walkImageComponents(node.Content, nodePath, out)
		}
	}
}

// buildChatExternalRef 对齐 build_chat_external_ref
func buildChatExternalRef(chatID string, messageID string, componentPath string) string {
	return "chat:" + chatID + ":" + messageID + ":" + componentPath
}

// summaryPlanFromPayload 从检查点负载恢复去重计划
func summaryPlanFromPayload(payload summaryImportPayload) *FactDedupPlan {
	facts := make([]FactDedupFact, 0, len(payload.Plan.Facts))
	for _, fact := range payload.Plan.Facts {
		facts = append(facts, FactDedupFact{FactID: fact.FactID, Text: fact.Text, PersonIDs: append([]string{}, fact.PersonIDs...)})
	}
	decisions := make([]FactDedupDecision, 0, len(payload.Plan.Decisions))
	for _, decision := range payload.Plan.Decisions {
		item := FactDedupDecision{FactID: decision.FactID, Action: decision.Action, CandidateIDs: append([]string{}, decision.CandidateIDs...)}
		if decision.Target != nil {
			item.Target = &FactDedupTarget{
				ID:             decision.Target.ID,
				Text:           decision.Target.Text,
				ClaimID:        decision.Target.ClaimID,
				ParagraphHash:  decision.Target.ParagraphHash,
				InputFactID:    decision.Target.InputFactID,
				ValidFrom:      decision.Target.ValidFrom,
				ValidTo:        decision.Target.ValidTo,
				EventTimeStart: decision.Target.EventTimeStart,
				EventTimeEnd:   decision.Target.EventTimeEnd,
				ObservedAt:     decision.Target.ObservedAt,
				PersonIDs:      append([]string{}, decision.Target.PersonIDs...),
				Observations:   append([]map[string]any{}, decision.Target.Observations...),
			}
		}
		decisions = append(decisions, item)
	}
	return &FactDedupPlan{
		ScopeType:       payload.Plan.ScopeType,
		ScopeID:         payload.Plan.ScopeID,
		Facts:           facts,
		Source:          payload.Plan.Source,
		Decisions:       decisions,
		RelationSupport: payload.Plan.RelationSupport,
	}
}

// summaryPayloadFromPlan 从去重计划构造检查点负载
func summaryPayloadFromPlan(
	plan *FactDedupPlan,
	facts []summaryPayloadFact,
	metadata map[string]any,
	entities []string,
	relations []map[string]string,
	timeMeta map[string]any,
	allowed map[[2]string]struct{},
) summaryImportPayload {
	decisions := make([]summaryPayloadDecision, 0, len(plan.Decisions))
	for _, decision := range plan.Decisions {
		item := summaryPayloadDecision{FactID: decision.FactID, Action: decision.Action}
		if decision.Target != nil {
			item.Target = &summaryPayloadTarget{
				ID:             decision.Target.ID,
				Text:           decision.Target.Text,
				ClaimID:        decision.Target.ClaimID,
				ParagraphHash:  decision.Target.ParagraphHash,
				InputFactID:    decision.Target.InputFactID,
				ValidFrom:      decision.Target.ValidFrom,
				ValidTo:        decision.Target.ValidTo,
				EventTimeStart: decision.Target.EventTimeStart,
				EventTimeEnd:   decision.Target.EventTimeEnd,
				ObservedAt:     decision.Target.ObservedAt,
				PersonIDs:      append([]string{}, decision.Target.PersonIDs...),
				Observations:   append([]map[string]any{}, decision.Target.Observations...),
			}
		}
		decisions = append(decisions, item)
	}
	allowedImages := make([][2]string, 0, len(allowed))
	for key := range allowed {
		allowedImages = append(allowedImages, key)
	}
	sort.Slice(allowedImages, func(left int, right int) bool {
		if allowedImages[left][0] != allowedImages[right][0] {
			return allowedImages[left][0] < allowedImages[right][0]
		}
		return allowedImages[left][1] < allowedImages[right][1]
	})
	return summaryImportPayload{
		Plan: summaryPayloadPlan{
			ScopeType:       plan.ScopeType,
			ScopeID:         plan.ScopeID,
			Facts:           append([]summaryPayloadFact{}, facts...),
			Source:          plan.Source,
			Decisions:       decisions,
			RelationSupport: plan.RelationSupport,
		},
		Metadata:      metadata,
		Entities:      append([]string{}, entities...),
		Relations:     append([]map[string]string{}, relations...),
		TimeMeta:      timeMeta,
		AllowedImages: allowedImages,
	}
}

// summaryPayloadResultMap 转成检查点写入所需字典
func summaryPayloadResultMap(payload summaryImportPayload) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// summaryPayloadFromCheckpoint 从检查点读取负载
func summaryPayloadFromCheckpoint(checkpoint map[string]any) (summaryImportPayload, error) {
	var payload summaryImportPayload
	value, exists := checkpoint["result_json"]
	if !exists || value == nil {
		return payload, fmt.Errorf("摘要检查点缺少 result_json")
	}
	raw := make([]byte, 0, 0)
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return payload, err
		}
		raw = encoded
	default:
		return payload, fmt.Errorf("摘要检查点 result_json 类型不支持: %T", value)
	}
	if len(raw) == 0 {
		return payload, fmt.Errorf("摘要检查点缺少 result_json")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

// summaryAllowedImageSet 转成集合，对齐 {tuple(value) for value in allowed_images}
func summaryAllowedImageSet(payload summaryImportPayload) map[[2]string]struct{} {
	allowed := make(map[[2]string]struct{}, len(payload.AllowedImages))
	for _, item := range payload.AllowedImages {
		allowed[item] = struct{}{}
	}
	return allowed
}

// summaryCleanReviewSummary 对齐 _clean_review_summary，按字符截断 500
func summaryCleanReviewSummary(text string) string {
	cleaned := pyStrip(strings.Join(strings.Fields(pyStr(text)), " "))
	runes := []rune(cleaned)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return cleaned
}

// summaryNow 对齐 time.time()
func summaryNow() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
