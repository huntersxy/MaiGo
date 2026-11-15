package utils

// Web 导入文本与 JSON 处理流程，对齐 web_import_manager.py 的切分、抽取与单元写入

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"MaiGo/internal/A_memorix/core/strategies"

	"golang.org/x/sync/semaphore"
)

// webImportMD5 对齐 hashlib.md5(content.encode("utf-8", errors="ignore")).hexdigest()
func webImportMD5(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// webImportTruncate 截断字符串到指定字符数
func webImportTruncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// webImportCoerceImportDataDict 对齐 _coerce_import_data_dict
func webImportCoerceImportDataDict(value any, contextName string) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	if object, ok := value.(map[string]string); ok {
		out := make(map[string]any, len(object))
		for key, item := range object {
			out[key] = item
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s 必须返回 JSON 对象，当前类型: %T", contextName, value)
}

// webImportTaskParams 复制任务参数快照
func (m *WebImportManager) webImportTaskParams(taskID string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(task.Params))
	for key, value := range task.Params {
		out[key] = value
	}
	return out
}

// webImportSetSchemaDetectedIfEmpty 仅在尚未检测 schema 时写入，对齐 Python 文本路径
func (m *WebImportManager) webImportSetSchemaDetectedIfEmpty(taskID string, schema string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task != nil && task.SchemaDetected == "" {
		task.SchemaDetected = schema
		task.UpdatedAt = m.now()
	}
}

// webImportSetSchemaDetected 写入 schema_detected
func (m *WebImportManager) webImportSetSchemaDetected(taskID string, schema string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task := m.tasks[taskID]; task != nil {
		task.SchemaDetected = schema
		task.UpdatedAt = m.now()
	}
}

// webImportChatMetadataFromParams 对齐 _chat_metadata_from_params
func webImportChatMetadataFromParams(params map[string]any) (map[string]any, error) {
	chatID := strings.TrimSpace(importValueText(params["chat_id"]))
	scopeType := strings.ToLower(strings.TrimSpace(importValueText(params["scope_type"])))
	if scopeType == "" {
		if chatID != "" {
			scopeType = "chat"
		} else {
			scopeType = "global"
		}
	}
	if scopeType == "global" && chatID == "" {
		return map[string]any{"scope_type": "global"}, nil
	}
	if scopeType == "chat" && chatID != "" {
		return map[string]any{"scope_type": "chat", "chat_id": chatID}, nil
	}
	return nil, fmt.Errorf("导入任务的 scope_type 与 chat_id 不一致")
}

// webImportStorageTypeFromStrategy 对齐 _storage_type_from_strategy
func webImportStorageTypeFromStrategy(value strategies.KnowledgeType) string {
	switch value {
	case strategies.KnowledgeTypeNarrative:
		return "narrative"
	case strategies.KnowledgeTypeFactual:
		return "factual"
	case strategies.KnowledgeTypeQuote:
		return "quote"
	default:
		return "mixed"
	}
}

// webImportSelectStrategy 对齐 select_import_strategy 的本地实现，避免反向依赖 storage
func webImportSelectStrategy(content string, override string, chatLog bool) (string, error) {
	if chatLog {
		return "narrative", nil
	}
	text := strings.ToLower(strings.TrimSpace(override))
	if text == "" || text == "auto" {
		if webImportLooksLikeQuoteText(content) {
			return "quote", nil
		}
		if webImportLooksLikeFactualText(content) {
			return "factual", nil
		}
		return "narrative", nil
	}
	for _, candidate := range webImportStrategyValues {
		if candidate == text {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("strategy_override 必须为 %s", strings.Join(webImportStrategyValues, "/"))
}

// webImportNonEmptyLines 按 Python splitlines 语义拆分并过滤空行
func webImportNonEmptyLines(content string) []string {
	raw := strings.FieldsFunc(content, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x85 || r == 0x2028 || r == 0x2029
	})
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// webImportLooksLikeQuoteText 对齐 looks_like_quote_text
func webImportLooksLikeQuoteText(content string) bool {
	lines := webImportNonEmptyLines(content)
	if len(lines) < 5 {
		return false
	}
	total := 0
	for _, line := range lines {
		total += utf8.RuneCountInString(line)
	}
	return float64(total)/float64(len(lines)) < 20
}

// webImportLooksLikeStructuredText 对齐 looks_like_structured_text
func webImportLooksLikeStructuredText(content string) bool {
	text := strings.TrimSpace(content)
	if strings.Count(text, "|") != 2 {
		return false
	}
	parts := strings.Split(text, "|")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

// webImportLooksLikeNarrativeText 对齐 looks_like_narrative_text
func webImportLooksLikeNarrativeText(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" {
		return false
	}
	markers := []string{"然后", "接着", "于是", "后来", "最后", "突然", "一天", "曾经", "有一次", "从前", "说道", "问道", "想着", "觉得"}
	score := 0
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			score++
		}
	}
	hasDialogue := strings.ContainsAny(text, "\"「『")
	head := text
	if utf8.RuneCountInString(head) > 500 {
		head = string([]rune(head)[:500])
	}
	hasChapter := strings.Contains(head, "Chapter") || strings.Contains(head, "CHAPTER") || strings.Contains(head, "###")
	return hasChapter || hasDialogue || score >= 2
}

// webImportLooksLikeFactualText 对齐 looks_like_factual_text
func webImportLooksLikeFactualText(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" || webImportLooksLikeStructuredText(text) || webImportLooksLikeQuoteText(text) {
		return false
	}
	markers := []string{"是", "有", "在", "为", "属于", "位于", "包含", "拥有", "成立于", "出生于"}
	score := 0
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			score++
		}
	}
	if score <= 0 {
		return false
	}
	if utf8.RuneCountInString(text) <= 240 {
		return true
	}
	return score >= 2 && !webImportLooksLikeNarrativeText(text)
}

