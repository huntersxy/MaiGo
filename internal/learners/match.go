package learners

import (
	"context"
	"strings"
)

// ExpressionSelectionResult 对齐 maisaka_expression_selector.MaisakaExpressionSelectionResult
type ExpressionSelectionResult struct {
	ExpressionHabits      string
	SelectedExpressionIDs []uint
	SelectedExpressions   []ExpressionPick
}

// ExpressionPick 选中的表达方式摘要
type ExpressionPick struct {
	ID        uint
	Situation string
	Style     string
	Count     int
}

// MatchExpressions 为回复侧选择表达方式
//
// 候选池优先向量 MMR，其次加权抽样与 DB 启发式；向量模式且已接 Router 时会调用选择模型二次精选
// expression.select.* Hook 已由 selectExpressionCandidatesWithHook 实现
func MatchExpressions(ctx context.Context, sessionID string, messages []ContextMessage, targetMessage, replyReason string) ExpressionSelectionResult {
	return MatchExpressionsWithArgs(ctx, sessionID, messages, targetMessage, replyReason, nil)
}

// MatchExpressionsWithArgs 在 MatchExpressions 基础上透传 Planner reply 工具参数
// query 与 Hook 均使用 expression_intent/reply_reference，对齐 maisaka_expression_selector
func MatchExpressionsWithArgs(ctx context.Context, sessionID string, messages []ContextMessage, targetMessage, replyReason string, replyToolArgs map[string]any) ExpressionSelectionResult {
	if strings.TrimSpace(sessionID) == "" {
		return ExpressionSelectionResult{}
	}

	reg := DefaultRegistry()
	useExpression, _ := reg.ChatConfig().GetExpressionConfig(sessionID)
	if !useExpression {
		return ExpressionSelectionResult{}
	}

	related := ResolveExpressionRelatedSessionIDs(sessionID)
	settings := reg.LearningSettings()
	filter := ExpressionFilter{
		SessionIDs:      related,
		IncludeGlobal:   true,
		UserCheckedOnly: settings.ExpressionCheckedOnly,
	}
	rows, err := loadAllExpressionCandidates(ctx, reg.Store(), filter)
	if err != nil || len(rows) == 0 {
		return ExpressionSelectionResult{}
	}

	query := BuildExpressionQueryText(replyReason, replyToolArgs)
	if strings.TrimSpace(query) == "" && replyToolArgs == nil {
		// 兼容仅传 target/reply_reason 的旧调用；Planner 参数存在时严格按 Python query 构造
		query = ExpressionQueryEmbeddingText(targetMessage, replyReason)
	}
	vectorFilter := ExpressionVectorFilter{
		SessionIDs:    related,
		IncludeGlobal: true,
	}
	valid := normalizeExpressionCandidates(rows)
	// 对齐 select_for_reply：本地候选不足 10 条时不注入表达方式
	if len(valid) < 10 {
		return ExpressionSelectionResult{}
	}

	picker := func(ctx context.Context, candidates []ExpressionPick) []ExpressionPick {
		allowed := make(map[uint]struct{}, len(candidates))
		for _, candidate := range candidates {
			allowed[candidate.ID] = struct{}{}
		}
		scoped := make([]ExpressionRecord, 0, len(valid))
		for _, row := range valid {
			if _, ok := allowed[row.ID]; ok {
				scoped = append(scoped, row)
			}
		}
		var selected []ExpressionRecord
		if settings.UseVectorExpression {
			selected = selectExpressionsByVector(ctx, reg, scoped, query, vectorFilter, settings.VectorCandidatePoolSize)
		}
		if len(selected) == 0 {
			selected = sampleLegacyExpressionCandidates(scoped)
		}
		if len(selected) == 0 {
			selected = selectExpressionsHeuristic(scoped, query)
		}
		picks := recordsToExpressionPicks(selected)
		if settings.UseVectorExpression {
			if refined, used := refineExpressionSelectionByLLM(ctx, reg, sessionID, picks); used {
				return refined
			}
		}
		return picks
	}

	result, _ := selectExpressionCandidatesWithHookArgs(
		ctx,
		reg.Store(),
		sessionID,
		messages,
		targetMessage,
		replyReason,
		replyToolArgs,
		recordsToExpressionPicks(valid),
		picker,
	)
	return result
}

// loadAllExpressionCandidates 拉取全部候选，避免 list 分页上限截断表达池
func loadAllExpressionCandidates(ctx context.Context, store Store, filter ExpressionFilter) ([]ExpressionRecord, error) {
	const pageSize = 200
	var all []ExpressionRecord
	for page := 1; ; page++ {
		filter.Page = page
		filter.PageSize = pageSize
		rows, total, err := store.ListExpressions(ctx, filter)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) == 0 || len(rows) < pageSize || total <= 0 || len(all) >= total {
			break
		}
	}
	return all, nil
}

// MatchJargon 为规划/回复上下文注入机械匹配黑话
func MatchJargon(ctx context.Context, sessionID string, messages []ContextMessage, excludedContents []string) []JargonMatch {
	reg := DefaultRegistry()
	useJargon, _ := reg.ChatConfig().GetJargonConfig(sessionID)
	if !useJargon {
		return nil
	}

	related := ResolveRelatedSessionIDs(sessionID)
	candidates, err := reg.Store().ListJargonCandidates(ctx, related, false)
	if err != nil || len(candidates) == 0 {
		return nil
	}

	hf, err := reg.Store().ListHighFrequencyTerms(ctx, sessionID)
	if err != nil {
		hf = map[string]HighFrequencyTermSummary{}
	}

	matches := MatchJargonsForContext(sessionID, messages, candidates, hf, MaxJargonReferenceMatches)
	if len(excludedContents) == 0 {
		return matches
	}
	excluded := map[string]struct{}{}
	for _, content := range excludedContents {
		key := NormalizeMatchText(content)
		if key != "" {
			excluded[key] = struct{}{}
		}
	}
	filtered := make([]JargonMatch, 0, len(matches))
	for _, match := range matches {
		if _, skip := excluded[NormalizeMatchText(match.Content)]; skip {
			continue
		}
		filtered = append(filtered, match)
	}
	return filtered
}
