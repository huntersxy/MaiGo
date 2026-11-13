package utils

// A_memorix 检索调优任务记录与报告，对齐 Python core/utils/retrieval_tuning_manager.py
// 记录字段、summary/detail 键集合、报告章节文案与 Python 保持一致

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// RetrievalQueryCase 检索调优测试用例，对齐 RetrievalQueryCase
type RetrievalQueryCase struct {
	CaseID         string
	Category       string
	Query          string
	ExpectedHashes []string
	ExpectedSPO    map[string]string
	Metadata       map[string]any
}

// NewRetrievalQueryCase 构造用例并补齐默认容器
func NewRetrievalQueryCase(caseID, category, query string) *RetrievalQueryCase {
	return &RetrievalQueryCase{
		CaseID:         caseID,
		Category:       category,
		Query:          query,
		ExpectedHashes: []string{},
		ExpectedSPO:    map[string]string{},
		Metadata:       map[string]any{},
	}
}

// ToDict 对齐 to_dict
func (c *RetrievalQueryCase) ToDict() map[string]any {
	hashes := make([]any, 0, len(c.ExpectedHashes))
	for _, hash := range c.ExpectedHashes {
		hashes = append(hashes, hash)
	}
	spo := make(map[string]any, len(c.ExpectedSPO))
	for key, value := range c.ExpectedSPO {
		spo[key] = value
	}
	return map[string]any{
		"case_id":         c.CaseID,
		"category":        c.Category,
		"query":           c.Query,
		"expected_hashes": hashes,
		"expected_spo":    spo,
		"metadata":        retrievalTuningDeepCopy(c.Metadata),
	}
}

// RetrievalTuningRoundRecord 单轮调优记录，对齐 RetrievalTuningRoundRecord
type RetrievalTuningRoundRecord struct {
	RoundIndex       int
	CandidateProfile map[string]any
	Metrics          map[string]any
	Score            float64
	LatencyMS        float64
	FailureSummary   map[string]any
	CreatedAt        float64
}

// ToDict 对齐 to_dict
func (r *RetrievalTuningRoundRecord) ToDict() map[string]any {
	failureSummary := r.FailureSummary
	if failureSummary == nil {
		failureSummary = map[string]any{}
	}
	return map[string]any{
		"round_index":       r.RoundIndex,
		"candidate_profile": retrievalTuningDeepCopy(r.CandidateProfile),
		"metrics":           retrievalTuningDeepCopy(r.Metrics),
		"score":             r.Score,
		"latency_ms":        r.LatencyMS,
		"failure_summary":   retrievalTuningDeepCopy(failureSummary),
		"created_at":        r.CreatedAt,
	}
}

// RetrievalTuningTaskRecord 调优任务记录，对齐 RetrievalTuningTaskRecord
type RetrievalTuningTaskRecord struct {
	TaskID            string
	Status            string
	Progress          float64
	Objective         string
	Intensity         string
	RoundsTotal       int
	RoundsDone        int
	BestProfile       map[string]any
	BestMetrics       map[string]any
	BestScore         float64
	BaselineProfile   map[string]any
	BaselineMetrics   map[string]any
	ValidationSummary map[string]any
	Recommended       bool
	Error             string
	Params            map[string]any
	QuerySetStats     map[string]any
	ArtifactPaths     map[string]string
	Rounds            []*RetrievalTuningRoundRecord
	CancelRequested   bool
	CreatedAt         float64
	StartedAt         *float64
	FinishedAt        *float64
	UpdatedAt         float64
	ApplyLog          []map[string]any
}

