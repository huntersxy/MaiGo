package utils

// Web 导入参数归一化与配置读取，对齐 web_import_manager.py 的参数构造函数

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// webImportStrategyValues 合法导入策略
var webImportStrategyValues = []string{"auto", "narrative", "factual", "quote"}

// webImportParseStrategy 解析策略覆盖值
func webImportParseStrategy(value any) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(importValueText(value)))
	if raw == "" {
		return "auto", nil
	}
	for _, item := range webImportStrategyValues {
		if item == raw {
			return raw, nil
		}
	}
	return "", fmt.Errorf("strategy_override 必须为 %s", strings.Join(webImportStrategyValues, "/"))
}

// normalizeCommonImportParams 对齐 _normalize_common_import_params
func (m *WebImportManager) normalizeCommonImportParams(payload map[string]any, defaultDedupe string) (map[string]any, error) {
	inputMode := strings.ToLower(strings.TrimSpace(importValueText(payload["input_mode"])))
	if inputMode == "" {
		inputMode = "text"
	}
	if inputMode != "text" && inputMode != "json" {
		return nil, fmt.Errorf("input_mode 必须为 text 或 json")
	}
	fileConcurrency := webImportCoerceInt(payload["file_concurrency"], m.defaultFileConcurrency())
	chunkConcurrency := webImportCoerceInt(payload["chunk_concurrency"], m.defaultChunkConcurrency())
	fileConcurrency = webImportClamp(fileConcurrency, 1, m.maxFileConcurrency())
	chunkConcurrency = webImportClamp(chunkConcurrency, 1, m.maxChunkConcurrency())
	llmEnabled := webImportCoerceBool(payload["llm_enabled"], true)
	strategyOverride, err := webImportParseStrategy(payload["strategy_override"])
	if err != nil {
		return nil, err
	}
	if _, present := payload["strategy_override"]; !present {
		strategyOverride = "auto"
	}
	dedupePolicy := strings.ToLower(strings.TrimSpace(importValueText(payload["dedupe_policy"])))
	if dedupePolicy == "" {
		dedupePolicy = defaultDedupe
	}
	if dedupePolicy != "content_hash" && dedupePolicy != "manifest" && dedupePolicy != "none" {
		return nil, fmt.Errorf("dedupe_policy 必须为 content_hash/manifest/none")
	}
	chatLog := webImportCoerceBool(payload["chat_log"], false)
	chatReferenceTime := strings.TrimSpace(importValueText(payload["chat_reference_time"]))
	chatID := strings.TrimSpace(importValueText(payload["chat_id"]))
	rawScopeType := strings.ToLower(strings.TrimSpace(importValueText(payload["scope_type"])))
	scopeType := rawScopeType
	if scopeType == "" {
		if chatID != "" {
			scopeType = "chat"
		} else {
			scopeType = "global"
		}
	}
	if scopeType != "global" && scopeType != "chat" {
		return nil, fmt.Errorf("scope_type 必须为 global 或 chat")
	}
	if scopeType == "chat" && chatID == "" {
		return nil, fmt.Errorf("scope_type=chat 时必须提供 chat_id")
	}
	if scopeType == "global" && chatID != "" {
		return nil, fmt.Errorf("scope_type=global 时不能同时提供 chat_id")
	}
	force := webImportCoerceBool(payload["force"], false)
	clearManifest := webImportCoerceBool(payload["clear_manifest"], false)
	maxChunk := m.maxImportChunkChars()
	narrativeWindow := webImportClamp(webImportCoerceInt(payload["narrative_window_size"], m.defaultNarrativeWindowSize()), 200, maxChunk)
	narrativeOverlap := webImportClamp(webImportCoerceInt(payload["narrative_overlap"], m.defaultNarrativeOverlap()), 0, max(0, narrativeWindow-1))
	factualTarget := webImportClamp(webImportCoerceInt(payload["factual_target_size"], m.defaultFactualTargetSize()), 200, maxChunk)
	return map[string]any{
		"input_mode":            inputMode,
		"file_concurrency":      fileConcurrency,
		"chunk_concurrency":     chunkConcurrency,
		"llm_enabled":           llmEnabled,
		"strategy_override":     strategyOverride,
		"chat_log":              chatLog,
		"chat_reference_time":   webImportNilIfEmpty(chatReferenceTime),
		"chat_id":               chatID,
		"scope_type":            scopeType,
		"force":                 force,
		"clear_manifest":        clearManifest,
		"dedupe_policy":         dedupePolicy,
		"narrative_window_size": narrativeWindow,
		"narrative_overlap":     narrativeOverlap,
		"factual_target_size":   factualTarget,
	}, nil
}

