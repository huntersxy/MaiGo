package runtime

import (
	"context"
	"fmt"
	"time"

	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/focus"
)

// buildFocusSummary 复用 Planner 请求快照，把共享组旧时间线压成 ConversationState
func (a *Agent) buildFocusSummary(ctx context.Context, input focus.SummaryInput) (*maisaka.ContextMessage, error) {
	if input.Snapshot == nil {
		return nil, fmt.Errorf("Focus 共享组压缩缺少 Planner 请求快照")
	}
	var timestamp time.Time
	if len(input.Sources) > 0 {
		timestamp = input.Sources[len(input.Sources)-1].Timestamp
	}
	return a.buildContextSummary(ctx, contextSummaryInput{
		Snapshot:      input.Snapshot,
		Removed:       input.Sources,
		Recent:        input.Recent,
		Previous:      input.Previous,
		SourceContext: input.SourceContext,
		SessionID:     input.SessionID,
		Timestamp:     timestamp,
		Focus:         true,
	})
}
