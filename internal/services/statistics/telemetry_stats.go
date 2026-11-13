package stats

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 遥测统计上传统计对齐 MaiBot src/common/remote.py 的 TelemetryStatsUploadTask

const (
	// statsSchemaVersion 载荷 schema 版本，对齐 MaiBot telemetry_stats_service
	statsSchemaVersion = 2
	// statsCursorVersion 游标 schema 版本
	statsCursorVersion = 1
	// statsUploadInterval 上传间隔，对齐 MaiBot STATS_UPLOAD_INTERVAL
	statsUploadInterval = 11451 * time.Second
	// statsMaxLookback 单次上传最多回溯 14 天
	statsMaxLookback = 14 * 24 * time.Hour
	// statsSendTimeout 单次请求总超时
	statsSendTimeout = 15 * time.Second
)

// statsRetryDelays 首次立即发送，失败后按 MaiBot RETRY_DELAYS 重试
var statsRetryDelays = []time.Duration{0, 5 * time.Second, 15 * time.Second, 45 * time.Second}

// telemetryStatsCursor 对齐 MaiBot local_store.json 的 telemetry_stats_cursor
type telemetryStatsCursor struct {
	SchemaVersion    int    `json:"schema_version"`
	LastSuccessEndAt string `json:"last_success_end_at"`
	UpdatedAt        string `json:"updated_at"`
}

// statsUploader 周期性统计上传；未配置数据库时只上报内存计数
type statsUploader struct {
	tele      *Telemetry
	db        *gorm.DB
	interval  time.Duration
	startedAt time.Time
	enabled   func() bool
	models    func() map[string][]string
	client    *http.Client
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}

// newStatsUploader 构造上传器；interval 由 statsUploadInterval 决定，测试内可覆盖
func newStatsUploader(tele *Telemetry, opts TelemetryOptions) *statsUploader {
	startedAt := ProcessStart()
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &statsUploader{
		tele:      tele,
		db:        opts.DB,
		interval:  statsUploadInterval,
		startedAt: startedAt,
		enabled:   opts.StatsEnabled,
		models:    opts.ModelAssignments,
		client:    &http.Client{Timeout: statsSendTimeout},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// loop 对齐 MaiBot AsyncTask：首次上传等待一个完整间隔，之后按间隔重复
func (u *statsUploader) loop(ctx context.Context) {
	defer close(u.done)
	if !waitStatsDelay(ctx, u.interval, u.stop) {
		return
	}
	for {
		if err := u.upload(ctx); err != nil {
			zap.L().Warn("遥测统计上传未完成", zap.Error(err))
		}
		if !waitStatsDelay(ctx, u.interval, u.stop) {
			return
		}
	}
}

// shutdown 停止上传循环并等待退出
func (u *statsUploader) shutdown() {
	u.once.Do(func() { close(u.stop) })
	<-u.done
}

// upload 执行一次统计上传，成功后推进游标
func (u *statsUploader) upload(ctx context.Context) error {
	if u.tele == nil || !u.tele.Enabled() {
		return nil
	}
	if u.enabled != nil && !u.enabled() {
		return nil
	}
	if !u.tele.ensureClientUUID(ctx) {
		return errors.New("遥测统计跳过：Client-UUID 不可用")
	}
	local, err := readTelemetryLocal(u.tele.instanceDir)
	if err != nil {
		zap.L().Debug("读取遥测统计游标失败，按进程启动时间计算区间", zap.Error(err))
	}
	periodStart, periodEnd, truncated := u.resolvePeriod(local.StatsCursor)
	if !periodEnd.After(periodStart) {
		return nil
	}
	if err := RefreshStatisticsAggregates(ctx, u.db); err != nil {
		zap.L().Warn("刷新统计汇总失败", zap.Error(err))
	}
	payload, err := u.buildStatsPayload(ctx, periodStart, periodEnd, truncated)
	if err != nil {
		return err
	}
	if !u.send(ctx, payload) {
		return errors.New("遥测统计上传失败")
	}
	if err := saveStatsCursor(u.tele.instanceDir, periodEnd); err != nil {
		zap.L().Warn("保存遥测统计游标失败", zap.Error(err))
	}
	zap.L().Info("遥测统计上传完成",
		zap.String("period_start", formatStatsTime(periodStart)),
		zap.String("period_end", formatStatsTime(periodEnd)),
		zap.Bool("truncated", truncated))
	return nil
}

// resolvePeriod 计算上传区间：优先游标，缺失时用进程启动时间，并限制回溯长度
func (u *statsUploader) resolvePeriod(cursor *telemetryStatsCursor) (time.Time, time.Time, bool) {
	periodEnd := time.Now()
	periodStart := u.startedAt
	if cursor != nil {
		if parsed, ok := parseStatsCursorTime(cursor.LastSuccessEndAt); ok {
			periodStart = parsed
		}
	}
	if minStart := periodEnd.Add(-statsMaxLookback); periodStart.Before(minStart) {
		return minStart, periodEnd, true
	}
	return periodStart, periodEnd, false
}

// parseStatsCursorTime 解析游标时间；无时区信息按 MaiBot 约定视为 UTC
func parseStatsCursorTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.In(time.Local), true
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed.In(time.Local), true
		}
	}
	return time.Time{}, false
}

// send 按 MaiBot 重试节奏上传；403 视为 UUID 失效
func (u *statsUploader) send(ctx context.Context, payload telemetryStatsPayload) bool {
	body, err := sonic.Marshal(payload)
	if err != nil {
		zap.L().Warn("遥测统计载荷序列化失败", zap.Error(err))
		return false
	}
	uuid := u.tele.currentClientUUID()
	url := u.tele.baseURL + "/stat/client_statistics"
	for attempt, delay := range statsRetryDelays {
		if delay > 0 && !waitStatsDelay(ctx, delay, u.stop) {
			return false
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			zap.L().Warn("遥测统计请求构建失败", zap.Error(err))
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Client-UUID", uuid)
		req.Header.Set("User-Agent", "TelemetryStatsClient/"+uuidPrefix(uuid))
		resp, err := u.client.Do(req)
		if err != nil {
			zap.L().Warn("遥测统计上传异常，稍后重试",
				zap.Int("attempt", attempt+1), zap.Error(err))
			continue
		}
		status := resp.StatusCode
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		if status >= 200 && status < 300 {
			return true
		}
		if status == http.StatusForbidden {
			zap.L().Warn("遥测统计上传 403，清除本地 UUID 并等待重新注册")
			u.tele.invalidateClientUUID()
			return false
		}
		zap.L().Warn("遥测统计上传失败",
			zap.Int("attempt", attempt+1),
			zap.Int("status", status),
			zap.ByteString("body", raw))
	}
	return false
}

// saveStatsCursor 记录本次成功上传的区间终点
func saveStatsCursor(instanceDir string, periodEnd time.Time) error {
	data, err := readTelemetryLocal(instanceDir)
	if err != nil {
		return err
	}
	data.StatsCursor = &telemetryStatsCursor{
		SchemaVersion:    statsCursorVersion,
		LastSuccessEndAt: formatStatsTime(periodEnd),
		UpdatedAt:        formatStatsTime(time.Now()),
	}
	return writeTelemetryLocal(instanceDir, data)
}

// waitStatsDelay 等待指定时长；ctx 或 stop 关闭时返回 false
func waitStatsDelay(ctx context.Context, delay time.Duration, stop <-chan struct{}) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

func uuidPrefix(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}
