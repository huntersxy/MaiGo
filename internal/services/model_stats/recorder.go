package modelstats

import (
	"context"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	statschema "MaiGo/internal/services/statistics/schema"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	callbacktpl "github.com/cloudwego/eino/utils/callbacks"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type key struct {
	Bucket time.Time
	Task   string
	Model  string
}

type aggregate struct {
	RequestCount       int64
	FailureCount       int64
	LatencyMSSum       int64
	PromptTokens       int64
	CompletionTokens   int64
	TotalTokens        int64
	UsageReportedCount int64
}

type usageRecord struct {
	Timestamp        time.Time
	Task             string
	Model            string
	ModelAssignName  string
	ModelIdentifier  string
	Provider         string
	SessionID        string
	RequestType      string
	DurationSeconds  float64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheEnabled     bool
	CacheHitTokens   int64
	CacheMissTokens  int64
	Cost             float64
}

type taskUsageRecord struct {
	Timestamp       time.Time
	Task            string
	Model           string
	ModelIdentifier string
	Provider        string
	DurationSeconds float64
	Succeeded       bool
}

// ModelUsageRecord 一次实际模型调用的完整用量；字段对齐 MaiBot LLMUsageRecorder
type ModelUsageRecord struct {
	Timestamp        time.Time
	Task             string
	Model            string // 模型分配名/展示名，写入 llm_usage.model_assign_name 与 model_call_hourly.model
	ModelIdentifier  string // API 模型标识符，写入 llm_usage.model_name
	Provider         string
	SessionID        string
	RequestType      string
	Duration         time.Duration
	Failed           bool
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheEnabled     bool
	CacheHitTokens   int64
	CacheMissTokens  int64
	Cost             float64
	UsageReported    bool
}

// ModelTaskUsageRecord 一次实际 Provider 调用的任务统计；失败与取消也计入尝试
type ModelTaskUsageRecord struct {
	Timestamp       time.Time
	Task            string
	Model           string
	ModelIdentifier string
	Provider        string
	Duration        time.Duration
	Succeeded       bool
}

type Recorder struct {
	mu               sync.Mutex
	pending          map[key]aggregate
	pendingUsage     []usageRecord
	pendingTaskUsage []taskUsageRecord
	db               *gorm.DB
	stop             chan struct{}
	done             chan struct{}
	once             sync.Once
}

var defaultRecorder atomic.Pointer[Recorder]

func NewRecorder() *Recorder {
	return &Recorder{pending: make(map[key]aggregate), stop: make(chan struct{}), done: make(chan struct{})}
}

func SetDefault(recorder *Recorder) { defaultRecorder.Store(recorder) }

// Default 返回当前进程默认记录器；未初始化时为 nil
func Default() *Recorder { return defaultRecorder.Load() }

func (r *Recorder) Start(db *gorm.DB) {
	if r == nil || db == nil {
		return
	}
	r.db = db
	if err := statschema.Ensure(context.Background(), db); err != nil {
		zap.L().Warn("初始化模型统计表失败", zap.Error(err))
	}
	if err := ensureModelTaskUsageTable(db); err != nil {
		zap.L().Warn("初始化模型任务统计表失败", zap.Error(err))
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		defer close(r.done)
		for {
			select {
			case <-ticker.C:
				r.flush()
			case <-r.stop:
				r.flush()
				return
			}
		}
	}()
}

func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
	if r.db != nil {
		<-r.done
	}
}

func (r *Recorder) Record(task, modelName string, duration time.Duration, failed bool, totalTokens int, usageReported bool) {
	r.RecordUsage(task, modelName, duration, failed, 0, 0, totalTokens, 0, usageReported)
}

// RecordUsage 记录一次模型调用，额外保存 prompt/completion/cache 明细
func (r *Recorder) RecordUsage(task, modelName string, duration time.Duration, failed bool, promptTokens, completionTokens, totalTokens, cacheHitTokens int, usageReported bool) {
	cacheEnabled := cacheHitTokens > 0
	cacheMiss := 0
	if cacheEnabled && promptTokens > cacheHitTokens {
		cacheMiss = promptTokens - cacheHitTokens
	}
	r.RecordModelUsage(ModelUsageRecord{
		Timestamp:        time.Now(),
		Task:             task,
		Model:            modelName,
		ModelIdentifier:  modelName,
		RequestType:      task,
		Duration:         duration,
		Failed:           failed,
		PromptTokens:     int64(promptTokens),
		CompletionTokens: int64(completionTokens),
		TotalTokens:      int64(totalTokens),
		CacheEnabled:     cacheEnabled,
		CacheHitTokens:   int64(cacheHitTokens),
		CacheMissTokens:  int64(cacheMiss),
		UsageReported:    usageReported,
	})
}