// webImportNilIfEmpty 空串返回 None 语义
func webImportNilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// normalizeParams 对齐 _normalize_params
func (m *WebImportManager) normalizeParams(payload map[string]any) (map[string]any, error) {
	params, err := m.normalizeCommonImportParams(payload, "content_hash")
	if err != nil {
		return nil, err
	}
	params["task_kind"] = "upload"
	return params, nil
}

// normalizeRawScanParams 对齐 _normalize_raw_scan_params
func (m *WebImportManager) normalizeRawScanParams(payload map[string]any) (map[string]any, error) {
	params, err := m.normalizeCommonImportParams(payload, "manifest")
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(importValueText(payload["alias"]))
	if alias == "" {
		alias = "raw"
	}
	if alias != "raw" {
		return nil, fmt.Errorf("raw_scan 仅允许使用 raw 导入目录")
	}
	relativePath := strings.TrimSpace(importValueText(payload["relative_path"]))
	glob := strings.TrimSpace(importValueText(payload["glob"]))
	if glob == "" {
		glob = "*"
	}
	recursive := webImportCoerceBool(payload["recursive"], true)
	segments := strings.Split(strings.ReplaceAll(relativePath, "\\", "/"), "/")
	for _, segment := range segments {
		if segment == ".." {
			return nil, fmt.Errorf("relative_path 不允许包含 ..")
		}
	}
	params["task_kind"] = "raw_scan"
	params["alias"] = alias
	params["relative_path"] = relativePath
	params["glob"] = glob
	params["recursive"] = recursive
	return params, nil
}

// normalizeLpmmOpenieParams 对齐 _normalize_lpmm_openie_params
func (m *WebImportManager) normalizeLpmmOpenieParams(payload map[string]any) (map[string]any, error) {
	params, err := m.normalizeCommonImportParams(payload, "manifest")
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(importValueText(payload["alias"]))
	if alias == "" {
		alias = "lpmm"
	}
	if alias != "lpmm" {
		return nil, fmt.Errorf("lpmm_openie 仅允许使用 lpmm 导入目录")
	}
	params["task_kind"] = "lpmm_openie"
	params["alias"] = alias
	params["relative_path"] = strings.TrimSpace(importValueText(payload["relative_path"]))
	params["include_all_json"] = webImportCoerceBool(payload["include_all_json"], false)
	params["input_mode"] = "json"
	return params, nil
}

// normalizeTemporalBackfillParams 对齐 _normalize_temporal_backfill_params
func (m *WebImportManager) normalizeTemporalBackfillParams(payload map[string]any) (map[string]any, error) {
	limitValue, err := webImportParseOptionalPositiveInt(payload["limit"], "limit")
	if err != nil {
		return nil, err
	}
	limit := 100000
	if limitValue != nil {
		limit = *limitValue
	}
	return map[string]any{
		"task_kind":           "temporal_backfill",
		"dry_run":             webImportCoerceBool(payload["dry_run"], false),
		"no_created_fallback": webImportCoerceBool(payload["no_created_fallback"], false),
		"limit":               limit,
	}, nil
}

