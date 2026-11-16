package utils

// A_memorix 检索调优任务队列与产物读取，对齐 Python core/utils/retrieval_tuning_manager.py
// 任务 CRUD、取消与后台消费从管理器拆分出来，保持单文件职责单一

import (
	"context"
	"fmt"
	"os"
	"time"
)

// pendingTaskCount 对齐 _pending_task_count
func (m *RetrievalTuningManager) pendingTaskCount() int {
	count := 0
	for _, task := range m.tasks {
		switch task.Status {
		case "queued", "running", "cancel_requested":
			count++
		}
	}
	return count
}

// CreateTask 对齐 create_task
func (m *RetrievalTuningManager) CreateTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("检索调优中心已禁用")
	}
	if err := m.EnsureReady(); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	objective := retrievalTuningPayloadString(payload, "objective")
	if objective == "" {
		objective = pyStr(m.cfg("web.tuning.default_objective", RetrievalTuningObjectivePrecisionPriority))
	}
	if !IsRetrievalTuningObjective(objective) {
		return nil, fmt.Errorf("objective 非法: %s", objective)
	}
	intensity := retrievalTuningPayloadString(payload, "intensity")
	if intensity == "" {
		intensity = pyStr(m.cfg("web.tuning.default_intensity", "standard"))
	}
	defaultRounds, ok := RetrievalTuningIntensityRounds(intensity)
	if !ok {
		return nil, fmt.Errorf("intensity 非法: %s", intensity)
	}
	roundsTotal := retrievalTuningClampInt(payload["rounds"], defaultRounds, 1, 200)
	sampleSize := retrievalTuningClampInt(payload["sample_size"], m.defaultSampleSize(), 4, 500)
	topKEval := retrievalTuningClampInt(payload["top_k_eval"], m.defaultTopKEval(), 5, 100)
	evalTimeout := retrievalTuningClampFloat(payload["eval_query_timeout_seconds"], m.evalQueryTimeoutSeconds(), 0.01, 120.0)
	llmEnabled := retrievalTuningCoerceBool(payload["llm_enabled"], true)
	seed, err := retrievalTuningToInt(payload["seed"])
	if err != nil {
		seed = int(m.nowFunc()) % 1000003
	}

	m.mu.Lock()
	if m.pendingTaskCount() >= m.queueLimit() {
		m.mu.Unlock()
		return nil, fmt.Errorf("调优任务队列已满，请稍后重试")
	}
	task := NewRetrievalTuningTaskRecord(m.newTaskIDFunc(), objective, intensity, roundsTotal, m.nowFunc())
	task.Params = map[string]any{
		"sample_size":                sampleSize,
		"top_k_eval":                 topKEval,
		"eval_query_timeout_seconds": evalTimeout,
		"llm_enabled":                llmEnabled,
		"seed":                       seed,
	}
	m.tasks[task.TaskID] = task
	m.taskOrder = append([]string{task.TaskID}, m.taskOrder...)
	m.queue = append(m.queue, task.TaskID)
	m.mu.Unlock()

	return task.ToSummary(), nil
}

// defaultSampleSize 读取默认测试集规模
func (m *RetrievalTuningManager) defaultSampleSize() int {
	return retrievalTuningClampInt(m.cfg("web.tuning.default_sample_size", 24), 24, 4, 200)
}

// defaultTopKEval 读取默认评估深度
func (m *RetrievalTuningManager) defaultTopKEval() int {
	return retrievalTuningClampInt(m.cfg("web.tuning.default_top_k_eval", 20), 20, 5, 100)
}

// retrievalTuningPayloadString 读取请求字段并转成字符串
func retrievalTuningPayloadString(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	return pyStrip(pyStr(value))
}

// ListTasks 对齐 list_tasks
func (m *RetrievalTuningManager) ListTasks(limit int) []map[string]any {
	limit = retrievalTuningClampInt(limit, 50, 1, 500)
	m.mu.Lock()
	defer m.mu.Unlock()
	order := m.taskOrder
	if limit < len(order) {
		order = order[:limit]
	}
	items := make([]map[string]any, 0, len(order))
	for _, taskID := range order {
		if task, ok := m.tasks[taskID]; ok {
			items = append(items, task.ToSummary())
		}
	}
	return items
}

// GetTask 对齐 get_task，任务不存在返回 nil
func (m *RetrievalTuningManager) GetTask(taskID string, includeRounds bool) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return nil
	}
	return task.ToDetail(includeRounds)
}

// GetRounds 对齐 get_rounds
func (m *RetrievalTuningManager) GetRounds(taskID string, offset int, limit int) map[string]any {
	if offset < 0 {
		offset = 0
	}
	limit = retrievalTuningClampInt(limit, 50, 1, 500)
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return nil
	}
	total := len(task.Rounds)
	items := make([]any, 0, limit)
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		for _, record := range task.Rounds[offset:end] {
			items = append(items, record.ToDict())
		}
	}
	return map[string]any{
		"total":  total,
		"offset": offset,
		"limit":  limit,
		"items":  items,
	}
}