// RecordModelUsage 记录一次完整模型调用；成功响应上报 usage 时同时写 llm_usage 明细
func (r *Recorder) RecordModelUsage(record ModelUsageRecord) {
	if r == nil {
		return
	}
	task := strings.TrimSpace(record.Task)
	model := strings.TrimSpace(record.Model)
	identifier := strings.TrimSpace(record.ModelIdentifier)
	if model == "" {
		model = identifier
	}
	if identifier == "" {
		identifier = model
	}
	if task == "" || model == "" {
		return
	}
	now := record.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	duration := record.Duration
	if duration < 0 {
		duration = 0
	}
	seconds := math.RoundToEven(duration.Seconds()*1000) / 1000
	k := key{Bucket: now.UTC().Truncate(time.Hour), Task: task, Model: model}
	r.mu.Lock()
	item := r.pending[k]
	item.RequestCount++
	if record.Failed {
		item.FailureCount++
	}
	item.LatencyMSSum += duration.Milliseconds()
	if record.UsageReported {
		item.PromptTokens += record.PromptTokens
		item.CompletionTokens += record.CompletionTokens
		item.TotalTokens += record.TotalTokens
		item.UsageReportedCount++
	}
	r.pending[k] = item
	if record.UsageReported {
		r.pendingUsage = append(r.pendingUsage, usageRecord{
			Timestamp:        now,
			Task:             task,
			Model:            model,
			ModelAssignName:  model,
			ModelIdentifier:  identifier,
			Provider:         strings.TrimSpace(record.Provider),
			SessionID:        strings.TrimSpace(record.SessionID),
			RequestType:      strings.TrimSpace(record.RequestType),
			DurationSeconds:  seconds,
			PromptTokens:     record.PromptTokens,
			CompletionTokens: record.CompletionTokens,
			TotalTokens:      record.TotalTokens,
			CacheEnabled:     record.CacheEnabled,
			CacheHitTokens:   record.CacheHitTokens,
			CacheMissTokens:  record.CacheMissTokens,
			Cost:             record.Cost,
		})
	}
	r.mu.Unlock()
}

// RecordModelTaskUsage 记录一次实际 Provider 调用；失败、取消与重试均单独计数
func (r *Recorder) RecordModelTaskUsage(record ModelTaskUsageRecord) {
	if r == nil {
		return
	}
	task := strings.TrimSpace(record.Task)
	model := strings.TrimSpace(record.Model)
	identifier := strings.TrimSpace(record.ModelIdentifier)
	provider := strings.TrimSpace(record.Provider)
	if model == "" {
		model = identifier
	}
	if identifier == "" {
		identifier = model
	}
	if task == "" || model == "" {
		return
	}
	now := record.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	duration := record.Duration
	if duration < 0 {
		duration = 0
	}
	r.mu.Lock()
	r.pendingTaskUsage = append(r.pendingTaskUsage, taskUsageRecord{
		Timestamp:       now,
		Task:            task,
		Model:           model,
		ModelIdentifier: identifier,
		Provider:        provider,
		DurationSeconds: duration.Seconds(),
		Succeeded:       record.Succeeded,
	})
	r.mu.Unlock()
}

