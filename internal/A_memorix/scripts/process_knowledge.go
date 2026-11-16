package scripts

// 知识导入主流程，对齐 src/A_memorix/scripts/process_knowledge.py
// 复用 AutoImporter 的存储初始化与 importToDB，新增策略分块、LLM 抽取与批处理

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/strategies"
	"MaiGo/internal/A_memorix/core/utils"

	"golang.org/x/sync/semaphore"
)

// RunProcessKnowledge 执行 process_knowledge.py 等价流程，返回进程退出码
func RunProcessKnowledge(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "process_knowledge.py", "A_Memorix Knowledge Importer (Strategy-Aware)")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "A_Memorix 数据目录")
	parser.AddBool([]string{"--force"}, "Force re-import")
	parser.AddBool([]string{"--clear-manifest"}, "Clear manifest")
	parser.AddString([]string{"--type", "-t"}, "auto", "Target import strategy override (auto/narrative/factual/quote)")
	parser.AddInt([]string{"--concurrency", "-c"}, 5, "")
	parser.AddBool([]string{"--chat-log"}, "聊天记录导入模式：强制 narrative 策略，并使用 LLM 语义抽取 event_time/event_time_range")
	parser.AddString([]string{"--chat-reference-time"}, "", "chat_log 模式的相对时间参考点（如 2026/02/12 10:30）；不传则使用当前本地时间")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}

	logger := newScriptLogger(ctx, "A_Memorix.AutoImport")
	importer, err := NewAutoImporter(AutoImporterOptions{
		Force:             args.Bool("force"),
		ClearManifest:     args.Bool("clear-manifest"),
		TargetType:        args.String("type"),
		Concurrency:       args.Int("concurrency"),
		ChatLog:           args.Bool("chat-log"),
		ChatReferenceTime: args.String("chat-reference-time"),
		DataDir:           args.String("data-dir"),
		Logger:            logger,
	})
	if err != nil {
		scriptPrintf(ctx, "❌ 无法初始化导入器: %v\n", err)
		return 1
	}
	defer func() { _ = importer.Close() }()

	if !importer.Initialize(ctx) {
		return 0
	}
	referenceTime := parseChatReferenceTime(importer.chatReferenceTime, logger)
	importer.processAndImport(ctx, referenceTime)
	return 0
}

// parseChatReferenceTime 对齐 _parse_reference_time
func parseChatReferenceTime(raw string, logger utils.Logger) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now()
	}
	layouts := []string{
		"2006/01/02 15:04:05",
		"2006/01/02 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006/01/02",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed
		}
	}
	logger.Warning(fmt.Sprintf("无法解析 chat_reference_time=%s，将回退为当前本地时间", raw))
	return time.Now()
}

// processAndImport 对齐 process_and_import
func (a *AutoImporter) processAndImport(ctx context.Context, referenceTime time.Time) {
	files := listRawTextFiles(a.rawDir)
	a.logger.Info(fmt.Sprintf("扫描到 %d 个文件 in %s", len(files), a.rawDir))
	if len(files) == 0 {
		return
	}

	concurrency := max(1, a.concurrencyLimit)
	sem := semaphore.NewWeighted(int64(concurrency))
	results := make([]bool, len(files))
	var waitGroup sync.WaitGroup
	for index, path := range files {
		if ctx.Err() != nil {
			break
		}
		waitGroup.Add(1)
		// 对齐原 channel 信号量：只限制并发度，不响应 ctx 取消。
		_ = sem.Acquire(context.Background(), 1)
		go func(index int, path string) {
			defer waitGroup.Done()
			defer sem.Release(1)
			results[index] = a.processSingleFile(ctx, path, referenceTime)
		}(index, path)
	}
	waitGroup.Wait()

	success := 0
	for _, ok := range results {
		if ok {
			success++
		}
	}
	a.logger.Info(fmt.Sprintf("本次主处理完成，共成功处理 %d/%d 个文件", success, len(files)))
	if a.vectorStore != nil {
		if err := a.vectorStore.Save("", nil); err != nil {
			a.logger.Error(fmt.Sprintf("向量保存失败: %v", err))
		}
	}
	if a.graphStore != nil {
		if err := a.graphStore.Save(""); err != nil {
			a.logger.Error(fmt.Sprintf("图保存失败: %v", err))
		}
	}
}

// listRawTextFiles 返回 raw 目录下的 *.txt，按名称排序
func listRawTextFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		out = append(out, filepath.Join(dir, entry.Name()))
	}
	return out
}

