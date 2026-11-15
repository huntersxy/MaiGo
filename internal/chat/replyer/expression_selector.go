package replyer

import (
	"context"
	"strings"

	"MaiGo/internal/learners"
)

// LearnersExpressionSelector 把 replyer 的选择端口接到 learners 的表达方式实现
// 候选池、向量/legacy 抽样、expression.select.* Hook 与 last_active_time 更新都在 learners
type LearnersExpressionSelector struct{}

// SelectForReply 实现 ExpressionSelector
func (LearnersExpressionSelector) SelectForReply(ctx context.Context, request ExpressionSelectRequest) (ExpressionSelection, error) {
	result := learners.MatchExpressionsWithArgs(
		ctx,
		request.SessionID,
		expressionContextMessages(request.ChatHistory),
		expressionTargetText(request.Target),
		request.ReplyReason,
		request.ReplyToolArgs,
	)
	selection := ExpressionSelection{
		ExpressionHabits: result.ExpressionHabits,
	}
	selection.SelectedExpressionIDs = make([]int, 0, len(result.SelectedExpressionIDs))
	for _, id := range result.SelectedExpressionIDs {
		selection.SelectedExpressionIDs = append(selection.SelectedExpressionIDs, int(id))
	}
	selection.SelectedExpressions = make([]map[string]any, 0, len(result.SelectedExpressions))
	for _, pick := range result.SelectedExpressions {
		detail := map[string]any{
			"situation": pick.Situation,
			"style":     pick.Style,
			"count":     pick.Count,
		}
		if pick.ID > 0 {
			detail["id"] = int(pick.ID)
		}
		selection.SelectedExpressions = append(selection.SelectedExpressions, detail)
	}
	return selection, nil
}

func expressionTargetText(target *ReplyTarget) string {
	if target == nil {
		return ""
	}
	if text := strings.TrimSpace(target.Content); text != "" {
		return text
	}
	return buildTextFromParts(target.Parts)
}
