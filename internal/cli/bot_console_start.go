package cli

import (
	"context"
	"fmt"
	"os"

	"MaiGo/internal/platform_io"
)

// BotConsoleStartNoticeEnabled 对齐 Python main.py 启用终端输入时的日志文案
const BotConsoleStartNoticeEnabled = "已启用终端输入，可输入普通消息或 /clear、/pm 等指令；输入 exit() 关闭终端输入"

// BotConsoleStartNoticeNotTTY 对齐 Python main.py 非交互终端时的跳过日志
const BotConsoleStartNoticeNotTTY = "终端输入已配置为启用，但当前 stdin 不是交互式终端，已跳过"

// BotConsoleStartDecision 启动判断结果
type BotConsoleStartDecision struct {
	Start  bool
	Notice string
}

// DecideBotConsoleStart 按 Python main.py:276-283 判断是否启动终端输入
// 只做配置与 TTY 判定，不创建 goroutine，便于 main 打印日志后再启动
func DecideBotConsoleStart(enabled bool, stdin *os.File) BotConsoleStartDecision {
	if !enabled {
		return BotConsoleStartDecision{}
	}
	if !IsInteractiveTerminal(stdin) {
		return BotConsoleStartDecision{Notice: BotConsoleStartNoticeNotTTY}
	}
	return BotConsoleStartDecision{Start: true, Notice: BotConsoleStartNoticeEnabled}
}

// BotConsoleRunOptions 启动终端输入所需依赖
type BotConsoleRunOptions struct {
	// Platform 用于注册 bot_console 输出驱动；nil 时只处理输入
	Platform *platformio.Manager
	// Receiver 正式消息处理链，通常为 chatwiring.Runtime.ChatBot
	Receiver BotConsoleMessageReceiver
	// Nickname 终端回复面板标题
	Nickname func() string
	// Console 与 Input 仅测试注入；零值使用默认终端与标准输入
	Console *Console
	Input   *InputReader
}

// RunBotConsole 运行终端输入直到 EOF、exit() 或 ctx 取消
// 退出时只注销终端驱动，不停止聊天流：Go 的会话运行时由 Agent 持有并复用
func RunBotConsole(ctx context.Context, options BotConsoleRunOptions) error {
	if options.Receiver == nil {
		return fmt.Errorf("终端消息接收链不能为空")
	}
	console := NewBotConsole(BotConsoleOptions{
		Console:  options.Console,
		Input:    options.Input,
		Platform: options.Platform,
		Nickname: options.Nickname,
		Dispatch: NewBotConsoleDispatch(options.Receiver),
	})
	return console.Run(ctx)
}
