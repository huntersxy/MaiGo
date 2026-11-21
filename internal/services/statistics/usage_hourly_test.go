package stats

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openUsageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取数据库连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := EnsureSchema(context.Background(), db); err != nil {
		t.Fatalf("初始化统计表失败: %v", err)
	}
	return db
}

func insertUsageRecord(t *testing.T, db *gorm.DB, timestamp, task string, promptTokens, completionTokens int64, cost float64, timeCost any) {
	t.Helper()
	err := db.Exec(`INSERT INTO llm_usage(
		model_name, model_assign_name, model_api_provider_name, session_id, task_name, request_type,
		time_cost, timestamp, prompt_tokens, completion_tokens, total_tokens,
		prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"model-a", "alias-a", "provider-a", "session-1", task, "replyer.request",
		timeCost, timestamp, promptTokens, completionTokens, promptTokens+completionTokens,
		false, 0, 0, cost,
	).Error
	if err != nil {
		t.Fatalf("写入调用记录失败: %v", err)
	}
}

func TestRefreshUsageHourlyAndWindowUsage(t *testing.T) {
	db := openUsageTestDB(t)
	ctx := context.Background()
	records := []struct {
		timestamp  string
		prompt     int64
		completion int64
		cost       float64
		timeCost   float64
	}{
		{"2026-01-01 10:15:00", 10, 1, 0.1, 1.5},
		{"2026-01-01 10:45:00", 20, 2, 0.2, 2.5},
		{"2026-01-01 11:10:00", 30, 3, 0.3, 3.5},
		{"2026-01-01 12:05:00", 40, 4, 0.4, 4.5},
		{"2026-01-01 12:30:00", 50, 5, 0.5, 5.5},
	}
	for _, record := range records {
		insertUsageRecord(t, db, record.timestamp, "replyer", record.prompt, record.completion, record.cost, record.timeCost)
	}

	state := RefreshUsageHourly(ctx, db, 5*time.Second)
	if !state.Ready {
		t.Fatalf("首轮汇总应当就绪: %+v", state)
	}
	if state.Cursor != int64(len(records)) {
		t.Fatalf("游标应为 %d，实际 %d", len(records), state.Cursor)
	}

	start := time.Date(2026, 1, 1, 10, 30, 0, 0, time.Local)
	end := time.Date(2026, 1, 1, 12, 30, 0, 0, time.Local)
	items, err := WindowUsage(ctx, db, start, end, true, state.Cursor)
	if err != nil {
		t.Fatalf("读取窗口用量失败: %v", err)
	}
	direct, err := UsageRecordsThrough(ctx, db, start, end)
	if err != nil {
		t.Fatalf("读取明细用量失败: %v", err)
	}
	got, want := sumUsageMetrics(items), sumUsageMetrics(direct)
	if got != want {
		t.Fatalf("汇总与明细结果不一致: got=%+v want=%+v", got, want)
	}
	if got.RequestCount != 4 || got.PromptTokens != 140 || got.CompletionTokens != 14 || got.Cost != 1.4 {
		t.Fatalf("窗口用量不符合预期: %+v", got)
	}
}

func sumUsageMetrics(items []UsageEntry) UsageMetrics {
	var total UsageMetrics
	for _, item := range items {
		addUsageMetrics(&total, item.Metrics)
	}
	return total
}

func TestNormalizeUsageRecord(t *testing.T) {
	zero := 0.0
	negative := -2.0
	cases := []struct {
		name     string
		record   usageRecord
		wantHit  int64
		wantMiss int64
		wantTask string
		wantDur  int64
		wantResp int64
	}{
		{
			name: "缓存关闭不统计命中",
			record: usageRecord{RequestType: "replyer", ProviderName: "p", ModelAssignName: "m", PromptTokens: 100,
				PromptCacheHitTokens: 80, TaskName: "replyer"},
			wantTask: "replyer",
		},
		{
			name: "缺失未命中按输入补齐",
			record: usageRecord{RequestType: "replyer", ProviderName: "p", ModelName: "m", PromptTokens: 100,
				PromptCacheEnabled: true, PromptCacheHitTokens: 100, TimeCost: &zero},
			wantHit: 100, wantTask: unrecordedTaskName, wantResp: 1,
		},
		{
			name: "未命中为负时按输入补齐",
			record: usageRecord{RequestType: "replyer", ProviderName: "p", ModelName: "m", PromptTokens: 100,
				PromptCacheEnabled: true, PromptCacheHitTokens: 30, PromptCacheMissTokens: -1, TimeCost: &negative},
			wantHit: 30, wantMiss: 70, wantTask: unrecordedTaskName, wantResp: 1,
		},
		{
			name: "空维度归一化",
			record: usageRecord{PromptTokens: 10, PromptCacheEnabled: true, PromptCacheMissTokens: 10, TimeCost: func() *float64 {
				value := 3.5
				return &value
			}()},
			wantMiss: 10, wantTask: unrecordedTaskName, wantDur: 1, wantResp: 1,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			key, metrics := normalizeUsageRecord(test.record, time.Now())
			if key.RequestType != "replyer" && test.record.RequestType == "replyer" {
				t.Fatalf("请求类型应归一化为 replyer，实际 %q", key.RequestType)
			}
			if key.ProviderName == "" || key.ModelName == "" {
				t.Fatalf("提供商与模型不应为空: %+v", key)
			}
			if key.TaskName != test.wantTask {
				t.Fatalf("任务名应为 %q，实际 %q", test.wantTask, key.TaskName)
			}
			if metrics.CacheHitTokens != test.wantHit || metrics.CacheMissTokens != test.wantMiss {
				t.Fatalf("缓存 token 不符: got=(%d,%d) want=(%d,%d)",
					metrics.CacheHitTokens, metrics.CacheMissTokens, test.wantHit, test.wantMiss)
			}
			if metrics.DurationCount != test.wantDur || metrics.ResponseTimeCount != test.wantResp {
				t.Fatalf("耗时条数不符: got=(%d,%d) want=(%d,%d)",
					metrics.DurationCount, metrics.ResponseTimeCount, test.wantDur, test.wantResp)
			}
		})
	}
}

func TestDashboardUsageAggregates(t *testing.T) {
	db := openUsageTestDB(t)
	ctx := context.Background()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS mai_messages (
		id INTEGER PRIMARY KEY, timestamp TIMESTAMP, received_at TIMESTAMP, reply_to INTEGER, external_message_id TEXT
	)`).Error; err != nil {
		t.Fatalf("创建消息表失败: %v", err)
	}
	start := time.Date(2026, 1, 1, 10, 30, 0, 0, time.Local)
	end := time.Date(2026, 1, 1, 12, 30, 0, 0, time.Local)
	if err := db.Exec(`INSERT INTO online_time(timestamp, duration_minutes, start_timestamp, end_timestamp)
		VALUES(?, 120, ?, ?)`, start, start, end).Error; err != nil {
		t.Fatalf("写入在线区间失败: %v", err)
	}
	items := []UsageEntry{
		{Timestamp: time.Date(2026, 1, 1, 10, 45, 0, 0, time.Local),
			Key:     UsageKey{RequestType: "replyer.request", ProviderName: "p", ModelName: "model-b", TaskName: "replyer"},
			Metrics: UsageMetrics{RequestCount: 2, PromptTokens: 100, CompletionTokens: 10, CacheHitTokens: 30, CacheMissTokens: 70, Cost: 1, ResponseTimeCount: 2, ResponseTimeSum: 4}},
		{Timestamp: time.Date(2026, 1, 1, 11, 20, 0, 0, time.Local),
			Key:     UsageKey{RequestType: "planner.request", ProviderName: "p", ModelName: "model-a", TaskName: "planner"},
			Metrics: UsageMetrics{RequestCount: 1, PromptTokens: 50, CompletionTokens: 5, Cost: 0.5, ResponseTimeCount: 1, ResponseTimeSum: 1}},
	}
	summary, err := summaryFromUsageItems(ctx, db, start, end, items)
	if err != nil {
		t.Fatalf("汇总小时用量失败: %v", err)
	}
	if summary.TotalRequests != 3 || summary.TotalTokens != 165 || summary.TotalCost != 1.5 || summary.AvgResponseTime != 5.0/3 {
		t.Fatalf("摘要口径不符合预期: %+v", summary)
	}
	if summary.CoreCacheHitTokens != 0 || summary.CoreCacheMissTokens != 0 {
		t.Fatalf("核心任务缓存 token 不符合预期: %+v", summary)
	}
	models := modelStatisticsFromUsage(items)
	if len(models) != 2 || models[0].ModelName != "model-b" || models[0].RequestCount != 2 {
		t.Fatalf("模型统计排序不符合预期: %+v", models)
	}
	hourly := hourlyStatisticsFromUsage(items, start, end)
	if len(hourly) != 3 || hourly[0].Requests != 2 || hourly[1].Requests != 1 || hourly[2].Requests != 0 {
		t.Fatalf("小时序列不符合预期: %+v", hourly)
	}
	daily := dailyStatisticsFromUsage(items, start, end)
	if len(daily) != 1 || daily[0].Requests != 3 || daily[0].Tokens != 165 {
		t.Fatalf("天序列不符合预期: %+v", daily)
	}
}