// webImportInstantiateStrategy 对齐 _instantiate_strategy
func (m *WebImportManager) webImportInstantiateStrategy(filename string, strategy string, params map[string]any) strategies.BaseStrategy {
	switch strategy {
	case "factual":
		return strategies.NewFactualStrategyWithTarget(filename, webImportCoerceInt(params["factual_target_size"], 1200))
	case "quote":
		return strategies.NewQuoteStrategy(filename)
	}
	windowSize := webImportCoerceInt(params["narrative_window_size"], 1600)
	overlap := webImportCoerceInt(params["narrative_overlap"], 400)
	if webImportCoerceBool(params["chat_log"], false) {
		return strategies.NewChatLogStrategy(filename, windowSize, overlap)
	}
	return strategies.NewNarrativeStrategyWithWindow(filename, windowSize, overlap)
}

// webImportDetermineStrategy 对齐 _determine_strategy
func (m *WebImportManager) webImportDetermineStrategy(filename string, content string, override string, chatLog bool, params map[string]any) (strategies.BaseStrategy, error) {
	selected := ""
	var err error
	if m.deps.StrategySelector != nil {
		selected, err = m.deps.StrategySelector(content, override, chatLog)
	} else {
		selected, err = webImportSelectStrategy(content, override, chatLog)
	}
	if err != nil {
		return nil, err
	}
	return m.webImportInstantiateStrategy(filename, selected, params), nil
}

