package cli

import (
	"context"
	"strings"

	"MaiGo/internal/core/localoperator"
	"MaiGo/internal/platform_io"
)

// BotConsoleDriverID 对齐 Python BotConsoleDriver.DRIVER_ID
const BotConsoleDriverID = "local.bot_console"

// BotConsoleDriver 将 bot_console 平台的出站消息渲染到终端
type BotConsoleDriver struct {
	platformio.BaseAdapter
	console  *Console
	nickname func() string
}

// NewBotConsoleDriver 创建终端驱动
func NewBotConsoleDriver(console *Console, nickname func() string) *BotConsoleDriver {
	if console == nil {
		console = DefaultConsole
	}
	return &BotConsoleDriver{
		BaseAdapter: platformio.NewBaseAdapter(platformio.DriverDescriptor{
			DriverID:  BotConsoleDriverID,
			Kind:      platformio.DriverKindLocal,
			Platform:  localoperator.BotConsolePlatform,
			AccountID: localoperator.BotConsoleBotID,
		}),
		console:  console,
		nickname: nickname,
	}
}

// Start 本地驱动无需启动
func (d *BotConsoleDriver) Start(ctx context.Context) error { return nil }

// Stop 本地驱动无需停止
func (d *BotConsoleDriver) Stop(ctx context.Context) error { return nil }

// Send 渲染出站消息并返回已送达回执
func (d *BotConsoleDriver) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	content := strings.TrimSpace(outbound.PlainText)
	if content == "" {
		names := make([]string, 0, len(outbound.Segments))
		for _, segment := range outbound.Segments {
			names = append(names, string(segment.Kind))
		}
		content = "[" + strings.Join(names, ", ") + "]"
	}
	title := "MaiBot"
	if d.nickname != nil {
		if nickname := strings.TrimSpace(d.nickname()); nickname != "" {
			title = nickname
		}
	}
	d.console.Panel(d.console.RenderMarkdown(content), PanelOptions{
		Title:             title,
		BorderStyle:       StyleMagenta,
		PaddingTop:        1,
		PaddingHorizontal: 2,
	})
	return platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusSent,
		DriverID:          d.Descriptor().DriverID,
		DriverKind:        d.Descriptor().Kind,
	}, nil
}
