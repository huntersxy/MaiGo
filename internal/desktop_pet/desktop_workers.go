package desktoppet

// 桌宠桌面读取、应用条件待办与探索任务，对齐 desktop_pet/service.py 的 screen/explore/todo 分支。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/common/media"
	"MaiGo/internal/services/schedules"
)

// SetPromptRenderer 注入 Prompt 渲染器
func (s *Service) SetPromptRenderer(render func(name string) (string, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.promptRenderer = render
	s.mu.Unlock()
}

// SetOwnerTaskEnqueuer 注入主人聊天流内部任务投递器
func (s *Service) SetOwnerTaskEnqueuer(
	enqueue func(ctx context.Context, sourceKind, text, reason string, images []media.Ref) error,
) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ownerTaskEnqueuer = enqueue
	s.mu.Unlock()
}

// SetOwnerSessionID 注入主人聊天流解析器
func (s *Service) SetOwnerSessionID(resolve func(ctx context.Context) (string, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ownerSessionID = resolve
	s.mu.Unlock()
}

func (s *Service) explorationDataDir() string {
	return filepath.Join(s.instanceRoot(), "data")
}

func (s *Service) screenReading() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.screenCancel != nil
}

func (s *Service) requireScreenAccess() error {
	s.mu.RLock()
	enabled, ready, closing, remote := s.cfg.ScreenReadEnabled, s.ready, s.closing, s.remote
	s.mu.RUnlock()
	if !enabled {
		return fmt.Errorf("请先在 WebUI 桌宠设置开启允许读取桌面")
	}
	if !ready || closing {
		return fmt.Errorf("桌宠未就绪，无法读取桌面")
	}
	if remote != nil && !remote.capabilities["screen_read"] {
		return fmt.Errorf("请在远端桌宠客户端允许读取本机桌面")
	}
	return nil
}

// CaptureScreen 捕获本机或远端主屏
func (s *Service) CaptureScreen(ctx context.Context) (DesktopScreenshot, error) {
	if err := s.requireScreenAccess(); err != nil {
		return DesktopScreenshot{}, err
	}
	s.mu.RLock()
	remote := s.remote
	s.mu.RUnlock()
	var shot DesktopScreenshot
	if remote == nil {
		captured, err := captureDesktop()
		if err != nil {
			return DesktopScreenshot{}, err
		}
		shot = captured
	} else {
		result, err := s.call(ctx, map[string]any{"type": "capture_screen"})
		if err != nil {
			return DesktopScreenshot{}, err
		}
		payload, _ := result.(map[string]any)
		decoded, err := DecodeRemoteScreenshot(payload)
		if err != nil {
			return DesktopScreenshot{}, err
		}
		s.mu.RLock()
		still := s.remote == remote
		s.mu.RUnlock()
		if !still {
			return DesktopScreenshot{}, fmt.Errorf("截图过程中远端桌宠已断开")
		}
		shot = decoded
	}
	if err := s.requireScreenAccess(); err != nil {
		return DesktopScreenshot{}, err
	}
	return shot, nil
}

func (s *Service) publishScreenState(ctx context.Context) {
	s.mu.RLock()
	ready, closing := s.ready, s.closing
	enabled := s.cfg.ScreenReadEnabled
	remote := s.remote
	reading := s.screenCancel != nil
	busy := s.screenBusy
	last := s.screenLastCapture
	screenErr := s.screenError
	s.mu.RUnlock()
	if !ready || closing {
		return
	}
	if remote != nil && !remote.capabilities["screen_read"] {
		enabled = false
	}
	_ = s.sendEvent(ctx, map[string]any{
		"type": "screen_state", "enabled": enabled, "reading": reading, "busy": busy,
		"captured_at": last, "error": screenErr,
	})
}

func (s *Service) stopScreenReader() {
	s.mu.Lock()
	cancel := s.screenCancel
	s.screenCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) handleScreenAction(ctx context.Context, action string) {
	s.mu.Lock()
	s.screenError = ""
	s.mu.Unlock()
	switch action {
	case "stop":
		s.stopScreenReader()
	case "start":
		if err := s.requireScreenAccess(); err != nil {
			s.setScreenError(err)
			break
		}
		s.mu.Lock()
		if s.screenCancel == nil {
			readerCtx, cancel := context.WithCancel(context.Background())
			s.screenCancel = cancel
			s.screenGeneration++
			generation := s.screenGeneration
			s.mu.Unlock()
			go s.runScreenReader(readerCtx, generation)
		} else {
			s.mu.Unlock()
		}
	case "read":
		s.readScreenOnce(ctx)
	default:
		s.setScreenError(fmt.Errorf("未知桌面读取操作"))
	}
	s.publishScreenState(ctx)
}

