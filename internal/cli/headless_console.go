package cli

// HeadlessConsole 供宿主经 IPC 提交终端输入；不读取 stdin，也不占用交互终端。
// 对齐 src/common/http_service/console.py 的按需 BotConsole。

import (
	"context"
	"errors"
	"sync"

	"MaiGo/internal/platform_io"
)

// HeadlessConsole 懒创建并复用 BotConsole 输出驱动
type HeadlessConsole struct {
	mu       sync.Mutex
	console  *BotConsole
	ctx      context.Context
	started  bool
}

// NewHeadlessConsole 创建不读取 stdin 的终端入口
func NewHeadlessConsole(ctx context.Context, platform *platformio.Manager, receiver BotConsoleMessageReceiver, nickname func() string) *HeadlessConsole {
	if ctx == nil {
		ctx = context.Background()
	}
	return &HeadlessConsole{
		ctx: ctx,
		console: NewBotConsole(BotConsoleOptions{
			Platform: platform,
			Nickname: nickname,
			Dispatch: NewBotConsoleDispatch(receiver),
		}),
	}
}

func (h *HeadlessConsole) ensureStarted() error {
	if h.started {
		return nil
	}
	if err := h.console.registerOutputDriver(h.ctx); err != nil {
		return err
	}
	h.started = true
	return nil
}

// Submit 提交一行终端输入；退出指令只关闭该次输入，不影响 Bot
func (h *HeadlessConsole) Submit(ctx context.Context, text string) error {
	if h == nil || h.console == nil {
		return errors.New("终端输入未启用")
	}
	if ctx == nil {
		ctx = h.ctx
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensureStarted(); err != nil {
		return err
	}
	_, err := h.console.HandleLine(ctx, text)
	return err
}

// Completions 返回可用指令补全
func (h *HeadlessConsole) Completions() []string {
	return append([]string(nil), BotConsoleCommandCompletions...)
}

// Close 注销输出驱动
func (h *HeadlessConsole) Close(ctx context.Context) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started {
		return
	}
	_ = h.console.unregisterOutputDriver(ctx)
	h.started = false
}
