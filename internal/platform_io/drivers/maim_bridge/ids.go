package maimbridge

import "maibot-go/internal/platform_io"

const (
	// DriverIDMaimMsg 旧版 maim_message WS 多平台汇聚驱动
	DriverIDMaimMsg = "legacy.maimmsg"
)

// DriverDescriptor 返回 Platform IO 驱动描述（单驱动按 platform 动态绑路由）
func DriverDescriptor() platformio.DriverDescriptor {
	return platformio.DriverDescriptor{
		DriverID: DriverIDMaimMsg,
		Kind:     platformio.DriverKindLegacy,
		Platform: "",
		Metadata: map[string]any{
			"source":             "maim_message_ws",
			"allow_any_platform": true,
		},
	}
}
