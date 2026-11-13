package personinfo

// 历史人物事实重分类后台循环，对齐 memory_flow_service 的 reclassify 循环

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	historicalReclassifyBatchSize = 50
	historicalReclassifyStartup   = 5 * time.Second
	historicalReclassifyBusy      = 1 * time.Second
	historicalReclassifyIdle      = 24 * time.Hour
	historicalReclassifyError     = 60 * time.Second
	historicalStateFileName       = "person_fact_classification_cursor.json"
)

type historicalReclassifyState struct {
	Cursor         string `json:"cursor"`
	ProcessedTotal int    `json:"processed_total"`
	PromotedTotal  int    `json:"promoted_total"`
	// UpdatedAt 使用 Unix 秒浮点，与 Python time.time() 写出的游标格式一致
	UpdatedAt float64 `json:"updated_at"`
}

// HistoricalReclassifyRunner 历史人物事实重分类后台运行器
type HistoricalReclassifyRunner struct {
	registry     *Registry
	Limit        int
	StatePath    string
	StartupDelay time.Duration
	BusyDelay    time.Duration
	IdleDelay    time.Duration
	ErrorDelay   time.Duration

	// Available 返回 false 时跳过本轮执行，按 UnavailableDelay 重新等待
	// 对齐 Python 重分类循环对开关与 metadata.db 存在性的检查
	Available func() bool
	// UnavailableDelay 前置条件不满足时的重试间隔，默认 30s
	UnavailableDelay time.Duration

	mu        sync.Mutex
	started   bool
	stopped   bool
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	doneOnce  sync.Once
}

// NewHistoricalReclassifyRunner 创建运行器
func NewHistoricalReclassifyRunner(registry *Registry) *HistoricalReclassifyRunner {
	if registry == nil {
		registry = DefaultRegistry()
	}
	return &HistoricalReclassifyRunner{
		registry: registry, Limit: historicalReclassifyBatchSize,
		StartupDelay: historicalReclassifyStartup, BusyDelay: historicalReclassifyBusy,
		IdleDelay: historicalReclassifyIdle, ErrorDelay: historicalReclassifyError,
		UnavailableDelay: 30 * time.Second,
		stop:             make(chan struct{}), done: make(chan struct{}),
	}
}

// Start 启动后台循环
func (h *HistoricalReclassifyRunner) Start(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("personinfo: 历史事实重分类运行器为空")
	}
	if h.StatePath == "" {
		dir, err := h.registry.runtimeDataDir()
		if err != nil {
			return err
		}
		h.StatePath = filepath.Join(dir, historicalStateFileName)
	}
	h.startOnce.Do(func() {
		h.mu.Lock()
		if h.stopped {
			h.mu.Unlock()
			h.closeDone()
			return
		}
		h.started = true
		h.mu.Unlock()
		go h.loop(ctx)
	})
	return nil
}

// Stop 停止后台循环
func (h *HistoricalReclassifyRunner) Stop(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	h.stopped = true
	started := h.started
	h.mu.Unlock()
	h.stopOnce.Do(func() { close(h.stop) })
	if !started {
		h.closeDone()
		return nil
	}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *HistoricalReclassifyRunner) closeDone() {
	h.doneOnce.Do(func() { close(h.done) })
}

// RunOnce 执行一批并持久化游标
func (h *HistoricalReclassifyRunner) RunOnce(ctx context.Context) (ReclassifyHistoricalResult, error) {
	state, err := h.loadState()
	if err != nil {
		return ReclassifyHistoricalResult{}, err
	}
	result, err := h.registry.ReclassifyHistoricalPersonFacts(ctx, state.Cursor, h.Limit)
	if err != nil {
		return result, err
	}
	state.Cursor = result.NextCursor
	state.ProcessedTotal += result.Processed
	state.PromotedTotal += result.Promoted
	state.UpdatedAt = float64(time.Now().UnixNano()) / float64(time.Second)
	if err := h.saveState(state); err != nil {
		return result, err
	}
	return result, nil
}

func (h *HistoricalReclassifyRunner) loop(ctx context.Context) {
	defer h.closeDone()
	delay := h.StartupDelay
	for {
		select {
		case <-h.stop:
			return
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if h.Available != nil && !h.Available() {
			delay = h.UnavailableDelay
			if delay <= 0 {
				delay = 30 * time.Second
			}
			continue
		}
		result, err := h.RunOnce(ctx)
		if err != nil {
			zap.L().Warn("历史人物事实重分类失败", zap.Error(err))
			delay = h.ErrorDelay
			continue
		}
		if result.HasMore {
			delay = h.BusyDelay
		} else {
			delay = h.IdleDelay
		}
	}
}

func (h *HistoricalReclassifyRunner) statePath() string {
	if h.StatePath != "" {
		return h.StatePath
	}
	return historicalStateFileName
}

func (h *HistoricalReclassifyRunner) loadState() (historicalReclassifyState, error) {
	var state historicalReclassifyState
	raw, err := os.ReadFile(h.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("personinfo: 读取重分类游标失败: %w", err)
	}
	if len(raw) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("personinfo: 解析重分类游标失败: %w", err)
	}
	return state, nil
}

func (h *HistoricalReclassifyRunner) saveState(state historicalReclassifyState) error {
	path := h.statePath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("personinfo: 创建重分类状态目录失败: %w", err)
		}
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o644); err != nil {
		return fmt.Errorf("personinfo: 写重分类游标失败: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		// Windows 下目标存在时 rename 可能失败，退化为覆盖
		_ = os.Remove(path)
		if err := os.Rename(temp, path); err != nil {
			return fmt.Errorf("personinfo: 替换重分类游标失败: %w", err)
		}
	}
	return nil
}

// MetadataAvailable 检查 metadata.db 是否真实存在且可读
func (h *HistoricalReclassifyRunner) MetadataAvailable() bool {
	if h == nil || h.registry == nil {
		return false
	}
	path, err := h.registry.metadataDBPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// HistoricalReclassifier 返回 Registry 的默认运行器
func (r *Registry) HistoricalReclassifier() *HistoricalReclassifyRunner {
	r.reclassifyOnce.Do(func() {
		r.reclassifier = NewHistoricalReclassifyRunner(r)
	})
	return r.reclassifier
}

// StartHistoricalReclassify 启动历史人物事实重分类
func (r *Registry) StartHistoricalReclassify(ctx context.Context) error {
	return r.HistoricalReclassifier().Start(ctx)
}

// StopHistoricalReclassify 停止历史人物事实重分类
func (r *Registry) StopHistoricalReclassify(ctx context.Context) error {
	if r.reclassifier == nil {
		return nil
	}
	return r.reclassifier.Stop(ctx)
}
