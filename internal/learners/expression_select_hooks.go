package learners

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"maibot-go/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const (
	hookExpressionSelectBeforeSelect   = "expression.select.before_select"
	hookExpressionSelectAfterSelection = "expression.select.after_selection"
)

// selectExpressionCandidatesWithHook 对齐 MaisakaExpressionSelector.select_for_reply 的 Hook 段。
//
// picker 负责在候选池上做实际挑选（向量 TopK / DB 启发式 / 未来的 LLM 子代理），
// Hook 只负责观察、过滤与覆盖结果。
func selectExpressionCandidatesWithHook(
	ctx context.Context,
	store Store,
	sessionID string,
	messages []ContextMessage,
	targetMessage string,
	replyReason string,
	candidates []ExpressionPick,
	picker func(ctx context.Context, candidates []ExpressionPick) []ExpressionPick,
) (ExpressionSelectionResult, bool) {
	return selectExpressionCandidatesWithHookArgs(ctx, store, sessionID, messages, targetMessage, replyReason, nil, candidates, picker)
}

// selectExpressionCandidatesWithHookArgs 透传 Planner reply 工具参数到 expression.select.* Hook
func selectExpressionCandidatesWithHookArgs(
	ctx context.Context,
	store Store,
	sessionID string,
	messages []ContextMessage,
	targetMessage string,
	replyReason string,
	replyToolArgs map[string]any,
	candidates []ExpressionPick,
	picker func(ctx context.Context, candidates []ExpressionPick) []ExpressionPick,
) (ExpressionSelectionResult, bool) {
	hookKwargs := expressionSelectionHookKwargs(sessionID, messages, targetMessage, replyReason, replyToolArgs, candidates)

	before := hooks.EmitGlobal(ctx, hookExpressionSelectBeforeSelect, hookKwargs)
	logExpressionSelectHookErrors(before)
	if before.Aborted {
		zap.L().Info("表达方式选择在开始前被 Hook 中止", zap.String("session_id", sessionID), zap.String("stopped_by", before.StoppedBy))
		return ExpressionSelectionResult{}, true
	}
	beforeKwargs := before.Kwargs
	if beforeKwargs == nil {
		beforeKwargs = hookKwargs
	}

	candidates = normalizeExpressionCandidateList(beforeKwargs["candidates"], candidates)
	maxNum := expressionHookInt(beforeKwargs["max_num"], len(candidates))
	if maxNum >= 0 && len(candidates) > maxNum {
		candidates = candidates[:maxNum]
	}
	if len(candidates) == 0 {
		zap.L().Info("表达方式选择已跳过：Hook 过滤后候选为空", zap.String("session_id", sessionID))
		return ExpressionSelectionResult{}, true
	}

	selected := picker(ctx, candidates)
	selectedPicks := append([]ExpressionPick(nil), selected...)
	selectedIDs := expressionPickIDs(selectedPicks)

	afterKwargs := make(map[string]any, len(beforeKwargs)+3)
	for key, value := range beforeKwargs {
		afterKwargs[key] = value
	}
	afterKwargs["candidates"] = serializeExpressionPicks(candidates)
	afterKwargs["selected_expressions"] = serializeExpressionPicks(selectedPicks)
	afterKwargs["selected_expression_ids"] = expressionIDValues(selectedIDs)

	after := hooks.EmitGlobal(ctx, hookExpressionSelectAfterSelection, afterKwargs)
	logExpressionSelectHookErrors(after)
	if after.Aborted {
		zap.L().Info("表达方式选择结果被 Hook 中止", zap.String("session_id", sessionID), zap.String("stopped_by", after.StoppedBy))
		return ExpressionSelectionResult{}, true
	}
	afterResult := after.Kwargs
	if afterResult == nil {
		afterResult = afterKwargs
	}

	rawSelectedIDs, hasSelectedIDs := afterResult["selected_expression_ids"]
	rawSelectedExpressions := afterResult["selected_expressions"]
	hookExpressions := normalizeSelectedExpressions(rawSelectedExpressions)
	if len(hookExpressions) > 0 && !reflect.DeepEqual(rawSelectedExpressions, afterKwargs["selected_expressions"]) {
		return buildExpressionSelectionFromPicks(ctx, store, hookExpressions), true
	}
	hookIDs := normalizeSelectedExpressionIDs(rawSelectedIDs, candidates)
	if len(hookIDs) > 0 || (hasSelectedIDs && isEmptyList(rawSelectedIDs)) {
		return buildExpressionSelectionFromIDs(ctx, store, candidates, hookIDs), true
	}

	touchExpressionLastActive(ctx, store, selectedIDs)
	return ExpressionSelectionResult{
		ExpressionHabits:      BuildExpressionHabitsBlock(selectedPicks),
		SelectedExpressionIDs: selectedIDs,
		SelectedExpressions:   selectedPicks,
	}, true
}

