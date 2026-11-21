package schedules

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Hooks struct {
	OwnerCheck      func(ctx context.Context, sessionID string) bool
	OwnerSession    func(ctx context.Context) (string, error)
	SessionName     func(ctx context.Context, sessionID string) (string, error)
	DesktopReady    func() bool
	DesktopReminder func(ctx context.Context, text string) error
	Dispatch        func(ctx context.Context, item map[string]any) error
	RefreshDesktop  func()
}

type Service struct {
	mu     sync.Mutex
	repo   *Repository
	hooks  Hooks
	cancel context.CancelFunc
	worker chan struct{}
}

var defaultService = &Service{}

func Default() *Service { return defaultService }

func (s *Service) Start(ctx context.Context, directory, legacyPath string, hooks Hooks) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker != nil {
		return nil
	}
	repository, err := openRepository(ctx, directory, legacyPath, hooks.OwnerSession)
	if err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.repo, s.hooks, s.cancel = repository, hooks, cancel
	s.worker = make(chan struct{})
	go s.run(workerCtx, s.worker)
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	worker := s.worker
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	if worker == nil {
		return
	}
	<-worker
	s.mu.Lock()
	if s.worker == worker {
		s.worker, s.cancel, s.repo = nil, nil, nil
		s.hooks = Hooks{}
	}
	s.mu.Unlock()
}

func (s *Service) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.worker != nil
}

func (s *Service) IsOwner(ctx context.Context, sessionID string) bool {
	s.mu.Lock()
	check := s.hooks.OwnerCheck
	s.mu.Unlock()
	return sessionID != "" && check != nil && check(ctx, sessionID)
}

func (s *Service) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			zap.L().Error("日程调度检查失败", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) Command(ctx context.Context, command map[string]any, sessionID string) (map[string]any, error) {
	action := asString(command["action"])
	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		return nil, errors.New("日程服务尚未启动")
	}
	if sessionID != "" && !allowedAction(ctx, s.hooks, sessionID, action) {
		s.mu.Unlock()
		return nil, errors.New("不支持的日程操作")
	}
	result, err := s.repo.execute(ctx, command, sessionID, s.hooks)
	refresh := s.hooks.RefreshDesktop
	s.mu.Unlock()
	if err == nil && refresh != nil && action != "list" && action != "image" && action != "claim_due" {
		refresh()
	}
	return result, err
}

func allowedAction(ctx context.Context, hooks Hooks, sessionID, action string) bool {
	switch action {
	case "list", "add", "update", "complete", "cancel", "claim_due":
		return true
	case "delete", "claim_condition", "finish":
		return hooks.OwnerCheck != nil && hooks.OwnerCheck(ctx, sessionID)
	default:
		return false
	}
}

func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		return errors.New("日程服务尚未启动")
	}
	sessions := s.repo.sessionIDs()
	ready := s.hooks.DesktopReady
	s.mu.Unlock()
	command := map[string]any{"action": "claim_due"}
	if ready == nil || !ready() {
		command["kind"] = "bot"
	}
	for _, sessionID := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		claimed, err := s.Command(ctx, command, sessionID)
		if err != nil {
			return err
		}
		for _, item := range claimed["items"].([]map[string]any) {
			if err := s.dispatchClaimed(ctx, item); err != nil {
				zap.L().Error("日程触发失败", zap.String("id", asString(item["id"])), zap.Error(err))
			}
		}
	}
	return nil
}

func (s *Service) dispatchClaimed(ctx context.Context, claimed map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		return errors.New("日程服务尚未启动")
	}
	store, item, err := s.repo.find(asString(claimed["session_id"]), asString(claimed["id"]))
	if err != nil || item["revision"] != claimed["revision"] || item["status"] != "dispatching" {
		s.mu.Unlock()
		return errors.New("待办已修改或删除，本次触发取消")
	}
	dispatchErr := s.dispatch(ctx, item)
	errorText := ""
	if dispatchErr != nil {
		errorText = dispatchErr.Error()
	}
	finishErr := store.Finish(asString(item["id"]), asString(item["revision"]), errorText)
	refresh := s.hooks.RefreshDesktop
	s.mu.Unlock()
	if refresh != nil && finishErr == nil {
		refresh()
	}
	return errors.Join(dispatchErr, finishErr)
}

func (s *Service) dispatch(ctx context.Context, item map[string]any) error {
	if item["kind"] == "user" {
		if s.hooks.DesktopReminder == nil {
			return errors.New("桌宠提醒通道未接线")
		}
		return s.hooks.DesktopReminder(ctx, asString(item["text"]))
	}
	if s.hooks.Dispatch == nil {
		return errors.New("日程投递通道未接线")
	}
	return s.hooks.Dispatch(ctx, item)
}

func (s *Service) TriggerCondition(ctx context.Context, sessionID, todoID, revision, evidence string) (map[string]any, error) {
	if !s.IsOwner(ctx, sessionID) {
		return nil, errors.New("条件待办仅限主人内部聊天")
	}
	return s.triggerCondition(ctx, sessionID, todoID, revision, evidence, false)
}

func (s *Service) TriggerConditionForApp(ctx context.Context, todoID, revision, evidence string) (map[string]any, error) {
	return s.triggerCondition(ctx, "", todoID, revision, evidence, true)
}

func (s *Service) triggerCondition(ctx context.Context, sessionID, todoID, revision, evidence string, appEvidence bool) (map[string]any, error) {
	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		return nil, errors.New("日程服务尚未启动")
	}
	store, original, err := s.repo.find(sessionID, todoID)
	if err != nil || original["revision"] != revision {
		s.mu.Unlock()
		return nil, errors.New("待办已修改或删除，请重新查询")
	}
	if original["condition_type"] == "app" && !appEvidence {
		s.mu.Unlock()
		return nil, errors.New("应用条件需要桌面进程检测，不能由模型直接触发")
	}
	claimed, err := store.ClaimCondition(todoID, revision, evidence)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := s.dispatchClaimed(ctx, claimed); err != nil {
		return nil, err
	}
	return map[string]any{"id": todoID, "status": "triggered", "evidence": claimed["evidence"]}, nil
}
