package utils

// A_memorix 检索调优任务执行流程，对齐 Python core/utils/retrieval_tuning_manager.py 的 _run_task 与 _evaluate_profile
// 产物写入注入的产物目录，检索评估与运行时重建分别由 Evaluator / Runtime 注入

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// retrievalTuningEvaluation 单次参数评估的中间结果，序列化口径对齐 _evaluate_profile 返回值
type retrievalTuningEvaluation struct {
	metrics   map[string]any
	score     float64
	latencyMS float64
	failure   map[string]any
}

// toMap 输出 {metrics, score, avg_elapsed_ms, failure_summary}
func (e retrievalTuningEvaluation) toMap() map[string]any {
	metrics := e.metrics
	if metrics == nil {
		metrics = map[string]any{}
	}
	failure := e.failure
	if failure == nil {
		failure = map[string]any{}
	}
	return map[string]any{
		"metrics":         metrics,
		"score":           e.score,
		"avg_elapsed_ms":  e.latencyMS,
		"failure_summary": failure,
	}
}

// retrievalTuningArtifacts 任务产物路径集合
type retrievalTuningArtifacts struct {
	querySet    string
	roundsJSONL string
	bestProfile string
	reportJSON  string
	reportMD    string
}

// paths 输出产物路径字典，键名对齐 Python artifact_paths
func (a retrievalTuningArtifacts) paths() map[string]string {
	if a.querySet == "" {
		return map[string]string{}
	}
	return map[string]string{
		"query_set":           a.querySet,
		"round_metrics_jsonl": a.roundsJSONL,
		"best_profile":        a.bestProfile,
		"report_json":         a.reportJSON,
		"report_md":           a.reportMD,
	}
}

// runArtifacts 准备任务产物目录，未配置根目录时返回空路径
func (m *RetrievalTuningManager) runArtifacts(taskID string) (retrievalTuningArtifacts, error) {
	dir := m.artifactsDir(taskID)
	if dir == "" {
		return retrievalTuningArtifacts{}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return retrievalTuningArtifacts{}, err
	}
	return retrievalTuningArtifacts{
		querySet:    filepath.Join(dir, "query_set.json"),
		roundsJSONL: filepath.Join(dir, "round_metrics.jsonl"),
		bestProfile: filepath.Join(dir, "best_profile.json"),
		reportJSON:  filepath.Join(dir, "report.json"),
		reportMD:    filepath.Join(dir, "report.md"),
	}, nil
}

// writeArtifact 写产物文件，空路径跳过
func (m *RetrievalTuningManager) writeArtifact(path string, content string) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// appendRoundArtifact 追加一行轮次 JSONL
func (m *RetrievalTuningManager) appendRoundArtifact(path string, record *RetrievalTuningRoundRecord) {
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		m.logger.Warning("检索调优轮次产物写入失败: " + err.Error())
		return
	}
	defer file.Close()
	if _, err := file.WriteString(retrievalTuningJSONDumps(record.ToDict(), 0) + "\n"); err != nil {
		m.logger.Warning("检索调优轮次产物写入失败: " + err.Error())
	}
}

// markTaskFailed 对齐 _run_task 异常分支
func (m *RetrievalTuningManager) markTaskFailed(taskID string, cause error) {
	m.logger.Error(fmt.Sprintf("Retrieval tuning task failed: task_id=%s, err=%s", taskID, cause.Error()))
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return
	}
	task.Status = "failed"
	task.Error = cause.Error()
	now := m.nowFunc()
	task.FinishedAt = &now
	task.UpdatedAt = now
}

// RunTask 执行一个任务，失败时登记 failed 状态
func (m *RetrievalTuningManager) RunTask(ctx context.Context, taskID string) error {
	err := m.runTask(ctx, taskID)
	if err != nil {
		m.markTaskFailed(taskID, err)
	}
	return err
}

