//go:build windows

package desktoppet

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"syscall"
	"unsafe"

	"github.com/disintegration/imaging"
)

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	gdi32                      = syscall.NewLazyDLL("gdi32.dll")
	procSetProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
)

const (
	smCXScreen   = 0
	smCYScreen   = 1
	srcCopy      = 0x00CC0020
	dibRGBColors = 0
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

func init() {
	_, _, _ = procSetProcessDPIAware.Call()
}

func captureDesktop() (DesktopScreenshot, error) {
	width, _, _ := procGetSystemMetrics.Call(smCXScreen)
	height, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if width == 0 || height == 0 {
		return DesktopScreenshot{}, fmt.Errorf("无法读取主屏尺寸")
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return DesktopScreenshot{}, fmt.Errorf("无法获取屏幕设备上下文")
	}
	defer procReleaseDC.Call(0, screenDC)
	memoryDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return DesktopScreenshot{}, fmt.Errorf("无法创建兼容设备上下文")
	}
	defer procDeleteDC.Call(memoryDC)
	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, width, height)
	if bitmap == 0 {
		return DesktopScreenshot{}, fmt.Errorf("无法创建屏幕位图")
	}
	defer procDeleteObject.Call(bitmap)
	if previous, _, _ := procSelectObject.Call(memoryDC, bitmap); previous != 0 {
		defer procSelectObject.Call(memoryDC, previous)
	}
	if result, _, _ := procBitBlt.Call(memoryDC, 0, 0, width, height, screenDC, 0, 0, srcCopy); result == 0 {
		return DesktopScreenshot{}, fmt.Errorf("屏幕位图复制失败")
	}
	info := bitmapInfo{}
	info.Header.Size = uint32(unsafe.Sizeof(info.Header))
	info.Header.Width = int32(width)
	info.Header.Height = -int32(height)
	info.Header.Planes = 1
	info.Header.BitCount = 32
	info.Header.Compression = 0
	pixels := make([]byte, int(width)*int(height)*4)
	if result, _, _ := procGetDIBits.Call(memoryDC, bitmap, 0, height, uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&info)), dibRGBColors); result == 0 {
		return DesktopScreenshot{}, fmt.Errorf("读取屏幕像素失败")
	}
	source := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for index := 0; index < len(pixels); index += 4 {
		offset := index / 4
		x := offset % int(width)
		y := offset / int(width)
		source.SetNRGBA(x, y, color.NRGBA{R: pixels[index+2], G: pixels[index+1], B: pixels[index], A: 255})
	}
	final := source
	if max(source.Bounds().Dx(), source.Bounds().Dy()) > screenThumbnailSide {
		widthScale := float64(screenThumbnailSide) / float64(source.Bounds().Dx())
		heightScale := float64(screenThumbnailSide) / float64(source.Bounds().Dy())
		scale := widthScale
		if heightScale < scale {
			scale = heightScale
		}
		final = imaging.Resize(source, max(1, int(float64(source.Bounds().Dx())*scale)),
			max(1, int(float64(source.Bounds().Dy())*scale)), imaging.Lanczos)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, final); err != nil {
		return DesktopScreenshot{}, err
	}
	return newDesktopScreenshot(buffer.Bytes(), screenCapturedNow(), final.Bounds().Dx(), final.Bounds().Dy()), nil
}