// NewRetrievalTuningTaskRecord 构造任务记录，默认值与 Python dataclass 一致
func NewRetrievalTuningTaskRecord(taskID, objective, intensity string, roundsTotal int, createdAt float64) *RetrievalTuningTaskRecord {
	return &RetrievalTuningTaskRecord{
		TaskID:            taskID,
		Status:            "queued",
		Progress:          0.0,
		Objective:         objective,
		Intensity:         intensity,
		RoundsTotal:       roundsTotal,
		RoundsDone:        0,
		BestProfile:       map[string]any{},
		BestMetrics:       map[string]any{},
		BestScore:         -1.0,
		BaselineProfile:   map[string]any{},
		BaselineMetrics:   map[string]any{},
		ValidationSummary: map[string]any{},
		Recommended:       false,
		Error:             "",
		Params:            map[string]any{},
		QuerySetStats:     map[string]any{},
		ArtifactPaths:     map[string]string{},
		Rounds:            []*RetrievalTuningRoundRecord{},
		CancelRequested:   false,
		CreatedAt:         createdAt,
		UpdatedAt:         createdAt,
		ApplyLog:          []map[string]any{},
	}
}

// ToSummary 对齐 to_summary
func (t *RetrievalTuningTaskRecord) ToSummary() map[string]any {
	querySetStats := t.QuerySetStats
	if querySetStats == nil {
		querySetStats = map[string]any{}
	}
	artifactPaths := make(map[string]any, len(t.ArtifactPaths))
	for key, value := range t.ArtifactPaths {
		artifactPaths[key] = value
	}
	summary := map[string]any{
		"task_id":            t.TaskID,
		"status":             t.Status,
		"progress":           t.Progress,
		"objective":          t.Objective,
		"intensity":          t.Intensity,
		"rounds_total":       t.RoundsTotal,
		"rounds_done":        t.RoundsDone,
		"best_score":         t.BestScore,
		"recommended":        t.Recommended,
		"validation_summary": retrievalTuningDeepCopy(t.ValidationSummary),
		"error":              t.Error,
		"query_set_stats":    retrievalTuningDeepCopy(querySetStats),
		"artifact_paths":     artifactPaths,
		"created_at":         t.CreatedAt,
		"started_at":         optionalFloatValue(t.StartedAt),
		"finished_at":        optionalFloatValue(t.FinishedAt),
		"updated_at":         t.UpdatedAt,
	}
	return summary
}

// ToDetail 对齐 to_detail，includeRounds 为真时附带逐轮记录
func (t *RetrievalTuningTaskRecord) ToDetail(includeRounds bool) map[string]any {
	payload := t.ToSummary()
	applyLog := make([]any, 0, len(t.ApplyLog))
	for _, item := range t.ApplyLog {
		applyLog = append(applyLog, retrievalTuningDeepCopy(item))
	}
	payload["params"] = retrievalTuningDeepCopy(t.Params)
	payload["best_profile"] = retrievalTuningDeepCopy(t.BestProfile)
	payload["best_metrics"] = retrievalTuningDeepCopy(t.BestMetrics)
	payload["baseline_profile"] = retrievalTuningDeepCopy(t.BaselineProfile)
	payload["baseline_metrics"] = retrievalTuningDeepCopy(t.BaselineMetrics)
	payload["recommended"] = t.Recommended
	payload["validation_summary"] = retrievalTuningDeepCopy(t.ValidationSummary)
	payload["apply_log"] = applyLog
	if includeRounds {
		rounds := make([]any, 0, len(t.Rounds))
		for _, record := range t.Rounds {
			rounds = append(rounds, record.ToDict())
		}
		payload["rounds"] = rounds
	}
	return payload
}

