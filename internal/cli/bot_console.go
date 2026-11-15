package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"MaiGo/internal/platform_io"
)

// BotConsoleCommandCompletions 对齐 Python BotConsole._COMMAND_COMPLETIONS
var BotConsoleCommandCompletions = []string{
	"/help",
	"/offline",
	"/online",
	"/pm",
	"/pm alias",
	"/pm alias add",
	"/pm alias help",
	"/pm alias list",
	"/pm alias remove",
	"/pm component",
	"/pm component disable global",
	"/pm component disable local",
	"/pm component enable global",
	"/pm component enable local",
	"/pm component help",
	"/pm component list",
	"/pm component list disabled",
	"/pm component list enabled",
	"/pm component list type",
	"/pm config",
	"/pm config get",
	"/pm config help",
	"/pm config set",
	"/pm help",
	"/pm plugin",
	"/pm plugin help",
	"/pm plugin list",
	"/pm plugin list_enabled",
	"/pm plugin load",
	"/pm plugin reload",
	"/pm plugin unload",
	"exit()",
}

// ConsoleInput 终端输入读取器；InputReader 与 ReadlineInputReader 都实现了它
type ConsoleInput interface {
	Start()
	Next(ctx context.Context) (string, error)
	Close()
}

// SelfPromptingInput 自带提示符的读取器，调用方不再单独打印提示符
type SelfPromptingInput interface {
	OwnsPrompt() bool
}

// BotConsoleOptions BotConsole 依赖
type BotConsoleOptions struct {
	Console  *Console
	Input    ConsoleInput
	Driver   *BotConsoleDriver
	Platform *platformio.Manager
	Nickname func() string
	// Dispatch 将输入交给正式消息处理链，返回 session_id
	Dispatch func(ctx context.Context, message *SessionMessage) (string, error)
	// StopRuntime 退出时停止已创建的聊天流
	StopRuntime func(ctx context.Context, sessionID string) error
	// SessionOptions 返回 /clear 可补全的聊天名到 session_id 映射
	SessionOptions func() map[string]string
}

// BotConsole 读取本地终端输入并交给正式消息处理链
type BotConsole struct {
	console        *Console
	input          ConsoleInput
	driver         *BotConsoleDriver
	platform       *platformio.Manager
	dispatch       func(ctx context.Context, message *SessionMessage) (string, error)
	stopRuntime    func(ctx context.Context, sessionID string) error
	sessionOptions func() map[string]string
	sessionID      string
	ownsInput      bool
}

// NewBotConsole 创建本地管理终端
func NewBotConsole(options BotConsoleOptions) *BotConsole {
	console := options.Console
	if console == nil {
		console = DefaultConsole
	}
	nickname := options.Nickname
	if nickname == nil {
		nickname = func() string { return "MaiBot" }
	}
	driver := options.Driver
	if driver == nil {
		driver = NewBotConsoleDriver(console, nickname)
	}
	return &BotConsole{
		console:        console,
		input:          options.Input,
		driver:         driver,
		platform:       options.Platform,
		dispatch:       options.Dispatch,
		stopRuntime:    options.StopRuntime,
		sessionOptions: options.SessionOptions,
	}
}

// Run 运行管理终端，直到 stdin 关闭、收到 exit() 或上下文取消
func (b *BotConsole) Run(ctx context.Context) error {
	if b.input == nil {
		if err := EnsureInteractiveStdin(); err != nil {
			return err
		}
		reader, err := NewReadlineInputReader(b.CommandCompletions, b.console.Out, b.console.Err)
		if err != nil {
			b.input = NewStdinInputReader()
		} else {
			b.input = reader
		}
		b.ownsInput = true
	}
	b.input.Start()
	if b.ownsInput {
		defer b.input.Close()
	}

	if err := b.registerOutputDriver(ctx); err != nil {
		return err
	}
	defer b.stopSessionRuntime(ctx)
	defer func() {
		if err := b.unregisterOutputDriver(ctx); err != nil {
			b.console.Errorf("注销终端驱动失败: %v\n", err)
		}
	}()

	b.showBanner()
	for {
		if own, ok := b.input.(SelfPromptingInput); !ok || !own.OwnsPrompt() {
			b.console.Prompt(StyleBoldCyan, "> ")
		}
		line, err := b.input.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				b.console.Println()
				b.console.PrintStyled(StyleMuted, "终端输入已关闭")
				return nil
			}
			if errors.Is(err, context.Canceled) {
				return ctx.Err()
			}
			return err
		}
		stop, err := b.HandleLine(ctx, line)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
}

// HandleLine 处理一行终端输入，stop 为 true 表示终端应退出
func (b *BotConsole) HandleLine(ctx context.Context, line string) (bool, error) {
	text := strings.TrimSpace(line)
	if text == "" {
		return false, nil
	}
	if strings.ToLower(text) == "exit()" {
		b.console.PrintStyled(StyleMuted, "终端输入已关闭，Bot 将继续运行")
		return true, nil
	}
	if text == "/help" {
		b.showHelp()
		return false, nil
	}
	if b.dispatch == nil {
		return false, fmt.Errorf("消息处理链未接入，无法投递终端消息")
	}
	message := BuildBotConsoleMessage(text, nowFunc())
	sessionID, err := b.dispatch(ctx, message)
	if err != nil {
		return false, err
	}
	b.sessionID = sessionID
	return false, nil
}

