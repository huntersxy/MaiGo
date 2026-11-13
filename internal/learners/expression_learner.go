package learners

import (
	"context"
	"errors"
	"strings"
	"sync"

	"go.uber.org/zap"
)

const DefaultMinMessagesForExtraction = 10

// ExpressionLearner 表达学习器，对齐 src/learners/expression_learner.py ExpressionLearner 生命周期
type ExpressionLearner struct {
	SessionID                string
	MinMessagesForExtraction int
	pending                  *PendingLearningBuffer[ContextMessage]
	LearningStatus           string
	LearningDetail           string
	LearningRetryBlocked     bool

	mu               sync.Mutex
	batchRunning     bool
	batchRunner      *BatchRunner
	learningContents []string

	batchExtracted    int
	batchFiltered     int
	batchCandidates   []string
	batchLimitedCount int
	batchModelLimited bool
	batchNoText       bool
}

func NewExpressionLearner(sessionID string) *ExpressionLearner {
	return &ExpressionLearner{
		SessionID:                sessionID,
		MinMessagesForExtraction: DefaultMinMessagesForExtraction,
		pending:                  NewPendingLearningBuffer(func(m ContextMessage) string { return m.MessageID }),
		LearningStatus:           "pending",
	}
}

// SetBatchRunner 注入可选 LLM 批次执行器
func (l *ExpressionLearner) SetBatchRunner(runner *BatchRunner) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.batchRunner = runner
	l.mu.Unlock()
}

// CollectContextMessages 收集裁切上下文中的真实聊天消息
func (l *ExpressionLearner) CollectContextMessages(messages []ContextMessage) {
	for index := range messages {
		messages[index].SessionID = effectiveExpressionSessionID(messages[index].SessionID, l.SessionID)
	}
	l.pending.Extend(messages)
}

// effectiveExpressionSessionID 优先使用消息真实会话，缺失时回退学习器会话
func effectiveExpressionSessionID(messageSessionID, learnerSessionID string) string {
	if sessionID := strings.TrimSpace(messageSessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(learnerSessionID)
}

// LearningContents 返回最近一次批次实际写入的表达，供裁切学习进度卡片展示
func (l *ExpressionLearner) LearningContents() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.learningContents...)
}

func (l *ExpressionLearner) resetLearningContents() {
	l.mu.Lock()
	l.learningContents = nil
	l.mu.Unlock()
}

func (l *ExpressionLearner) appendLearningContent(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	l.mu.Lock()
	l.learningContents = append(l.learningContents, content)
	l.mu.Unlock()
}

// Observe 入站单条用户消息写入待学习缓冲，对齐 collect_context_messages 的增量路径
func (l *ExpressionLearner) Observe(messageID, text string) {
	l.ObserveContext(ContextMessage{
		MessageID:  messageID,
		SourceKind: "user",
		Text:       text,
	})
}

// ObserveContext 写入带平台与用户身份的入站消息，供黑名单与来源校验使用
func (l *ExpressionLearner) ObserveContext(message ContextMessage) {
	if l == nil {
		return
	}
	message.Text = strings.TrimSpace(message.Text)
	if message.Text == "" {
		return
	}
	if strings.TrimSpace(message.MessageID) == "" {
		return
	}
	if message.SourceKind == "" {
		message.SourceKind = "user"
	}
	sessionID := effectiveExpressionSessionID(message.SessionID, l.SessionID)
	_, enableLearning := DefaultRegistry().ChatConfig().GetExpressionConfig(sessionID)
	if !enableLearning {
		return
	}
	message.SessionID = sessionID
	l.pending.Extend([]ContextMessage{message})
}

// LearningState 返回最近一次学习状态与说明
func (l *ExpressionLearner) LearningState() (string, string) {
	if l == nil {
		return "skipped", ""
	}
	return l.LearningStatus, l.LearningDetail
}

// PendingCount 返回待学习素材数量
func (l *ExpressionLearner) PendingCount() int {
	if l == nil {
		return 0
	}
	return l.pending.Len()
}

