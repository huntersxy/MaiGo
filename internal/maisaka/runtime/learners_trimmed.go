package runtime

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"MaiGo/internal/learners"
	"MaiGo/internal/maisaka"

	"go.uber.org/zap"
)

// triggerTrimmedHistoryLearning 对齐 Maisaka runtime._run_trimmed_history_learning：
// 裁切历史先转成学习素材，再异步并行执行表达与黑话批次，避免阻塞决策主链
func (a *Agent) triggerTrimmedHistoryLearning(ctx context.Context, sessionID string, messages []maisaka.ContextMessage, onProgress func(map[string]any)) error {
	if a == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	converted := maisakaHistoryToLearningContext(messages, sessionID)
	if len(converted) == 0 {
		publishTrimmedLearningProgress(onProgress, "skipped", "裁切历史中没有可学习的真实聊天消息", nil)
		return nil
	}
	// 对齐 Python runtime.py:1858-1868：必须在检查后台任务之前收集素材，
	// 否则等待期间裁切的素材会永久漏学
	expressionLearner := a.expressionLearnerFor(sessionID)
	jargonLearner := a.jargonLearnerFor(sessionID)
	reg := learners.DefaultRegistry()
	expressionEnabled, _ := reg.ChatConfig().GetExpressionConfig(sessionID)
	jargonEnabled, _ := reg.ChatConfig().GetJargonConfig(sessionID)
	if expressionEnabled && expressionLearner != nil {
		expressionLearner.CollectContextMessages(converted)
	}
	if jargonEnabled && jargonLearner != nil {
		jargonLearner.CollectContextMessages(converted)
	}
	if _, loaded := a.trimmedLearning.LoadOrStore(sessionID, struct{}{}); loaded {
		// 素材已并入缓冲，等当前批次结束后由 runTrimmedHistoryLearning 的续跑循环消费
		publishTrimmedLearningProgress(onProgress, "waiting",
			"上一批学习仍在运行，已收集本轮素材；达到门槛后自动继续学习",
			trimmedWaitingItems(expressionEnabled, expressionLearner, jargonEnabled, jargonLearner))
		return nil
	}
	go func() {
		defer a.trimmedLearning.Delete(sessionID)
		a.runTrimmedHistoryLearning(context.WithoutCancel(ctx), sessionID, nil, onProgress)
	}()
	return nil
}

// trimmedWaitingItems 构造「上一批仍在运行」时的学习卡片，对齐 Python 的 waiting 快照
func trimmedWaitingItems(expressionEnabled bool, expressionLearner any, jargonEnabled bool, jargonLearner any) []map[string]any {
	item := func(name string, enabled bool, learner any) map[string]any {
		card := map[string]any{"name": name, "status": "disabled", "contents": []any{}, "detail": "学习未启用"}
		if !enabled {
			return card
		}
		card["status"] = "waiting"
		card["detail"] = ""
		if provider, ok := learner.(interface {
			LearningState() (string, string)
		}); ok {
			if _, detail := provider.LearningState(); detail != "" {
				card["detail"] = detail
			}
		}
		return card
	}
	return []map[string]any{
		item("表达学习", expressionEnabled, expressionLearner),
		item("黑话学习", jargonEnabled, jargonLearner),
		{"name": "高频词学习", "status": "skipped", "contents": []any{}, "detail": "上一批学习仍在运行，本轮未执行高频词统计"},
	}
}

// runTrimmedHistoryLearning 并行执行表达与黑话学习，并按批次回传进度
// messages 为 nil 时只消费缓冲里已收集的素材；批次结束后按 Python runtime.py:2036-2044
// 的续跑条件继续，直到缓冲不足门槛或学习被明确阻断
func (a *Agent) runTrimmedHistoryLearning(ctx context.Context, sessionID string, messages []learners.ContextMessage, onProgress func(map[string]any)) {
	for {
		more := a.runTrimmedHistoryLearningOnce(ctx, sessionID, messages, onProgress)
		if !more || ctx.Err() != nil {
			return
		}
		// 续跑批次不再重复投递本轮裁切素材，只消费运行期间新收集的缓冲
		messages = nil
	}
}