// registerOutputDriver 注册终端驱动并绑定发送路由
func (b *BotConsole) registerOutputDriver(ctx context.Context) error {
	if b.platform == nil {
		return nil
	}
	var err error
	if b.platform.IsStarted() {
		err = b.platform.AddDriver(ctx, b.driver)
	} else {
		err = b.platform.Register(b.driver)
	}
	if err != nil {
		return err
	}
	descriptor := b.driver.Descriptor()
	if err := b.platform.BindSendRoute(platformio.RouteBinding{
		RouteKey:   descriptor.RouteKey(),
		DriverID:   descriptor.DriverID,
		DriverKind: descriptor.Kind,
	}); err != nil {
		if b.platform.IsStarted() {
			_, _ = b.platform.RemoveDriver(ctx, descriptor.DriverID)
		} else {
			b.platform.Unregister(descriptor.DriverID)
		}
		return err
	}
	return nil
}

// unregisterOutputDriver 注销发送路由与驱动
func (b *BotConsole) unregisterOutputDriver(ctx context.Context) error {
	if b.platform == nil {
		return nil
	}
	descriptor := b.driver.Descriptor()
	b.platform.UnbindSendRoute(descriptor.RouteKey(), descriptor.DriverID)
	if b.platform.IsStarted() {
		_, err := b.platform.RemoveDriver(ctx, descriptor.DriverID)
		return err
	}
	b.platform.Unregister(descriptor.DriverID)
	return nil
}

// showBanner 输出管理终端横幅
func (b *BotConsole) showBanner() {
	banner := b.console.decorate(StyleBoldCyan, "MaiBot 管理终端") + "\n" +
		b.console.decorate(StyleMuted, "输入消息或管理指令，使用 /help 查看帮助") + "\n" +
		b.console.decorate(StyleMuted, "Tab 补全 | ↑↓ 历史 | exit() 关闭终端输入 | Ctrl+C 退出 Bot")
	b.console.Panel(banner, PanelOptions{
		BorderStyle:       StyleCyan,
		PaddingTop:        1,
		PaddingHorizontal: 2,
		Box:               DoubleEdgeBox,
	})
	b.console.Println()
}

// showHelp 输出管理指令帮助
func (b *BotConsole) showHelp() {
	tick := string(rune(0x60))
	helpText := "### 管理指令\n" +
		"\n" +
		"- " + tick + "/help" + tick + "：显示本帮助\n" +
		"- " + tick + "/clear <聊天名>" + tick + "：清空指定真实聊天的 Maisaka 历史；输入 " + tick + "/clear " + tick + " 后按 Tab 选择\n" +
		"- " + tick + "/offline" + tick + "：关闭当前所有适配器插件\n" +
		"- " + tick + "/online" + tick + "：恢复本次运行中由 " + tick + "/offline" + tick + " 关闭的适配器插件\n" +
		"- " + tick + "/pm help" + tick + "：查看插件与组件管理指令\n" +
		"- " + tick + "exit()" + tick + "：关闭终端输入，Bot 继续运行\n" +
		"\n" +
		"### 输入操作\n" +
		"\n" +
		"- " + tick + "Tab" + tick + "：补全指令或聊天名\n" +
		"- " + tick + "↑" + tick + " / " + tick + "↓" + tick + "：浏览本次运行的输入历史\n" +
		"- " + tick + "Ctrl+C" + tick + "：退出 Bot\n" +
		"- 其他文本：作为本地操作员消息进入正式聊天处理链\n"
	b.console.Panel(b.console.RenderMarkdown(helpText), PanelOptions{
		Title:             "MaiBot 管理终端帮助",
		BorderStyle:       StyleCyan,
		PaddingTop:        1,
		PaddingHorizontal: 2,
		Box:               RoundedBox,
	})
}

// stopSessionRuntime 退出时停止已创建的聊天流
func (b *BotConsole) stopSessionRuntime(ctx context.Context) {
	if b.stopRuntime == nil || strings.TrimSpace(b.sessionID) == "" {
		return
	}
	if err := b.stopRuntime(ctx, b.sessionID); err != nil {
		b.console.Errorf("停止聊天流失败: %v\n", err)
	}
}

// CommandCompletions 返回命令与聊天名补全候选
func (b *BotConsole) CommandCompletions() []string {
	completions := append([]string(nil), BotConsoleCommandCompletions...)
	if b.sessionOptions != nil {
		names := make([]string, 0, len(b.sessionOptions()))
		for name := range b.sessionOptions() {
			names = append(names, name)
		}
		sortStrings(names)
		for _, name := range names {
			completions = append(completions, "/clear "+name)
		}
	}
	return completions
}

// sortStrings 简单插入排序，避免为 CLI 补全引入额外依赖
func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for current := index; current > 0 && values[current] < values[current-1]; current-- {
			values[current], values[current-1] = values[current-1], values[current]
		}
	}
}

// nowFunc 便于测试替换时间
var nowFunc = func() time.Time { return time.Now() }
