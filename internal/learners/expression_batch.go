package learners

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// BatchRunner 可选表达学习 LLM 批次（TaskLearner→utils）
type BatchRunner struct {
	Router  *llm.Router
	Prompts *prompt.Manager
	BotName string
}

var (
	batchPromptOnce sync.Once
	batchPromptMgr  *prompt.Manager
	batchPromptErr  error
)

func defaultBatchPromptManager() *prompt.Manager {
	batchPromptOnce.Do(func() {
		batchPromptMgr, batchPromptErr = prompt.NewManager()
		if batchPromptErr != nil {
			zap.L().Debug("表达学习 prompt 管理器初始化失败", zap.Error(batchPromptErr))
		}
	})
	return batchPromptMgr
}

// Run 从快照消息提取 situation/style 并 RecordCandidate；LLM 或解析失败时返回 0,nil
func (r *BatchRunner) Run(ctx context.Context, learner *ExpressionLearner, messages []ContextMessage) (int, error) {
	if learner == nil {
		return 0, nil
	}
	return r.RunWithSession(ctx, learner, learner.SessionID, messages)
}

// RunWithSession 使用解析后的真实会话 ID 执行批次，避免把学习结果写到错误聊天流
func (r *BatchRunner) RunWithSession(ctx context.Context, learner *ExpressionLearner, sessionID string, messages []ContextMessage) (int, error) {
	if r == nil || r.Router == nil || learner == nil || len(messages) == 0 {
		return 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	systemPrompt, err := r.buildSystemPrompt()
	if err != nil {
		return 0, err
	}
	users, userTexts, hasText := buildLearningUserMessages(messages)
	if !hasText {
		learner.batchNoText = true
		return 0, nil
	}
	text, err := r.generate(ctx, sessionID, systemPrompt, users)
	if err != nil {
		return 0, err
	}
	builtCount := len(users)
	if strings.TrimSpace(systemPrompt) != "" {
		builtCount++
	}
	saveExpressionPromptPreview(sessionID, learner.SessionID, len(messages), builtCount, systemPrompt, userTexts, text)
	expressions, _ := ParseExpressionResponse(text)
	raw := make([]extractedExpression, 0, len(expressions))
	for _, item := range expressions {
		raw = append(raw, extractedExpression{
			Situation: item.Situation,
			Style:     item.Style,
			SourceID:  item.SourceID,
		})
	}
	return r.applyExtractedWithSession(ctx, learner, sessionID, messages, raw)
}

// applyExtracted 对解析后的候选执行 Hook、过滤与写库（沿用 learner.SessionID）
func (r *BatchRunner) applyExtracted(ctx context.Context, learner *ExpressionLearner, messages []ContextMessage, raw []extractedExpression) (int, error) {
	if learner == nil {
		return 0, nil
	}
	return r.applyExtractedWithSession(ctx, learner, learner.SessionID, messages, raw)
}

// applyExtractedWithSession 使用真实聊天流会话 ID 执行 Hook、审核与写库
func (r *BatchRunner) applyExtractedWithSession(ctx context.Context, learner *ExpressionLearner, sessionID string, messages []ContextMessage, raw []extractedExpression) (int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(learner.SessionID)
	}
	learner.batchExtracted = len(raw)
	if len(raw) > maxExpressionsPerBatch {
		learner.batchModelLimited = true
		learner.batchLimitedCount = len(raw)
		// Python 侧模型超限后清空候选，但仍会触发 after_extract 供插件观察
		raw = nil
	}
	hooked, aborted := emitExpressionAfterExtract(ctx, sessionID, len(messages), raw)
	if aborted {
		return 0, errExpressionExtractHookAborted
	}
	learner.batchExtracted = len(hooked)
	if len(hooked) == 0 {
		return 0, nil
	}
	reg := DefaultRegistry()
	settings := reg.LearningSettings()
	botName := strings.TrimSpace(r.BotName)
	if botName == "" {
		botName = settings.BotNickname
	}
	filtered := filterExtractedExpressions(hooked, messages, botName, settings)
	learner.batchFiltered = len(filtered)
	learner.batchCandidates = make([]string, 0, len(filtered))
	for _, item := range filtered {
		learner.batchCandidates = append(learner.batchCandidates, item.Situation+"->"+item.Style)
	}
	if len(filtered) == 0 {
		return 0, nil
	}

	svc := NewExpressionService(reg)
	// 对齐 Python _check_expression_before_upsert：配置开启自审时，即使 Router 缺失也走审核
	// 失败路径并写审核日志，不能因为依赖未配置而静默放行
	selfReflect := settings.ExpressionSelfReflect
	reviewStore := DefaultExpressionReviewStore()
	written := 0
	for _, item := range filtered {
		situation, style, skip := emitExpressionBeforeUpsert(ctx, sessionID, item.Situation, item.Style)
		if skip {
			continue
		}
		var modifiedBy *ModifiedBy
		if selfReflect {
			suitable, reason, errorText := CheckExpressionSuitability(ctx, sessionID, situation, style)
			entry := AIReviewLogInput{
				SessionID: sessionID,
				Situation: situation,
				Style:     style,
				Passed:    suitable,
				Reason:    reason,
				Source:    "learn_before_upsert",
			}
			if errorText != "" {
				entry.Passed = false
				if strings.TrimSpace(entry.Reason) == "" {
					entry.Reason = errorText
				}
				entry.Error = errorText
				if _, err := reviewStore.AppendAIReviewLog(entry); err != nil {
					zap.L().Debug("表达 AI 审核日志写入失败", zap.Error(err))
				}
				continue
			}
			if _, err := reviewStore.AppendAIReviewLog(entry); err != nil {
				zap.L().Debug("表达 AI 审核日志写入失败", zap.Error(err))
			}
			if !suitable {
				continue
			}
			ai := ModifiedByAI
			modifiedBy = &ai
		}
		_, err := svc.RecordCandidate(ctx, sessionID, situation, style, false, modifiedBy)
		if err != nil {
			zap.L().Debug("表达候选写入失败（已忽略）",
				zap.String("session_id", sessionID),
				zap.String("situation", situation),
				zap.Error(err))
			continue
		}
		written++
		learner.appendLearningContent(situation + " → " + style)
	}
	return written, nil
}