// DroppedCount 返回因超过缓冲上限被淘汰的素材数量
func (l *ExpressionLearner) DroppedCount() int {
	if l == nil {
		return 0
	}
	return l.pending.DroppedCount()
}

// RecordCandidate 学习批次写入表达方式并异步更新向量索引（Observe 缓冲后的落库入口）
func (l *ExpressionLearner) RecordCandidate(ctx context.Context, situation, style string, checked bool, modifiedBy *ModifiedBy) (*ExpressionRecord, error) {
	if l == nil {
		return nil, errors.New("expression: learner 未初始化")
	}
	svc := NewExpressionService(DefaultRegistry())
	return svc.RecordCandidate(ctx, l.SessionID, situation, style, checked, modifiedBy)
}

// ShouldRunBatch 缓冲达阈值且未在运行
func (l *ExpressionLearner) ShouldRunBatch() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.batchRunning {
		return false
	}
	return l.pending.Len() >= l.MinMessagesForExtraction
}

// TryScheduleBatchAsync 非阻塞触发 LLM 批次；router 未配置时无操作
func (l *ExpressionLearner) TryScheduleBatchAsync(parent context.Context) {
	if l == nil || !l.ShouldRunBatch() {
		return
	}
	reg := DefaultRegistry()
	// 对齐 Python 批次执行前检查：先按学习器会话判定，解析真实会话后再判定一次
	if _, enabled := reg.ChatConfig().GetExpressionConfig(l.SessionID); !enabled {
		l.pending.Clear()
		l.LearningStatus = "skipped"
		l.LearningDetail = "学习开关已关闭，已清空待学习素材"
		zap.L().Info("表达学习开关已关闭，已清空待学习素材",
			zap.String("session_id", l.SessionID))
		return
	}
	runner := l.ResolveBatchRunner(reg.ExpressionRouter())
	if runner == nil {
		return
	}

	l.mu.Lock()
	if l.batchRunning || l.pending.Len() < l.MinMessagesForExtraction {
		l.mu.Unlock()
		return
	}
	snapshot := l.pending.Snapshot()
	l.batchRunning = true
	l.learningContents = nil
	l.mu.Unlock()

	learningSessionID, resolveErr := DefaultRegistry().ResolveLearningSessionID(
		context.WithoutCancel(parent), contextSessionIDs(snapshot), l.SessionID)
	if resolveErr != nil {
		l.mu.Lock()
		l.batchRunning = false
		l.mu.Unlock()
		l.LearningRetryBlocked = true
		l.LearningStatus = "skipped"
		l.LearningDetail = "无法解析到有效聊天流，素材已保留"
		zap.L().Warn("表达学习无法解析有效聊天流",
			zap.String("learner_session_id", l.SessionID),
			zap.Error(resolveErr))
		return
	}
	if learningSessionID != l.SessionID {
		zap.L().Info("表达学习会话 ID 已按真实消息修正",
			zap.String("learner_session_id", l.SessionID),
			zap.String("learning_session_id", learningSessionID))
	}

	_, learningEnabled := reg.ChatConfig().GetExpressionConfig(learningSessionID)
	if !learningEnabled {
		l.pending.Clear()
		l.mu.Lock()
		l.batchRunning = false
		l.mu.Unlock()
		l.LearningStatus = "skipped"
		l.LearningDetail = "学习开关已关闭，已清空待学习素材"
		zap.L().Info("表达学习开关已关闭，已清空待学习素材",
			zap.String("session_id", learningSessionID))
		return
	}

	gate := defaultExpressionBatchGate
	settings := reg.LearningSettings()
	acquired := gate.acquire(learningSessionID, settings.MaxExpressionLearners)
	if !acquired.Acquired {
		l.mu.Lock()
		l.batchRunning = false
		l.mu.Unlock()
		l.LearningStatus = "waiting"
		l.LearningDetail = batchGateDetail(acquired, l.pending.Len())
		return
	}
	l.pending.Discard(snapshot)

	go func() {
		defer func() {
			gate.release(learningSessionID)
			l.mu.Lock()
			l.batchRunning = false
			l.mu.Unlock()
		}()
		ctx := context.WithoutCancel(parent)
		if _, err := l.runBatchWithSnapshot(ctx, runner, learningSessionID, snapshot); err != nil {
			zap.L().Debug("表达学习 LLM 批次失败（已忽略）",
				zap.String("session_id", learningSessionID),
				zap.Error(err))
		}
	}()
}