// runTrimmedHistoryLearningOnce 执行一批学习，返回是否还有满足门槛的素材需要继续
func (a *Agent) runTrimmedHistoryLearningOnce(ctx context.Context, sessionID string, messages []learners.ContextMessage, onProgress func(map[string]any)) bool {
	reg := learners.DefaultRegistry()
	_, expressionEnabled := reg.ChatConfig().GetExpressionConfig(sessionID)
	_, jargonEnabled := reg.ChatConfig().GetJargonConfig(sessionID)
	// 对齐 runtime.enable_high_frequency_learning：表达或黑话任一启用即统计高频词
	highFrequencyEnabled := expressionEnabled || jargonEnabled

	expressionLearner := a.expressionLearnerFor(sessionID)
	jargonLearner := a.jargonLearnerFor(sessionID)
	items := []map[string]any{
		trimmedLearningItem("表达学习", expressionEnabled, expressionLearner),
		trimmedLearningItem("黑话学习", jargonEnabled, jargonLearner),
		trimmedHighFrequencyItem(highFrequencyEnabled, learners.HighFrequencyLearningResult{}, nil),
	}
	if (!expressionEnabled || expressionLearner == nil) && (!jargonEnabled || jargonLearner == nil) {
		publishTrimmedLearningProgress(onProgress, "skipped", "表达与黑话学习未启用", items)
		return false
	}
	publishTrimmedLearningProgress(onProgress, "running", "", items)

	var wg sync.WaitGroup
	var expressionChanged, jargonChanged bool
	var expressionErr, jargonErr error
	var highFrequencyResult learners.HighFrequencyLearningResult
	var highFrequencyErr error
	if expressionEnabled && expressionLearner != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			expressionChanged, expressionErr = expressionLearner.LearnFromContextMessages(ctx, messages)
		}()
	}
	if jargonEnabled && jargonLearner != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jargonChanged, jargonErr = jargonLearner.LearnFromContextMessages(ctx, messages)
		}()
	}
	if highFrequencyEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			highFrequencyResult, highFrequencyErr = learners.UpdateHighFrequencyTermsFromContextMessages(ctx, reg, messages, learners.HighFrequencyLearningOptions{})
		}()
	}
	wg.Wait()

	items = []map[string]any{
		trimmedLearningItem("表达学习", expressionEnabled, expressionLearner),
		trimmedLearningItem("黑话学习", jargonEnabled, jargonLearner),
		trimmedHighFrequencyItem(highFrequencyEnabled, highFrequencyResult, highFrequencyErr),
	}
	status := "no_results"
	detail := ""
	switch {
	case expressionErr != nil || jargonErr != nil || highFrequencyErr != nil:
		status = "failed"
		detail = firstNonEmptyString(errorText(expressionErr), errorText(jargonErr), errorText(highFrequencyErr))
		zap.L().Warn("裁切历史学习失败",
			zap.String("session_id", sessionID),
			zap.Error(expressionErr),
			zap.Error(jargonErr),
			zap.Error(highFrequencyErr))
	case expressionLearner != nil && expressionLearner.LearningStatus == "waiting",
		jargonLearner != nil && jargonLearner.LearningStatus == "waiting":
		status = "waiting"
	case expressionChanged || jargonChanged || highFrequencyResult.UpdatedCount > 0:
		status = "completed"
	case expressionLearner != nil && expressionLearner.LearningStatus == "skipped",
		jargonLearner != nil && jargonLearner.LearningStatus == "skipped":
		status = "skipped"
	}
	if dropped := trimmedLearningDropped(expressionLearner, jargonLearner); dropped > 0 && !strings.Contains(detail, "淘汰") {
		detail += "；积压超过 100 条，已淘汰最旧的 " + strconv.Itoa(dropped) + " 条素材"
	}
	publishTrimmedLearningProgress(onProgress, status, detail, items)
	return trimmedLearningShouldContinue(expressionEnabled, expressionLearner, jargonEnabled, jargonLearner)
}

// trimmedLearningShouldContinue 对齐 Python runtime.py:2036-2044 的续跑条件：
// 学习器未 disabled/failed、未被重试阻断，且缓冲仍达到提取门槛
func trimmedLearningShouldContinue(expressionEnabled bool, expressionLearner *learners.ExpressionLearner, jargonEnabled bool, jargonLearner *learners.JargonLearner) bool {
	if expressionEnabled && expressionLearner != nil {
		status, _ := expressionLearner.LearningState()
		if status != "disabled" && status != "failed" && !expressionLearner.LearningRetryBlocked &&
			expressionLearner.PendingCount() >= expressionLearner.MinMessagesForExtraction {
			return true
		}
	}
	if jargonEnabled && jargonLearner != nil {
		status, _ := jargonLearner.LearningState()
		if status != "disabled" && status != "failed" && !jargonLearner.LearningRetryBlocked &&
			jargonLearner.PendingCount() >= jargonLearner.MinMessagesForExtraction {
			return true
		}
	}
	return false
}