func (r *BatchRunner) buildSystemPrompt() (string, error) {
	pm := r.Prompts
	if pm == nil {
		pm = defaultBatchPromptManager()
	}
	if pm == nil {
		if batchPromptErr != nil {
			return "", batchPromptErr
		}
		return "", fmt.Errorf("表达学习 prompt 管理器不可用")
	}
	rendered, err := pm.Render("learn_style", map[string]string{
		"chat_str": "聊天记录将在后续多条 user message 中给出；请以每条消息中的 id 作为来源行编号。",
	})
	if err != nil {
		return "", err
	}
	return rendered + "\n\n注意：聊天记录会在后续多条 user message 中给出。每条消息内的 id 是本轮学习的来源编号；speaker=SELF 的消息只作为上下文，不要从 SELF 的发言中学习。", nil
}

// buildLearningUserMessages 对齐 _build_multi_learning_messages：逐条来源消息 + 末尾输出指令
func buildLearningUserMessages(messages []ContextMessage) ([]*schema.AgenticMessage, []string, bool) {
	users := make([]*schema.AgenticMessage, 0, len(messages)+1)
	userTexts := make([]string, 0, len(messages)+1)
	for index, message := range messages {
		content := strings.TrimSpace(message.Text)
		if content == "" {
			continue
		}
		speaker := "USER"
		if message.IsBotSelf || message.SourceKind == "assistant" {
			speaker = "SELF"
		}
		name := strings.TrimSpace(message.CardName)
		if name == "" {
			name = strings.TrimSpace(message.Name)
		}
		if name == "" {
			name = "未知用户"
		}
		body := fmt.Sprintf("[id:%d]\n[speaker:%s]\n[name:%s]\n[content]\n%s", index+1, speaker, name, content)
		users = append(users, schema.UserAgenticMessage(body))
		userTexts = append(userTexts, body)
	}
	if len(users) == 0 {
		return nil, nil, false
	}
	instruction := "请根据以上聊天消息输出 JSON。"
	users = append(users, schema.UserAgenticMessage(instruction))
	userTexts = append(userTexts, instruction)
	return users, userTexts, true
}

func (r *BatchRunner) generate(ctx context.Context, sessionID, systemPrompt string, users []*schema.AgenticMessage) (string, error) {
	resolved, client, err := r.Router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		resolved, client, err = r.Router.NewClientForTask(ctx, llm.TaskUtils)
	}
	if err != nil {
		return "", err
	}
	_ = sessionID
	// llm_usage 的 request_type 对齐 MaiBot 的 expression.learner
	ctx = llm.WithTask(ctx, "expression.learner", batchModelName(resolved))
	messages := make([]*schema.AgenticMessage, 0, len(users)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, schema.SystemAgenticMessage(systemPrompt))
	}
	messages = append(messages, users...)
	msg, err := client.Generate(ctx, messages)
	if err != nil {
		return "", err
	}
	return batchAgenticPlainText(msg), nil
}

func batchModelName(resolved llm.ResolvedRoute) string {
	if name := strings.TrimSpace(resolved.MaiBotModel); name != "" {
		return name
	}
	if name := strings.TrimSpace(resolved.Profile.ModelName); name != "" {
		return name
	}
	return strings.TrimSpace(resolved.Config.Model)
}

func batchAgenticPlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
