package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"MaiGo/internal/platform_io"
)

// MaisakaCLIStartNoticeEnabled BufferCLI 挂载成功时的日志文案
const MaisakaCLIStartNoticeEnabled = "已启用 Maisaka CLI（BufferCLI），输入内容开始对话；Ctrl+C 退出 Bot"

// MaisakaCLIStartNoticeNotTTY 非交互终端时跳过 BufferCLI
const MaisakaCLIStartNoticeNotTTY = "maibot maisaka 需要交互式终端，当前 stdin 不是 TTY，已跳过 BufferCLI"

// MaisakaCLIStartDecision 启动判断结果
type MaisakaCLIStartDecision struct {
	Start  bool
	Notice string
}

// DecideMaisakaCLIStart 判断是否挂载 BufferCLI：仅 WantMaisakaCLI 且 stdin 为 TTY 时启动
func DecideMaisakaCLIStart(stdin *os.File) MaisakaCLIStartDecision {
	if !WantMaisakaCLI() {
		return MaisakaCLIStartDecision{}
	}
	if !IsInteractiveTerminal(stdin) {
		return MaisakaCLIStartDecision{Notice: MaisakaCLIStartNoticeNotTTY}
	}
	return MaisakaCLIStartDecision{Start: true, Notice: MaisakaCLIStartNoticeEnabled}
}

// MaisakaCLIRunOptions 挂载 BufferCLI 所需依赖
type MaisakaCLIRunOptions struct {
	// Platform 注册 maisaka_cli 出站驱动；nil 时只处理输入
	Platform *platformio.Manager
	// Receiver 正式消息处理链，通常为 chatwiring.Runtime.ChatBot
	Receiver BotConsoleMessageReceiver
	// Nickname 终端回复面板标题
	Nickname func() string
	// ModelName Banner 展示的 planner 模型名
	ModelName func() string
	// Console 与 Input 仅测试注入；零值使用默认终端与标准输入
	Console *Console
	Input   *InputReader
}

// RunMaisakaCLI 运行 BufferCLI：入站走 ChatBot.ReceiveMessage，出站经 maisaka_cli 本地驱动。
// 退出时只注销驱动，不停止聊天流：会话运行时由 Agent 持有并复用（与 BotConsole 一致）。
func RunMaisakaCLI(ctx context.Context, options MaisakaCLIRunOptions) error {
	if options.Receiver == nil {
		return fmt.Errorf("Maisaka CLI 消息接收链不能为空")
	}
	console := options.Console
	if console == nil {
		console = DefaultConsole
	}
	nickname := options.Nickname
	if nickname == nil {
		nickname = resolveCLIMessageNickname
	}
	if name := strings.TrimSpace(nickname()); name != "" {
		SetCLIMessageNickname(name)
	}
	driver := NewMaisakaCLIDriver(console, nickname)
	if err := registerLocalOutputDriver(ctx, options.Platform, driver); err != nil {
		return err
	}
	defer func() {
		if err := unregisterLocalOutputDriver(ctx, options.Platform, driver); err != nil {
			console.Errorf("注销 Maisaka CLI 驱动失败: %v\n", err)
		}
	}()

	buffer := NewBufferCLI(MaisakaCLIOptions{
		Console:   console,
		Input:     options.Input,
		ModelName: options.ModelName,
		// Sessions 留空：ChatBot 内 ChatManager 建流；本地只 EnsureSessionID。
		// Receiver 经 HeartFC 的 Store 口桥接 ChatBot，避免与 ChatBot 内 store/heartflow 双重执行。
		Receiver: newChatBotHeartFCBridge(options.Receiver),
	})
	return buffer.Run(ctx)
}

// newChatBotHeartFCBridge 把 ChatBot.ReceiveMessage 接到 BufferCLI 的 HeartFC 接收口。
// 只用 Store 端口投递，其余 HeartFC 依赖留空，完整链路由 ChatBot 完成。
func newChatBotHeartFCBridge(receiver BotConsoleMessageReceiver) *HeartFCMessageReceiver {
	return NewHeartFCMessageReceiver(HeartFCDependencies{
		Store: chatBotAsStore{receiver: receiver},
	})
}

type chatBotAsStore struct {
	receiver BotConsoleMessageReceiver
}

func (s chatBotAsStore) StoreMessage(ctx context.Context, message *SessionMessage) error {
	if s.receiver == nil {
		return fmt.Errorf("消息接收链不能为空")
	}
	converted, err := botConsoleReceiveMessage(message)
	if err != nil {
		return err
	}
	return s.receiver.ReceiveMessage(ctx, converted)
}

func registerLocalOutputDriver(ctx context.Context, platform *platformio.Manager, driver platformio.Adapter) error {
	if platform == nil || driver == nil {
		return nil
	}
	var err error
	if platform.IsStarted() {
		err = platform.AddDriver(ctx, driver)
	} else {
		err = platform.Register(driver)
	}
	if err != nil {
		return err
	}
	descriptor := driver.Descriptor()
	if err := platform.BindSendRoute(platformio.RouteBinding{
		RouteKey:   descriptor.RouteKey(),
		DriverID:   descriptor.DriverID,
		DriverKind: descriptor.Kind,
	}); err != nil {
		if platform.IsStarted() {
			_, _ = platform.RemoveDriver(ctx, descriptor.DriverID)
		} else {
			platform.Unregister(descriptor.DriverID)
		}
		return err
	}
	return nil
}

func unregisterLocalOutputDriver(ctx context.Context, platform *platformio.Manager, driver platformio.Adapter) error {
	if platform == nil || driver == nil {
		return nil
	}
	descriptor := driver.Descriptor()
	platform.UnbindSendRoute(descriptor.RouteKey(), descriptor.DriverID)
	if platform.IsStarted() {
		_, err := platform.RemoveDriver(ctx, descriptor.DriverID)
		return err
	}
	platform.Unregister(descriptor.DriverID)
	return nil
}
