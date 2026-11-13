package learners

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maibot-go/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const (
	hookExpressionLearnAfterExtract = "expression.learn.after_extract"
	hookExpressionLearnBeforeUpsert = "expression.learn.before_upsert"
)

// errExpressionExtractHookAborted 对齐 MaiBot：after_extract 中止时本轮学习标记 skipped
var errExpressionExtractHookAborted = errors.New("expression: 表达候选被 expression.learn.after_extract Hook 中止")

// serializeExpressionCandidates 对齐 ExpressionLearner._serialize_expressions
func serializeExpressionCandidates(items []extractedExpression) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		situation := strings.TrimSpace(item.Situation)
		style := NormalizeExpressionStyleForLearning(strings.TrimSpace(item.Style))
		if situation == "" || style == "" {
			continue
		}
		out = append(out, map[string]any{
			"situation": situation,
			"style":     style,
			"source_id": strings.TrimSpace(item.SourceID),
		})
	}
	return out
}

// deserializeExpressionCandidates 对齐 ExpressionLearner._deserialize_expressions
func deserializeExpressionCandidates(raw any) []extractedExpression {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]extractedExpression, 0, len(list))
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		situation := strings.TrimSpace(expressionHookText(item["situation"]))
		style := NormalizeExpressionStyleForLearning(strings.TrimSpace(expressionHookText(item["style"])))
		sourceID := strings.TrimSpace(expressionHookText(item["source_id"]))
		if situation == "" || style == "" {
			continue
		}
		out = append(out, extractedExpression{Situation: situation, Style: style, SourceID: sourceID})
	}
	return out
}

// emitExpressionAfterExtract 触发 expression.learn.after_extract；aborted 时调用方跳过整轮
func emitExpressionAfterExtract(ctx context.Context, sessionID string, messageCount int, items []extractedExpression) ([]extractedExpression, bool) {
	result := hooks.EmitGlobal(ctx, hookExpressionLearnAfterExtract, map[string]any{
		"session_id":     sessionID,
		"message_count":  messageCount,
		"expressions":    serializeExpressionCandidates(items),
		"jargon_entries": []any{},
	})
	logExpressionHookResult(hookExpressionLearnAfterExtract, result)
	if result.Aborted {
		return nil, true
	}
	if raw, ok := result.Kwargs["expressions"]; ok && raw != nil {
		return deserializeExpressionCandidates(raw), false
	}
	return items, false
}

// emitExpressionBeforeUpsert 触发 expression.learn.before_upsert；skip 表示候选被 Hook 丢弃
func emitExpressionBeforeUpsert(ctx context.Context, sessionID, situation, style string) (string, string, bool) {
	result := hooks.EmitGlobal(ctx, hookExpressionLearnBeforeUpsert, map[string]any{
		"session_id": sessionID,
		"situation":  situation,
		"style":      style,
	})
	logExpressionHookResult(hookExpressionLearnBeforeUpsert, result)
	if result.Aborted {
		return "", "", true
	}
	outSituation := strings.TrimSpace(expressionHookValueOr(result.Kwargs, "situation", situation))
	outStyle := NormalizeExpressionStyleForLearning(strings.TrimSpace(expressionHookValueOr(result.Kwargs, "style", style)))
	if outSituation == "" || outStyle == "" {
		return "", "", true
	}
	return outSituation, outStyle, false
}

func expressionHookValueOr(kwargs map[string]any, key, fallback string) string {
	if kwargs == nil {
		return fallback
	}
	raw, ok := kwargs[key]
	if !ok || raw == nil {
		return fallback
	}
	return expressionHookText(raw)
}

// expressionHookText 对齐 Python str(value or "").strip() 的常用分支
func expressionHookText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func logExpressionHookResult(name string, result hooks.DispatchResult) {
	for _, message := range result.Errors {
		zap.L().Warn("表达学习 Hook 执行失败",
			zap.String("hook", name),
			zap.String("error", message))
	}
	if result.Aborted {
		zap.L().Info("表达学习 Hook 中止",
			zap.String("hook", name),
			zap.String("stopped_by", result.StoppedBy))
	}
}
