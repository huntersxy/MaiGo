package runtime

import (
	"context"
	"strings"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

// replyAfterSplit 发射 maisaka.reply.after_split，并严格校验插件返回的分段
func (a *Agent) replyAfterSplit(ctx context.Context, segments []replyer.Segment, enableChineseTypo bool) []replyer.Segment {
	sessionID := ""
	if tc := tools.GetToolContext(ctx); tc != nil {
		sessionID = tc.SessionID
	}
	payload := make([]any, 0, len(segments))
	for _, segment := range segments {
		payload = append(payload, map[string]any{
			"text":           segment.Text,
			"separator":      segment.Separator,
			"quote_previous": segment.QuotePrevious,
			"is_correction":  segment.IsCorrection,
		})
	}
	result := hooks.EmitGlobal(ctx, "maisaka.reply.after_split", map[string]any{
		"segments":            payload,
		"session_id":          sessionID,
		"enable_chinese_typo": enableChineseTypo,
	})
	if len(result.Errors) > 0 {
		zap.L().Warn("回复分段 Hook 执行失败，保持原分段", zap.Strings("errors", result.Errors))
		return segments
	}
	rawSegments, ok := result.Kwargs["segments"].([]any)
	if !ok || len(rawSegments) == 0 {
		zap.L().Warn("回复分段 Hook 必须返回非空 segments 列表，保持原分段")
		return segments
	}
	validated := make([]replyer.Segment, 0, len(rawSegments))
	for _, raw := range rawSegments {
		item, ok := raw.(map[string]any)
		if !ok || len(item) != 4 {
			zap.L().Warn("回复分段 Hook 返回了无效的消息段，保持原分段")
			return segments
		}
		text, textOK := item["text"].(string)
		separator, separatorOK := item["separator"].(string)
		quotePrevious, quoteOK := item["quote_previous"].(bool)
		isCorrection, correctionOK := item["is_correction"].(bool)
		if !textOK || strings.TrimSpace(text) == "" || !separatorOK || !quoteOK || !correctionOK {
			zap.L().Warn("回复分段 Hook 返回了无效的消息段，保持原分段")
			return segments
		}
		validated = append(validated, replyer.Segment{
			Text:          text,
			Separator:     separator,
			QuotePrevious: quotePrevious,
			IsCorrection:  isCorrection,
		})
	}
	return validated
}