// LearnFromContextMessages 从上下文触发 LLM 提取与写库
func (l *ExpressionLearner) LearnFromContextMessages(ctx context.Context, messages []ContextMessage) (bool, error) {
	l.LearningRetryBlocked = false
	l.resetLearningContents()
	reg := DefaultRegistry()
	// 对齐 Python 收集阶段：先按当前会话判定 learn，再决定是否收集素材
	if _, enabled := reg.ChatConfig().GetExpressionConfig(l.SessionID); !enabled {
		l.pending.Clear()
		l.LearningStatus = "skipped"
		l.LearningDetail = "学习开关已关闭，已清空待学习素材"
		zap.L().Info("表达学习开关已关闭，跳过收集",
			zap.String("session_id", l.SessionID))
		return false, nil
	}
	l.CollectContextMessages(messages)
	snapshot := l.pending.Snapshot()
	if len(snapshot) < l.MinMessagesForExtraction {
		if len(snapshot) > 0 {
			l.LearningStatus = "waiting"
			l.LearningDetail = l.pending.WaitingDetail(l.MinMessagesForExtraction)
		} else {
			l.LearningStatus = "skipped"
			l.LearningDetail = "裁切历史中没有可学习的真实聊天消息"
		}
		return false, nil
	}

	learningSessionID, resolveErr := DefaultRegistry().ResolveLearningSessionID(
		ctx, contextSessionIDs(snapshot), l.SessionID)
	if resolveErr != nil {
		l.LearningRetryBlocked = true
		l.LearningStatus = "skipped"
		l.LearningDetail = "无法解析到有效聊天流，素材已保留"
		zap.L().Warn("表达学习无法解析有效聊天流",
			zap.String("learner_session_id", l.SessionID),
			zap.Error(resolveErr))
		return false, nil
	}
	if learningSessionID != l.SessionID {
		zap.L().Info("表达学习会话 ID 已按真实消息修正",
			zap.String("learner_session_id", l.SessionID),
			zap.String("learning_session_id", learningSessionID))
	}

	if _, enable := reg.ChatConfig().GetExpressionConfig(learningSessionID); !enable {
		l.pending.Clear()
		l.LearningStatus = "skipped"
		l.LearningDetail = "学习开关已关闭，已清空待学习素材"
		zap.L().Info("表达学习开关已关闭，已清空待学习素材",
			zap.String("session_id", learningSessionID))
		return false, nil
	}

	runner := l.ResolveBatchRunner(reg.ExpressionRouter())
	if runner == nil {
		l.LearningStatus = "waiting"
		l.LearningDetail = "表达学习 LLM 未配置 Router"
		return false, nil
	}

	gate := defaultExpressionBatchGate
	settings := reg.LearningSettings()
	acquired := gate.acquire(learningSessionID, settings.MaxExpressionLearners)
	if !acquired.Acquired {
		l.LearningRetryBlocked = acquired.Max <= 0
		l.LearningStatus = "waiting"
		if l.LearningRetryBlocked {
			l.LearningStatus = "skipped"
		}
		l.LearningDetail = batchGateDetail(acquired, l.pending.Len())
		return false, nil
	}
	defer gate.release(learningSessionID)

	l.pending.Discard(snapshot)
	l.LearningStatus = "running"
	l.LearningDetail = ""
	written, err := l.runBatchWithSnapshot(ctx, runner, learningSessionID, snapshot)
	if err != nil {
		l.LearningStatus = "waiting"
		l.LearningDetail = "表达学习 LLM 批次失败: " + err.Error()
		return false, nil
	}
	return written > 0, nil
}

