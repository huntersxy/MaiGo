package learners

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// MeaningInferencer 可选黑话含义推断（TaskLearner→utils）
type MeaningInferencer struct {
	Router  *llm.Router
	Prompts *prompt.Manager
	BotName string
}

var (
	defaultPromptOnce sync.Once
	defaultPromptMgr  *prompt.Manager
	defaultPromptErr  error
)

func defaultPromptManager() *prompt.Manager {
	defaultPromptOnce.Do(func() {
		defaultPromptMgr, defaultPromptErr = prompt.NewManager()
		if defaultPromptErr != nil {
			zap.L().Debug("黑话推断 prompt 管理器初始化失败", zap.Error(defaultPromptErr))
		}
	})
	return defaultPromptMgr
}

// ShouldInferRecord 对齐 _should_infer_meaning：阈值阶梯、完成与 MANUAL 跳过
func ShouldInferRecord(rec *JargonRecord) bool {
	if rec == nil || rec.ID == 0 {
		return false
	}
	if rec.CreatedBy == JargonCreatedByManual {
		return false
	}
	if rec.IsComplete {
		return false
	}
	count := rec.Count
	last := rec.LastInferenceCount
	if count < inferenceThresholds[0] || count <= last {
		return false
	}
	for _, threshold := range inferenceThresholds {
		if threshold > last {
			return count >= threshold
		}
	}
	return false
}

// InferMeaningAsync 非阻塞触发推断；router 未配置时无操作
func InferMeaningAsync(parent context.Context, inf *MeaningInferencer, svc *JargonService, sessionID, sessionName string, rec JargonRecord) {
	if inf == nil || inf.Router == nil || svc == nil || !ShouldInferRecord(&rec) {
		return
	}
	go func() {
		ctx := context.WithoutCancel(parent)
		if err := inf.InferMeaning(ctx, svc, sessionID, sessionName, rec.ID); err != nil {
			zap.L().Debug("黑话含义 LLM 推断失败（已忽略）",
				zap.Uint("jargon_id", rec.ID),
				zap.String("content", rec.Content),
				zap.Error(err))
		}
	}()
}

// InferMeaning 三阶段推断并写库，对齐 infer_meaning
func (inf *MeaningInferencer) InferMeaning(ctx context.Context, svc *JargonService, sessionID, sessionName string, jargonID uint) error {
	if inf == nil || inf.Router == nil || svc == nil || jargonID == 0 {
		return nil
	}
	rec, err := svc.Get(ctx, jargonID)
	if err != nil || rec == nil {
		return err
	}
	if rec.CreatedBy == JargonCreatedByManual {
		return nil
	}
	if !ShouldInferRecord(rec) {
		return nil
	}
	count := rec.Count
	previousMeaning := strings.TrimSpace(rec.Meaning)

	contexts, cleaned, changed := loadEvidenceContexts(ctx, svc, rec)
	if len(contexts) > 0 {
		cleanupEvidenceMessages(ctx, svc, rec, nil)
	} else if changed {
		cleanupEvidenceMessages(ctx, svc, rec, cleaned)
	}
	if len(contexts) == 0 {
		zap.L().Warn("没有可用证据上下文，跳过推断",
			zap.String("session_id", sessionID),
			zap.String("content", rec.Content))
		return nil
	}

	// 阶段一：结合上下文推断含义
	withContextPrompt, err := inf.buildContextPrompt(rec.Content, contexts, previousMeaning)
	if err != nil {
		return err
	}
	firstText, err := inf.generateWithPreview(ctx, sessionID, sessionName, rec.Content, "with_context", withContextPrompt)
	if err != nil {
		return err
	}
	first, ok := parseInferenceMap(firstText)
	if !ok {
		zap.L().Debug("黑话含义推断响应解析失败",
			zap.String("content", rec.Content),
			zap.String("raw", firstText))
		return nil
	}
	noInfo := jargonHookBool(first["no_info"], false)
	meaning := strings.TrimSpace(jargonHookText(first["meaning"]))
	if noInfo || meaning == "" {
		rec.LastInferenceCount = count
		return svc.UpdateInference(ctx, *rec)
	}

	// 阶段二：仅凭词条再推断一次
	contentOnlyPrompt, err := inf.buildContentOnlyPrompt(rec.Content)
	if err != nil {
		return err
	}
	secondText, err := inf.generateWithPreview(ctx, sessionID, sessionName, rec.Content, "content_only", contentOnlyPrompt)
	if err != nil {
		return err
	}
	second, ok := parseInferenceMap(secondText)
	if !ok {
		zap.L().Debug("黑话含义仅内容推断响应解析失败",
			zap.String("content", rec.Content))
		return nil
	}

	// 阶段三：对比两次推断，判断是否为黑话
	comparePrompt, err := inf.buildComparePrompt(first, second)
	if err != nil {
		return err
	}
	compareText, err := inf.generateWithPreview(ctx, sessionID, sessionName, rec.Content, "compare", comparePrompt)
	if err != nil {
		return err
	}
	comparison, ok := parseInferenceMap(compareText)
	if !ok {
		zap.L().Debug("黑话含义对比推断响应解析失败",
			zap.String("content", rec.Content))
		return nil
	}

	isJargon := !jargonHookBool(comparison["is_similar"], false)
	finalized := meaning
	if !isJargon {
		finalized = previousMeaning
	}
	isComplete := count >= inferenceThresholds[len(inferenceThresholds)-1]
	state, aborted := emitJargonBeforeFinalize(
		ctx,
		sessionID,
		sessionName,
		rec.Content,
		count,
		contexts,
		comparison,
		isJargon,
		finalized,
		isComplete,
		count,
	)
	if aborted {
		return nil
	}
	rec.Meaning = state.Meaning
	rec.IsJargon = state.IsJargon
	rec.LastInferenceCount = state.LastInferenceCount
	rec.IsComplete = state.IsComplete
	if err = svc.UpdateInference(ctx, *rec); err != nil {
		return err
	}
	if state.IsJargon {
		zap.L().Info("黑话含义推断完成",
			zap.String("session_id", sessionID),
			zap.String("content", rec.Content),
			zap.String("meaning", state.Meaning))
	} else {
		zap.L().Info("词条推断为非黑话",
			zap.String("session_id", sessionID),
			zap.String("content", rec.Content))
	}
	return nil
}

