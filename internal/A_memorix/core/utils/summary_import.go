package utils

// A_memorix 聊天摘要导入主流程，对齐 core/utils/summary_importer.py 的 import_from_stream

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"unicode/utf8"
)

// importFromStreamScoped 对齐 _import_from_stream_scoped
func (i *SummaryImporter) ImportFromStream(ctx context.Context, streamID string, params SummaryImportParams) SummaryImportResult {
	stream := pyStrip(streamID)
	if stream == "" {
		return SummaryImportResult{Success: false, Detail: "stream_id 不能为空"}
	}
	metadata := summaryMetadata(params.Metadata)
	externalID := pyStrip(importValueText(metadata["external_id"]))
	streamLock := i.lockFor("stream:" + stream)
	streamLock.Lock()
	defer streamLock.Unlock()
	if externalID != "" {
		externalLock := i.lockFor("external:" + externalID)
		externalLock.Lock()
		defer externalLock.Unlock()
	}
	if i.store == nil {
		return i.failed(fmt.Errorf("摘要存储未初始化"))
	}
	release, err := AcquireFactScopeLock(ctx, i.store, "chat", stream)
	if err != nil {
		return i.failed(err)
	}
	defer release()
	result, err := i.importFromStreamScoped(ctx, stream, params, metadata)
	if err != nil {
		return i.failed(err)
	}
	return result
}

// failed 对齐异常日志与错误文案
func (i *SummaryImporter) failed(err error) SummaryImportResult {
	i.logger.Error(fmt.Sprintf("总结导入过程中出错: %v\n%s", err, debug.Stack()))
	return SummaryImportResult{Success: false, Detail: "错误: " + err.Error()}
}