// CancelTask 对齐 cancel_task
func (m *RetrievalTuningManager) CancelTask(taskID string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return nil
	}
	switch task.Status {
	case "completed", "failed", "cancelled":
		return task.ToSummary()
	case "queued":
		task.Status = "cancelled"
		task.CancelRequested = true
		now := m.nowFunc()
		task.FinishedAt = &now
		task.UpdatedAt = now
		filtered := make([]string, 0, len(m.queue))
		for _, queued := range m.queue {
			if queued != taskID {
				filtered = append(filtered, queued)
			}
		}
		m.queue = filtered
		return task.ToSummary()
	default:
		task.Status = "cancel_requested"
		task.CancelRequested = true
		task.UpdatedAt = m.nowFunc()
		return task.ToSummary()
	}
}

// ApplyBest 对齐 apply_best
func (m *RetrievalTuningManager) ApplyBest(ctx context.Context, taskID string, validate bool) (map[string]any, error) {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("任务不存在")
	}
	if task.Status != "completed" {
		m.mu.Unlock()
		return nil, fmt.Errorf("任务未完成，无法应用最优参数")
	}
	if len(task.BestProfile) == 0 {
		m.mu.Unlock()
		return nil, fmt.Errorf("任务没有可应用的最优参数")
	}
	if validate && len(task.ValidationSummary) > 0 && !task.Recommended {
		m.mu.Unlock()
		return nil, fmt.Errorf("任务未通过 holdout/online_like 验证，无法应用最优参数")
	}
	best := retrievalTuningDeepCopy(task.BestProfile)
	m.mu.Unlock()

	applied, err := m.ApplyProfile(ctx, best, "task:"+taskID+":apply_best", validate)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if task, ok := m.tasks[taskID]; ok {
		now := m.nowFunc()
		task.ApplyLog = append(task.ApplyLog, map[string]any{
			"applied_at": now,
			"reason":     "apply_best",
			"profile":    best,
			"validate":   validate,
		})
		task.UpdatedAt = now
	}
	m.mu.Unlock()
	return applied, nil
}

// GetReport 对齐 get_report
func (m *RetrievalTuningManager) GetReport(taskID string, format string) map[string]any {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	artifacts := make(map[string]string, len(task.ArtifactPaths))
	for key, value := range task.ArtifactPaths {
		artifacts[key] = value
	}
	m.mu.Unlock()

	normalized := pyLower(pyStrip(format))
	if normalized != "md" && normalized != "json" {
		normalized = "md"
	}
	pathKey := "report_md"
	if normalized == "json" {
		pathKey = "report_json"
	}
	path := artifacts[pathKey]
	payload := map[string]any{"format": normalized, "content": "", "path": path}
	if path == "" {
		return payload
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return payload
	}
	payload["content"] = string(content)
	return payload
}

// StartWorker 启动后台任务消费协程
func (m *RetrievalTuningManager) StartWorker(ctx context.Context) {
	m.mu.Lock()
	if m.workerCancel != nil {
		m.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	m.workerCancel = cancel
	m.workerDone = make(chan struct{})
	m.stopping = false
	done := m.workerDone
	m.mu.Unlock()

	go func() {
		defer close(done)
		for {
			select {
			case <-workerCtx.Done():
				return
			default:
			}
			ran, err := m.RunNext(workerCtx)
			if err != nil {
				return
			}
			if !ran {
				duration := time.Duration(m.pollIntervalSeconds() * float64(time.Second))
				if err := m.sleepFunc(workerCtx, duration); err != nil {
					return
				}
			}
		}
	}()
}

// Shutdown 停止后台协程；超时保留 worker 引用，允许稍后重试关闭
func (m *RetrievalTuningManager) Shutdown() {
	m.mu.Lock()
	cancel := m.workerCancel
	done := m.workerDone
	m.stopping = true
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	task := StoppableTask{Name: "retrieval_tuning_worker", Cancel: cancel, Done: done}
	if err := CancelAndWaitForTasks(context.Background(), "关闭检索调优任务", []StoppableTask{task}, func(message string) {
		OrLogger(m.logger).Error(message)
	}); err != nil {
		return
	}
	m.mu.Lock()
	if m.workerDone == done {
		m.workerCancel = nil
		m.workerDone = nil
	}
	m.mu.Unlock()
}

// RunNext 取出一个排队任务并执行，返回是否执行了任务
func (m *RetrievalTuningManager) RunNext(ctx context.Context) (bool, error) {
	m.mu.Lock()
	var taskID string
	for len(m.queue) > 0 {
		candidate := m.queue[0]
		m.queue = m.queue[1:]
		task, ok := m.tasks[candidate]
		if !ok || task.Status != "queued" {
			continue
		}
		taskID = candidate
		m.activeTaskID = candidate
		break
	}
	m.mu.Unlock()
	if taskID == "" {
		return false, nil
	}
	err := m.RunTask(ctx, taskID)
	m.mu.Lock()
	if m.activeTaskID == taskID {
		m.activeTaskID = ""
	}
	m.mu.Unlock()
	return true, err
}