// runTask 对齐 _run_task
func (m *RetrievalTuningManager) runTask(ctx context.Context, taskID string) error {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	task.Status = "running"
	now := m.nowFunc()
	task.StartedAt = &now
	task.UpdatedAt = now
	objective := task.Objective
	roundsTotal := task.RoundsTotal
	params := task.Params
	if params == nil {
		params = map[string]any{}
	}
	paramsCopy := make(map[string]any, len(params))
	for key, value := range params {
		paramsCopy[key] = value
	}
	m.mu.Unlock()

	artifacts, err := m.runArtifacts(taskID)
	if err != nil {
		return err
	}

	sampleSize := retrievalTuningClampInt(paramsCopy["sample_size"], 24, 4, 500)
	topKEval := retrievalTuningClampInt(paramsCopy["top_k_eval"], 20, 5, 100)
	llmEnabled := retrievalTuningCoerceBool(paramsCopy["llm_enabled"], true)
	seed := 0
	if parsed, parseErr := retrievalTuningToInt(paramsCopy["seed"]); parseErr == nil {
		seed = parsed
	}
	timeoutRaw, hasTimeout := paramsCopy["eval_query_timeout_seconds"]
	evalTimeout := m.evalQueryTimeoutSeconds()
	if hasTimeout && isTruthy(timeoutRaw) {
		evalTimeout = retrievalTuningClampFloat(timeoutRaw, evalTimeout, 0.01, 120.0)
	}

	cases, stats := m.BuildQuerySet(ctx, sampleSize, seed, llmEnabled)
	if len(cases) == 0 {
		return fmt.Errorf("当前知识库样本不足，无法构建调优测试集")
	}
	trainCases, holdoutCases, splitStats := SplitRetrievalTuningQueryCases(cases, seed)
	stats["split"] = splitStats

	items := make([]any, 0, len(cases))
	for _, item := range cases {
		items = append(items, item.ToDict())
	}
	querySetPayload := map[string]any{
		"task_id":    taskID,
		"created_at": m.nowFunc(),
		"stats":      stats,
		"items":      items,
	}
	if err := m.writeArtifact(artifacts.querySet, retrievalTuningJSONDumps(querySetPayload, 2)+"\n"); err != nil {
		return err
	}

	baselineProfile := m.GetProfileSnapshot()
	baselineEval, err := m.evaluateProfile(ctx, baselineProfile, trainCases, objective, topKEval, evalTimeout, "stable")
	if err != nil {
		return err
	}
	baselineRound := &RetrievalTuningRoundRecord{
		RoundIndex:       0,
		CandidateProfile: baselineProfile,
		Metrics:          baselineEval.metrics,
		Score:            baselineEval.score,
		LatencyMS:        baselineEval.latencyMS,
		FailureSummary:   baselineEval.failure,
		CreatedAt:        m.nowFunc(),
	}
	m.appendRoundArtifact(artifacts.roundsJSONL, baselineRound)

	m.mu.Lock()
	task = m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return nil
	}
	task.QuerySetStats = retrievalTuningDeepCopy(stats)
	task.BaselineProfile = retrievalTuningDeepCopy(baselineProfile)
	task.BaselineMetrics = retrievalTuningDeepCopy(baselineEval.metrics)
	task.Rounds = append(task.Rounds, baselineRound)
	task.BestProfile = retrievalTuningDeepCopy(baselineProfile)
	task.BestMetrics = retrievalTuningDeepCopy(baselineEval.metrics)
	task.BestScore = baselineEval.score
	task.Progress = 0
	task.UpdatedAt = m.nowFunc()
	m.mu.Unlock()

	bestProfile := retrievalTuningDeepCopy(baselineProfile)
	bestMetrics := retrievalTuningDeepCopy(baselineEval.metrics)
	bestFailure := retrievalTuningDeepCopy(baselineEval.failure)
	bestScore := baselineEval.score
	var llmSuggestions []map[string]any
	taskCancelled := false

	for roundIndex := 1; roundIndex <= roundsTotal; roundIndex++ {
		m.mu.Lock()
		task = m.tasks[taskID]
		if task == nil {
			m.mu.Unlock()
			return nil
		}
		if task.CancelRequested || task.Status == "cancel_requested" {
			task.Status = "cancelled"
			finished := m.nowFunc()
			task.FinishedAt = &finished
			task.UpdatedAt = finished
			taskCancelled = true
			m.mu.Unlock()
			break
		}
		currentObjective := task.Objective
		currentIntensity := task.Intensity
		m.mu.Unlock()

		if roundIndex == 1 || (roundIndex%5 == 0 && len(llmSuggestions) == 0) {
			llmSuggestions = m.suggestProfilesWithLLM(ctx, bestProfile, bestFailure, currentObjective, 3, llmEnabled)
		}
		candidate := m.generateCandidateProfile(taskID, roundIndex, currentObjective, baselineProfile, bestProfile, &llmSuggestions)
		evalCases := SelectRetrievalTuningRoundCases(trainCases, currentIntensity, roundIndex, seed)
		evalResult, evalErr := m.evaluateProfile(ctx, candidate, evalCases, currentObjective, topKEval, evalTimeout, "stable")
		if evalErr != nil {
			return evalErr
		}
		roundRecord := &RetrievalTuningRoundRecord{
			RoundIndex:       roundIndex,
			CandidateProfile: candidate,
			Metrics:          evalResult.metrics,
			Score:            evalResult.score,
			LatencyMS:        evalResult.latencyMS,
			FailureSummary:   evalResult.failure,
			CreatedAt:        m.nowFunc(),
		}
		m.appendRoundArtifact(artifacts.roundsJSONL, roundRecord)
		if evalResult.score > bestScore {
			bestScore = evalResult.score
			bestProfile = retrievalTuningDeepCopy(candidate)
			bestMetrics = retrievalTuningDeepCopy(evalResult.metrics)
			bestFailure = retrievalTuningDeepCopy(evalResult.failure)
		}

		m.mu.Lock()
		task = m.tasks[taskID]
		if task == nil {
			m.mu.Unlock()
			return nil
		}
		task.RoundsDone = roundIndex
		task.Rounds = append(task.Rounds, roundRecord)
		task.BestProfile = retrievalTuningDeepCopy(bestProfile)
		task.BestMetrics = retrievalTuningDeepCopy(bestMetrics)
		task.BestScore = bestScore
		progress := 1.0
		if task.RoundsTotal > 0 {
			progress = math.Min(1.0, float64(roundIndex)/float64(task.RoundsTotal))
		}
		task.Progress = progress
		task.UpdatedAt = m.nowFunc()
		m.mu.Unlock()
	}

	m.mu.Lock()
	task = m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return nil
	}
	currentObjective := task.Objective
	m.mu.Unlock()

	if len(bestProfile) > 0 && !taskCancelled {
		bestFull, fullErr := m.evaluateProfile(ctx, bestProfile, trainCases, currentObjective, topKEval, evalTimeout, "stable")
		if fullErr != nil {
			return fullErr
		}
		bestMetrics = retrievalTuningDeepCopy(bestFull.metrics)
		bestFailure = retrievalTuningDeepCopy(bestFull.failure)
		bestScore = bestFull.score
		if bestScore < baselineEval.score {
			bestProfile = retrievalTuningDeepCopy(baselineProfile)
			bestMetrics = retrievalTuningDeepCopy(baselineEval.metrics)
			bestFailure = retrievalTuningDeepCopy(baselineEval.failure)
			bestScore = baselineEval.score
		}

		validationSummary, validationErr := m.validateBestProfile(ctx, baselineProfile, bestProfile, holdoutCases, currentObjective, topKEval, evalTimeout)
		if validationErr != nil {
			return validationErr
		}
		recommended := retrievalTuningCoerceBool(validationSummary["recommended"], false)
		onlineLike := retrievalTuningMapValue(validationSummary["online_like"])
		onlineBaseline := retrievalTuningMapValue(onlineLike["baseline"])
		onlineBest := retrievalTuningMapValue(onlineLike["best"])
		if recommended {
			if metrics := retrievalTuningMapValue(onlineBest["metrics"]); len(metrics) > 0 {
				bestMetrics = retrievalTuningDeepCopy(metrics)
			}
			if score, ok := retrievalTuningOptionalScore(onlineBest["score"]); ok {
				bestScore = score
			}
		} else {
			bestProfile = retrievalTuningDeepCopy(baselineProfile)
			if metrics := retrievalTuningMapValue(onlineBaseline["metrics"]); len(metrics) > 0 {
				bestMetrics = retrievalTuningDeepCopy(metrics)
			} else {
				bestMetrics = retrievalTuningDeepCopy(baselineEval.metrics)
			}
			if score, ok := retrievalTuningOptionalScore(onlineBaseline["score"]); ok {
				bestScore = score
			} else {
				bestScore = baselineEval.score
			}
		}
		baselineMetricsForReport := retrievalTuningDeepCopy(baselineEval.metrics)
		if metrics := retrievalTuningMapValue(onlineBaseline["metrics"]); len(metrics) > 0 {
			baselineMetricsForReport = retrievalTuningDeepCopy(metrics)
		}

		m.mu.Lock()
		if task = m.tasks[taskID]; task != nil {
			task.BestProfile = retrievalTuningDeepCopy(bestProfile)
			task.BestMetrics = retrievalTuningDeepCopy(bestMetrics)
			task.BestScore = bestScore
			task.BaselineMetrics = baselineMetricsForReport
			task.ValidationSummary = retrievalTuningDeepCopy(validationSummary)
			task.Recommended = recommended
			task.UpdatedAt = m.nowFunc()
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	task = m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return nil
	}
	if task.Status != "cancelled" && task.Status != "failed" {
		task.Status = "completed"
		task.Progress = 1
		finished := m.nowFunc()
		task.FinishedAt = &finished
		task.UpdatedAt = finished
	}
	finalTask := cloneRetrievalTuningTask(task)
	m.mu.Unlock()

	if finalTask.Status == "completed" {
		if err := m.writeArtifact(artifacts.bestProfile, retrievalTuningJSONDumps(finalTask.BestProfile, 2)+"\n"); err != nil {
			return err
		}
		reportPayload := BuildRetrievalTuningReportPayload(finalTask)
		if err := m.writeArtifact(artifacts.reportJSON, retrievalTuningJSONDumps(reportPayload, 2)+"\n"); err != nil {
			return err
		}
		if err := m.writeArtifact(artifacts.reportMD, BuildRetrievalTuningReportMarkdown(finalTask, reportPayload)); err != nil {
			return err
		}
	}

	m.mu.Lock()
	if task = m.tasks[taskID]; task != nil {
		task.ArtifactPaths = artifacts.paths()
		task.UpdatedAt = m.nowFunc()
	}
	m.mu.Unlock()
	return nil
}

