package host

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
)

// 关停排空超时，对齐 protocol.envelope.ShutdownPayload
const (
	defaultShutdownDrainTimeoutMS int64 = 5000
	fastShutdownDrainTimeoutMS    int64 = 1500
)

// ShutdownOptions plugin.prepare_shutdown / plugin.shutdown 载荷
type ShutdownOptions struct {
	Reason         string `json:"reason"`
	DrainTimeoutMS int64  `json:"drain_timeout_ms"`
}

func (o ShutdownOptions) normalized() ShutdownOptions {
	if strings.TrimSpace(o.Reason) == "" {
		o.Reason = "normal"
	}
	if o.DrainTimeoutMS <= 0 {
		o.DrainTimeoutMS = defaultShutdownDrainTimeoutMS
	}
	return o
}

func (o ShutdownOptions) payload() map[string]any {
	o = o.normalized()
	return map[string]any{
		"reason":           o.Reason,
		"drain_timeout_ms": o.DrainTimeoutMS,
	}
}

// PrepareShutdown 请求 Runner 进入排空状态，对齐 supervisor._shutdown_runner 的第一步。
func (h *Host) PrepareShutdown(ctx context.Context, group RunnerGroup, opts ShutdownOptions) error {
	return h.sendShutdownRequest(ctx, group, protocol.MethodPluginPrepareShutdown, opts)
}

// ShutdownRunner 请求 Runner 立即退出，对齐 supervisor._shutdown_runner 的第二步。
func (h *Host) ShutdownRunner(ctx context.Context, group RunnerGroup, opts ShutdownOptions) error {
	return h.sendShutdownRequest(ctx, group, protocol.MethodPluginShutdown, opts)
}

func (h *Host) sendShutdownRequest(ctx context.Context, group RunnerGroup, method string, opts ShutdownOptions) error {
	rpc := h.rpcServerForGroup(group)
	if rpc == nil || !rpc.HasActiveConnection() {
		return fmt.Errorf("Runner 未连接")
	}
	opts = opts.normalized()
	if _, err := rpc.SendRequest(ctx, method, "", opts.payload(), opts.DrainTimeoutMS); err != nil {
		return fmt.Errorf("Runner %s 请求失败: %w", strings.TrimPrefix(method, "plugin."), err)
	}
	return nil
}

// shutdownRunnerRPC 在终止子进程前尽力通知 Runner 排空并退出，失败只记录日志。
func (h *Host) shutdownRunnerRPC(state *RunnerState, reason string, drainTimeoutMS int64) {
	if state == nil || state.rpc == nil || !state.rpc.HasActiveConnection() {
		return
	}
	opts := ShutdownOptions{Reason: reason, DrainTimeoutMS: drainTimeoutMS}.normalized()
	ctx := context.Background()
	for _, method := range []string{protocol.MethodPluginPrepareShutdown, protocol.MethodPluginShutdown} {
		if _, err := state.rpc.SendRequest(ctx, method, "", opts.payload(), opts.DrainTimeoutMS); err != nil {
			zap.L().Warn("Runner 关停请求失败",
				zap.String("group", string(state.Group)),
				zap.String("method", method),
				zap.String("reason", opts.Reason),
				zap.Error(err))
		}
	}
}