func (s *Service) setScreenError(err error) {
	s.mu.Lock()
	s.screenError = err.Error()
	s.mu.Unlock()
	zap.L().Warn("桌宠桌面读取失败", zap.Error(err))
}

func (s *Service) screenIntervals() (time.Duration, time.Duration, float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	delivery := time.Duration(s.cfg.ScreenReadIntervalSeconds) * time.Second
	if delivery <= 0 {
		delivery = 10 * time.Second
	}
	sample := time.Duration(s.cfg.ScreenSampleIntervalSeconds * float64(time.Second))
	if sample <= 0 {
		sample = 2 * time.Second
	}
	threshold := s.cfg.ScreenChangeThreshold
	if threshold <= 0 {
		threshold = 0.015
	}
	return delivery, sample, threshold, s.cfg.ScreenReadEnabled && s.ready && !s.closing
}

func (s *Service) runScreenReader(ctx context.Context, generation uint64) {
	defer func() {
		s.mu.Lock()
		if s.screenGeneration == generation {
			s.screenCancel = nil
		}
		s.mu.Unlock()
		s.publishScreenState(context.Background())
	}()
	timeline := &ScreenTimeline{}
	deadline := time.Now()
	firstSample := true
	for {
		delivery, sample, threshold, enabled := s.screenIntervals()
		if !enabled || ctx.Err() != nil {
			return
		}
		var shot DesktopScreenshot
		var captureErr error
		s.mu.RLock()
		remote := s.remote
		s.mu.RUnlock()
		if remote == nil {
			shot, captureErr = s.CaptureScreen(ctx)
		} else {
			if accessErr := s.requireScreenAccess(); accessErr != nil {
				s.setScreenError(accessErr)
				return
			}
			result, commandErr := s.call(ctx, map[string]any{
				"type": "sample_screen", "reset": firstSample, "threshold": threshold,
			})
			captureErr = commandErr
			if commandErr == nil {
				if payload, ok := result.(map[string]any); ok && payload["unchanged"] == true {
					shot = DesktopScreenshot{}
				} else {
					shot, captureErr = DecodeRemoteScreenshot(asMap(result))
				}
			}
		}
		firstSample = false
		if captureErr != nil {
			s.setScreenError(captureErr)
			return
		}
		if shot.Data != nil {
			timeline.Add(shot, threshold)
		}
		now := time.Now()
		if now.After(deadline) {
			if timeline.Changed && len(timeline.Frames) > 0 {
				frames := append([]DesktopScreenshot(nil), timeline.Frames...)
				storyboard, composeErr := ComposeScreenTimeline(frames)
				if composeErr != nil {
					s.setScreenError(composeErr)
					return
				}
				frameTimes := make([]string, len(frames))
				for index, frame := range frames {
					frameTimes[index] = frame.CapturedAt
				}
				s.deliverScreen(ctx, storyboard, frameTimes)
				timeline.Delivered()
			}
			deadline = now.Add(delivery)
		}
		sleep := sample
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		if sleep < 100*time.Millisecond {
			sleep = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
	}
}

func (s *Service) readScreenOnce(ctx context.Context) {
	s.mu.Lock()
	if s.screenBusy {
		s.mu.Unlock()
		return
	}
	s.screenBusy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.screenBusy = false
		s.mu.Unlock()
		s.publishScreenState(ctx)
	}()
	s.publishScreenState(ctx)
	shot, err := s.CaptureScreen(ctx)
	if err != nil {
		s.setScreenError(err)
		return
	}
	s.deliverScreen(ctx, shot, nil)
}

