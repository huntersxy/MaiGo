package utils

// A_memorix 事实去重计划阶段，对齐 Python FactDeduplicator.plan

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// factDedupRequestType 对齐 Python llm_service 的 request_type
const factDedupRequestType = "A_Memorix.FactDeduplication"

// factDedupTemporalKeys 提示词中保留的时间字段
// Python 用 set 迭代，顺序随机；这里固定顺序保证 Go 输出稳定
var factDedupTemporalKeys = []string{"observed_at", "event_time_start", "event_time_end", "valid_from", "valid_to"}

// factDedupAllowedIDs 排序后的候选别名，用于错误上下文
func factDedupAllowedIDs(allowed map[string]factDedupCandidate) []string {
	ids := make([]string, 0, len(allowed))
	for id := range allowed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var factDedupTemporalPattern = regexp.MustCompile(`(?i)今天|昨天|明天|刚刚|再次|每天|参加|跑了|买了|完成|出差|去了|\d{1,4}[-/年月日:]|today|yesterday|again|attended|ran|bought|今日|昨日|明日|参加|購入`)

var factDedupDurablePattern = regexp.MustCompile(`(?i)喜欢|偏好|爱好|职业|常住|长期|惯用|偏爱|居住|prefers?|likes?|lives?|occupation|好き|好む|職業|住んで`)

var factDedupTermPattern = regexp.MustCompile(`[a-zA-Z0-9_]+|[\x{3400}-\x{9fff}]{2}`)

var factDedupFencePattern = regexp.MustCompile("^\x60\x60\x60(?:json)?\\s*|\\s*\x60\x60\x60$")

// factDedupCandidate 内部候选事实，对齐 Python 候选字典
type factDedupCandidate struct {
	ID             string
	Text           string
	ParagraphHash  string
	ClaimID        string
	InputFactID    string
	ValidFrom      *float64
	ValidTo        *float64
	EventTimeStart *float64
	EventTimeEnd   *float64
	ObservedAt     *float64
	PersonIDs      []string
	Observations   []map[string]any
}

// Plan 生成去重计划，对齐 Python plan
func (d *FactDeduplicator) Plan(
	ctx context.Context,
	scopeType string,
	scopeID string,
	facts []FactDedupFact,
	source map[string]any,
	relations []map[string]string,
) (*FactDedupPlan, error) {
	if d == nil || d.store == nil {
		return nil, fmt.Errorf("事实去重缺少存储")
	}
	seenIDs := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		if _, exists := seenIDs[fact.FactID]; exists {
			return nil, fmt.Errorf("去重事实 ID 重复")
		}
		seenIDs[fact.FactID] = struct{}{}
	}
	paragraphIDs, err := d.scopeParagraphs(scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	decisions := make([]FactDedupDecision, 0, len(facts))
	support := make(map[string][]string)
	const batchSize = 8
	for offset := 0; offset < len(facts); offset += batchSize {
		end := offset + batchSize
		if end > len(facts) {
			end = len(facts)
		}
		batch := facts[offset:end]
		candidates := make(map[string][]factDedupCandidate, len(batch))
		fixed := make(map[string]FactDedupDecision, len(batch))
		fixedOrder := make([]FactDedupDecision, 0, len(batch))
		for index, fact := range batch {
			vectorCandidates := make([]factDedupCandidate, 0, 8)
			found, err := d.candidates(scopeType, scopeID, fact.Text, paragraphIDs)
			if err != nil {
				return nil, err
			}
			found = factDedupCompatibleCandidates(scopeType, fact, found)
			exact := factDedupFindExact(found, fact.Text)
			temporal := factDedupTemporalPattern.MatchString(fact.Text)
			durable := factDedupDurablePattern.MatchString(fact.Text)
			if exact != nil && durable && !temporal {
				target := factDedupTargetFromCandidate(*exact)
				fixed[fact.FactID] = FactDedupDecision{
					FactID: fact.FactID,
					Action: "reuse",
					Target: &target,
				}
				fixedOrder = append(fixedOrder, fixed[fact.FactID])
			} else if len(paragraphIDs) > 0 && d.embedding != nil && d.vectors != nil {
				vector, err := d.embedding.Encode(ctx, fact.Text)
				if err != nil {
					return nil, err
				}
				hits, err := d.vectors.Search(ctx, vector, 8, paragraphIDs)
				if err != nil {
					return nil, err
				}
				vectorCandidates, err = d.paragraphCandidates(hits, scopeType, scopeID)
				if err != nil {
					return nil, err
				}
			}
			earlier := facts[:offset+index]
			inputs := make([]factDedupCandidate, 0, 8)
			if len(earlier) > 8 {
				earlier = earlier[len(earlier)-8:]
			}
			for _, row := range earlier {
				personIDs := row.PersonIDs
				if personIDs == nil {
					personIDs = []string{}
				}
				inputs = append(inputs, factDedupCandidate{
					ID:          "input:" + row.FactID,
					Text:        row.Text,
					InputFactID: row.FactID,
					PersonIDs:   personIDs,
				})
			}
			normalizedFact := NormalizedFact(fact.Text)
			sort.SliceStable(inputs, func(left, right int) bool {
				leftRatio := pySequenceRatio(normalizedFact, NormalizedFact(inputs[left].Text))
				rightRatio := pySequenceRatio(normalizedFact, NormalizedFact(inputs[right].Text))
				return leftRatio > rightRatio
			})
			// 精确命中优先，其余名额由向量、关键词和同批输入轮流使用。
			// 向量候选不能追加在已满的关键词列表后，否则截断时整路召回都会丢失。
			found = factDedupInterleaveChannels(exact, vectorCandidates, found, inputs)
			found = factDedupCompatibleCandidates(scopeType, fact, found)
			found = factDedupUnique(found)
			if len(found) > 8 {
				found = found[:8]
			}
			candidates[fact.FactID] = found
			if len(found) == 0 {
				fixed[fact.FactID] = FactDedupDecision{FactID: fact.FactID, Action: "keep"}
				fixedOrder = append(fixedOrder, fixed[fact.FactID])
			}
		}
		needsModel := len(relations) > 0
		for _, fact := range batch {
			if _, ok := fixed[fact.FactID]; !ok {
				needsModel = true
				break
			}
		}
		if !needsModel {
			for _, fact := range batch {
				decisions = append(decisions, fixed[fact.FactID])
			}
			continue
		}
		aliases := make(map[string]string)
		for _, fact := range batch {
			for _, row := range candidates[fact.FactID] {
				if _, exists := aliases[row.ID]; exists {
					continue
				}
				if strings.HasPrefix(row.ID, "input:") {
					aliases[row.ID] = row.ID
				} else {
					aliases[row.ID] = fmt.Sprintf("c%d", len(aliases)+1)
				}
			}
		}
		factsPayload := make([]any, 0, len(batch))
		for _, fact := range batch {
			factsPayload = append(factsPayload, map[string]any{"fact_id": fact.FactID, "text": fact.Text})
		}
		modelCandidates := make(map[string]any, len(candidates))
		for _, fact := range batch {
			rows := candidates[fact.FactID]
			models := make([]any, 0, len(rows))
			for _, row := range rows {
				models = append(models, factDedupModelCandidate(row, aliases))
			}
			modelCandidates[fact.FactID] = models
		}
		sourcePayload := make(map[string]any)
		for _, key := range factDedupTemporalKeys {
			if value, ok := source[key]; ok && value != nil {
				sourcePayload[key] = value
			}
		}
		fixedPayload := make([]any, 0, len(fixedOrder))
		for _, decision := range fixedOrder {
			item := map[string]any{"fact_id": decision.FactID, "action": decision.Action}
			if decision.Target != nil {
				alias, ok := aliases[decision.Target.ID]
				if !ok {
					return nil, fmt.Errorf("去重固定决定缺少候选别名")
				}
				item["target_id"] = alias
			}
			fixedPayload = append(fixedPayload, item)
		}
		payload := map[string]any{
			"facts":      factsPayload,
			"candidates": modelCandidates,
			"source":     sourcePayload,
			"fixed":      fixedPayload,
			"relations":  relations,
		}
		payloadJSON, err := pyJSONMarshal(payload)
		if err != nil {
			return nil, err
		}
		if d.promptBuilder == nil {
			return nil, fmt.Errorf("事实去重缺少提示词构建器")
		}
		prompt, err := d.promptBuilder(payloadJSON)
		if err != nil {
			return nil, err
		}
		if d.llm == nil {
			return nil, fmt.Errorf("事实去重缺少模型服务")
		}
		response, err := d.llm.Generate(ctx, FactDedupLLMRequest{
			TaskName:    d.taskName,
			RequestType: factDedupRequestType,
			Prompt:      prompt,
			Temperature: 0.0,
		})
		if err != nil {
			return nil, fmt.Errorf("事实去重模型调用失败: %v", err)
		}
		if !response.Success {
			return nil, fmt.Errorf("事实去重模型调用失败: %s", response.Error)
		}
		raw := strings.TrimSpace(response.Completion)
		if strings.HasPrefix(raw, "\x60\x60\x60") {
			raw = factDedupFencePattern.ReplaceAllString(raw, "")
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return nil, fmt.Errorf("解析事实去重响应失败: %w", err)
		}
		outputAny, exists := data["decisions"]
		if !exists {
			return nil, fmt.Errorf("去重响应必须逐条覆盖当前批次")
		}
		output, ok := factDedupAnyList(outputAny)
		if !ok || len(output) != len(batch) {
			return nil, fmt.Errorf("去重响应必须逐条覆盖当前批次")
		}
		byID := make(map[string]map[string]any, len(batch))
		for _, item := range output {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("去重响应包含无效或重复的事实 ID")
			}
			byID[factDedupString(row["fact_id"])] = row
		}
		if len(byID) != len(batch) {
			return nil, fmt.Errorf("去重响应包含无效或重复的事实 ID")
		}
		for _, fact := range batch {
			if _, ok := byID[fact.FactID]; !ok {
				return nil, fmt.Errorf("去重响应包含无效或重复的事实 ID")
			}
		}
		for _, fact := range batch {
			row := byID[fact.FactID]
			action := factDedupString(row["action"])
			if action != "keep" && action != "reuse" && action != "possible_duplicate" {
				return nil, fmt.Errorf("无效去重决定")
			}
			allowed := make(map[string]factDedupCandidate, len(candidates[fact.FactID]))
			for _, candidate := range candidates[fact.FactID] {
				allowed[aliases[candidate.ID]] = candidate
			}
			if decision, ok := fixed[fact.FactID]; ok {
				decisions = append(decisions, decision)
				continue
			}
			if action == "reuse" {
				targetID := factDedupString(row["target_id"])
				candidate, ok := allowed[targetID]
				if !ok {
					return nil, fmt.Errorf(
						"去重目标不属于当前候选: scope=%s:%s fact_id=%s target_id=%v allowed_ids=%v",
						scopeType, scopeID, fact.FactID, row["target_id"], factDedupAllowedIDs(allowed),
					)
				}
				target := factDedupTargetFromCandidate(candidate)
				decisions = append(decisions, FactDedupDecision{
					FactID: fact.FactID,
					Action: action,
					Target: &target,
				})
				continue
			}
			possible := []any{}
			if value, exists := row["candidate_ids"]; exists {
				parsed, ok := factDedupAnyList(value)
				if !ok {
					return nil, fmt.Errorf(
						"疑似重复候选无效: scope=%s:%s fact_id=%s candidate_ids=%v allowed_ids=%v",
						scopeType, scopeID, fact.FactID, value, factDedupAllowedIDs(allowed),
					)
				}
				possible = parsed
			}
			ids := make([]string, 0, len(possible))
			for _, token := range possible {
				candidate, ok := allowed[factDedupString(token)]
				if !ok {
					return nil, fmt.Errorf(
						"疑似重复候选无效: scope=%s:%s fact_id=%s candidate_ids=%v allowed_ids=%v",
						scopeType, scopeID, fact.FactID, possible, factDedupAllowedIDs(allowed),
					)
				}
				ids = append(ids, candidate.ID)
			}
			if action == "possible_duplicate" && len(possible) == 0 {
				return nil, fmt.Errorf(
					"疑似重复候选无效: scope=%s:%s fact_id=%s candidate_ids=%v allowed_ids=%v",
					scopeType, scopeID, fact.FactID, possible, factDedupAllowedIDs(allowed),
				)
			}
			decisions = append(decisions, FactDedupDecision{
				FactID:       fact.FactID,
				Action:       action,
				CandidateIDs: ids,
			})
		}
		// 对齐 Python data.get("relation_support", {})：缺键按空对象，显式 null 视为非对象报错
		rawSupport, hasSupport := data["relation_support"]
		if !hasSupport {
			rawSupport = map[string]any{}
		}
		if supportMap, ok := rawSupport.(map[string]any); ok {
			for key, valuesAny := range supportMap {
				index, err := strconv.Atoi(key)
				if err != nil || index < 0 || index >= len(relations) || strconv.Itoa(index) != key {
					return nil, fmt.Errorf("关系支持信息引用了输入以外的内容")
				}
				values, ok := factDedupAnyList(valuesAny)
				if !ok {
					return nil, fmt.Errorf("关系支持信息引用了输入以外的内容")
				}
				for _, value := range values {
					token := factDedupString(value)
					if _, ok := byID[token]; !ok {
						return nil, fmt.Errorf("关系支持信息引用了输入以外的内容")
					}
					support[key] = append(support[key], token)
				}
			}
		} else {
			return nil, fmt.Errorf("关系支持信息必须是对象")
		}
	}
	return &FactDedupPlan{
		ScopeType:       scopeType,
		ScopeID:         scopeID,
		Facts:           facts,
		Source:          source,
		Decisions:       decisions,
		RelationSupport: support,
	}, nil
}