func expressionSelectionHookKwargs(
	sessionID string,
	messages []ContextMessage,
	targetMessage string,
	replyReason string,
	replyToolArgs map[string]any,
	candidates []ExpressionPick,
) map[string]any {
	history := make([]any, 0, 10)
	lines := make([]string, 0, 10)
	start := 0
	if len(messages) > 10 {
		start = len(messages) - 10
	}
	for _, message := range messages[start:] {
		text := strings.TrimSpace(message.Text)
		if text == "" {
			continue
		}
		history = append(history, map[string]any{
			"role":        contextMessageRole(message, sessionID),
			"text":        text,
			"timestamp":   "",
			"source_kind": message.SourceKind,
		})
		lines = append(lines, text)
	}
	if replyToolArgs == nil {
		replyToolArgs = map[string]any{}
	}
	return map[string]any{
		"chat_id":         sessionID,
		"session_id":      sessionID,
		"chat_info":       strings.Join(lines, "\n"),
		"chat_history":    history,
		"reply_message":   nil,
		"reply_tool_args": replyToolArgs,
		"target_message":  strings.TrimSpace(targetMessage),
		"reply_reason":    replyReason,
		"max_num":         len(candidates),
		"think_level":     0,
		"candidates":      serializeExpressionPicks(candidates),
	}
}

// contextMessageRole 用 source_kind 近似 MaiBot 的 role 字段。
func contextMessageRole(message ContextMessage, sessionID string) string {
	switch message.SourceKind {
	case "user":
		if message.IsBotSelf {
			return "assistant"
		}
		return "user"
	case "assistant":
		return "assistant"
	case "":
		return "system"
	default:
		return message.SourceKind
	}
}

func serializeExpressionPicks(picks []ExpressionPick) []any {
	out := make([]any, 0, len(picks))
	for _, pick := range picks {
		situation := strings.TrimSpace(pick.Situation)
		style := NormalizeExpressionStyleForLearning(pick.Style)
		if situation == "" || style == "" {
			continue
		}
		count := pick.Count
		if count == 0 {
			count = 1
		}
		entry := map[string]any{
			"id":        int(pick.ID),
			"situation": situation,
			"style":     style,
			"count":     count,
		}
		out = append(out, entry)
	}
	return out
}

func normalizeExpressionCandidateList(raw any, fallback []ExpressionPick) []ExpressionPick {
	list, ok := raw.([]any)
	if !ok {
		return fallback
	}
	out := make([]ExpressionPick, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := expressionHookID(entry["id"])
		if !ok || id <= 0 {
			continue
		}
		situation := strings.TrimSpace(expressionSelectHookText(entry["situation"]))
		style := NormalizeExpressionStyleForLearning(expressionSelectHookText(entry["style"]))
		if situation == "" || style == "" || IsPromptExampleExpressionStyle(style) {
			continue
		}
		count := expressionHookInt(entry["count"], 1)
		if count < 1 {
			count = 1
		}
		out = append(out, ExpressionPick{ID: id, Situation: situation, Style: style, Count: count})
	}
	return out
}

