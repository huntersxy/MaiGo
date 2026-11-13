package replyer

import (
	"context"

	"maibot-go/internal/learners"
)

// SelectExpressions 委托 learners.MatchExpressions（有内存向量索引时优先 TopK，否则 DB 启发式）
func SelectExpressions(ctx context.Context, req ExpressionRequest) learners.ExpressionSelectionResult {
	if req.SessionID == "" {
		return learners.ExpressionSelectionResult{}
	}
	return learners.MatchExpressionsWithArgs(ctx, req.SessionID, nil, req.TargetMessage, req.ReplyReason, req.ReplyToolArgs)
}