// contextSessionIDs 提取真实消息中的会话归属，供频次解析使用
func contextSessionIDs(messages []ContextMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		if sessionID := strings.TrimSpace(message.SessionID); sessionID != "" {
			ids = append(ids, sessionID)
		}
	}
	return ids
}

// batchGateDetail 对齐 Python 并发闸门失败文案
func batchGateDetail(result learningBatchAcquireResult, pending int) string {
	var reason string
	switch result.Reason {
	case "session_busy":
		reason = "当前会话已有学习批次正在运行"
	case "global_limit":
		reason = "全局学习并发已满（" + itoaInt(result.Active) + "/" + itoaInt(result.Max) + "）"
	case "max_expression_learner <= 0":
		reason = "学习并发配置小于或等于 0"
	default:
		reason = result.Reason
	}
	return reason + "，已保留 " + itoaInt(pending) + " 条素材等待重试"
}

func (l *ExpressionLearner) runBatchWithSnapshot(ctx context.Context, runner *BatchRunner, sessionID string, snapshot []ContextMessage) (int, error) {
	l.batchExtracted = 0
	l.batchFiltered = 0
	l.batchCandidates = nil
	l.batchLimitedCount = 0
	l.batchModelLimited = false
	l.batchNoText = false
	written, err := runner.RunWithSession(ctx, l, sessionID, snapshot)
	if err != nil {
		if errors.Is(err, errExpressionExtractHookAborted) {
			l.LearningStatus = "skipped"
			l.LearningDetail = "表达候选被 expression.learn.after_extract Hook 中止"
			return 0, nil
		}
		return 0, err
	}
	l.logBatchCompletion(ctx, sessionID, snapshot, written)
	switch {
	case l.batchNoText:
		l.LearningStatus = "no_results"
		l.LearningDetail = "没有可学习的文字正文"
	case l.batchModelLimited:
		l.LearningStatus = "no_results"
		l.LearningDetail = "模型返回 " + itoaInt(l.batchLimitedCount) + " 条表达，超过单批 " + itoaInt(maxExpressionsPerBatch) + " 条限制"
	case written > 0:
		l.LearningStatus = "completed"
		l.LearningDetail = "写入 " + itoaInt(written) + " 条表达"
	case l.batchExtracted > 0 && l.batchFiltered == 0:
		l.LearningStatus = "no_results"
		l.LearningDetail = itoaInt(l.batchExtracted) + " 条表达候选均未通过内容或来源校验"
	case l.batchExtracted == 0:
		l.LearningStatus = "no_results"
		l.LearningDetail = "模型未提取出表达候选"
	default:
		l.LearningStatus = "no_results"
		l.LearningDetail = itoaInt(l.batchFiltered) + " 条有效候选均被写入 Hook、审核或写入校验阻止"
	}
	return written, nil
}

// logBatchCompletion 对齐 Python 完成日志：候选 debug 列表 + 完成统计
func (l *ExpressionLearner) logBatchCompletion(ctx context.Context, sessionID string, messages []ContextMessage, written int) {
	if l == nil || l.batchFiltered <= 0 {
		return
	}
	reg := DefaultRegistry()
	displayName := reg.LearningSessionDisplayName(ctx, sessionID, LearningSessionNameHintFromContexts(messages, sessionID))
	title := "学习到的表达"
	if reg.LearningSettings().ExpressionSelfReflect {
		title = "待优化的表达方式"
	}
	candidates := append([]string(nil), l.batchCandidates...)
	zap.L().Debug("["+displayName+"] "+title,
		zap.String("session_id", sessionID),
		zap.Strings("candidates", candidates))
	zap.L().Info("["+displayName+"] 表达学习完成",
		zap.String("session_id", sessionID),
		zap.Int("parsed", l.batchExtracted),
		zap.Int("candidates", l.batchFiltered),
		zap.Int("written", written))
}
