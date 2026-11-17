//go:build !windows

package desktoppet

import "fmt"

func captureDesktop() (DesktopScreenshot, error) {
	return DesktopScreenshot{}, fmt.Errorf("本机桌面读取目前仅支持 Windows")
}