// normalizeLpmmConvertParams 对齐 _normalize_lpmm_convert_params
func (m *WebImportManager) normalizeLpmmConvertParams(payload map[string]any) (map[string]any, error) {
	alias := strings.TrimSpace(importValueText(payload["alias"]))
	if alias == "" {
		alias = "lpmm"
	}
	if alias != "lpmm" {
		return nil, fmt.Errorf("lpmm_convert 仅允许使用 lpmm 导入目录")
	}
	targetAlias := strings.TrimSpace(importValueText(payload["target_alias"]))
	if targetAlias == "" {
		targetAlias = "converted"
	}
	if targetAlias == "plugin_data" {
		targetAlias = "converted"
	}
	if targetAlias != "converted" {
		return nil, fmt.Errorf("lpmm_convert 仅允许写入 converted 导入目录")
	}
	dimensionValue, err := webImportParseOptionalPositiveInt(payload["dimension"], "dimension")
	if err != nil {
		return nil, err
	}
	dimension := webImportCoerceInt(m.cfg("embedding.dimension", 384), 384)
	if dimensionValue != nil {
		dimension = *dimensionValue
	}
	batchValue, err := webImportParseOptionalPositiveInt(payload["batch_size"], "batch_size")
	if err != nil {
		return nil, err
	}
	batchSize := 1024
	if batchValue != nil {
		batchSize = *batchValue
	}
	return map[string]any{
		"task_kind":            "lpmm_convert",
		"alias":                alias,
		"relative_path":        strings.TrimSpace(importValueText(payload["relative_path"])),
		"target_alias":         targetAlias,
		"target_relative_path": strings.TrimSpace(importValueText(payload["target_relative_path"])),
		"dimension":            dimension,
		"batch_size":           batchSize,
	}, nil
}

// normalizeByTaskKind 对齐 _normalize_by_task_kind
func (m *WebImportManager) normalizeByTaskKind(taskKind string, payload map[string]any) (map[string]any, error) {
	kind := strings.ToLower(strings.TrimSpace(taskKind))
	switch kind {
	case "upload", "paste":
		params, err := m.normalizeParams(payload)
		if err != nil {
			return nil, err
		}
		params["task_kind"] = kind
		return params, nil
	case "maibot_migration":
		return m.normalizeMigrationParams(payload)
	case "raw_scan":
		return m.normalizeRawScanParams(payload)
	case "lpmm_openie":
		return m.normalizeLpmmOpenieParams(payload)
	case "temporal_backfill":
		return m.normalizeTemporalBackfillParams(payload)
	case "lpmm_convert":
		return m.normalizeLpmmConvertParams(payload)
	default:
		return m.normalizeParams(payload)
	}
}

