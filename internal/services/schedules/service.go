package schedules

// 独立于桌宠窗口的单次日程调度，向真实聊天流投递内部任务，对齐 services/schedules/service.py。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/common/database"
)

// Hooks 宿主运行时依赖
type Hooks struct {
	// OwnerCheck 判断会话是否为主人内部聊天
	OwnerCheck func(ctx context.Context, sessionID string) bool
	// DesktopReady 桌宠窗口是否可接收用户提醒
	DesktopReady func() bool
	// DesktopReminder 在桌宠窗口显示用户提醒
	DesktopReminder func(ctx context.Context, text string) error
	// Dispatch 向真实聊天流投递 Bot 日程
	Dispatch func(ctx context.Context, item map[string]any) error
	// RefreshDesktop 通知桌宠刷新待办列表
	RefreshDesktop func()
}

// Service 日程服务
type Service struct {
	mu      sync.Mutex
	path    string
	store   *TodoStore
	hooks   Hooks
	cancel  context.CancelFunc
	worker  chan struct{}
	started bool
}

var defaultService = &Service{}

// Default 返回进程内日程服务
func Default() *Service { return defaultService }

// Start 加载存储并启动调度协程
func (s *Service) Start(ctx context.Context, path string, hooks Hooks) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		if strings.TrimSpace(path) == "" {
			path = filepath.Join(memory.InstanceRoot(), "data", "plugins", "sengokucola.desktop-pet", "todos.json")
		}
		s.path = path
		s.store = NewTodoStore(path)
	}
	if s.hooks.OwnerCheck == nil {
		s.hooks = hooks
	}
	if s.started {
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.worker = make(chan struct{})
	s.started = true
	go s.run(workerCtx, s.worker)
	return nil
}

// Stop 停止调度并等待在途命令结束
func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	worker := s.worker
	s.cancel = nil
	s.worker = nil
	s.started = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if worker != nil {
		<-worker
	}
}

// Started 日程服务是否已启动
func (s *Service) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *Service) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				zap.L().Error("日程调度检查失败", zap.Error(err))
			}
		}
	}
}

// Command 执行日程命令；sessionID 非空时限制在当前聊天可见范围
func (s *Service) Command(ctx context.Context, command map[string]any, sessionID string) (map[string]any, error) {
	s.mu.Lock()
	store := s.store
	hooks := s.hooks
	s.mu.Unlock()
	if store == nil {
		return nil, errors.New("日程服务尚未启动")
	}
	result, err := s.execute(ctx, store, hooks, command, sessionID)
	if err != nil {
		return nil, err
	}
	action := asString(command["action"])
	if action != "list" && action != "image" && action != "claim_due" {
		if hooks.RefreshDesktop != nil {
			hooks.RefreshDesktop()
		}
	}
	return result, nil
}