type hourlyTestProvider struct {
	DetailedProvider
	entries []UsageEntry
}

func (p *hourlyTestProvider) UsageHourlyBetween(context.Context, time.Time, time.Time) ([]UsageEntry, error) {
	return p.entries, nil
}

func TestAccumulateDetailedUsageHourly(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 30, 0, 0, time.Local)
	starts := []time.Time{now.Add(-3 * time.Hour), now.Add(-time.Hour)}
	periods := []*detailedPeriodData{newDetailedPeriodData(), newDetailedPeriodData()}
	record := func(id int64, hour, minute int, tokens int64) DetailedUsage {
		return DetailedUsage{
			ID: id, Timestamp: time.Date(2026, 1, 1, hour, minute, 0, 0, time.Local),
			RequestType: "replyer.request", ProviderName: "p", ModelName: "m", TaskName: "replyer",
			PromptTokens: tokens, CompletionTokens: 0,
		}
	}
	usage := []DetailedUsage{
		record(1, 9, 45, 4),
		record(2, 10, 15, 8),
		record(3, 11, 45, 5),
		record(4, 12, 10, 6),
		record(5, 10, 30, 7),
	}
	provider := &hourlyTestProvider{entries: []UsageEntry{
		{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local),
			Key:     UsageKey{RequestType: "replyer.request", ProviderName: "p", ModelName: "m", TaskName: "replyer"},
			Metrics: UsageMetrics{RequestCount: 2, PromptTokens: 20, Cost: 1}},
		{Timestamp: time.Date(2026, 1, 1, 11, 0, 0, 0, time.Local),
			Key:     UsageKey{RequestType: "replyer.request", ProviderName: "p", ModelName: "m", TaskName: "replyer"},
			Metrics: UsageMetrics{RequestCount: 3, PromptTokens: 30, Cost: 2}},
	}}
	if err := accumulateDetailedUsageHourly(context.Background(), provider, starts, periods, usage, now, 4); err != nil {
		t.Fatalf("按小时汇总累加失败: %v", err)
	}
	if periods[0].totalRequests != 8 || periods[0].inputTokens != 67 || periods[0].totalCost != 3 {
		t.Fatalf("三小时时段汇总不符合预期: %+v", periods[0])
	}
	if periods[1].totalRequests != 2 || periods[1].inputTokens != 11 {
		t.Fatalf("最近一小时时段汇总不符合预期: %+v", periods[1])
	}
}

func TestAddDetailedUsageSampleWeights(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	starts := []time.Time{now.Add(-time.Hour)}
	periods := []*detailedPeriodData{newDetailedPeriodData()}
	sample := detailedUsageSample{
		Timestamp: now.Add(-30 * time.Minute), RequestType: "replyer.request", ProviderName: "provider-a",
		ModelName: "model-a", SessionID: "session-1", TaskName: "replyer", RequestCount: 5,
		PromptTokens: 100, CompletionTokens: 20, CacheHitTokens: 30, CacheMissTokens: 70, Cost: 1.25,
		DurationCount: 4, DurationSum: 6, DurationSumSq: 10,
	}
	addDetailedUsageSample(starts, periods, sample)
	period := periods[0]
	if period.totalRequests != 5 || period.inputTokens != 100 || period.outputTokens != 20 || period.totalCost != 1.25 {
		t.Fatalf("周期汇总未按请求数加权: %+v", period)
	}
	totals := period.classified[detailedClassKey{"model", "generation", "model-a"}]
	if totals == nil || totals.requestCount != 5 || totals.durationCount != 4 {
		t.Fatalf("模型分类汇总不符合预期: %+v", totals)
	}
}