func (r *Recorder) flush() {
	if r == nil || r.db == nil {
		return
	}
	r.mu.Lock()
	batch := r.pending
	batchUsage := r.pendingUsage
	batchTaskUsage := r.pendingTaskUsage
	r.pending = make(map[key]aggregate)
	r.pendingUsage = nil
	r.pendingTaskUsage = nil
	r.mu.Unlock()
	if len(batch) == 0 && len(batchUsage) == 0 && len(batchTaskUsage) == 0 {
		return
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		for k, item := range batch {
			if err := tx.Exec(`INSERT INTO model_call_hourly(bucket_start,task,model,request_count,failure_count,latency_ms_sum,prompt_tokens,completion_tokens,total_tokens,usage_reported_count)
				VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(bucket_start,task,model) DO UPDATE SET
				request_count=model_call_hourly.request_count+EXCLUDED.request_count,
				failure_count=model_call_hourly.failure_count+EXCLUDED.failure_count,
				latency_ms_sum=model_call_hourly.latency_ms_sum+EXCLUDED.latency_ms_sum,
				prompt_tokens=model_call_hourly.prompt_tokens+EXCLUDED.prompt_tokens,
				completion_tokens=model_call_hourly.completion_tokens+EXCLUDED.completion_tokens,
				total_tokens=model_call_hourly.total_tokens+EXCLUDED.total_tokens,
				usage_reported_count=model_call_hourly.usage_reported_count+EXCLUDED.usage_reported_count`,
				k.Bucket, k.Task, k.Model, item.RequestCount, item.FailureCount, item.LatencyMSSum,
				item.PromptTokens, item.CompletionTokens, item.TotalTokens, item.UsageReportedCount).Error; err != nil {
				return err
			}
		}
		for _, usage := range batchUsage {
			if err := tx.Exec(`INSERT INTO llm_usage(model_name,model_assign_name,model_api_provider_name,session_id,task_name,request_type,time_cost,timestamp,prompt_tokens,completion_tokens,total_tokens,prompt_cache_enabled,prompt_cache_hit_tokens,prompt_cache_miss_tokens,cost)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				usage.ModelIdentifier, usage.ModelAssignName, usage.Provider, usage.SessionID, usage.Task, usage.RequestType,
				usage.DurationSeconds, usage.Timestamp, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
				usage.CacheEnabled, usage.CacheHitTokens, usage.CacheMissTokens, usage.Cost).Error; err != nil {
				return err
			}
		}
		for _, taskUsage := range batchTaskUsage {
			failureCount := 0
			successDuration := 0.0
			if taskUsage.Succeeded {
				successDuration = taskUsage.DurationSeconds
			} else {
				failureCount = 1
			}
			if err := tx.Exec(`INSERT INTO model_task_usage(bucket_time,task_name,model_name,model_identifier,provider_name,request_count,failure_count,success_duration)
				VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(bucket_time,task_name,model_name,model_identifier,provider_name) DO UPDATE SET
				request_count=model_task_usage.request_count+1,
				failure_count=model_task_usage.failure_count+EXCLUDED.failure_count,
				success_duration=model_task_usage.success_duration+EXCLUDED.success_duration`,
				taskUsage.Timestamp.Truncate(time.Minute), taskUsage.Task, taskUsage.Model, taskUsage.ModelIdentifier, taskUsage.Provider,
				1, failureCount, successDuration).Error; err != nil {
				return err
			}
		}
		if len(batchTaskUsage) > 0 {
			// 对齐 MaiBot model_task_usage_service：每次记录后清理 7 天前的桶
			cutoff := time.Now().Add(-7 * 24 * time.Hour)
			if err := tx.Exec(`DELETE FROM model_task_usage WHERE bucket_time < ?`, cutoff).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return
	}
	zap.L().Warn("写入模型调用统计失败，将保留到下次重试", zap.Error(err))
	r.mu.Lock()
	for k, item := range batch {
		current := r.pending[k]
		mergeAggregate(&current, item)
		r.pending[k] = current
	}
	r.pendingUsage = append(batchUsage, r.pendingUsage...)
	r.pendingTaskUsage = append(batchTaskUsage, r.pendingTaskUsage...)
	r.mu.Unlock()
}

// ensureModelTaskUsageTable 与迁移 v44 的 DDL 保持一致，确保统计写入在迁移未跑时也可用
func ensureModelTaskUsageTable(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS model_task_usage (
		bucket_time TIMESTAMP NOT NULL,
		task_name TEXT NOT NULL,
		model_name TEXT NOT NULL,
		model_identifier TEXT NOT NULL,
		provider_name TEXT NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		failure_count INTEGER NOT NULL DEFAULT 0,
		success_duration REAL NOT NULL DEFAULT 0,
		PRIMARY KEY(bucket_time,task_name,model_name,model_identifier,provider_name))`).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS ix_model_task_usage_bucket_time ON model_task_usage(bucket_time)`).Error
}

func mergeAggregate(target *aggregate, source aggregate) {
	target.RequestCount += source.RequestCount
	target.FailureCount += source.FailureCount
	target.LatencyMSSum += source.LatencyMSSum
	target.PromptTokens += source.PromptTokens
	target.CompletionTokens += source.CompletionTokens
	target.TotalTokens += source.TotalTokens
	target.UsageReportedCount += source.UsageReportedCount
}

type startedAtKey struct{}

func Handler(task, modelName string) callbacks.Handler {
	recorder := defaultRecorder.Load()
	return callbacktpl.NewHandlerHelper().
		AgenticModel(&callbacktpl.AgenticModelCallbackHandler{
			OnStart: func(ctx context.Context, _ *callbacks.RunInfo, _ *model.AgenticCallbackInput) context.Context {
				return context.WithValue(ctx, startedAtKey{}, time.Now())
			},
			OnEnd: func(ctx context.Context, _ *callbacks.RunInfo, output *model.AgenticCallbackOutput) context.Context {
				if ClientUsageRecording(ctx) {
					return ctx
				}
				prompt, completion, total, cacheHit, reported := 0, 0, 0, 0, false
				if output != nil && output.TokenUsage != nil {
					usage := output.TokenUsage
					prompt = usage.PromptTokens
					completion = usage.CompletionTokens
					total = usage.TotalTokens
					cacheHit = usage.PromptTokenDetails.CachedTokens
					reported = true
				}
				recorder.RecordUsage(task, modelName, elapsed(ctx), false, prompt, completion, total, cacheHit, reported)
				return ctx
			},
			OnError: func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
				if !ClientUsageRecording(ctx) {
					recorder.Record(task, modelName, elapsed(ctx), true, 0, false)
				}
				return ctx
			},
		}).Handler()
}

func elapsed(ctx context.Context) time.Duration {
	started, _ := ctx.Value(startedAtKey{}).(time.Time)
	if started.IsZero() {
		return 0
	}
	return time.Since(started)
}

type AggregateRow struct {
	Label              string  `json:"label"`
	RequestCount       int64   `json:"request_count"`
	FailureCount       int64   `json:"failure_count"`
	AverageLatencyMS   float64 `json:"average_latency_ms"`
	TotalTokens        int64   `json:"total_tokens"`
	UsageReportedCount int64   `json:"usage_reported_count"`
}

type SeriesRow struct {
	BucketStart  time.Time `json:"bucket_start"`
	RequestCount int64     `json:"request_count"`
	TotalTokens  int64     `json:"total_tokens"`
	LatencyMSSum int64     `json:"latency_ms_sum"`
}

type Snapshot struct {
	Totals  AggregateRow   `json:"totals"`
	ByTask  []AggregateRow `json:"by_task"`
	ByModel []AggregateRow `json:"by_model"`
	Series  []SeriesRow    `json:"series"`
}

func Query(ctx context.Context, db *gorm.DB, since time.Time) (Snapshot, error) {
	var snapshot Snapshot
	base := `FROM model_call_hourly`
	args := []any{}
	if !since.IsZero() {
		base += ` WHERE bucket_start>=?`
		args = append(args, since)
	}
	avgLatency := `CASE WHEN COALESCE(SUM(request_count),0)>0 THEN CAST(SUM(latency_ms_sum) AS REAL)/SUM(request_count) ELSE 0 END`
	if err := db.WithContext(ctx).Raw(`SELECT '全部' label, COALESCE(SUM(request_count),0) request_count, COALESCE(SUM(failure_count),0) failure_count,
		`+avgLatency+` average_latency_ms,
		COALESCE(SUM(total_tokens),0) total_tokens, COALESCE(SUM(usage_reported_count),0) usage_reported_count `+base, args...).Scan(&snapshot.Totals).Error; err != nil {
		return Snapshot{}, err
	}
	if err := db.WithContext(ctx).Raw(`SELECT task label, SUM(request_count) request_count, SUM(failure_count) failure_count,
		`+avgLatency+` average_latency_ms, SUM(total_tokens) total_tokens, SUM(usage_reported_count) usage_reported_count `+base+` GROUP BY task ORDER BY request_count DESC`, args...).Scan(&snapshot.ByTask).Error; err != nil {
		return Snapshot{}, err
	}
	for i := range snapshot.ByTask {
		snapshot.ByTask[i].Label = taskLabel(snapshot.ByTask[i].Label)
	}
	if err := db.WithContext(ctx).Raw(`SELECT model label, SUM(request_count) request_count, SUM(failure_count) failure_count,
		`+avgLatency+` average_latency_ms, SUM(total_tokens) total_tokens, SUM(usage_reported_count) usage_reported_count `+base+` GROUP BY model ORDER BY request_count DESC`, args...).Scan(&snapshot.ByModel).Error; err != nil {
		return Snapshot{}, err
	}
	bucket := seriesBucketExpression(since)
	if err := db.WithContext(ctx).Raw(`SELECT `+bucket+` bucket_start,SUM(request_count) request_count,SUM(total_tokens) total_tokens,SUM(latency_ms_sum) latency_ms_sum `+base+` GROUP BY 1 ORDER BY 1`, args...).Scan(&snapshot.Series).Error; err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func seriesBucketExpression(since time.Time) string {
	if since.IsZero() {
		return `datetime(strftime('%Y-%m-01 00:00:00', bucket_start))`
	}
	duration := time.Since(since)
	if duration > 14*24*time.Hour {
		return `datetime(strftime('%Y-%m-%d 00:00:00', bucket_start))`
	}
	if duration > 2*24*time.Hour {
		// 对齐旧 PG date_bin('6 hours')：按 6 小时边界归桶
		return `datetime(strftime('%Y-%m-%d', bucket_start) || ' ' || printf('%02d:00:00', (CAST(strftime('%H', bucket_start) AS INTEGER)/6)*6))`
	}
	return `bucket_start`
}

func taskLabel(task string) string {
	switch task {
	case "react":
		return "主对话"
	case "compaction":
		return "上下文压缩"
	default:
		return task
	}
}
