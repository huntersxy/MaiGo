package learners

import (
	"context"
	"errors"
	"strings"
	"sync"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// 对齐 src/learners/expression_utils.py 的 check_expression_suitability

var (
	evaluationPromptOnce sync.Once
	evaluationPromptMgr  *prompt.Manager
	evaluationPromptErr  error
)

func defaultEvaluationPromptManager() *prompt.Manager {
	evaluationPromptOnce.Do(func() {
		evaluationPromptMgr, evaluationPromptErr = prompt.NewManager()
		if evaluationPromptErr != nil {
			zap.L().Debug("表达评估 prompt 管理器初始化失败", zap.Error(evaluationPromptErr))
		}
	})
	return evaluationPromptMgr
}

// CheckExpressionSuitability 单次 LLM 评估表达方式是否适合学习
// 返回 (suitable, reason, errorText)，出错时 suitable 为 false 且 errorText 为原始错误
func CheckExpressionSuitability(ctx context.Context, sessionID, situation, style string) (bool, string, string) {
	router := DefaultRegistry().ExpressionRouter()
	if router == nil {
		message := "表达评估 LLM 未配置"
		return false, "评估表达方式时发生错误: " + message, message
	}

	pm := defaultEvaluationPromptManager()
	if pm == nil {
		message := "表达评估 prompt 管理器不可用"
		if evaluationPromptErr != nil {
			message = evaluationPromptErr.Error()
		}
		return false, "评估表达方式时发生错误: " + message, message
	}

	criteria := []string{
		"表达方式或言语风格是否与使用条件或使用情景匹配",
		"允许部分语法错误或口语化或缺省出现",
		"表达方式不能太过特指，需要具有泛用性",
		"一般不涉及具体的人名或名称",
	}
	var criteriaBuilder strings.Builder
	for index, item := range criteria {
		if index > 0 {
			criteriaBuilder.WriteString("\n")
		}
		criteriaBuilder.WriteString(itoa(index + 1))
		criteriaBuilder.WriteString(". ")
		criteriaBuilder.WriteString(item)
	}

	rendered, err := pm.Render("expression_evaluation", map[string]string{
		"situation":     situation,
		"style":         style,
		"criteria_list": criteriaBuilder.String(),
	})
	if err != nil {
		return false, "评估表达方式时发生错误: " + err.Error(), err.Error()
	}

	response, err := generateExpressionEvaluation(ctx, router, sessionID, rendered)
	if err != nil {
		return false, "评估表达方式时发生错误: " + err.Error(), err.Error()
	}

	parsed, err := ParseEvaluationResponse(response)
	if err != nil {
		return false, "评估表达方式时发生错误: " + err.Error(), err.Error()
	}

	rawSuitable, hasSuitable := parsed["suitable"]
	if !hasSuitable {
		message := "评估结果缺少 suitable 字段"
		return false, "评估结果格式错误: " + message, message
	}
	suitable, ok := rawSuitable.(bool)
	if !ok {
		message := "评估结果 suitable 字段必须为 JSON 布尔类型"
		return false, "评估结果格式错误: " + message, message
	}

	reason := "未提供理由"
	if rawReason, ok := parsed["reason"]; ok {
		reason = NormalizeReasonText(rawReason)
	}
	return suitable, reason, ""
}

func generateExpressionEvaluation(ctx context.Context, router *llm.Router, sessionID, promptText string) (string, error) {
	if router == nil {
		return "", errors.New("表达评估 LLM 未配置")
	}
	resolved, client, err := router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		resolved, client, err = router.NewClientForTask(ctx, llm.TaskUtils)
	}
	if err != nil {
		return "", err
	}
	_ = sessionID
	// llm_usage 的 request_type 对齐 MaiBot 的 expression.check
	ctx = llm.WithTask(ctx, "expression.check", expressionEvaluationModelName(resolved))
	message, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage(promptText),
	})
	if err != nil {
		return "", err
	}
	return agenticMessageText(message), nil
}

func expressionEvaluationModelName(resolved llm.ResolvedRoute) string {
	if name := strings.TrimSpace(resolved.MaiBotModel); name != "" {
		return name
	}
	if name := strings.TrimSpace(resolved.Profile.ModelName); name != "" {
		return name
	}
	return strings.TrimSpace(resolved.Config.Model)
}

func agenticMessageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				builder.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				builder.WriteString(block.AssistantGenText.Text)
			}
		}
	}
	return strings.TrimSpace(builder.String())
}