// optionalFloatValue 对齐 Optional[float] 到 JSON 的转换
func optionalFloatValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// BuildRetrievalTuningReportPayload 对齐 _build_report_payload
func BuildRetrievalTuningReportPayload(task *RetrievalTuningTaskRecord) map[string]any {
	baseline := task.BaselineMetrics
	if baseline == nil {
		baseline = map[string]any{}
	}
	best := task.BestMetrics
	if best == nil {
		best = map[string]any{}
	}
	delta := func(name string) float64 {
		return retrievalTuningMetricFloat(best, name) - retrievalTuningMetricFloat(baseline, name)
	}
	influencers := make([]any, 0, 12)
	for _, item := range RetrievalTuningNonTunedInfluencers() {
		influencers = append(influencers, item)
	}
	applyLog := make([]any, 0, len(task.ApplyLog))
	for _, item := range task.ApplyLog {
		applyLog = append(applyLog, retrievalTuningDeepCopy(item))
	}
	return map[string]any{
		"task_id":            task.TaskID,
		"objective":          task.Objective,
		"intensity":          task.Intensity,
		"status":             task.Status,
		"created_at":         task.CreatedAt,
		"started_at":         optionalFloatValue(task.StartedAt),
		"finished_at":        optionalFloatValue(task.FinishedAt),
		"rounds_total":       task.RoundsTotal,
		"rounds_done":        task.RoundsDone,
		"best_score":         task.BestScore,
		"baseline_score":     RetrievalTuningScoreMetrics(baseline, task.Objective),
		"recommended":        task.Recommended,
		"validation_summary": retrievalTuningDeepCopy(task.ValidationSummary),
		"query_set_stats":    retrievalTuningDeepCopy(task.QuerySetStats),
		"baseline_metrics":   retrievalTuningDeepCopy(baseline),
		"best_metrics":       retrievalTuningDeepCopy(best),
		"deltas": map[string]any{
			"precision_at_1":        delta("precision_at_1"),
			"precision_at_3":        delta("precision_at_3"),
			"mrr":                   delta("mrr"),
			"recall_at_k":           delta("recall_at_k"),
			"spo_relation_hit_rate": delta("spo_relation_hit_rate"),
			"empty_rate":            delta("empty_rate"),
			"timeout_count":         delta("timeout_count"),
			"avg_elapsed_ms":        delta("avg_elapsed_ms"),
		},
		"best_profile":                    retrievalTuningDeepCopy(task.BestProfile),
		"baseline_profile":                retrievalTuningDeepCopy(task.BaselineProfile),
		"profile_diff":                    RetrievalTuningProfileDiff(task.BaselineProfile, task.BestProfile),
		"non_tuned_retrieval_influencers": influencers,
		"apply_log":                       applyLog,
	}
}

