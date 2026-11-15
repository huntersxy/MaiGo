package host

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
	"MaiGo/internal/plugin_runtime/transport"
)

// dispatchAsyncRequest 异步处理 Runner 请求并串行写回响应，对齐 rpc_server._handle_request
func (s *RPCServer) dispatchAsyncRequest(ctx context.Context, conn *transport.FramedConn, env protocol.Envelope) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		zap.L().Error("Runner RPC 请求处理 panic",
			zap.String("method", env.Method),
			zap.String("plugin_id", env.PluginID),
			zap.Int64("request_id", env.RequestID),
			zap.Error(fmt.Errorf("panic: %v", recovered)),
			zap.Stack("stack"))
		// 对齐 Python _handle_request 的 Exception 分支：回 E_UNKNOWN，不吞掉真实失败
		s.sendHandlerResponse(ctx, conn, env.MakeErrorResponse(
			string(protocol.ErrorCodeUnknown),
			fmt.Sprintf("RPC 请求处理异常: %v", recovered),
			nil,
		))
	}()

	resp := s.dispatchRequest(ctx, env)
	s.sendHandlerResponse(ctx, conn, resp)
}

// dispatchAsyncBroadcast 异步处理 Runner 广播，对齐 rpc_server._handle_broadcast
func (s *RPCServer) dispatchAsyncBroadcast(ctx context.Context, env protocol.Envelope) {
	if err := s.dispatchBroadcast(ctx, env); err != nil {
		zap.L().Warn("Runner 广播处理失败",
			zap.String("method", env.Method),
			zap.String("plugin_id", env.PluginID),
			zap.Error(err))
	}
}

// sendHandlerResponse 通过单写协程发送 handler 响应；连接关闭/服务器停止时只记录不 panic
func (s *RPCServer) sendHandlerResponse(ctx context.Context, conn *transport.FramedConn, env protocol.Envelope) {
	if err := s.enqueueSend(ctx, conn, env); err != nil {
		if ctx.Err() != nil || s.isStopped() {
			return
		}
		zap.L().Warn("Runner RPC 响应发送失败",
			zap.String("method", env.Method),
			zap.String("plugin_id", env.PluginID),
			zap.Int64("request_id", env.RequestID),
			zap.Error(err))
	}
}
