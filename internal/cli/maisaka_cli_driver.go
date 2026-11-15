package cli

import (
	"context"
	"strings"

	"MaiGo/internal/core/localoperator"
	"MaiGo/internal/platform_io"
)

// MaisakaCLIDriverID 对齐 Python Maisaka CLI 本地出站驱动编号
const MaisakaCLIDriverID = "local.maisaka_cli"

// MaisakaCLIDriver 将 maisaka_cli 平台的出站消息渲染到终端
// 对齐 Python reply/emoji 工具对 CLI_PLATFORM_NAME 走 render_cli_message 的路径；
// Go 统一经 platform_io 驱动出站，故用本驱动承接，而非在 reply 工具内特判。
type MaisakaCLIDriver struct {
	platformio.BaseAdapter
	console  *Console
	nickname func() string
}

// NewMaisakaCLIDriver 创建 Maisaka CLI 出站驱动
func NewMaisakaCLIDriver(console *Console, nickname func() string) *MaisakaCLIDriver {
	if console == nil {
		console = DefaultConsole
	}
	return &MaisakaCLIDriver{
		BaseAdapter: platformio.NewBaseAdapter(platformio.DriverDescriptor{
			DriverID:  MaisakaCLIDriverID,
			Kind:      platformio.DriverKindLocal,
			Platform:  localoperator.MaisakaCLIPlatform,
			AccountID: localoperator.MaisakaCLIBotID,
		}),
		console:  console,
		nickname: nickname,
	}
}

// Start 本地驱动无需启动
func (d *MaisakaCLIDriver) Start(ctx context.Context) error { return nil }

// Stop 本地驱动无需停止
func (d *MaisakaCLIDriver) Stop(ctx context.Context) error { return nil }

// Send 渲染出站消息并返回已送达回执
func (d *MaisakaCLIDriver) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	content := strings.TrimSpace(outbound.PlainText)
	if content == "" {
		names := make([]string, 0, len(outbound.Segments))
		for _, segment := range outbound.Segments {
			names = append(names, string(segment.Kind))
		}
		content = "[" + strings.Join(names, ", ") + "]"
	}
	title := ""
	if d.nickname != nil {
		title = strings.TrimSpace(d.nickname())
	}
	RenderCLIMessageTo(d.console, content, title)
	return platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusSent,
		DriverID:          d.Descriptor().DriverID,
		DriverKind:        d.Descriptor().Kind,
	}, nil
}