func (s *Service) execute(ctx context.Context, store *TodoStore, hooks Hooks, command map[string]any, sessionID string) (map[string]any, error) {
	if sessionID != "" {
		owner := hooks.OwnerCheck != nil && hooks.OwnerCheck(ctx, sessionID)
		belongs := func(item map[string]any) bool {
			return asString(item["session_id"]) == sessionID || (owner && asString(item["session_id"]) == "")
		}
		action := asString(command["action"])
		if action == "list" {
			includeCompleted, _ := command["include_completed"].(bool)
			items := make([]map[string]any, 0)
			for _, item := range store.List(includeCompleted) {
				if belongs(item) {
					items = append(items, item)
				}
			}
			return map[string]any{"items": items}, nil
		}
		if action != "add" && action != "update" && action != "cancel" && action != "complete" {
			return nil, errors.New("不支持的日程操作")
		}
		if action != "add" {
			todoID := asString(command["todo_id"])
			found := false
			for _, item := range store.List(true) {
				if asString(item["id"]) == todoID && belongs(item) {
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("当前聊天中不存在此日程，请先查询真实 ID")
			}
		}
		options, _ := command["options"].(map[string]any)
		if action == "add" || action == "update" {
			target := sessionID
			if raw, exists := options["session_id"]; exists {
				target = asString(raw)
			}
			if target != sessionID {
				return nil, errors.New("不能修改日程所属聊天")
			}
		}
	}
	return ExecuteTodo(store, command)
}

// Tick 领取到期事项并逐个投递
func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	store := s.store
	hooks := s.hooks
	s.mu.Unlock()
	if store == nil {
		return errors.New("日程服务尚未启动")
	}
	command := map[string]any{"action": "claim_due"}
	desktopReady := hooks.DesktopReady != nil && hooks.DesktopReady()
	if !desktopReady {
		command["kind"] = "bot"
	}
	result, err := s.Command(ctx, command, "")
	if err != nil {
		return err
	}
	items, _ := result["items"].([]map[string]any)
	for _, item := range items {
		itemID := asString(item["id"])
		revision := asString(item["revision"])
		if asString(item["kind"]) == "user" {
			if hooks.DesktopReminder == nil {
				s.finish(itemID, revision, "桌宠提醒通道未接线")
				continue
			}
			if err := hooks.DesktopReminder(ctx, asString(item["text"])); err != nil {
				s.finish(itemID, revision, err.Error())
				continue
			}
			s.finish(itemID, revision, "")
			continue
		}
		if hooks.Dispatch == nil {
			s.finish(itemID, revision, "日程投递通道未接线")
			continue
		}
		if err := hooks.Dispatch(ctx, item); err != nil {
			zap.L().Error("日程触发失败", zap.String("id", itemID), zap.Error(err))
			s.finish(itemID, revision, err.Error())
			continue
		}
		s.finish(itemID, revision, "")
	}
	return nil
}

// TriggerCondition 模型判断条件成立后领取并立即投递
func (s *Service) TriggerCondition(ctx context.Context, todoID, revision, evidence string) (map[string]any, error) {
	return s.triggerCondition(ctx, todoID, revision, evidence, false)
}

// TriggerConditionForApp 桌面应用条件已由设备侧确认后领取并投递
func (s *Service) TriggerConditionForApp(ctx context.Context, todoID, revision, evidence string) (map[string]any, error) {
	return s.triggerCondition(ctx, todoID, revision, evidence, true)
}

func (s *Service) triggerCondition(ctx context.Context, todoID, revision, evidence string, appEvidence bool) (map[string]any, error) {
	s.mu.Lock()
	store := s.store
	hooks := s.hooks
	s.mu.Unlock()
	if store == nil {
		return nil, errors.New("日程服务尚未启动")
	}
	var original map[string]any
	for _, item := range store.List(false) {
		if asString(item["id"]) == todoID {
			original = item
			break
		}
	}
	if original == nil || asString(original["revision"]) != revision {
		return nil, errors.New("待办已修改或删除，请重新查询")
	}
	if asString(original["condition_type"]) == "app" && !appEvidence {
		return nil, errors.New("应用条件需要桌面进程检测，不能由模型直接触发")
	}
	item, err := store.ClaimCondition(todoID, revision, evidence)
	if err != nil {
		return nil, err
	}
	current := store.List(false)
	found := false
	for _, entry := range current {
		if asString(entry["id"]) == todoID && asString(entry["revision"]) == revision {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("待办已修改或删除，本次触发取消")
	}
	var dispatchErr error
	if asString(item["kind"]) == "user" {
		if hooks.DesktopReminder == nil {
			dispatchErr = errors.New("桌宠提醒通道未接线")
		} else {
			dispatchErr = hooks.DesktopReminder(ctx, asString(item["text"]))
		}
	} else {
		if hooks.Dispatch == nil {
			dispatchErr = errors.New("日程投递通道未接线")
		} else {
			dispatchErr = hooks.Dispatch(ctx, item)
		}
	}
	if dispatchErr != nil {
		store.Finish(todoID, revision, dispatchErr.Error())
		return nil, dispatchErr
	}
	store.Finish(todoID, revision, "")
	return map[string]any{"item": item}, nil
}

func (s *Service) finish(todoID, revision, errorText string) {
	_, _ = s.Command(context.Background(), map[string]any{
		"action": "finish", "todo_id": todoID, "revision": revision, "error": errorText,
	}, "")
}

// ListItems 返回未完成事项副本，供每轮待办上下文重建
func (s *Service) ListItems() []map[string]any {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.List(false)
}
