//go:build windows

package transport

import (
	"fmt"
	"net"
)

// ListenUDS Windows 不支持 Unix domain socket（请使用 Named Pipe 或 TCP）
func ListenUDS(_ string) (net.Listener, string, error) {
	return nil, "", fmt.Errorf("UDS 不支持 Windows 平台，请使用 Named Pipe（MAIBOT_PLUGIN_IPC=pipe）或 TCP")
}