func normalizeSelectedExpressions(raw any) []ExpressionPick {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]ExpressionPick, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		situation := strings.TrimSpace(expressionSelectHookText(entry["situation"]))
		style := NormalizeExpressionStyleForLearning(expressionSelectHookText(entry["style"]))
		if situation == "" || style == "" || IsPromptExampleExpressionStyle(style) {
			continue
		}
		pick := ExpressionPick{Situation: situation, Style: style, Count: expressionHookInt(entry["count"], 1)}
		if pick.Count < 1 {
			pick.Count = 1
		}
		if id, ok := expressionHookID(entry["id"]); ok && id > 0 {
			pick.ID = id
		}
		out = append(out, pick)
	}
	return out
}

func normalizeSelectedExpressionIDs(raw any, candidates []ExpressionPick) []uint {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	allowed := make(map[uint]struct{}, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = struct{}{}
	}
	out := make([]uint, 0, len(list))
	seen := make(map[uint]struct{}, len(list))
	for _, item := range list {
		id, ok := expressionHookID(item)
		if !ok || id <= 0 {
			continue
		}
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func buildExpressionSelectionFromPicks(ctx context.Context, store Store, picks []ExpressionPick) ExpressionSelectionResult {
	ids := expressionPickIDs(picks)
	touchExpressionLastActive(ctx, store, ids)
	return ExpressionSelectionResult{
		ExpressionHabits:      BuildExpressionHabitsBlock(picks),
		SelectedExpressionIDs: ids,
		SelectedExpressions:   picks,
	}
}

func buildExpressionSelectionFromIDs(ctx context.Context, store Store, candidates []ExpressionPick, ids []uint) ExpressionSelectionResult {
	byID := make(map[uint]ExpressionPick, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	picks := make([]ExpressionPick, 0, len(ids))
	for _, id := range ids {
		if pick, ok := byID[id]; ok {
			picks = append(picks, pick)
		}
	}
	touchExpressionLastActive(ctx, store, ids)
	return ExpressionSelectionResult{
		ExpressionHabits:      BuildExpressionHabitsBlock(picks),
		SelectedExpressionIDs: ids,
		SelectedExpressions:   picks,
	}
}

func expressionPickIDs(picks []ExpressionPick) []uint {
	out := make([]uint, 0, len(picks))
	for _, pick := range picks {
		if pick.ID == 0 {
			continue
		}
		out = append(out, pick.ID)
	}
	return out
}

func expressionIDValues(ids []uint) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, int(id))
	}
	return out
}

func isEmptyList(raw any) bool {
	list, ok := raw.([]any)
	return ok && len(list) == 0
}

func expressionSelectHookText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return hookValueString(value)
}

// expressionHookID 只接受整数类型，对齐 Python isinstance(value, int)。
func expressionHookID(value any) (uint, bool) {
	switch v := value.(type) {
	case int:
		return uint(v), v > 0
	case int8:
		return uint(v), v > 0
	case int16:
		return uint(v), v > 0
	case int32:
		return uint(v), v > 0
	case int64:
		return uint(v), v > 0
	case uint:
		return v, v > 0
	case uint8:
		return uint(v), v > 0
	case uint16:
		return uint(v), v > 0
	case uint32:
		return uint(v), v > 0
	case uint64:
		return uint(v), v > 0
	case bool:
		if v {
			return 1, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// expressionHookInt 复刻 Python int(value)（含字符串/浮点截断），失败时返回 fallback。
func expressionHookInt(value any, fallback int) int {
	switch v := value.(type) {
	case nil:
		return fallback
	case int:
		return v
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	case uint:
		return int(v)
	case uint8:
		return int(v)
	case uint16:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return fallback
		}
		number, err := strconv.Atoi(text)
		if err != nil {
			if parsed, ferr := strconv.ParseFloat(text, 64); ferr == nil {
				return int(parsed)
			}
			return fallback
		}
		return number
	default:
		return fallback
	}
}

func hookValueString(value any) string {
	return toString(value)
}

func toString(value any) string {

	switch v := value.(type) {
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if v {
			return "True"
		}
		return "False"
	default:
		return ""
	}
}

func logExpressionSelectHookErrors(result hooks.DispatchResult) {
	if len(result.Errors) == 0 {
		return
	}
	zap.L().Warn("表达方式选择 Hook 执行失败",
		zap.String("hook", result.HookName),
		zap.Strings("errors", result.Errors))
}
