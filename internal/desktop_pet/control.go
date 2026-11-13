package desktoppet

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

// Control 对齐 DesktopPetService.control：仅接受已校验的公开操作
func (s *Service) Control(ctx context.Context, action, text string, active bool) (Status, error) {
	switch action {
	case "status":
		return s.Status(), nil
	case "refresh_buttons":
		s.buttonsChanged()
		return s.Status(), nil
	case "show", "hide":
		if _, err := s.call(ctx, map[string]any{"type": action}); err != nil {
			return s.Status(), err
		}
		return s.Status(), nil
	case "say":
		parts := []map[string]any{{"kind": "text", "text": text}}
		if _, err := s.call(ctx, map[string]any{"type": "notice", "parts": parts}); err != nil {
			return s.Status(), err
		}
		return s.Status(), nil
	case "set_thinking":
		command := "idle"
		if active {
			command = "thinking"
		}
		if _, err := s.call(ctx, map[string]any{"type": command}); err != nil {
			return s.Status(), err
		}
		return s.Status(), nil
	default:
		return s.Status(), fmt.Errorf("未知桌宠操作: %s", action)
	}
}

// ApplySkin 对齐 apply_skin：校验并下发皮肤，成功后更新皮肤 ID 并触发 skin_changed
func (s *Service) ApplySkin(ctx context.Context, skinID string) error {
	skin, err := findSkin(s.instanceRoot(), skinID, true)
	if err != nil {
		return err
	}
	if _, err := s.call(ctx, map[string]any{"type": "skin", "skin": skin}); err != nil {
		return err
	}
	s.mu.Lock()
	changed := s.skinID != skinID
	s.skinID = skinID
	s.errMsg = ""
	s.mu.Unlock()
	if changed {
		s.emitHook(ctx, "skin_changed", map[string]any{"skin_id": skinID})
	}
	return nil
}

// Animation 对齐 animation：动画结果带 error 时按 code: message 报错
func (s *Service) Animation(ctx context.Context, owner, action string, args map[string]any) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	result, err := s.call(ctx, map[string]any{
		"type":   "animation",
		"owner":  owner,
		"action": action,
		"args":   args,
	})
	if err != nil {
		return nil, err
	}
	payload, _ := result.(map[string]any)
	if payload == nil {
		return nil, fmt.Errorf("桌宠动画未返回结果")
	}
	if failure, ok := payload["error"]; ok && failure != nil {
		detail, _ := failure.(map[string]any)
		return nil, fmt.Errorf("%v: %v", detail["code"], detail["message"])
	}
	return payload, nil
}

// TodoCommand 下发待办指令并返回窗口结果，对齐 todo_tools 经 _command 的调用
func (s *Service) TodoCommand(ctx context.Context, action, todoID, text string) (map[string]any, error) {
	text = strings.TrimSpace(text)
	todoID = strings.TrimSpace(todoID)
	validateText := func() error {
		length := utf8.RuneCountInString(text)
		if length < 1 || length > 200 {
			return fmt.Errorf("待办内容必须为 1 至 200 字")
		}
		return nil
	}
	validateID := func() error {
		if todoID == "" {
			return fmt.Errorf("请先查询待办并提供 todo_id")
		}
		return nil
	}
	command := map[string]any{"type": "todo", "action": action}
	switch action {
	case "list":
	case "add":
		if err := validateText(); err != nil {
			return nil, err
		}
		command["text"] = text
	case "update":
		if err := validateID(); err != nil {
			return nil, err
		}
		if err := validateText(); err != nil {
			return nil, err
		}
		command["todo_id"] = todoID
		command["text"] = text
	case "delete":
		if err := validateID(); err != nil {
			return nil, err
		}
		command["todo_id"] = todoID
	default:
		return nil, fmt.Errorf("未知待办操作: %s", action)
	}
	result, err := s.call(ctx, command)
	if err != nil {
		return nil, err
	}
	payload, _ := result.(map[string]any)
	if payload == nil {
		return nil, fmt.Errorf("桌宠待办未返回结果")
	}
	return payload, nil
}

// ChatRelay 把桌宠消息投递到内部聊天，返回消息 ID
type ChatRelay func(ctx context.Context, pluginID, content string, images []ChatRequestImage) (string, error)

// SetChatRelay 注入内部聊天中继
func (s *Service) SetChatRelay(relay ChatRelay) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.chatRelay = relay
	s.mu.Unlock()
}

// handleChat 对齐 _chat：读取请求文件后中继到内部聊天并回执
func (s *Service) handleChat(ctx context.Context, requestID string) {
	fail := func(message string) {
		s.setError(fmt.Errorf("%s", message))
		zap.L().Warn("桌宠聊天请求失败",
			zap.String("request_id", requestID),
			zap.String("reason", message))
		if _, err := s.call(ctx, map[string]any{
			"type":       "failed",
			"request_id": requestID,
			"error":      message,
		}); err != nil {
			zap.L().Warn("桌宠聊天失败回执发送失败", zap.Error(err))
		}
	}
	requestDir := filepath.Join(s.instanceRoot(), "temp", "desktop_pet", "requests")
	request, err := ReadChatRequest(requestDir, requestID)
	if err != nil {
		fail(err.Error())
		return
	}
	s.mu.RLock()
	relay := s.chatRelay
	s.mu.RUnlock()
	if relay == nil {
		fail("桌宠聊天中继尚未就绪")
		return
	}
	if _, err := relay(ctx, "builtin.desktop_pet", request.Text, request.Images); err != nil {
		fail(err.Error())
		return
	}
	if _, err := s.call(ctx, map[string]any{"type": "accepted", "request_id": requestID}); err != nil {
		zap.L().Warn("桌宠聊天成功回执发送失败", zap.Error(err))
	}
}

// NotifyThinking 通知桌宠进入思考状态，对齐 notify_thinking
func (s *Service) NotifyThinking() {
	s.mu.RLock()
	ready := s.ready && !s.closing
	s.mu.RUnlock()
	if !ready {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.call(ctx, map[string]any{"type": "thinking"}); err != nil {
			s.setError(err)
			zap.L().Warn("桌宠思考状态下发失败", zap.Error(err))
		}
	}()
}

// NotifyReply 把内部聊天机器人消息渲染为气泡内容下发
// text 仅为调用方兼容保留，气泡内容与 Python 一样只看 raw_message 段
func (s *Service) NotifyReply(segments []map[string]any, text string) {
	_ = text
	s.mu.RLock()
	ready := s.ready && !s.closing
	s.mu.RUnlock()
	if !ready {
		return
	}
	var skipped []string
	parts, err := RenderParts(segments, &skipped)
	if err != nil {
		zap.L().Warn("桌宠回复渲染失败", zap.Error(err))
		return
	}
	if len(parts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.call(ctx, map[string]any{"type": "reply", "parts": parts}); err != nil {
		s.setError(err)
		zap.L().Warn("桌宠回复气泡发送失败", zap.Error(err))
	}
}

// instanceRoot 实例根目录，对齐 get_instance_root
func (s *Service) instanceRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if root := s.spawn.InstanceRoot; root != "" {
		return root
	}
	return s.spawn.ProjectRoot
}