// importFromStreamScopedInner 对齐 _import_from_stream_scoped 主流程
func (i *SummaryImporter) importFromStreamScoped(ctx context.Context, stream string, params SummaryImportParams, metadata map[string]any) (SummaryImportResult, error) {
	externalID := pyStrip(importValueText(metadata["external_id"]))
	existing, err := i.existingSummaryResult(stream, metadata)
	if err != nil {
		return SummaryImportResult{}, err
	}
	if existing != nil {
		return *existing, nil
	}
	if externalID != "" {
		checkpoint, err := i.store.GetSummaryCheckpoint(externalID)
		if err != nil {
			return SummaryImportResult{}, err
		}
		if checkpoint != nil && pyStrip(pyStr(checkpoint["phase"])) == "pending" {
			payload, err := summaryPayloadFromCheckpoint(checkpoint)
			if err != nil {
				return SummaryImportResult{}, err
			}
			return i.finishFactSummary(ctx, payload)
		}
	}
	selfCheckOK, selfCheckMessage := i.ensureRuntimeSelfCheck(ctx)
	if !selfCheckOK {
		return SummaryImportResult{Success: false, Detail: "导入前自检失败: " + selfCheckMessage}, nil
	}
	contextLength := 50
	if params.ContextLength != nil {
		contextLength = *params.ContextLength
	} else if raw, exists := summaryNestedMap(i.pluginConfig, "summarization")["context_length"]; exists && isTruthy(raw) {
		if parsed, err := pyFloat64(raw); err == nil {
			contextLength = int(parsed)
		}
	}
	includePersonality := true
	if params.IncludePersonality != nil {
		includePersonality = *params.IncludePersonality
	} else {
		includePersonality = isTruthy(summaryConfigValue(i.pluginConfig, true, "summarization", "include_personality"))
	}
	queryTimeEnd := summaryNow()
	if params.TimeEnd != nil {
		queryTimeEnd = *params.TimeEnd
	}
	if i.messages == nil {
		return SummaryImportResult{}, fmt.Errorf("消息来源未初始化")
	}
	messages, err := i.messages.GetMessagesByTimeInChat(ctx, stream, 0, queryTimeEnd, contextLength, "latest")
	if err != nil {
		return SummaryImportResult{}, err
	}
	if len(messages) == 0 {
		return SummaryImportResult{Success: false, Detail: "未找到有效的聊天记录进行总结"}, nil
	}
	if i.identityStore == nil || i.identityHost == nil {
		return SummaryImportResult{}, fmt.Errorf("摘要身份依赖未初始化")
	}
	identityContext := NewSummaryIdentityContext(i.identityStore, i.identityHost, messages)
	chatHistory, identityCatalog, err := identityContext.Render()
	if err != nil {
		return SummaryImportResult{}, err
	}
	imageCatalog, allowedImages, err := i.imageEvidenceCatalog(messages, stream)
	if err != nil {
		return SummaryImportResult{}, err
	}
	reviewCount := i.summaryReviewCount(metadata)
	previousContext := i.buildPreviousSummaryContext(stream, reviewCount)
	personalityContext := ""
	if includePersonality && pyStrip(i.personality) != "" {
		personalityContext = "你的性格设定是：" + i.personality
	}
	if i.prompt == nil {
		return SummaryImportResult{}, fmt.Errorf("提示词提供者未初始化")
	}
	template, err := i.prompt.Template(SummaryPromptName)
	if err != nil {
		return SummaryImportResult{}, err
	}
	if !strings.Contains(template, "{identity_catalog}") {
		return SummaryImportResult{}, fmt.Errorf("聊天摘要模板缺少 {identity_catalog}，请更新模板以使用程序确认的身份映射")
	}
	prompt, err := i.prompt.Render(SummaryPromptName, map[string]any{
		"bot_name":                 i.botName,
		"personality_context":      personalityContext,
		"previous_summary_context": previousContext,
		"chat_history":             chatHistory,
		"image_evidence_catalog":   imageCatalog,
		"identity_catalog":         identityCatalog,
	})
	if err != nil {
		return SummaryImportResult{}, err
	}
	taskName, taskConfig, found, err := i.resolveSummaryModelTask()
	if err != nil {
		return SummaryImportResult{}, err
	}
	if !found {
		return SummaryImportResult{Success: false, Detail: "未找到可用的总结模型配置"}, nil
	}
	i.logger.Info(fmt.Sprintf("正在为流 %s 执行总结，消息条数: %d|总结模型任务: %s|候选列表: %v", stream, len(messages), taskName, taskConfig.ModelList))
	if i.llm == nil {
		return SummaryImportResult{}, fmt.Errorf("LLM 未初始化")
	}
	temperature := taskConfig.Temperature
	maxTokens := taskConfig.MaxTokens
	response, err := i.llm.Generate(ctx, TextGenerationRequest{
		TaskName:    taskName,
		RequestType: "A_Memorix.ChatSummarization",
		Prompt:      prompt,
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		return SummaryImportResult{}, err
	}
	if !response.Success || pyStrip(response.Completion) == "" {
		return SummaryImportResult{Success: false, Detail: "LLM 生成总结失败"}, nil
	}
	data := i.parseLLMResponse(response.Completion)
	summaryRaw, hasSummary := data["summary"]
	if len(data) == 0 || !hasSummary {
		return SummaryImportResult{Success: false, Detail: "解析 LLM 响应失败或总结为空"}, nil
	}
	summaryText, ok := summaryRaw.(string)
	if !ok {
		return SummaryImportResult{Success: false, Detail: "增量总结 summary 必须是字符串"}, nil
	}
	entities := normalizeSummaryEntityItems(data["entities"])
	relations := normalizeSummaryRelationItems(data["relations"])
	facts := normalizeSummaryFacts(data["facts"])
	stripped := pyStrip(summaryText)
	triggerCount := summaryIntValue(metadata["trigger_message_count"], 0)
	if stripped == "" {
		if !summaryPyEmptyList(data["entities"]) || !summaryPyEmptyList(data["relations"]) || len(facts) > 0 {
			return SummaryImportResult{Success: false, Detail: "空增量必须同时返回空实体、空关系和空事实数组"}, nil
		}
		if externalID != "" {
			if err := i.store.RecordSummaryCheckpoint(externalID, stream, triggerCount, "complete", nil); err != nil {
				return SummaryImportResult{}, err
			}
		}
		return SummaryImportResult{
			Success: true,
			Detail:  "当前窗口没有新增长期记忆，跳过段落写入",
			Source:  "chat_summary:" + stream,
			Skipped: true,
		}, nil
	}
	texts := make([]string, 0, 1+len(entities)+len(relations)*3+len(facts))
	texts = append(texts, stripped)
	texts = append(texts, entities...)
	for _, relation := range relations {
		texts = append(texts, relation["subject"], relation["predicate"], relation["object"])
	}
	for _, fact := range facts {
		texts = append(texts, fact.Text)
	}
	identityMetadata, err := identityContext.Metadata(texts)
	if err != nil {
		return SummaryImportResult{}, err
	}
	restoredSummary, err := identityContext.RestoreText(stripped)
	if err != nil {
		return SummaryImportResult{}, err
	}
	for index := range entities {
		entities[index], err = identityContext.RestoreText(entities[index])
		if err != nil {
			return SummaryImportResult{}, err
		}
	}
	for index := range relations {
		restoredRelation := make(map[string]string, len(relations[index]))
		for key, value := range relations[index] {
			restored, err := identityContext.RestoreText(value)
			if err != nil {
				return SummaryImportResult{}, err
			}
			restoredRelation[key] = restored
		}
		relations[index] = restoredRelation
	}
	for index := range facts {
		personMetadata, err := identityContext.Metadata([]string{facts[index].Text})
		if err != nil {
			return SummaryImportResult{}, err
		}
		facts[index].PersonIDs = summaryStringList(personMetadata["person_ids"])
		facts[index].Text, err = identityContext.RestoreText(facts[index].Text)
		if err != nil {
			return SummaryImportResult{}, err
		}
	}
	metadata = summaryMapFromItems(metadata, identityMetadata)
	msgTimes := make([]float64, 0, len(messages))
	messageIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		messageIDs = append(messageIDs, message.MessageID)
		if timestamp, ok := messageTimestamp(message); ok {
			msgTimes = append(msgTimes, timestamp)
		}
	}
	timeMeta := map[string]any{}
	var observedAt any
	if len(msgTimes) > 0 {
		minTime, maxTime := msgTimes[0], msgTimes[0]
		for _, value := range msgTimes[1:] {
			if value < minTime {
				minTime = value
			}
			if value > maxTime {
				maxTime = value
			}
		}
		timeMeta = map[string]any{
			"event_time_start": minTime,
			"event_time_end":   maxTime,
			"time_granularity": "minute",
			"time_confidence":  0.95,
		}
		observedAt = maxTime
	}
	for _, fact := range facts {
		for _, evidence := range fact.ImageEvidence {
			if _, ok := allowedImages[[2]string{evidence.MessageID, evidence.ComponentPath}]; !ok {
				return SummaryImportResult{}, fmt.Errorf("总结模型返回了窗口外图片证据: %s:%s", evidence.MessageID, evidence.ComponentPath)
			}
		}
	}
	if len(facts) > 0 {
		source := map[string]any{
			"platform":    pyStr(messages[0].Platform),
			"chat_id":     stream,
			"message_ids": messageIDs,
			"observed_at": observedAt,
		}
		for key, value := range timeMeta {
			source[key] = value
		}
		observationID, err := ObservationID(messages[0].Platform, stream, messageIDs, importValueText(metadata["external_id"]))
		if err != nil {
			return SummaryImportResult{}, err
		}
		source["observation_id"] = observationID
		if externalID == "" {
			externalID = "summary:" + observationID
			metadata["external_id"] = externalID
		}
		planFacts := make([]FactDedupFact, 0, len(facts))
		for _, fact := range facts {
			planFacts = append(planFacts, FactDedupFact{FactID: fact.FactID, Text: fact.Text, PersonIDs: append([]string{}, fact.PersonIDs...)})
		}
		dedup := i.newDeduplicator(taskName)
		if dedup == nil {
			return SummaryImportResult{}, fmt.Errorf("事实去重器未初始化")
		}
		plan, err := dedup.Plan(ctx, "chat", stream, planFacts, source, relations)
		if err != nil {
			return SummaryImportResult{}, err
		}
		payload := summaryPayloadFromPlan(plan, facts, metadata, entities, relations, timeMeta, allowedImages)
		resultMap, err := summaryPayloadResultMap(payload)
		if err != nil {
			return SummaryImportResult{}, err
		}
		if err := i.store.RecordSummaryCheckpoint(externalID, stream, triggerCount, "pending", resultMap); err != nil {
			return SummaryImportResult{}, err
		}
		return i.finishFactSummary(ctx, payload)
	}
	paragraphHash, err := i.executeImport(ctx, restoredSummary, entities, relations, stream, timeMeta, metadata)
	if err != nil {
		return SummaryImportResult{}, err
	}
	if _, err := i.linkSummaryImageEvidence(stream, paragraphHash, facts, allowedImages); err != nil {
		return SummaryImportResult{}, err
	}
	if err := i.persistVectorStore(i.vectorStore); err != nil {
		return SummaryImportResult{}, err
	}
	if i.graphStore != nil {
		if err := i.graphStore.Save(); err != nil {
			return SummaryImportResult{}, err
		}
	}
	if externalID != "" {
		if _, err := i.store.UpsertExternalMemoryRef(externalID, paragraphHash, "chat_summary", map[string]any{
			"chat_id":               stream,
			"person_ids":            summaryPersonIDsFromMetadata(metadata),
			"trigger_message_count": triggerCount,
		}); err != nil {
			return SummaryImportResult{}, err
		}
		if err := i.store.RecordSummaryCheckpoint(externalID, stream, triggerCount, "complete", nil); err != nil {
			return SummaryImportResult{}, err
		}
	}
	return SummaryImportResult{
		Success:       true,
		Detail:        fmt.Sprintf("总结导入成功|长度: %d|实体: %d|关系: %d", utf8.RuneCountInString(restoredSummary), len(entities), len(relations)),
		ParagraphHash: paragraphHash,
		Source:        "chat_summary:" + stream,
	}, nil
}

// summaryStringList 把任意列表转为字符串切片
func summaryStringList(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string{}, typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, pyStr(item))
		}
		return out
	default:
		return []string{}
	}
}
