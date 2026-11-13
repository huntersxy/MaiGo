package learners

import (
	"context"
	"errors"
	"strings"
	"sync"

	"maibot-go/internal/llm_models"
	"maibot-go/internal/prompt"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// 对齐 maisaka_expression_selector._build_default_selection_result 的 LLM 二次选择

var (
	selectorPromptOnce sync.Once
	selectorPromptMgr  *prompt.Manager
	selectorPromptErr  error
)

func defaultSelectorPromptManager() *prompt.Manager {
	selectorPromptOnce.Do(func() {
		selectorPromptMgr, selectorPromptErr = prompt.NewManager()
		if selectorPromptErr != nil {
			zap.L().Debug("表达选择 prompt 管理器初始化失败", zap.Error(selectorPromptErr))
		}
	})
	return selectorPromptMgr
}

// refineExpressionSelectionByLLM 在向量候选池上调用选择模型
// 返回的 used 为 false 表示未配置 Router 或调用失败，调用方应直接注入候选池
func refineExpressionSelectionByLLM(ctx context.Context, reg *Registry, sessionID string, candidates []ExpressionPick) ([]ExpressionPick, bool) {
	if reg == nil || len(candidates) == 0 {
		return candidates, false
	}
	router := reg.ExpressionRouter()
	if router == nil {
		zap.L().Debug("表达方式 LLM 选择已跳过：未配置表达 Router", zap.String("session_id", sessionID))
		return candidates, false
	}
	pm := defaultSelectorPromptManager()
	if pm == nil {
		zap.L().Warn("表达方式 LLM 选择已跳过：prompt 管理器不可用", zap.String("session_id", sessionID))
		return candidates, false
	}

	lines := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		lines = append(lines, itoa(int(candidate.ID))+": 情景="+candidate.Situation+" | 风格="+candidate.Style)
	}
	rendered, err := pm.Render("maisaka_expression_selector", map[string]string{
		"max_selected": itoa(MaxSelectedExpressions),
		"candidates":   strings.Join(lines, "\n"),
	})
	if err != nil {
		zap.L().Warn("表达方式 LLM 选择 prompt 渲染失败，回退为直接注入",
			zap.String("session_id", sessionID),
			zap.Error(err))
		return candidates, false
	}

	response, err := generateExpressionSelection(ctx, router, rendered)
	if err != nil {
		zap.L().Warn("表达方式 LLM 选择失败，回退为直接注入",
			zap.String("session_id", sessionID),
			zap.Error(err))
		return candidates, false
	}
	selectedIDs := parseExpressionSelectedIDs(response, candidates)
	selected := make([]ExpressionPick, 0, len(selectedIDs))
	byID := make(map[uint]ExpressionPick, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	for _, id := range selectedIDs {
		if candidate, ok := byID[id]; ok {
			selected = append(selected, candidate)
		}
	}
	zap.L().Debug("表达方式 LLM 选择完成",
		zap.String("session_id", sessionID),
		zap.Int("candidates", len(candidates)),
		zap.Int("selected", len(selected)))
	return selected, true
}

func generateExpressionSelection(ctx context.Context, router *llm.Router, promptText string) (string, error) {
	if router == nil {
		return "", errors.New("表达选择 LLM 未配置")
	}
	_, client, err := router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		_, client, err = router.NewClientForTask(ctx, llm.TaskUtils)
	}
	if err != nil {
		return "", err
	}
	message, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage(promptText),
	})
	if err != nil {
		return "", err
	}
	return agenticMessageText(message), nil
}

// parseExpressionSelectedIDs 对齐 _parse_selected_ids：只接受候选内整数、去重、最多 5 个
func parseExpressionSelectedIDs(raw string, candidates []ExpressionPick) []uint {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	parsed := ParseJSONLoose(text)
	object, ok := parsed.(map[string]any)
	if !ok {
		zap.L().Warn("表达方式选择结果解析失败", zap.String("response", truncateForLog(text)))
		return nil
	}
	rawIDs, ok := object["selected_ids"].([]any)
	if !ok {
		return nil
	}
	allowed := make(map[uint]struct{}, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = struct{}{}
	}
	out := make([]uint, 0, MaxSelectedExpressions)
	seen := make(map[uint]struct{}, MaxSelectedExpressions)
	for _, rawID := range rawIDs {
		id, ok := expressionSelectedNumber(rawID)
		if !ok {
			continue
		}
		if _, exists := allowed[id]; !exists {
			continue
		}
		if _, duplicated := seen[id]; duplicated {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= MaxSelectedExpressions {
			break
		}
	}
	return out
}

func expressionSelectedNumber(raw any) (uint, bool) {
	switch value := raw.(type) {
	case float64:
		if value <= 0 || value != float64(uint(value)) {
			return 0, false
		}
		return uint(value), true
	case int:
		if value <= 0 {
			return 0, false
		}
		return uint(value), true
	case int64:
		if value <= 0 {
			return 0, false
		}
		return uint(value), true
	default:
		return 0, false
	}
}

func truncateForLog(text string) string {
	if len(text) <= 300 {
		return text
	}
	return text[:300]
}
