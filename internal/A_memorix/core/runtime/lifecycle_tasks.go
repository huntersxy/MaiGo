package memruntime

// 后台任务启动与取消，对齐 lifecycle_orchestrator.py 的 start_background_tasks 与 cancel_background_tasks

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// 后台任务名，对齐 Python 侧 7 个任务字段
const (
	BackgroundTaskScheduledImport           = "scheduled_import"
	BackgroundTaskAutoSave                  = "auto_save"
	BackgroundTaskPersonProfileRefresh      = "person_profile_refresh"
	BackgroundTaskPersonProfileRefreshQueue = "person_profile_refresh_queue"
	BackgroundTaskMemoryMaintenance         = "memory_maintenance"
	BackgroundTaskRelationVectorBackfill    = "relation_vector_backfill"
	BackgroundTaskEpisodeGeneration         = "episode_generation"
)

// BackgroundTaskOrder 对齐 cancel_background_tasks 中任务清单的固定顺序
var BackgroundTaskOrder = []string{
	BackgroundTaskScheduledImport,
	BackgroundTaskAutoSave,
	BackgroundTaskPersonProfileRefresh,
	BackgroundTaskPersonProfileRefreshQueue,
	BackgroundTaskMemoryMaintenance,
	BackgroundTaskRelationVectorBackfill,
	BackgroundTaskEpisodeGeneration,
}

// BackgroundTasks 保存插件后台任务句柄
type BackgroundTasks struct {
	mu    sync.Mutex
	tasks map[string]BackgroundTask
}

// NewBackgroundTasks 创建任务表
func NewBackgroundTasks() *BackgroundTasks {
	return &BackgroundTasks{tasks: map[string]BackgroundTask{}}
}

// Task 读取任务，未设置返回 nil
func (t *BackgroundTasks) Task(name string) BackgroundTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tasks == nil {
		return nil
	}
	return t.tasks[name]
}

// SetTask 写入任务
func (t *BackgroundTasks) SetTask(name string, task BackgroundTask) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tasks == nil {
		t.tasks = map[string]BackgroundTask{}
	}
	t.tasks[name] = task
}

// goroutineTask 是 asyncio.Task 的 goroutine 包装
type goroutineTask struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

// Done 判断任务是否结束
func (t *goroutineTask) Done() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// Cancel 请求取消任务
func (t *goroutineTask) Cancel() { t.cancel() }

// Wait 等待任务结束并返回错误
func (t *goroutineTask) Wait() error {
	<-t.done
	return t.err
}

// SpawnGoroutine 是默认的 TaskSpawner 实现
func SpawnGoroutine(name string, loop func() error) BackgroundTask {
	ctx, cancel := context.WithCancel(context.Background())
	task := &goroutineTask{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(task.done)
		errCh := make(chan error, 1)
		go func() { errCh <- loop() }()
		select {
		case err := <-errCh:
			task.err = err
		case <-ctx.Done():
			task.err = ctx.Err()
		}
	}()
	return task
}

// ensureSpawner 返回可用 spawner
func ensureSpawner(spawner TaskSpawner) TaskSpawner {
	if spawner == nil {
		return SpawnGoroutine
	}
	return spawner
}

// taskReusable 对齐 task is None or task.done()
func taskReusable(task BackgroundTask) bool {
	return task == nil || task.Done()
}

// startBackgroundTask 按条件创建任务
func startBackgroundTask(tasks *BackgroundTasks, spawner TaskSpawner, name string, condition bool, loop func() error) {
	if !condition || loop == nil || !taskReusable(tasks.Task(name)) {
		return
	}
	tasks.SetTask(name, spawner(name, loop))
}

// StartBackgroundTasks 对齐 start_background_tasks
func StartBackgroundTasks(plugin LifecyclePlugin, loops LifecycleLoops, tasks *BackgroundTasks, spawner TaskSpawner) {
	if tasks == nil {
		return
	}
	spawn := ensureSpawner(spawner)

	startBackgroundTask(tasks, spawn, BackgroundTaskScheduledImport,
		PyTruthy(plugin.GetConfig("summarization.enabled", true)) && PyTruthy(plugin.GetConfig("schedule.enabled", true)),
		loops.ScheduledImport)
	startBackgroundTask(tasks, spawn, BackgroundTaskAutoSave,
		PyTruthy(plugin.GetConfig("advanced.enable_auto_save", true)),
		loops.AutoSave)
	startBackgroundTask(tasks, spawn, BackgroundTaskPersonProfileRefresh,
		PyTruthy(plugin.GetConfig("person_profile.enabled", true)),
		loops.PersonProfileRefresh)
	startBackgroundTask(tasks, spawn, BackgroundTaskPersonProfileRefreshQueue,
		PyTruthy(plugin.GetConfig("person_profile.enabled", true)),
		loops.PersonProfileRefreshQueue)
	startBackgroundTask(tasks, spawn, BackgroundTaskMemoryMaintenance, true, loops.MemoryMaintenance)

	relationVectorConfig, _ := plugin.GetConfig("retrieval.relation_vectorization", map[string]any{}).(map[string]any)
	relationVectorEnabled := false
	relationVectorBackfill := false
	if relationVectorConfig != nil {
		relationVectorEnabled = PyTruthy(relationVectorConfig["enabled"])
		relationVectorBackfill = PyTruthy(relationVectorConfig["backfill_enabled"])
	}
	startBackgroundTask(tasks, spawn, BackgroundTaskRelationVectorBackfill,
		relationVectorEnabled && relationVectorBackfill,
		loops.RelationVectorBackfill)

	startBackgroundTask(tasks, spawn, BackgroundTaskEpisodeGeneration,
		PyTruthy(plugin.GetConfig("episode.enabled", true)) && PyTruthy(plugin.GetConfig("episode.generation_enabled", true)),
		loops.EpisodeGeneration)
}

// CancelBackgroundTasks 对齐 cancel_background_tasks
func CancelBackgroundTasks(tasks *BackgroundTasks) {
	if tasks == nil {
		return
	}
	logger := NewZapLogger(LifecycleOrchestratorLoggerName)

	for _, name := range BackgroundTaskOrder {
		task := tasks.Task(name)
		if task != nil && !task.Done() {
			task.Cancel()
		}
	}
	for _, name := range BackgroundTaskOrder {
		task := tasks.Task(name)
		if task == nil {
			continue
		}
		if err := task.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warning(fmt.Sprintf("后台任务 %s 退出异常: %v", name, err))
		}
	}
	for _, name := range BackgroundTaskOrder {
		tasks.SetTask(name, nil)
	}
}
