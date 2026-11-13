package stats

import (
	"context"
	"sort"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// OnlineInterval 对齐 MaiBot OnlineTime 的一条在线区间
type OnlineInterval struct {
	Start time.Time `gorm:"column:start_timestamp" json:"start_timestamp"`
	End   time.Time `gorm:"column:end_timestamp" json:"end_timestamp"`
}

// recordOnlineHeartbeat 按 MaiBot OnlineTimeRecordTask 语义维护当前在线区间
//
// 已有 record_id 时只延长 end_timestamp；记录丢失或首次运行时复用一分钟内结束的
// 最近记录，否则插入新的五分钟区间
func (t *OnlineTracker) recordOnlineHeartbeat(ctx context.Context, now time.Time) error {
	if t == nil || t.db == nil {
		return nil
	}
	extended := now.Add(onlineRecordInterval)
	t.mu.Lock()
	recordID := t.recordID
	t.mu.Unlock()

	db := t.db.WithContext(ctx)
	if recordID > 0 {
		result := db.Exec(`UPDATE online_time SET end_timestamp = ? WHERE id = ?`, extended, recordID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}
		t.mu.Lock()
		t.recordID = 0
		t.mu.Unlock()
	}

	var existing int64
	if err := db.Raw(`SELECT id FROM online_time WHERE end_timestamp >= ? ORDER BY end_timestamp DESC LIMIT 1`,
		now.Add(-onlineRecordInterval)).Scan(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		result := db.Exec(`UPDATE online_time SET end_timestamp = ? WHERE id = ?`, extended, existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			t.setRecordID(existing)
			return nil
		}
	}

	var inserted int64
	if err := db.Raw(`INSERT INTO online_time(timestamp, duration_minutes, start_timestamp, end_timestamp)
		VALUES(?, 5, ?, ?) RETURNING id`, now, now, extended).Scan(&inserted).Error; err != nil {
		return err
	}
	t.setRecordID(inserted)
	return nil
}

func (t *OnlineTracker) setRecordID(id int64) {
	t.mu.Lock()
	t.recordID = id
	t.mu.Unlock()
}

// queryOnlineIntervals 查询与 [start,end] 有交集的在线区间
func queryOnlineIntervals(ctx context.Context, db *gorm.DB, start, end time.Time) ([]OnlineInterval, error) {
	if db == nil {
		return nil, nil
	}
	var rows []OnlineInterval
	if err := db.WithContext(ctx).Raw(`SELECT start_timestamp, end_timestamp FROM online_time
		WHERE start_timestamp <= ? AND end_timestamp >= ? ORDER BY start_timestamp`,
		end, start).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// clipOnlineIntervals 将区间裁剪到窗口并按开始时间排序
func clipOnlineIntervals(intervals []OnlineInterval, start, end time.Time) []OnlineInterval {
	out := make([]OnlineInterval, 0, len(intervals))
	for _, item := range intervals {
		clippedStart := item.Start
		if clippedStart.Before(start) {
			clippedStart = start
		}
		clippedEnd := item.End
		if clippedEnd.After(end) {
			clippedEnd = end
		}
		if !clippedEnd.After(clippedStart) {
			continue
		}
		out = append(out, OnlineInterval{Start: clippedStart, End: clippedEnd})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// mergeOnlineIntervals 合并重叠或首尾相接的区间
func mergeOnlineIntervals(intervals []OnlineInterval) []OnlineInterval {
	if len(intervals) == 0 {
		return nil
	}
	merged := make([]OnlineInterval, 0, len(intervals))
	for _, item := range intervals {
		if len(merged) == 0 {
			merged = append(merged, item)
			continue
		}
		last := &merged[len(merged)-1]
		if !item.Start.After(last.End) {
			if item.End.After(last.End) {
				last.End = item.End
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

// onlineSecondsInRange 按区间重叠秒数求和；对齐 MaiBot summary 不合并区间
func onlineSecondsInRange(intervals []OnlineInterval, start, end time.Time) float64 {
	var total float64
	for _, item := range clipOnlineIntervals(intervals, start, end) {
		total += item.End.Sub(item.Start).Seconds()
	}
	return total
}

// onlineSecondsMergedInRange 合并重叠区间后求和，对齐遥测在线时长口径
func onlineSecondsMergedInRange(intervals []OnlineInterval, start, end time.Time) float64 {
	var total float64
	for _, item := range mergeOnlineIntervals(clipOnlineIntervals(intervals, start, end)) {
		total += item.End.Sub(item.Start).Seconds()
	}
	return total
}

// hourlyOnlineSeconds 合并区间后拆分到本地小时桶
func hourlyOnlineSeconds(intervals []OnlineInterval, start, end time.Time) map[string]float64 {
	merged := mergeOnlineIntervals(clipOnlineIntervals(intervals, start, end))
	out := make(map[string]float64)
	for _, item := range merged {
		cursor := item.Start
		for cursor.Before(item.End) {
			hourStart := floorLocalHour(cursor)
			nextHour := hourStart.Add(time.Hour)
			segmentEnd := item.End
			if nextHour.Before(segmentEnd) {
				segmentEnd = nextHour
			}
			key := hourStart.In(time.Local).Format("2006-01-02T15:00:00")
			out[key] += segmentEnd.Sub(cursor).Seconds()
			cursor = segmentEnd
		}
	}
	return out
}

// floorLocalHour 按本地墙钟取整点，避免非整小时时区被 Truncate 截错
func floorLocalHour(t time.Time) time.Time {
	t = t.In(time.Local)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
}

// onlineIntervalsEmpty 判断 online_time 是否仍为空表，用于兼容仅有 online_time_daily 的旧库
func onlineIntervalsEmpty(ctx context.Context, db *gorm.DB) bool {
	if db == nil {
		return true
	}
	var count int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM online_time`).Scan(&count).Error; err != nil {
		zap.L().Warn("检查在线区间表失败", zap.Error(err))
		return false
	}
	return count == 0
}
