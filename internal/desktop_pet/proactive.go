package desktoppet

// 桌宠定期主动思考，对齐 desktop_pet/service.py 的 proactive wake。

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	internalchat "MaiGo/internal/core/internal_chat"
)

const (
	maxUnansweredProactiveWakes = 2
	proactiveWakeSilence        = 20 * time.Minute
)

// SetProactiveWake 注入主动思考回调；窗口就绪后按配置间隔触发
func (s *Service) SetProactiveWake(wake func(ctx context.Context) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.proactiveWake = wake
	s.mu.Unlock()
	s.restartProactiveWake()
}

// NotifyUserMessage 主人在桌宠或网页聊天中回复后重置主动唤醒额度
func (s *Service) NotifyUserMessage(platform, userID string, isGroup bool) {
	if s == nil || isGroup || platform != internalchat.Platform {
		return
	}
	trimmed := strings.TrimSpace(userID)
	if trimmed != internalchat.OwnerUserID && trimmed != strconv.FormatInt(internalchat.OwnerNumericID, 10) {
		return
	}
	s.NotifyOwnerChatActivity()
}

// NotifyOwnerChatActivity 记录主人最近活动时间
func (s *Service) NotifyOwnerChatActivity() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.unansweredWakes = 0
	s.lastOwnerActivity = time.Now()
	s.mu.Unlock()
}

func (s *Service) canWakeProactively() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unansweredWakes < maxUnansweredProactiveWakes &&
		time.Since(s.lastOwnerActivity) > proactiveWakeSilence
}

func (s *Service) restartProactiveWake() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.proactiveCancel != nil {
		s.proactiveCancel()
		s.proactiveCancel = nil
	}
	enabled := s.cfg.ProactiveWakeEnabled && s.proactiveWake != nil && s.ready && !s.closing
	intervalMinutes := s.cfg.ProactiveWakeIntervalMinutes
	if intervalMinutes <= 0 {
		intervalMinutes = 30
	}
	wake := s.proactiveWake
	if !enabled {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.proactiveCancel = cancel
	s.mu.Unlock()
	go func() {
		interval := time.Duration(intervalMinutes) * time.Minute
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if s.canWakeProactively() {
					if err := wake(ctx); err != nil {
						zap.L().Error("桌宠定期主动思考唤醒失败", zap.Error(err))
					} else {
						s.mu.Lock()
						s.unansweredWakes++
						s.mu.Unlock()
					}
				}
				timer.Reset(interval)
			}
		}
	}()
}

func (s *Service) stopProactiveWake() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.proactiveCancel != nil {
		s.proactiveCancel()
		s.proactiveCancel = nil
	}
	s.mu.Unlock()
}