func (inf *MeaningInferencer) promptManager() (*prompt.Manager, error) {
	pm := inf.Prompts
	if pm == nil {
		pm = defaultPromptManager()
	}
	if pm == nil {
		if defaultPromptErr != nil {
			return nil, defaultPromptErr
		}
		return nil, fmt.Errorf("黑话推断 prompt 管理器不可用")
	}
	return pm, nil
}

func (inf *MeaningInferencer) botName() string {
	name := strings.TrimSpace(inf.BotName)
	if name == "" {
		name = "Bot"
	}
	return name
}

// buildContextPrompt 对齐 jargon_inference_with_context
func (inf *MeaningInferencer) buildContextPrompt(content string, contexts []string, previousMeaning string) (string, error) {
	pm, err := inf.promptManager()
	if err != nil {
		return "", err
	}
	prevSection := ""
	prevInstruction := ""
	if prev := strings.TrimSpace(previousMeaning); prev != "" {
		prevSection = "\n**上一次推断的含义（仅供参考）**\n" + prev
		prevInstruction = "- 请参考上一次推断的含义，结合新的上下文信息，给出更准确或更新的推断结果"
	}
	return pm.Render("jargon_inference_with_context", map[string]string{
		"content":                      strings.TrimSpace(content),
		"raw_content_list":             formatEvidenceSegments(contexts),
		"bot_name":                     inf.botName(),
		"previous_meaning_section":     prevSection,
		"previous_meaning_instruction": prevInstruction,
	})
}

// buildContentOnlyPrompt 对齐 jargon_inference_content_only
func (inf *MeaningInferencer) buildContentOnlyPrompt(content string) (string, error) {
	pm, err := inf.promptManager()
	if err != nil {
		return "", err
	}
	return pm.Render("jargon_inference_content_only", map[string]string{
		"content": strings.TrimSpace(content),
	})
}

// buildComparePrompt 对齐 jargon_compare_inference
func (inf *MeaningInferencer) buildComparePrompt(first, second map[string]any) (string, error) {
	pm, err := inf.promptManager()
	if err != nil {
		return "", err
	}
	firstRaw, err := marshalJSONNoEscape(first)
	if err != nil {
		return "", err
	}
	secondRaw, err := marshalJSONNoEscape(second)
	if err != nil {
		return "", err
	}
	return pm.Render("jargon_compare_inference", map[string]string{
		"inference1": firstRaw,
		"inference2": secondRaw,
	})
}

// marshalJSONNoEscape 对齐 json.dumps(ensure_ascii=False)：保留中文、不转义 HTML
func marshalJSONNoEscape(value any) (string, error) {
	raw, err := noEscapeJSONConfig.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

var noEscapeJSONConfig = sonic.Config{EscapeHTML: false}.Froze()

// formatEvidenceSegments 把证据组渲染为带编号的对话片段
func formatEvidenceSegments(contexts []string) string {
	parts := make([]string, 0, len(contexts))
	for index, contextText := range contexts {
		text := strings.TrimSpace(contextText)
		if text == "" {
			continue
		}
		parts = append(parts, "【对话片段 "+jargonItoa(index+1)+"】\n"+text)
	}
	return strings.Join(parts, "\n\n")
}

func (inf *MeaningInferencer) generateWithPreview(ctx context.Context, sessionID, sessionName, jargonContent, stage, userBody string) (string, error) {
	text, modelName, err := inf.generate(ctx, userBody)
	if err != nil {
		return "", err
	}
	saveJargonInferencePromptPreview(sessionID, sessionName, jargonContent, stage, userBody, text, modelName)
	return text, nil
}

func (inf *MeaningInferencer) generate(ctx context.Context, userBody string) (string, string, error) {
	resolved, client, err := inf.Router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		resolved, client, err = inf.Router.NewClientForTask(ctx, llm.TaskUtils)
	}
	if err != nil {
		return "", "", err
	}
	modelName := learningModelName(resolved)
	// llm_usage 的 request_type 对齐 MaiBot 的 jargon.inference
	ctx = llm.WithTask(ctx, "jargon.inference", modelName)
	msg, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage(userBody),
	})
	if err != nil {
		return "", "", err
	}
	return agenticPlainText(msg), modelName, nil
}

func agenticPlainText(msg *schema.AgenticMessage) string {
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