// retrievalTuningOptionalScore 读取可选的得分值
func retrievalTuningOptionalScore(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// cloneRetrievalTuningTask 深拷贝任务，避免产物构建期间与后台更新竞争
func cloneRetrievalTuningTask(task *RetrievalTuningTaskRecord) *RetrievalTuningTaskRecord {
	clone := *task
	clone.BestProfile = retrievalTuningDeepCopy(task.BestProfile)
	clone.BestMetrics = retrievalTuningDeepCopy(task.BestMetrics)
	clone.BaselineProfile = retrievalTuningDeepCopy(task.BaselineProfile)
	clone.BaselineMetrics = retrievalTuningDeepCopy(task.BaselineMetrics)
	clone.ValidationSummary = retrievalTuningDeepCopy(task.ValidationSummary)
	clone.QuerySetStats = retrievalTuningDeepCopy(task.QuerySetStats)
	if task.Params != nil {
		clone.Params = retrievalTuningDeepCopy(task.Params)
	}
	if task.Rounds != nil {
		clone.Rounds = make([]*RetrievalTuningRoundRecord, len(task.Rounds))
		copy(clone.Rounds, task.Rounds)
	}
	if task.ApplyLog != nil {
		clone.ApplyLog = make([]map[string]any, len(task.ApplyLog))
		for index, item := range task.ApplyLog {
			clone.ApplyLog[index] = retrievalTuningDeepCopy(item)
		}
	}
	if task.ArtifactPaths != nil {
		clone.ArtifactPaths = map[string]string{}
		for key, value := range task.ArtifactPaths {
			clone.ArtifactPaths[key] = value
		}
	}
	if task.StartedAt != nil {
		value := *task.StartedAt
		clone.StartedAt = &value
	}
	if task.FinishedAt != nil {
		value := *task.FinishedAt
		clone.FinishedAt = &value
	}
	return &clone
}

// evaluateProfile 对齐 _evaluate_profile 的可注入实现
func (m *RetrievalTuningManager) evaluateProfile(ctx context.Context, profile map[string]any, cases []*RetrievalQueryCase, objective string, topKEval int, queryTimeoutSeconds float64, evaluationMode string) (retrievalTuningEvaluation, error) {
	if m.evaluator == nil {
		return retrievalTuningEvaluation{}, fmt.Errorf("检索评估器未就绪")
	}
	normalized := m.NormalizeProfile(profile)
	mode := pyLower(pyStrip(evaluationMode))
	if mode != "stable" && mode != "online_like" {
		mode = "stable"
	}
	result, err := m.evaluator.EvaluateRetrievalProfile(ctx, RetrievalTuningEvaluationRequest{
		Profile:             normalized,
		Cases:               cases,
		Objective:           objective,
		TopKEval:            retrievalTuningClampInt(topKEval, 20, 1, 1000),
		QueryTimeoutSeconds: retrievalTuningClampFloat(queryTimeoutSeconds, m.evalQueryTimeoutSeconds(), 0.01, 120.0),
		EvaluationMode:      mode,
	})
	if err != nil {
		return retrievalTuningEvaluation{}, err
	}
	metrics := result.Metrics
	if metrics == nil {
		metrics = map[string]any{}
	}
	failure := result.FailureSummary
	if failure == nil {
		failure = map[string]any{}
	}
	return retrievalTuningEvaluation{
		metrics:   metrics,
		score:     RetrievalTuningScoreMetrics(metrics, objective),
		latencyMS: result.AvgElapsedMS,
		failure:   failure,
	}, nil
}

// BuildRetrievalTuningStableConfig 对齐 _build_runtime_config 的纯合并部分
// 插件实例与向量库注入由调用方负责，stable 模式强制关闭并行与 PPR
func BuildRetrievalTuningStableConfig(base map[string]any, profile map[string]any, evaluationMode string) map[string]any {
	merged := map[string]any{}
	if base != nil {
		merged = retrievalTuningDeepCopy(base)
	}
	for _, key := range RetrievalTuningRuntimeConfigInstanceKeys() {
		delete(merged, key)
	}
	merged = retrievalTuningDeepMerge(merged, profile)
	if pyLower(pyStrip(evaluationMode)) == "" || pyLower(pyStrip(evaluationMode)) == "stable" {
		retrievalTuningNestedSet(merged, "retrieval.enable_parallel", false)
		retrievalTuningNestedSet(merged, "retrieval.enable_ppr", false)
	}
	return merged
}
