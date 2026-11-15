package display

import "MaiGo/internal/maisaka/monitor"

// StageStatus 单个会话的阶段状态，数据定义在 monitor 包避免 display→maisaka 循环
type StageStatus = monitor.StageStatus

// StageStatusBoard 维护 Maisaka 阶段状态并推送给 WebUI
type StageStatusBoard = monitor.StageStatusBoard

// NewStageStatusBoard 创建阶段状态板
func NewStageStatusBoard(emit func(StageStatus), removed func(sessionID, sessionName string)) *StageStatusBoard {
	return monitor.NewStageStatusBoard(emit, removed)
}

// UpdateStageStatusGlobal 更新全局阶段状态
func UpdateStageStatusGlobal(status StageStatus) { monitor.UpdateStageStatusGlobal(status) }

// RemoveStageStatusGlobal 移除全局阶段状态
func RemoveStageStatusGlobal(sessionID string) { monitor.RemoveStageStatusGlobal(sessionID) }

// GetStageStatusSnapshot 获取全局阶段状态快照
func GetStageStatusSnapshot() []StageStatus { return monitor.GetStageStatusSnapshot() }

// SnapshotPayload 返回可直接下发 stage.snapshot 的条目列表
func SnapshotPayload() []map[string]any { return monitor.StageStatusSnapshotPayload() }
