package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaisakaSessionManager 对齐 Python chat_manager 的注册与会话创建
type MaisakaSessionManager interface {
	RegisterMessage(ctx context.Context, message *SessionMessage) error
	GetOrCreateSession(ctx context.Context, message *SessionMessage) (string, error)
}

// MaisakaCLIOptions Maisaka CLI 依赖
type MaisakaCLIOptions struct {
	Console     *Console
	Input       *InputReader
	Receiver    *HeartFCMessageReceiver
	Sessions    MaisakaSessionManager
	StopRuntime func(ctx context.Context, sessionID string) error
	ModelName   func() string
}

// BufferCLI 对齐 MaiBot BufferCLI 的终端交互环
type BufferCLI struct {
	console     *Console
	input       *InputReader
	receiver    *HeartFCMessageReceiver
	sessions    MaisakaSessionManager
	stopRuntime func(ctx context.Context, sessionID string) error
	modelName   func() string
	sessionID   string
	ownsInput   bool
}

// NewBufferCLI 创建 Maisaka 命令交互入口
func NewBufferCLI(options MaisakaCLIOptions) *BufferCLI {
	console := options.Console
	if console == nil {
		console = DefaultConsole
	}
	receiver := options.Receiver
	if receiver == nil {
		receiver = NewHeartFCMessageReceiver(HeartFCDependencies{})
	}
	modelName := options.ModelName
	if modelName == nil {
		modelName = func() string { return "未配置" }
	}
	return &BufferCLI{
		console:     console,
		input:       options.Input,
		receiver:    receiver,
		sessions:    options.Sessions,
		stopRuntime: options.StopRuntime,
		modelName:   modelName,
	}
}

// Run 运行交互循环，直到 stdin 关闭或上下文取消
func (c *BufferCLI) Run(ctx context.Context) error {
	if c.input == nil {
		c.input = NewStdinInputReader()
		c.ownsInput = true
	}
	c.input.Start()
	if c.ownsInput {
		defer c.input.Close()
	}
	c.ShowBanner()
	defer c.stopSessionRuntime(ctx)

	for {
		c.console.Prompt(StyleBoldCyan, "> ")
		line, err := c.input.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.console.Println()
				c.console.PrintStyled(StyleMuted, "再见")
				return nil
			}
			return err
		}
		userText := strings.TrimSpace(line)
		if userText == "" {
			continue
		}
		if err := c.DispatchInput(ctx, userText); err != nil {
			return err
		}
	}
}

// DispatchInput 构造消息并送入心流接收链
func (c *BufferCLI) DispatchInput(ctx context.Context, userText string) error {
	message := BuildMaisakaCLISessionMessage(userText, nowFunc())
	if c.sessions != nil {
		if err := c.sessions.RegisterMessage(ctx, message); err != nil {
			return fmt.Errorf("注册消息失败: %w", err)
		}
		sessionID, err := c.sessions.GetOrCreateSession(ctx, message)
		if err != nil {
			return fmt.Errorf("创建聊天流失败: %w", err)
		}
		c.sessionID = sessionID
		message.SessionID = sessionID
	} else if err := message.EnsureSessionID(); err != nil {
		return err
	} else {
		c.sessionID = message.SessionID
	}
	if c.receiver != nil {
		c.receiver.ProcessMessageSafe(ctx, message)
	}
	return nil
}

// ShowBanner 输出版本、模型与操作提示
func (c *BufferCLI) ShowBanner() {
	modelName := strings.TrimSpace(c.modelName())
	if modelName == "" {
		modelName = "未配置"
	}
	banner := c.console.decorate(StyleBoldCyan, "MaiSaka") + c.console.decorate(StyleMuted, " v2.0") + "\n" +
		c.console.decorate(StyleMuted, "模型: "+modelName) + "\n" +
		c.console.decorate(StyleMuted, "输入内容开始对话 | Ctrl+C 退出")
	c.console.Panel(banner, PanelOptions{
		BorderStyle:       StyleCyan,
		PaddingTop:        1,
		PaddingHorizontal: 2,
		Box:               DoubleEdgeBox,
	})
	c.console.Println()
}

// SessionID 返回最近一次对话的聊天流编号
func (c *BufferCLI) SessionID() string {
	return c.sessionID
}

// stopSessionRuntime 对齐 Python finally 中停止 heartflow runtime
func (c *BufferCLI) stopSessionRuntime(ctx context.Context) {
	if c.stopRuntime == nil || strings.TrimSpace(c.sessionID) == "" {
		return
	}
	if err := c.stopRuntime(ctx, c.sessionID); err != nil {
		c.console.Errorf("停止聊天流失败: %v\n", err)
	}
}