// retrievalTuningMetricFloat 读取指标中的浮点值，缺失按 0 处理
func retrievalTuningMetricFloat(metrics map[string]any, name string) float64 {
	value, ok := metrics[name]
	if !ok || value == nil {
		return 0
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return parsed
}

// BuildRetrievalTuningReportMarkdown 对齐 _build_report_markdown
func BuildRetrievalTuningReportMarkdown(task *RetrievalTuningTaskRecord, payload map[string]any) string {
	baseline := retrievalTuningMapValue(payload["baseline_metrics"])
	best := retrievalTuningMapValue(payload["best_metrics"])
	deltas := retrievalTuningMapValue(payload["deltas"])
	validation := retrievalTuningMapValue(payload["validation_summary"])
	recommended := retrievalTuningCoerceBool(payload["recommended"], false)

	formatTime := func(value *float64) string {
		if value == nil {
			return "-"
		}
		return time.Unix(int64(*value), 0).Local().Format("2006-01-02 15:04:05")
	}
	yesNo := "否"
	if recommended {
		yesNo = "是"
	}
	validationResult := "未通过"
	if retrievalTuningCoerceBool(validation["recommended"], false) {
		validationResult = "通过"
	}
	reason := retrievalTuningStringValue(validation["reason"])
	if reason == "" {
		reason = "-"
	}
	lines := []string{
		fmt.Sprintf("# 检索调优报告（%s）", task.TaskID),
		"",
		"## 1. 任务信息",
		fmt.Sprintf("- 状态: %s", task.Status),
		fmt.Sprintf("- 目标函数: %s", task.Objective),
		fmt.Sprintf("- 强度: %s", task.Intensity),
		fmt.Sprintf("- 轮次: baseline + %d", task.RoundsTotal),
		fmt.Sprintf("- 建议应用: %s", yesNo),
		fmt.Sprintf("- 创建时间: %s", formatTime(&task.CreatedAt)),
		fmt.Sprintf("- 开始时间: %s", formatTime(task.StartedAt)),
		fmt.Sprintf("- 完成时间: %s", formatTime(task.FinishedAt)),
		"",
		"## 2. 基线 vs 最优",
		fmt.Sprintf("- baseline score: %.6f", retrievalTuningPayloadFloat(payload, "baseline_score")),
		fmt.Sprintf("- best score: %.6f", task.BestScore),
		fmt.Sprintf("- P@1: %.4f -> %.4f (Δ %+.4f)", retrievalTuningMetricFloat(baseline, "precision_at_1"), retrievalTuningMetricFloat(best, "precision_at_1"), retrievalTuningMetricFloat(deltas, "precision_at_1")),
		fmt.Sprintf("- P@3: %.4f -> %.4f (Δ %+.4f)", retrievalTuningMetricFloat(baseline, "precision_at_3"), retrievalTuningMetricFloat(best, "precision_at_3"), retrievalTuningMetricFloat(deltas, "precision_at_3")),
		fmt.Sprintf("- MRR: %.4f -> %.4f (Δ %+.4f)", retrievalTuningMetricFloat(baseline, "mrr"), retrievalTuningMetricFloat(best, "mrr"), retrievalTuningMetricFloat(deltas, "mrr")),
		fmt.Sprintf("- Recall@K: %.4f -> %.4f (Δ %+.4f)", retrievalTuningMetricFloat(baseline, "recall_at_k"), retrievalTuningMetricFloat(best, "recall_at_k"), retrievalTuningMetricFloat(deltas, "recall_at_k")),
		fmt.Sprintf("- SPO relation hit: %.4f -> %.4f (Δ %+.4f)", retrievalTuningMetricFloat(baseline, "spo_relation_hit_rate"), retrievalTuningMetricFloat(best, "spo_relation_hit_rate"), retrievalTuningMetricFloat(deltas, "spo_relation_hit_rate")),
		fmt.Sprintf("- 空结果率: %.4f -> %.4f (Δ %+.4f)", retrievalTuningMetricFloat(baseline, "empty_rate"), retrievalTuningMetricFloat(best, "empty_rate"), retrievalTuningMetricFloat(deltas, "empty_rate")),
		fmt.Sprintf("- 超时数: %d -> %d (Δ %+d)", retrievalTuningMetricInt(baseline, "timeout_count"), retrievalTuningMetricInt(best, "timeout_count"), retrievalTuningMetricInt(deltas, "timeout_count")),
		fmt.Sprintf("- 平均耗时(ms): %.2f -> %.2f (Δ %+.2f)", retrievalTuningMetricFloat(baseline, "avg_elapsed_ms"), retrievalTuningMetricFloat(best, "avg_elapsed_ms"), retrievalTuningMetricFloat(deltas, "avg_elapsed_ms")),
		"",
		"## 3. 验证摘要",
		fmt.Sprintf("- holdout 样本数: %d", retrievalTuningMetricInt(validation, "holdout_case_count")),
		fmt.Sprintf("- 验证结果: %s", validationResult),
		fmt.Sprintf("- 原因: %s", reason),
		fmt.Sprintf("- 指标变化: %s", retrievalTuningJSONDumps(validation["deltas"], 0)),
		fmt.Sprintf("- 检查项: %s", retrievalTuningJSONDumps(validation["checks"], 0)),
		"",
		"## 4. 最优参数",
		"```json",
		retrievalTuningJSONDumps(task.BestProfile, 2),
		"```",
		"",
		"## 5. 参数变化",
		"```json",
		retrievalTuningJSONDumps(payload["profile_diff"], 2),
		"```",
		"",
		"## 6. 测试集规模",
		fmt.Sprintf("- %s", retrievalTuningJSONDumps(task.QuerySetStats, 0)),
		"",
		"## 7. 说明",
		"- 本报告仅对当前已存储图谱与向量状态有效。",
		"- 参数应用策略：默认只热应用到运行时，不自动写入 config.toml。",
		fmt.Sprintf("- 未参与自动调优但影响召回的参数: %s", strings.Join(RetrievalTuningNonTunedInfluencers(), ", ")),
	}
	return pyStrip(strings.Join(lines, "\n")) + "\n"
}

// retrievalTuningMapValue 取出字典值，非字典返回空字典
func retrievalTuningMapValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

// retrievalTuningPayloadFloat 读取载荷中的浮点项
func retrievalTuningPayloadFloat(payload map[string]any, key string) float64 {
	value, ok := payload[key]
	if !ok || value == nil {
		return 0
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return parsed
}

// retrievalTuningStringValue 取出字符串值
func retrievalTuningStringValue(value any) string {
	if value == nil {
		return ""
	}
	return pyStr(value)
}

// retrievalTuningMetricInt 读取指标中的整数值，对齐 int(value or 0)
func retrievalTuningMetricInt(metrics map[string]any, name string) int {
	value, ok := metrics[name]
	if !ok || value == nil {
		return 0
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return int(parsed)
}

// retrievalTuningJSONDumps 近似 json.dumps(value, ensure_ascii=False, indent=indent)
// Go 字典无序，这里按键排序输出；indent 为 0 时使用 ", " 与 ": " 分隔
func retrievalTuningJSONDumps(value any, indent int) string {
	var builder strings.Builder
	retrievalTuningWriteJSON(&builder, value, indent, 0)
	return builder.String()
}

func retrievalTuningWriteJSON(builder *strings.Builder, value any, indent int, depth int) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		retrievalTuningWriteJSONString(builder, typed)
	case float64:
		builder.WriteString(pyFloatRepr(typed))
	case float32:
		builder.WriteString(pyFloatRepr(float64(typed)))
	case int:
		builder.WriteString(strconv.Itoa(typed))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case map[string]any:
		retrievalTuningWriteJSONObject(builder, typed, indent, depth)
	case []any:
		retrievalTuningWriteJSONArray(builder, typed, indent, depth)
	case []string:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		retrievalTuningWriteJSONArray(builder, items, indent, depth)
	default:
		builder.WriteString(pyStr(typed))
	}
}

func retrievalTuningWriteJSONObject(builder *strings.Builder, value map[string]any, indent int, depth int) {
	if len(value) == 0 {
		builder.WriteString("{}")
		return
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	builder.WriteString("{")
	for index, key := range keys {
		if index > 0 {
			builder.WriteString(", ")
		}
		if indent > 0 {
			builder.WriteString("\n")
			builder.WriteString(strings.Repeat(" ", indent*(depth+1)))
		}
		retrievalTuningWriteJSONString(builder, key)
		builder.WriteString(": ")
		retrievalTuningWriteJSON(builder, value[key], indent, depth+1)
	}
	if indent > 0 {
		builder.WriteString("\n")
		builder.WriteString(strings.Repeat(" ", indent*depth))
	}
	builder.WriteString("}")
}

func retrievalTuningWriteJSONArray(builder *strings.Builder, value []any, indent int, depth int) {
	if len(value) == 0 {
		builder.WriteString("[]")
		return
	}
	builder.WriteString("[")
	for index, item := range value {
		if index > 0 {
			builder.WriteString(", ")
		}
		if indent > 0 {
			builder.WriteString("\n")
			builder.WriteString(strings.Repeat(" ", indent*(depth+1)))
		}
		retrievalTuningWriteJSON(builder, item, indent, depth+1)
	}
	if indent > 0 {
		builder.WriteString("\n")
		builder.WriteString(strings.Repeat(" ", indent*depth))
	}
	builder.WriteString("]")
}

// retrievalTuningWriteJSONString 对齐 ensure_ascii=False 的字符串转义
func retrievalTuningWriteJSONString(builder *strings.Builder, text string) {
	builder.WriteString("\"")
	for _, char := range text {
		switch char {
		case '"':
			builder.WriteString("\"")
		case '\\':
			builder.WriteString("\\")
		case '\n':
			builder.WriteString("\n")
		case '':
			builder.WriteString("\r")
		case '	':
			builder.WriteString("\t")
		case '':
			builder.WriteString("\b")
		case '':
			builder.WriteString("\f")
		default:
			if char < 0x20 {
				fmt.Fprintf(builder, "\\u%04x", char)
				continue
			}
			if char == utf8.RuneError {
				builder.WriteString("�")
				continue
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteString("\"")
}

// retrievalTuningRound1 对齐 round(value, 1)
func retrievalTuningRound1(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	return pyRound(value, 1)
}