// processSingleFile 对齐 _process_single_file
func (a *AutoImporter) processSingleFile(ctx context.Context, path string, referenceTime time.Time) bool {
	filename := filepath.Base(path)
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	content := string(contentBytes)
	fileHash := a.GetFileHash(content)
	if !a.force {
		if record, ok := a.manifest[filename].(map[string]any); ok {
			if pyString(record["hash"]) == fileHash && isTruthy(record["imported"]) {
				a.logger.Info(fmt.Sprintf("跳过已导入文件: %s", filename))
				return false
			}
		}
	}

	a.logger.Info(fmt.Sprintf(">>> 开始处理: %s", filename))
	strategy, err := a.determineStrategy(filename, content)
	if err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	a.logger.Info(fmt.Sprintf("  策略: %s", strategyClassName(strategy)))

	chunks := strategy.Split(content)
	a.logger.Info(fmt.Sprintf("  初步分块: %d", len(chunks)))
	processedData := map[string]any{
		"paragraphs": []any{},
		"entities":   []any{},
		"relations":  []any{},
	}

	gateway, err := newScriptLLMGateway(ctx, a.pluginConfig, a.logger)
	if err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}

	for index, chunk := range chunks {
		currentStrategy := strategy
		if rescue := a.chunkRescue(chunk, filename); rescue != nil {
			chunk.Type = strategies.KnowledgeTypeQuote
			chunk.Flags.Verbatim = true
			chunk.Flags.RequiresLLM = false
			currentStrategy = rescue
		}

		var resultChunk strategies.ProcessedChunk
		if chunk.Flags.RequiresLLM {
			resultChunk, err = currentStrategy.Extract(ctx, chunk, gateway.completion)
		} else {
			resultChunk, err = currentStrategy.Extract(ctx, chunk, nil)
		}
		if err != nil {
			a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
			return false
		}

		var timeMeta map[string]any
		if a.chatLog {
			timeMeta = a.extractChatTimeMeta(ctx, resultChunk.Chunk.Text, gateway, referenceTime)
		}
		a.normalizeAndAggregate(resultChunk, processedData, timeMeta)
		a.logger.Info(fmt.Sprintf("  已处理块 %d/%d", index+1, len(chunks)))
	}

	jsonPath := filepath.Join(a.processedDir, strings.TrimSuffix(filename, filepath.Ext(filename))+".json")
	raw, err := MarshalJSONIndent(processedData)
	if err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	if err := os.WriteFile(jsonPath, []byte(raw), 0o644); err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}

	a.storageLock.Lock()
	defer a.storageLock.Unlock()
	if err := a.importToDB(ctx, processedData, nil); err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	a.manifest[filename] = map[string]any{"hash": fileHash, "timestamp": float64(time.Now().UnixNano()) / 1e9, "imported": true}
	if err := a.saveManifest(); err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	if err := a.vectorStore.Save("", nil); err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	if err := a.graphStore.Save(""); err != nil {
		a.logger.Error(fmt.Sprintf("❌ 处理失败 %s: %v", filename, err))
		return false
	}
	a.logger.Info(fmt.Sprintf("✅ 文件 %s 处理并导入完成", filename))
	return true
}

// determineStrategy 对齐 _determine_strategy
func (a *AutoImporter) determineStrategy(filename, content string) (strategies.BaseStrategy, error) {
	strategy, err := storage.SelectImportStrategy(content, a.targetType, a.chatLog)
	if err != nil {
		return nil, err
	}
	if a.chatLog {
		a.logger.Info(fmt.Sprintf("chat_log 模式: %s 强制使用 NarrativeStrategy", filename))
	} else if strategy == storage.ImportQuote {
		a.logger.Info(fmt.Sprintf("Auto-detected Quote/Lyric type for %s", filename))
	}
	switch strategy {
	case storage.ImportFactual:
		return strategies.NewFactualStrategy(filename), nil
	case storage.ImportQuote:
		return strategies.NewQuoteStrategy(filename), nil
	default:
		return strategies.NewNarrativeStrategy(filename), nil
	}
}

// strategyClassName 对齐 type(strategy).__name__
func strategyClassName(strategy strategies.BaseStrategy) string {
	switch strategy.(type) {
	case *strategies.FactualStrategy:
		return "FactualStrategy"
	case *strategies.QuoteStrategy:
		return "QuoteStrategy"
	default:
		return "NarrativeStrategy"
	}
}

// chunkRescue 对齐 _chunk_rescue
func (a *AutoImporter) chunkRescue(chunk strategies.ProcessedChunk, filename string) strategies.BaseStrategy {
	if chunk.Type == strategies.KnowledgeTypeQuote {
		return nil
	}
	if !storage.LooksLikeQuoteText(chunk.Chunk.Text) {
		return nil
	}
	a.logger.Info(fmt.Sprintf("  > Rescuing chunk %d as Quote", chunk.Chunk.Index))
	return strategies.NewQuoteStrategy(filename)
}