func (s *Service) deliverScreen(ctx context.Context, shot DesktopScreenshot, frameTimes []string) {
	s.mu.Lock()
	s.screenBusy = true
	render := s.promptRenderer
	enqueue := s.ownerTaskEnqueuer
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.screenBusy = false
		s.mu.Unlock()
		s.publishScreenState(ctx)
	}()
	s.publishScreenState(ctx)
	if render == nil || enqueue == nil {
		s.setScreenError(fmt.Errorf("桌宠桌面阅读依赖未接线"))
		return
	}
	text, err := render("desktop_pet_screen_read")
	if err != nil || strings.TrimSpace(text) == "" {
		s.setScreenError(fmt.Errorf("渲染桌面阅读提示词失败: %w", err))
		return
	}
	mode := "once"
	if frameTimes != nil {
		mode = "continuous"
	}
	text += fmt.Sprintf("\n[captured_at=%s; mode=%s; screen=primary]", shot.CapturedAt, mode)
	if frameTimes != nil {
		encoded, _ := json.Marshal(map[string]any{"frame_times": frameTimes})
		text += "\n[timeline: left-to-right, top-to-bottom; selected keyframes; not a live single frame]\n"
		text += string(encoded)
	}
	if err := s.requireScreenAccess(); err != nil {
		s.setScreenError(err)
		return
	}
	tempDir := filepath.Join(s.instanceRoot(), "temp", "desktop_pet")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		s.setScreenError(err)
		return
	}
	file, err := os.CreateTemp(tempDir, "screen-*.png")
	if err != nil {
		s.setScreenError(err)
		return
	}
	path := file.Name()
	if _, err := file.Write(shot.Data); err != nil {
		file.Close()
		os.Remove(path)
		s.setScreenError(err)
		return
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		s.setScreenError(err)
		return
	}
	defer os.Remove(path)
	imageRef := media.Ref{Hash: shot.Digest, Path: path, Mime: "image/png", Bytes: int64(len(shot.Data))}
	if err := enqueue(ctx, "desktop_pet_screen", text, "桌宠桌面阅读", []media.Ref{imageRef}); err != nil {
		s.setScreenError(err)
		return
	}
	s.mu.Lock()
	s.screenLastCapture = shot.CapturedAt
	s.mu.Unlock()
}

func (s *Service) publishExplorationState(ctx context.Context) {
	s.mu.RLock()
	ready, closing, enabled := s.ready, s.closing, s.cfg.ExplorationEnabled
	s.mu.RUnlock()
	if !ready || closing {
		return
	}
	_ = s.sendEvent(ctx, map[string]any{"type": "exploration_state", "enabled": enabled})
}

func (s *Service) startAppTodoLoop() {
	s.mu.Lock()
	if s.appTodoCancel != nil {
		s.appTodoCancel()
		s.appTodoCancel = nil
	}
	if !s.cfg.Enabled {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.appTodoCancel = cancel
	s.mu.Unlock()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.RLock()
				active := s.ready && !s.closing
				s.mu.RUnlock()
				if !active {
					continue
				}
				if err := s.checkAppTodos(ctx); err != nil {
					s.mu.Lock()
					s.errMsg = err.Error()
					s.mu.Unlock()
					zap.L().Warn("桌宠应用条件检测失败", zap.Error(err))
				}
			}
		}
	}()
}

func (s *Service) stopAppTodoLoop() {
	s.mu.Lock()
	cancel := s.appTodoCancel
	s.appTodoCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) checkAppTodos(ctx context.Context) error {
	result, err := schedules.Default().Command(ctx, map[string]any{"action": "list"}, "")
	if err != nil {
		return err
	}
	items, _ := result["items"].([]map[string]any)
	candidates := make([]map[string]any, 0)
	for _, item := range items {
		if asString(item["condition_type"]) != "app" || asString(item["status"]) != "pending" {
			continue
		}
		due, hasDue := item["due_at"]
		if hasDue && due != nil && asFloat(due) > float64(time.Now().UnixNano())/1e9 {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return nil
	}
	applications, err := s.runningApplications(ctx)
	if err != nil {
		return err
	}
	for _, item := range candidates {
		evidence := matchingApplication(asString(item["condition"]), applications)
		if evidence == "" {
			continue
		}
		_, err := schedules.Default().TriggerConditionForApp(ctx, asString(item["id"]), asString(item["revision"]), evidence)
		if err != nil {
			zap.L().Info("应用条件待办本次未触发", zap.String("todo_id", asString(item["id"])), zap.Error(err))
		}
	}
	return nil
}

func (s *Service) runningApplications(ctx context.Context) ([]map[string]string, error) {
	s.mu.RLock()
	remote := s.remote
	s.mu.RUnlock()
	if remote == nil {
		return runningApplications()
	}
	result, err := s.call(ctx, map[string]any{"type": "applications"})
	if err != nil {
		return nil, err
	}
	payload, _ := result.(map[string]any)
	rawItems, _ := payload["items"].([]any)
	if len(rawItems) > 10000 {
		return nil, fmt.Errorf("远端应用列表无效")
	}
	items := make([]map[string]string, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("远端应用信息无效")
		}
		process, processOK := item["process"].(string)
		title, titleOK := item["title"].(string)
		if !processOK || !titleOK || len(process) > 4096 || len(title) > 4096 {
			return nil, fmt.Errorf("远端应用信息无效")
		}
		items = append(items, map[string]string{"process": process, "title": title})
	}
	return items, nil
}