// processTextFile 对齐 _process_text_file
func (m *WebImportManager) processTextFile(ctx context.Context, taskID string, file *WebImportFileRecord, content string, chunkSem *semaphore.Weighted) error {
	params := m.webImportTaskParams(taskID)
	m.webImportSetSchemaDetectedIfEmpty(taskID, "plain_text")
	strategy, err := m.webImportDetermineStrategy(file.Name, content, importValueText(params["strategy_override"]), webImportCoerceBool(params["chat_log"], false), params)
	if err != nil {
		return err
	}
	strategyName := "unknown"
	switch strategy.(type) {
	case *strategies.ChatLogStrategy:
		strategyName = "chat_log"
	case *strategies.NarrativeStrategy:
		strategyName = "narrative"
	case *strategies.FactualStrategy:
		strategyName = "factual"
	case *strategies.QuoteStrategy:
		strategyName = "quote"
	}
	m.setFileStrategy(taskID, file.FileID, strategyName)
	m.setFileState(taskID, file.FileID, WebImportFileSplitting, "splitting")
	if err := m.ensureEmbeddingRuntimeReady(ctx); err != nil {
		return err
	}
	chunks := strategy.Split(content)
	if chatLog, ok := strategy.(*strategies.ChatLogStrategy); ok {
		if chatLog.SplitWarning != "" {
			m.appendFileWarning(taskID, file.FileID, chatLog.SplitWarning)
		}
		if chatLog.OversizedMessageCount > 0 {
			m.appendFileWarning(taskID, file.FileID, fmt.Sprintf("有 %d 条消息超过切块窗口，已保留为完整消息", chatLog.OversizedMessageCount))
		}
	}
	selected := chunks
	if file.RetryMode == "chunk" {
		allowed := map[int]struct{}{}
		for _, index := range file.RetryChunkIndexes {
			allowed[index] = struct{}{}
		}
		selected = make([]strategies.ProcessedChunk, 0, len(chunks))
		for _, chunk := range chunks {
			if _, ok := allowed[chunk.Chunk.Index]; ok {
				selected = append(selected, chunk)
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("失败分块重试索引无效，未匹配到可执行分块")
		}
		m.deps.Logger.Info(fmt.Sprintf("重试任务按失败分块执行: file=%s selected=%d total=%d", file.Name, len(selected), len(chunks)))
	}
	m.webImportRegisterChunks(taskID, file.FileID, selected)
	m.setFileState(taskID, file.FileID, WebImportFileExtracting, "extracting")
	var resolved *ResolvedLLMModel
	if webImportCoerceBool(params["llm_enabled"], false) {
		model, err := m.selectModel()
		if err != nil {
			return err
		}
		resolved = &model
	}
	var wg sync.WaitGroup
	errs := make([]error, len(selected))
	for index, chunk := range selected {
		wg.Add(1)
		go func(index int, chunk strategies.ProcessedChunk) {
			defer wg.Done()
			errs[index] = m.processTextChunk(ctx, taskID, file, chunk, strategy, webImportCoerceBool(params["llm_enabled"], false), resolved, chunkSem, webImportCoerceBool(params["chat_log"], false), strings.TrimSpace(importValueText(params["chat_reference_time"])), webImportChatMetadataOrNil(params))
		}(index, chunk)
	}
	wg.Wait()
	for index, chunkErr := range errs {
		if chunkErr != nil {
			m.setChunkFailed(taskID, file.FileID, selected[index].Chunk.ChunkID, fmt.Sprintf("分块处理失败: %v", chunkErr))
		}
	}
	if m.isCancelRequested(taskID) {
		m.setFileCancelled(taskID, file.FileID, "任务已取消")
		return nil
	}
	m.setFileState(taskID, file.FileID, WebImportFileSaving, "saving")
	m.storageMu.Lock()
	m.saveRuntimeStoresLocked()
	m.storageMu.Unlock()
	return m.webImportFinalizeFile(taskID, file.FileID)
}

// webImportChatMetadataOrNil 计算段落公共元数据
func webImportChatMetadataOrNil(params map[string]any) map[string]any {
	metadata, err := webImportChatMetadataFromParams(params)
	if err != nil {
		return nil
	}
	return metadata
}

// processTextChunk 对齐 _process_text_chunk
func (m *WebImportManager) processTextChunk(ctx context.Context, taskID string, file *WebImportFileRecord, chunk strategies.ProcessedChunk, strategy strategies.BaseStrategy, llmEnabled bool, resolved *ResolvedLLMModel, chunkSem *semaphore.Weighted, chatLog bool, chatReferenceTime string, paragraphMetadata map[string]any) error {
	// 对齐原 channel 信号量：只限制并发度，不响应 ctx 取消。
	_ = chunkSem.Acquire(context.Background(), 1)
	defer chunkSem.Release(1)
	chunkID := chunk.Chunk.ChunkID
	if m.isCancelRequested(taskID) {
		m.setChunkCancelled(taskID, file.FileID, chunkID, "任务已取消")
		return nil
	}
	m.setChunkState(taskID, file.FileID, chunkID, WebImportChunkExtracting, "extracting", 0.25)
	processed := chunk
	currentStrategy := strategy
	if chunk.Type != strategies.KnowledgeTypeQuote && webImportLooksLikeQuoteText(chunk.Chunk.Text) {
		processed.Type = strategies.KnowledgeTypeQuote
		processed.Flags.Verbatim = true
		processed.Flags.RequiresLLM = false
		currentStrategy = strategies.NewQuoteStrategy(file.Name)
	}
	llmFunc := func(ctx context.Context, prompt string) (map[string]any, error) {
		if resolved == nil {
			return nil, fmt.Errorf("没有可用 LLM 模型")
		}
		return m.llmCall(ctx, prompt, *resolved)
	}
	var err error
	if llmEnabled && processed.Flags.RequiresLLM {
		if resolved == nil {
			m.setChunkFailed(taskID, file.FileID, chunkID, "抽取失败: 没有可用 LLM 模型")
			return nil
		}
		processed, err = currentStrategy.Extract(ctx, processed, llmFunc)
	} else if processed.Type == strategies.KnowledgeTypeQuote {
		processed, err = currentStrategy.Extract(ctx, processed, llmFunc)
	}
	if err != nil {
		m.setChunkFailed(taskID, file.FileID, chunkID, fmt.Sprintf("抽取失败: %v", err))
		return nil
	}
	if m.isCancelRequested(taskID) {
		m.setChunkCancelled(taskID, file.FileID, chunkID, "任务已取消")
		return nil
	}
	m.setChunkState(taskID, file.FileID, chunkID, WebImportChunkWriting, "writing", 0.7)
	var timeMeta map[string]any
	if chatLog && llmEnabled && resolved != nil {
		timeMeta, _ = m.extractChatTimeMetaWithLLM(ctx, processed.Chunk.Text, *resolved, chatReferenceTime)
	}
	if err := m.persistProcessedChunk(ctx, file, processed, paragraphMetadata, timeMeta); err != nil {
		m.setChunkFailed(taskID, file.FileID, chunkID, fmt.Sprintf("写入失败: %v", err))
		return nil
	}
	m.setChunkCompleted(taskID, file.FileID, chunkID)
	return nil
}

// webImportRegisterChunks 对齐 _register_chunks
func (m *WebImportManager) webImportRegisterChunks(taskID string, fileID string, chunks []strategies.ProcessedChunk) {
	records := make([]*WebImportChunkRecord, 0, len(chunks))
	for index, chunk := range chunks {
		record := NewWebImportChunkRecord(chunk.Chunk.ChunkID, index, chunk.Type.Value(), m.now())
		record.ContentPreview = webImportTruncate(chunk.Chunk.Text, 120)
		records = append(records, record)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	file.Chunks = records
	file.TotalChunks = len(records)
	file.DoneChunks = 0
	file.FailedChunks = 0
	file.CancelledChunks = 0
	if len(records) == 0 {
		file.Progress = 1
	} else {
		file.Progress = 0
	}
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// webImportFinalizeFile 收敛文件状态
func (m *WebImportManager) webImportFinalizeFile(taskID string, fileID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return nil
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return nil
	}
	switch {
	case file.FailedChunks > 0:
		file.Status = WebImportFileFailed
		file.CurrentStep = "failed"
		if file.Error == "" {
			file.Error = fmt.Sprintf("存在失败分块: %d", file.FailedChunks)
		}
	case task.Status == WebImportTaskCancelRequested:
		file.Status = WebImportFileCancelled
		file.CurrentStep = "cancelled"
	default:
		file.Status = WebImportFileCompleted
		file.CurrentStep = "completed"
		file.Progress = 1
	}
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
	return nil
}
