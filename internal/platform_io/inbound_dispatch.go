package platformio

import (
	"context"
	"runtime/debug"

	"go.uber.org/zap"
)

// scheduleInboundDispatch 把已通过路由与去重的入站消息交给后台任务执行
// 对齐 Python PlatformIOManager._schedule_inbound_dispatch 的 create_task 语义
func (m *Manager) scheduleInboundDispatch(ctx context.Context, envelope InboundMessageEnvelope, dispatcher InboundDispatcher) bool {
	m.inboundMu.Lock()
	if m.inboundStopping || m.inboundCtx == nil {
		m.inboundMu.Unlock()
		return false
	}
	m.inboundWG.Add(1)
	managerCtx := m.inboundCtx
	m.inboundMu.Unlock()

	taskCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopAfter := context.AfterFunc(managerCtx, cancel)
	go func() {
		defer m.inboundWG.Done()
		defer cancel()
		defer stopAfter()
		defer func() {
			if recovered := recover(); recovered != nil {
				zap.L().Error("Platform IO 入站消息后台分发 panic",
					append(inboundDispatchLogFields(envelope), zap.Any("panic", recovered), zap.ByteString("stack", debug.Stack()))...)
			}
		}()
		if err := dispatcher(taskCtx, envelope); err != nil && managerCtx.Err() == nil {
			// Stop 取消在途任务时与 Python 对 cancelled 任务不记录 exception 对齐
			zap.L().Error("Platform IO 入站消息后台分发失败",
				append(inboundDispatchLogFields(envelope), zap.Error(err))...)
		}
	}()
	return true
}

// stopInboundDispatch 取消并等待全部在途入站任务，对齐 Python stop 中的 cancel + gather
func (m *Manager) stopInboundDispatch() {
	m.inboundMu.Lock()
	m.inboundStopping = true
	cancel := m.inboundCancel
	m.inboundMu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.inboundWG.Wait()
}

// resetInboundDispatch 为下一次 Start 重建后台分发上下文
func (m *Manager) resetInboundDispatch() {
	m.inboundMu.Lock()
	if m.inboundCancel != nil {
		m.inboundCancel()
	}
	// 持锁等待，阻止新任务在 WaitGroup 复位窗口内 Add
	m.inboundWG.Wait()
	m.inboundCtx, m.inboundCancel = context.WithCancel(context.Background())
	m.inboundStopping = false
	m.inboundMu.Unlock()
}

// inboundDispatchLogFields 统一后台分发日志字段
func inboundDispatchLogFields(envelope InboundMessageEnvelope) []zap.Field {
	fields := []zap.Field{
		zap.String("driver_id", envelope.DriverID),
		zap.String("route_key", envelope.RouteKey.Platform+"/"+envelope.RouteKey.AccountID+"/"+envelope.RouteKey.Scope),
		zap.String("external_message_id", envelope.ExternalMessageID),
		zap.String("dedupe_key", envelope.DedupeKey),
	}
	if envelope.ChatMessage != nil {
		fields = append(fields,
			zap.String("platform", envelope.ChatMessage.Platform),
			zap.String("account_id", envelope.ChatMessage.AccountID),
			zap.String("scope", envelope.ChatMessage.Scope),
			zap.String("message_id", envelope.ChatMessage.MessageID),
		)
	}
	return fields
}