// normalizeMigrationParams 对齐 _normalize_migration_params
func (m *WebImportManager) normalizeMigrationParams(payload map[string]any) (map[string]any, error) {
	sourceDB, err := m.resolveMaibotSourceDB(strings.TrimSpace(importValueText(payload["source_db"])))
	if err != nil {
		return nil, err
	}
	timeFrom := strings.TrimSpace(importValueText(payload["time_from"]))
	timeTo := strings.TrimSpace(importValueText(payload["time_to"]))
	startID, err := webImportParseOptionalPositiveInt(payload["start_id"], "start_id")
	if err != nil {
		return nil, err
	}
	endID, err := webImportParseOptionalPositiveInt(payload["end_id"], "end_id")
	if err != nil {
		return nil, err
	}
	if startID != nil && endID != nil && *startID > *endID {
		return nil, fmt.Errorf("start_id 不能大于 end_id")
	}
	readBatchSize := 2000
	if value, err := webImportParseOptionalPositiveInt(payload["read_batch_size"], "read_batch_size"); err != nil {
		return nil, err
	} else if value != nil {
		readBatchSize = *value
	}
	commitWindowRows := 20000
	if value, err := webImportParseOptionalPositiveInt(payload["commit_window_rows"], "commit_window_rows"); err != nil {
		return nil, err
	} else if value != nil {
		commitWindowRows = *value
	}
	embedBatchSize := 256
	if value, err := webImportParseOptionalPositiveInt(payload["embed_batch_size"], "embed_batch_size"); err != nil {
		return nil, err
	} else if value != nil {
		embedBatchSize = *value
	}
	entityEmbedBatchSize := 512
	if value, err := webImportParseOptionalPositiveInt(payload["entity_embed_batch_size"], "entity_embed_batch_size"); err != nil {
		return nil, err
	} else if value != nil {
		entityEmbedBatchSize = *value
	}
	var embedWorkers any
	if value, err := webImportParseOptionalPositiveInt(payload["embed_workers"], "embed_workers"); err != nil {
		return nil, err
	} else if value != nil {
		embedWorkers = *value
	}
	maxErrors := 0
	if value, err := webImportParseOptionalNonNegativeInt(payload["max_errors"], "max_errors"); err != nil {
		return nil, err
	} else if value != nil {
		maxErrors = *value
	}
	logEvery := 5000
	if value, err := webImportParseOptionalPositiveInt(payload["log_every"], "log_every"); err != nil {
		return nil, err
	} else if value != nil {
		logEvery = *value
	}
	previewLimit := 20
	if value, err := webImportParseOptionalPositiveInt(payload["preview_limit"], "preview_limit"); err != nil {
		return nil, err
	} else if value != nil {
		previewLimit = *value
	}
	return map[string]any{
		"task_kind":               "maibot_migration",
		"source_db":               sourceDB,
		"target_data_dir":         m.resolveDataDir(),
		"time_from":               webImportNilIfEmpty(timeFrom),
		"time_to":                 webImportNilIfEmpty(timeTo),
		"stream_ids":              webImportCoerceList(payload["stream_ids"]),
		"group_ids":               webImportCoerceList(payload["group_ids"]),
		"user_ids":                webImportCoerceList(payload["user_ids"]),
		"start_id":                webImportOptionalIntValue(startID),
		"end_id":                  webImportOptionalIntValue(endID),
		"read_batch_size":         readBatchSize,
		"commit_window_rows":      commitWindowRows,
		"embed_batch_size":        embedBatchSize,
		"entity_embed_batch_size": entityEmbedBatchSize,
		"embed_workers":           embedWorkers,
		"max_errors":              maxErrors,
		"log_every":               logEvery,
		"preview_limit":           previewLimit,
		"no_resume":               webImportCoerceBool(payload["no_resume"], false),
		"reset_state":             webImportCoerceBool(payload["reset_state"], false),
		"dry_run":                 webImportCoerceBool(payload["dry_run"], false),
		"verify_only":             webImportCoerceBool(payload["verify_only"], false),
	}, nil
}

// webImportOptionalIntValue 可选整数转 any
func webImportOptionalIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// resolveMaibotSourceDB 对齐 _resolve_maibot_source_db，仅允许默认库或 maibot 导入目录
func (m *WebImportManager) resolveMaibotSourceDB(raw string) (string, error) {
	defaultSource, err := filepath.Abs(m.defaultMaibotSourceDB())
	if err != nil {
		return "", err
	}
	defaultSource = filepath.Clean(defaultSource)
	text := webImportExpandUser(strings.TrimSpace(raw))
	if text == "" {
		return defaultSource, nil
	}
	maibotRoot, err := filepath.Abs(filepath.Join(m.importRoot, "source", "maibot"))
	if err != nil {
		return "", err
	}
	maibotRoot = filepath.Clean(maibotRoot)
	resolved := ""
	if filepath.IsAbs(text) {
		resolved = filepath.Clean(text)
	} else {
		repoCandidate, err := filepath.Abs(filepath.Join(m.deps.RepoRoot, text))
		if err != nil {
			return "", err
		}
		repoCandidate = filepath.Clean(repoCandidate)
		if repoCandidate == defaultSource {
			return defaultSource, nil
		}
		if webImportPathWithin(maibotRoot, repoCandidate) {
			resolved = repoCandidate
		} else {
			aliased, err := m.ResolvePathAlias("maibot", text, false)
			if err != nil {
				return "", err
			}
			resolved = aliased
		}
	}
	if resolved == defaultSource {
		return defaultSource, nil
	}
	if !webImportPathWithin(maibotRoot, resolved) {
		return "", fmt.Errorf("自定义 MaiBot 数据库必须位于导入目录: %s", maibotRoot)
	}
	return resolved, nil
}

// webImportExpandUser 对齐 Path.expanduser 的 "~" 展开
func webImportExpandUser(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// webImportPathWithin 判断 target 是否位于 root 目录之内
func webImportPathWithin(root string, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