// publishExplorationTasks 回传探索任务列表
func (s *Service) publishExplorationTasks(ctx context.Context, taskID, requestID string) {
	payload := map[string]any{
		"type": "exploration_tasks", "request_id": requestID, "items": []any{}, "selected": nil, "error": "",
	}
	sessionID, err := s.explorationSessionID(ctx)
	if err != nil {
		payload["error"] = err.Error()
		_ = s.sendEvent(ctx, payload)
		return
	}
	recordsAny, err := ExplorationTasks(s.explorationDataDir(), sessionID, "list", "", "", "", "", nil)
	if err != nil {
		payload["error"] = err.Error()
		_ = s.sendEvent(ctx, payload)
		return
	}
	records, _ := recordsAny.([]map[string]any)
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"id": record["id"], "goal": record["goal"], "status": record["status"],
			"created_at": record["created_at"], "updated_at": record["updated_at"],
			"resource_count":   len(anyList(record["resources"])),
			"experience_count": len(anyList(record["experiences"])),
		}
		items = append(items, item)
	}
	payload["items"] = items
	var selected map[string]any
	if taskID != "" {
		for _, record := range records {
			if asString(record["id"]) == taskID {
				selected = record
				break
			}
		}
	} else if len(records) > 0 {
		selected = records[0]
	}
	if selected != nil {
		copied := copyAnyMap(selected)
		refs := make([]any, 0)
		for _, raw := range anyList(selected["experiences"]) {
			if experience, ok := raw.(map[string]any); ok {
				refs = append(refs, ExperienceRef(experience))
			}
		}
		copied["experience_refs"] = refs
		payload["selected"] = copied
	}
	_ = s.sendEvent(ctx, payload)
}

func (s *Service) explorationSessionID(ctx context.Context) (string, error) {
	s.mu.RLock()
	resolve := s.ownerSessionID
	s.mu.RUnlock()
	if resolve == nil {
		return "", fmt.Errorf("请先在桌宠或网页聊天中建立唯一的主人会话")
	}
	sessionID, err := resolve(ctx)
	if err != nil || strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("请先在桌宠或网页聊天中建立唯一的主人会话")
	}
	return sessionID, nil
}

// handleExplore 创建探索任务并投递内部任务
func (s *Service) handleExplore(ctx context.Context, goal string) {
	s.mu.RLock()
	enabled := s.cfg.ExplorationEnabled
	render := s.promptRenderer
	enqueue := s.ownerTaskEnqueuer
	s.mu.RUnlock()
	var taskID string
	var failure string
	defer func() {
		_ = s.sendEvent(ctx, map[string]any{"type": "exploration_result", "error": failure, "task_id": taskID})
	}()
	if !enabled {
		failure = "请先在实验性功能中开启桌宠自由探索"
		return
	}
	sessionID, err := s.explorationSessionID(ctx)
	if err != nil {
		failure = err.Error()
		return
	}
	created, err := ExplorationTasks(s.explorationDataDir(), sessionID, "create", "", goal, "", "", nil)
	if err != nil {
		failure = err.Error()
		return
	}
	task, _ := created.(map[string]any)
	taskID = asString(task["id"])
	if render == nil || enqueue == nil {
		failure = "桌宠探索依赖未接线"
		return
	}
	text, err := render("desktop_pet_exploration")
	if err != nil {
		failure = err.Error()
	} else {
		text = text + "\n[exploration_task_id=" + taskID + "]\n" + strings.TrimSpace(goal)
		if err := enqueue(ctx, "desktop_pet_exploration", text, "主人发起探索任务", nil); err != nil {
			failure = err.Error()
		}
	}
	if failure != "" {
		_, _ = ExplorationTasks(s.explorationDataDir(), sessionID, "fail", taskID, "", failure, "", nil)
	}
}

func asMap(value any) map[string]any {
	if mapping, ok := value.(map[string]any); ok {
		return mapping
	}
	return map[string]any{}
}

func copyAnyMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
