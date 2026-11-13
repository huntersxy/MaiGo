package heartflow

import "time"

// CyclePlanInfo 对齐 Python CyclePlanInfo，当前为开放字典
type CyclePlanInfo map[string]any

// CycleActionInfo 对齐 Python CycleActionInfo，当前为开放字典
type CycleActionInfo map[string]any

// CycleDetail 循环信息记录类，对应 heartFC_utils.CycleDetail
type CycleDetail struct {
	CycleID        int
	ThinkingID     string
	StartTime      time.Time
	EndTime        *time.Time
	TimeRecords    map[string]float64
	LoopPlanInfo   CyclePlanInfo
	LoopActionInfo CycleActionInfo
}

// NewCycleDetail 创建循环记录，开始时间默认当前时刻
func NewCycleDetail(cycleID int) *CycleDetail {
	return &CycleDetail{
		CycleID:     cycleID,
		StartTime:   time.Now(),
		TimeRecords: map[string]float64{},
	}
}

// Finish 标记循环结束
func (c *CycleDetail) Finish(now time.Time) {
	if c == nil {
		return
	}
	finished := now
	c.EndTime = &finished
}

// RecordElapsed 记录命名阶段耗时，key 为计时器名称，单位秒
func (c *CycleDetail) RecordElapsed(name string, elapsed time.Duration) {
	if c == nil || name == "" {
		return
	}
	if c.TimeRecords == nil {
		c.TimeRecords = map[string]float64{}
	}
	c.TimeRecords[name] = elapsed.Seconds()
}
