package monitor

import (
	"sync"
	"time"
)

// StageStatus 单个会话的阶段状态，对齐 MaisakaStageStatusBoard 的条目
type StageStatus struct {
	SessionID      string
	SessionName    string
	Stage          string
	Detail         string
	RoundText      string
	AgentState     string
	WaitUntil      *float64
	StageStartedAt float64
	UpdatedAt      float64
	Timestamp      float64
}

// StageStatusBoard 维护 Maisaka 阶段状态并推送给 WebUI
type StageStatusBoard struct {
	mu      sync.Mutex
	entries map[string]StageStatus
	emit    func(StageStatus)
	removed func(sessionID, sessionName string)
}

// NewStageStatusBoard 创建阶段状态板
func NewStageStatusBoard(emit func(StageStatus), removed func(sessionID, sessionName string)) *StageStatusBoard {
	return &StageStatusBoard{entries: map[string]StageStatus{}, emit: emit, removed: removed}
}

// Update 更新一个会话的阶段状态；stage 变化时重置 stage_started_at
func (b *StageStatusBoard) Update(status StageStatus) {
	now := float64(time.Now().UnixNano()) / 1e9
	b.mu.Lock()
	current, ok := b.entries[status.SessionID]
	stageStartedAt := now
	if ok {
		stageStartedAt = current.StageStartedAt
		if current.Stage != status.Stage {
			stageStartedAt = now
		}
	}
	status.StageStartedAt = stageStartedAt
	status.UpdatedAt = now
	status.Timestamp = now
	b.entries[status.SessionID] = status
	b.mu.Unlock()
	if b.emit != nil {
		b.emit(status)
	}
}

// Remove 移除一个会话的阶段状态
func (b *StageStatusBoard) Remove(sessionID string) {
	b.mu.Lock()
	removed, ok := b.entries[sessionID]
	delete(b.entries, sessionID)
	b.mu.Unlock()
	if b.removed != nil {
		name := ""
		if ok {
			name = removed.SessionName
		}
		b.removed(sessionID, name)
	}
}

// Snapshot 返回当前所有聊天流的阶段状态快照
func (b *StageStatusBoard) Snapshot() []StageStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]StageStatus, 0, len(b.entries))
	for _, entry := range b.entries {
		out = append(out, entry)
	}
	return out
}

// Payload 返回可直接下发 stage.snapshot 的条目列表
func (b *StageStatusBoard) Payload() []map[string]any {
	snapshot := b.Snapshot()
	entries := make([]map[string]any, 0, len(snapshot))
	for _, status := range snapshot {
		entries = append(entries, map[string]any{
			"session_id":       status.SessionID,
			"session_name":     status.SessionName,
			"stage":            status.Stage,
			"detail":           status.Detail,
			"round_text":       status.RoundText,
			"agent_state":      status.AgentState,
			"wait_until":       status.WaitUntil,
			"stage_started_at": status.StageStartedAt,
			"updated_at":       status.UpdatedAt,
			"timestamp":        status.Timestamp,
		})
	}
	return entries
}

var defaultStageBoard = NewStageStatusBoard(
	func(status StageStatus) {
		EmitStageStatus(
			status.SessionID, status.SessionName, status.Stage, status.Detail, status.RoundText,
			status.AgentState, status.WaitUntil, status.StageStartedAt, status.UpdatedAt, status.Timestamp,
		)
	},
	func(sessionID, sessionName string) {
		EmitStageRemoved(sessionID, sessionName)
	},
)

// UpdateStageStatusGlobal 更新全局阶段状态
func UpdateStageStatusGlobal(status StageStatus) { defaultStageBoard.Update(status) }

// RemoveStageStatusGlobal 移除全局阶段状态
func RemoveStageStatusGlobal(sessionID string) { defaultStageBoard.Remove(sessionID) }

// GetStageStatusSnapshot 获取全局阶段状态快照
func GetStageStatusSnapshot() []StageStatus { return defaultStageBoard.Snapshot() }

// StageStatusSnapshotPayload 返回全局阶段状态的下发载荷
func StageStatusSnapshotPayload() []map[string]any { return defaultStageBoard.Payload() }