// extractChatTimeMeta 对齐 _extract_chat_time_meta_with_llm
func (a *AutoImporter) extractChatTimeMeta(ctx context.Context, text string, gateway *scriptLLMGateway, referenceTime time.Time) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var builder strings.Builder
	builder.WriteString("You are a time extraction engine for chat logs.\n")
	builder.WriteString("Extract temporal information from the following chat paragraph.\n\n")
	builder.WriteString("Rules:\n")
	builder.WriteString("1. Use semantic understanding, not regex matching.\n")
	builder.WriteString("2. Convert relative expressions (e.g., yesterday evening, last Friday morning) to absolute local datetime using reference_now.\n")
	builder.WriteString("3. If a time span exists, return event_time_start/event_time_end.\n")
	builder.WriteString("4. If only one point in time exists, return event_time.\n")
	builder.WriteString("5. If no reliable time can be inferred, return all time fields as null.\n")
	builder.WriteString("6. Output ONLY valid JSON. No markdown, no explanation.\n\n")
	builder.WriteString("reference_now: " + referenceTime.Format("2006/01/02 15:04") + "\n")
	builder.WriteString("timezone: local system timezone\n\n")
	builder.WriteString("Allowed output formats for time values:\n")
	builder.WriteString("- \"YYYY/MM/DD\"\n")
	builder.WriteString("- \"YYYY/MM/DD HH:mm\"\n\n")
	builder.WriteString("JSON schema:\n")
	builder.WriteString("{\n")
	builder.WriteString("  \"event_time\": null,\n")
	builder.WriteString("  \"event_time_start\": null,\n")
	builder.WriteString("  \"event_time_end\": null,\n")
	builder.WriteString("  \"time_range\": null,\n")
	builder.WriteString("  \"time_granularity\": \"day\",\n")
	builder.WriteString("  \"time_confidence\": 0.0\n")
	builder.WriteString("}\n\n")
	builder.WriteString("Chat paragraph:\n\"\"\"" + text + "\"\"\"\n")

	// 时间字段抽取独立使用快速模型，不继承完整知识抽取的模型选择。
	result, err := gateway.completionFast(ctx, builder.String())
	if err != nil {
		a.logger.Warning(fmt.Sprintf("chat_log 时间语义抽取失败: %v", err))
		return nil
	}
	if result == nil {
		return nil
	}
	rawTimeMeta := map[string]any{
		"event_time":       result["event_time"],
		"event_time_start": result["event_time_start"],
		"event_time_end":   result["event_time_end"],
		"time_range":       result["time_range"],
		"time_granularity": result["time_granularity"],
		"time_confidence":  result["time_confidence"],
	}
	normalized, err := utils.NormalizeTimeMeta(rawTimeMeta)
	if err != nil {
		a.logger.Warning(fmt.Sprintf("chat_log 时间语义抽取结果不可用，已忽略: %v", err))
		return nil
	}
	if _, ok := normalized["event_time"]; !ok {
		if _, startOK := normalized["event_time_start"]; !startOK {
			if _, endOK := normalized["event_time_end"]; !endOK {
				return nil
			}
		}
	}
	return normalized
}

// normalizeAndAggregate 对齐 _normalize_and_aggregate
func (a *AutoImporter) normalizeAndAggregate(chunk strategies.ProcessedChunk, allData map[string]any, timeMeta map[string]any) {
	knowledgeType, err := resolveScriptKnowledgeType(string(chunk.Type), chunk.Chunk.Text)
	if err != nil {
		knowledgeType = string(storage.KnowledgeNarrative)
	}
	paragraph := map[string]any{
		"content":        chunk.Chunk.Text,
		"source":         chunk.Source.File,
		"knowledge_type": knowledgeType,
		"entities":       []any{},
		"relations":      []any{},
	}
	entities := make([]any, 0)
	relations := make([]any, 0)
	data := chunk.Data

	if triples, ok := data["triples"].([]any); ok {
		for _, item := range triples {
			triple, ok := item.(map[string]any)
			if !ok {
				continue
			}
			relations = append(relations, map[string]any{
				"subject": triple["subject"], "predicate": triple["predicate"], "object": triple["object"],
			})
			entities = append(entities, triple["subject"], triple["object"])
		}
	}
	if events, ok := data["events"].([]any); ok {
		entities = append(entities, events...)
	}
	if rawRelations, ok := data["relations"].([]any); ok {
		for _, item := range rawRelations {
			relation, ok := item.(map[string]any)
			if !ok {
				continue
			}
			relations = append(relations, relation)
			entities = append(entities, relation["subject"], relation["object"])
		}
	}
	if verbatim, ok := data["verbatim_entities"].([]any); ok {
		entities = append(entities, verbatim...)
	}

	deduped := make([]any, 0, len(entities))
	seen := make(map[string]struct{}, len(entities))
	for _, entity := range entities {
		text := pyString(entity)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		deduped = append(deduped, text)
	}
	paragraph["entities"] = deduped
	paragraph["relations"] = relations
	if len(timeMeta) > 0 {
		paragraph["time_meta"] = timeMeta
	}

	appendToAnySlice(allData, "paragraphs", paragraph)
	for _, entity := range deduped {
		appendToAnySlice(allData, "entities", entity)
	}
	for _, relation := range relations {
		appendToAnySlice(allData, "relations", relation)
	}
}

// appendToAnySlice 向 map 中的 []any 追加元素
func appendToAnySlice(data map[string]any, key string, value any) {
	items, _ := data[key].([]any)
	data[key] = append(items, value)
}
