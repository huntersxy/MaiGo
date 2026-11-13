package stats

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

const onlineRecordInterval = 60 * time.Second

// OnlineSnapshot 供 WebUI system/status 使用的在线时长汇总
type OnlineSnapshot struct {
	TodaySeconds float64 `json:"today_seconds"`
	TotalSeconds float64 `json:"total_seconds"`
}

// OnlineTracker 进程运行期间按 MaiBot OnlineTimeRecordTask 间隔累计在线秒数并写入 online_time / online_time_daily
type OnlineTracker struct {
	mu       sync.Mutex
	db       *gorm.DB
	pending  map[time.Time]int64 // 本地日历日 00:00 -> 待 flush 秒数
	lastTick time.Time
	recordID int64
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

var defaultOnline atomic.Pointer[OnlineTracker]

// NewOnlineTracker 创建在线时长跟踪器
func NewOnlineTracker() *OnlineTracker {
	return &OnlineTracker{
		pending: make(map[time.Time]int64),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// SetDefaultOnline 设置全局跟踪器
func SetDefaultOnline(t *OnlineTracker) { defaultOnline.Store(t) }

// DefaultOnlineTracker 返回全局跟踪器（可能为 nil）
func DefaultOnlineTracker() *OnlineTracker { return defaultOnline.Load() }

// Start 绑定数据库并启动 60s  tick；db 为 nil 时仅内存累计
func (t *OnlineTracker) Start(db *gorm.DB) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	t.db = db
	t.lastTick = now
	t.mu.Unlock()
	if db == nil {
		return
	}
	if err := EnsureSchema(context.Background(), db); err != nil {
		zap.L().Warn("初始化统计表失败，在线区间将降级为按日统计", zap.Error(err))
	} else if err := t.recordOnlineHeartbeat(context.Background(), now); err != nil {
		zap.L().Warn("写入在线区间失败", zap.Error(err))
	}
	go func() {
		ticker := time.NewTicker(onlineRecordInterval)
		defer ticker.Stop()
		defer close(t.done)
		for {
			select {
			case <-ticker.C:
				t.accumulateSinceLastTick()
				t.flush()
				if err := t.recordOnlineHeartbeat(context.Background(), time.Now()); err != nil {
					zap.L().Warn("写入在线区间失败", zap.Error(err))
				}
			case <-t.stop:
				t.accumulateSinceLastTick()
				t.flush()
				return
			}
		}
	}()
}

// Close 停止后台任务并 flush 剩余秒数
func (t *OnlineTracker) Close() {
	if t == nil {
		return
	}
	t.once.Do(func() { close(t.stop) })
	if t.db != nil {
		<-t.done
	}
}

// Snapshot 返回今日与历史累计在线秒数（含尚未 flush 的 pending）
func (t *OnlineTracker) Snapshot(ctx context.Context) OnlineSnapshot {
	if t == nil {
		return OnlineSnapshot{}
	}
	today, total := t.totals(ctx)
	return OnlineSnapshot{
		TodaySeconds: float64(today),
		TotalSeconds: float64(total),
	}
}

func (t *OnlineTracker) accumulateSinceLastTick() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.lastTick.IsZero() {
		t.lastTick = now
		return
	}
	if !now.After(t.lastTick) {
		return
	}
	addDurationToDays(t.pending, t.lastTick, now)
	t.lastTick = now
}

func (t *OnlineTracker) totals(ctx context.Context) (today, total int64) {
	loc := time.Local
	now := time.Now().In(loc)
	todayStart := startOfLocalDay(now)

	t.mu.Lock()
	pendingCopy := make(map[time.Time]int64, len(t.pending))
	for day, sec := range t.pending {
		pendingCopy[day] = sec
	}
	db := t.db
	t.mu.Unlock()

	for day, sec := range pendingCopy {
		total += sec
		if day.Equal(todayStart) {
			today += sec
		}
	}
	if db == nil {
		return today, total
	}

	var row struct {
		Today int64
		All   int64
	}
	err := db.WithContext(ctx).Raw(`
		SELECT
			COALESCE((SELECT online_seconds FROM online_time_daily WHERE day_date = ?), 0) AS today,
			COALESCE((SELECT SUM(online_seconds) FROM online_time_daily), 0) AS "all"`,
		todayStart.Format("2006-01-02"),
	).Scan(&row).Error
	if err != nil {
		zap.L().Warn("读取在线时长汇总失败", zap.Error(err))
		return today, total
	}
	return today + row.Today, total + row.All
}

func (t *OnlineTracker) flush() {
	if t == nil || t.db == nil {
		return
	}
	t.mu.Lock()
	batch := t.pending
	if len(batch) == 0 {
		t.mu.Unlock()
		return
	}
	t.pending = make(map[time.Time]int64)
	t.mu.Unlock()

	err := t.db.Transaction(func(tx *gorm.DB) error {
		for day, sec := range batch {
			if sec <= 0 {
				continue
			}
			dayStr := day.Format("2006-01-02")
			if err := tx.Exec(`INSERT INTO online_time_daily(day_date,online_seconds)
				VALUES(?,?) ON CONFLICT(day_date) DO UPDATE SET
				online_seconds=online_time_daily.online_seconds+EXCLUDED.online_seconds`,
				dayStr, sec).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return
	}
	zap.L().Warn("写入在线时长统计失败，将保留到下次重试", zap.Error(err))
	t.mu.Lock()
	for day, sec := range batch {
		t.pending[day] += sec
	}
	t.mu.Unlock()
}

func startOfLocalDay(t time.Time) time.Time {
	loc := t.Location()
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// addDurationToDays 将 [from, to) 按本地日历日切分并累加秒数
func addDurationToDays(pending map[time.Time]int64, from, to time.Time) {
	if !to.After(from) {
		return
	}
	cursor := from
	for cursor.Before(to) {
		dayStart := startOfLocalDay(cursor)
		nextMidnight := dayStart.Add(24 * time.Hour)
		segmentEnd := to
		if nextMidnight.Before(segmentEnd) {
			segmentEnd = nextMidnight
		}
		sec := int64(segmentEnd.Sub(cursor).Seconds())
		if sec > 0 {
			pending[dayStart] += sec
		}
		cursor = segmentEnd
	}
}

// QueryOnlineTotals 从 PG 与 optional pending 合并今日/总在线秒数（无运行中 tracker 时可用）
func QueryOnlineTotals(ctx context.Context, db *gorm.DB, pending map[time.Time]int64) (today, total int64) {
	if t := defaultOnline.Load(); t != nil && pending == nil {
		return t.totals(ctx)
	}
	todayStart := startOfLocalDay(time.Now())
	for day, sec := range pending {
		total += sec
		if day.Equal(todayStart) {
			today += sec
		}
	}
	if db == nil {
		return today, total
	}
	var row struct {
		Today int64
		All   int64
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT
			COALESCE((SELECT online_seconds FROM online_time_daily WHERE day_date = ?), 0) AS today,
			COALESCE((SELECT SUM(online_seconds) FROM online_time_daily), 0) AS "all"`,
		todayStart.Format("2006-01-02"),
	).Scan(&row).Error; err != nil {
		zap.L().Warn("读取在线时长汇总失败", zap.Error(err))
		return today, total
	}
	return today + row.Today, total + row.All
}
