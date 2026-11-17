package desktoppet

// 可丢弃的桌宠阶段摘要，对齐 desktop_pet/thinking_caption.py。

import (
	"context"
	"strings"
	"time"
)

const (
	thinkingCaptionPlaceholder = "麦麦思考中......"
	thinkingCaptionTimeout     = 2 * time.Second
	thinkingCaptionMaxRunes    = 18
)

var inactiveThinkingStages = map[string]struct{}{
	"空闲": {}, "等待": {}, "错误": {}, "失败": {}, "停止": {}, "Focus 共享": {},
}

// SetCaptionSummarizer 注入 fast_model 阶段摘要器
func (s *Service) SetCaptionSummarizer(summarize func(ctx context.Context, data map[string]any) (string, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.captionSummarizer = summarize
	s.mu.Unlock()
}

// ObserveStage 接收 Planner 阶段事件并刷新像素字幕
func (s *Service) ObserveStage(data map[string]any) {
	s.mu.RLock()
	ready, closing, enabled := s.ready, s.closing, s.cfg.ShowCurrentTask
	summarize := s.captionSummarizer
	ownerSession := s.ownerSessionID
	s.mu.RUnlock()
	if !ready || closing || !enabled || summarize == nil {
		s.clearThinkingCaption()
		return
	}
	sessionID := asString(data["session_id"])
	if sessionID == "" || ownerSession == nil {
		return
	}
	ownerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	ownerID, err := ownerSession(ownerCtx)
	cancel()
	if err != nil || sessionID != ownerID {
		return
	}
	key := strings.Join([]string{
		sessionID, asString(data["stage"]), asString(data["detail"]), asString(data["round_text"]),
		asString(data["stage_started_at"]),
	}, "\x00")
	s.mu.Lock()
	if key == s.captionKey {
		s.mu.Unlock()
		return
	}
	if s.captionCancel != nil {
		s.captionCancel()
	}
	s.captionRevision++
	revision := s.captionRevision
	s.captionKey = key
	captionCtx, captionCancel := context.WithCancel(context.Background())
	s.captionCancel = captionCancel
	s.mu.Unlock()
	go s.runThinkingCaption(captionCtx, revision, data, summarize)
}

func (s *Service) runThinkingCaption(ctx context.Context, revision uint64, data map[string]any, summarize func(context.Context, map[string]any) (string, error)) {
	stage := asString(data["stage"])
	if _, inactive := inactiveThinkingStages[stage]; inactive {
		s.publishThinkingCaption(ctx, revision, "")
		return
	}
	s.publishThinkingCaption(ctx, revision, thinkingCaptionPlaceholder)
	summaryCtx, cancel := context.WithTimeout(ctx, thinkingCaptionTimeout)
	defer cancel()
	text, err := summarize(summaryCtx, data)
	if err != nil || summaryCtx.Err() != nil {
		return
	}
	cleaned := strings.TrimSpace(strings.Trim(strings.Join(strings.Fields(text), " "), "\"“”"))
	cleaned = string([]rune(cleaned)[:min(len([]rune(cleaned)), thinkingCaptionMaxRunes)])
	if cleaned != "" {
		s.publishThinkingCaption(ctx, revision, cleaned)
	}
}

func (s *Service) publishThinkingCaption(ctx context.Context, revision uint64, text string) {
	s.mu.RLock()
	current := s.captionRevision
	s.mu.RUnlock()
	if current != revision || ctx.Err() != nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_ = s.sendEvent(sendCtx, map[string]any{
		"type": "thinking_caption", "text": text, "revision": revision,
	})
}

func (s *Service) clearThinkingCaption() {
	s.mu.Lock()
	if s.captionCancel != nil {
		s.captionCancel()
		s.captionCancel = nil
	}
	s.captionRevision++
	s.captionKey = ""
	s.mu.Unlock()
}