// trimmedHighFrequencyItem 构造高频词学习卡片，对齐 runtime._run_trimmed_history_learning 的第三路
func trimmedHighFrequencyItem(enabled bool, result learners.HighFrequencyLearningResult, err error) map[string]any {
	contents := make([]any, 0, len(result.Contents))
	for _, content := range result.Contents {
		contents = append(contents, content)
	}
	item := map[string]any{
		"name":     "高频词学习",
		"status":   "disabled",
		"contents": []any{},
		"detail":   "学习开关已关闭",
	}
	if !enabled {
		return item
	}
	item["contents"] = contents
	switch {
	case err != nil:
		item["status"] = "failed"
		item["detail"] = err.Error()
	case result.UpdatedCount > 0:
		item["status"] = "completed"
		item["detail"] = "更新 " + strconv.Itoa(result.UpdatedCount) + " 个词条"
	default:
		item["status"] = "no_results"
		item["detail"] = "用户正文中没有出现至少 2 次的有效词条"
	}
	return item
}

func trimmedLearningItem(name string, enabled bool, learner any) map[string]any {
	item := map[string]any{
		"name":     name,
		"status":   "disabled",
		"contents": []any{},
		"detail":   "学习开关已关闭",
	}
	if !enabled {
		return item
	}
	if learner == nil {
		item["status"] = "skipped"
		item["detail"] = "学习器未初始化"
		return item
	}
	item["status"] = "running"
	item["detail"] = ""
	if provider, ok := learner.(interface {
		LearningState() (string, string)
	}); ok {
		status, detail := provider.LearningState()
		item["status"] = status
		item["detail"] = detail
	}
	if provider, ok := learner.(interface {
		LearningContents() []string
	}); ok {
		learned := provider.LearningContents()
		contents := make([]any, 0, len(learned))
		for _, content := range learned {
			contents = append(contents, content)
		}
		item["contents"] = contents
	}
	return item
}

func trimmedLearningDropped(learnersList ...any) int {
	total := 0
	for _, learner := range learnersList {
		if counter, ok := learner.(interface{ DroppedCount() int }); ok {
			total += counter.DroppedCount()
		}
	}
	return total
}

func publishTrimmedLearningProgress(onProgress func(map[string]any), status, detail string, items []map[string]any) {
	if onProgress == nil {
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	onProgress(map[string]any{
		"status":   status,
		"detail":   detail,
		"learners": items,
	})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// maisakaHistoryToLearningContext 只保留真实用户与 SELF 消息，过滤工具结果、参考与系统消息
func maisakaHistoryToLearningContext(messages []maisaka.ContextMessage, sessionID string) []learners.ContextMessage {
	out := make([]learners.ContextMessage, 0, len(messages))
	seen := map[string]struct{}{}
	for _, message := range messages {
		if message.Role != maisaka.RoleUser && message.Role != maisaka.RoleAssistant {
			continue
		}
		if message.Source == maisaka.SourceToolResult || message.Source == maisaka.SourceReference {
			continue
		}
		text := strings.TrimSpace(message.Content)
		messageID := strings.TrimSpace(message.MessageID)
		if text == "" || messageID == "" {
			continue
		}
		if _, ok := seen[messageID]; ok {
			continue
		}
		seen[messageID] = struct{}{}
		isBotSelf := message.Role == maisaka.RoleAssistant || message.Source == maisaka.SourceGuidedReply
		sourceKind := "user"
		if isBotSelf {
			sourceKind = "assistant"
		}
		name := firstNonEmptyString(message.MetadataString("sender_name"), message.MetadataString("nickname"), message.UserID)
		out = append(out, learners.ContextMessage{
			MessageID:  messageID,
			SourceKind: sourceKind,
			Text:       text,
			IsBotSelf:  isBotSelf,
			IsNotify:   message.MetadataBool("is_notify"),
			Platform:   strings.TrimSpace(message.Platform),
			UserID:     strings.TrimSpace(message.UserID),
			Name:       name,
			SessionID:  firstNonEmptyString(strings.TrimSpace(message.SessionID), strings.TrimSpace(sessionID)),
		})
	}
	return out
}
