package desktoppet

// 本机主屏截图与屏幕时间线，对齐 desktop_pet/screen.py 与 remote_protocol.decode_screenshot。

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	screenThumbnailSide       = 1920
	screenChangeWidth         = 320
	screenChangeHeight        = 180
	screenTimelineMaxFrames   = 6
	screenTileWidth           = 960
	screenTileHeight          = 540
	screenTileLabelHeight     = 32
	screenRemoteMaxEncodedLen = 24 * 1024 * 1024
	screenRemoteMaxBytes      = 16 * 1024 * 1024
)

// DesktopScreenshot 一张已编码屏幕截图
type DesktopScreenshot struct {
	Data       []byte
	CapturedAt string
	Width      int
	Height     int
	Digest     string
}

// CaptureDesktop 捕获本机主屏；不写截图文件
func CaptureDesktop() (DesktopScreenshot, error) {
	return captureDesktop()
}

func newDesktopScreenshot(data []byte, capturedAt string, width, height int) DesktopScreenshot {
	sum := sha256.Sum256(data)
	return DesktopScreenshot{
		Data: data, CapturedAt: capturedAt, Width: width, Height: height,
		Digest: hex.EncodeToString(sum[:]),
	}
}

// ScreenChangeRatio 在缩略图上计算明显变化像素比例
func ScreenChangeRatio(previous, current DesktopScreenshot) float64 {
	if previous.Width != current.Width || previous.Height != current.Height {
		return 1
	}
	if previous.Digest == current.Digest {
		return 0
	}
	before, err := imaging.Decode(bytes.NewReader(previous.Data))
	if err != nil {
		return 1
	}
	after, err := imaging.Decode(bytes.NewReader(current.Data))
	if err != nil {
		return 1
	}
	before = imaging.Resize(before, screenChangeWidth, screenChangeHeight, imaging.Lanczos)
	after = imaging.Resize(after, screenChangeWidth, screenChangeHeight, imaging.Lanczos)
	bounds := before.Bounds()
	changed := 0
	total := bounds.Dx() * bounds.Dy()
	if total == 0 {
		return 1
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, _ := before.At(x, y).RGBA()
			r2, g2, b2, _ := after.At(x, y).RGBA()
			dr := absDiff(int(r1>>8), int(r2>>8))
			dg := absDiff(int(g1>>8), int(g2>>8))
			db := absDiff(int(b1>>8), int(b2>>8))
			luma := (299*dr + 587*dg + 114*db) / 1000
			if luma >= 20 {
				changed++
			}
		}
	}
	return float64(changed) / float64(total)
}

func absDiff(left, right int) int {
	if left > right {
		return left - right
	}
	return right - left
}

// ScreenTimeline 保留上一批末帧作起点和最新变化帧
type ScreenTimeline struct {
	Frames  []DesktopScreenshot
	Changed bool
}

// Add 记录变化帧；无足够变化时返回 false
func (t *ScreenTimeline) Add(shot DesktopScreenshot, threshold float64) bool {
	if len(t.Frames) > 0 && ScreenChangeRatio(t.Frames[len(t.Frames)-1], shot) < threshold {
		return false
	}
	t.Frames = append(t.Frames, shot)
	if len(t.Frames) > screenTimelineMaxFrames {
		t.Frames = append(t.Frames[:1], t.Frames[2:]...)
	}
	t.Changed = true
	return true
}

// Delivered 标记本批已投递，只保留末帧作为下一批起点
func (t *ScreenTimeline) Delivered() {
	if len(t.Frames) > 1 {
		t.Frames = t.Frames[len(t.Frames)-1:]
	}
	t.Changed = false
}

// ComposeScreenTimeline 从左到右、从上到下拼接变化帧并编号
func ComposeScreenTimeline(frames []DesktopScreenshot) (DesktopScreenshot, error) {
	if len(frames) < 1 || len(frames) > screenTimelineMaxFrames {
		return DesktopScreenshot{}, errors.New("屏幕时间线需要 1～6 帧")
	}
	columns := len(frames)
	if columns > 2 {
		columns = 2
	}
	rows := (len(frames) + columns - 1) / columns
	canvas := image.NewRGBA(image.Rect(0, 0, columns*screenTileWidth, rows*(screenTileHeight+screenTileLabelHeight)))
	fillScreenColor(canvas, colorRGB(0xff, 0xf8, 0xec))
	face, err := screenLabelFace()
	if err != nil {
		return DesktopScreenshot{}, err
	}
	defer face.Close()
	labelColor := image.NewUniform(colorRGB(0x1d, 0x1d, 0x1b))
	borderColor := colorRGB(0xdc, 0xc4, 0xa3)
	for index, frame := range frames {
		left := index % columns * screenTileWidth
		top := index / columns * (screenTileHeight + screenTileLabelHeight)
		drawer := &font.Drawer{
			Dst: canvas, Src: labelColor, Face: face,
			Dot: fixed.P(left+10, top+24),
		}
		drawer.DrawString(fmt.Sprintf("#%d  %s", index+1, frame.CapturedAt))
		source, decodeErr := imaging.Decode(bytes.NewReader(frame.Data))
		if decodeErr == nil {
			thumb := imaging.Fit(source, screenTileWidth-8, screenTileHeight-8, imaging.Lanczos)
			pasteAt := image.Pt(
				left+(screenTileWidth-thumb.Bounds().Dx())/2,
				top+screenTileLabelHeight+(screenTileHeight-thumb.Bounds().Dy())/2,
			)
			for y := 0; y < thumb.Bounds().Dy(); y++ {
				for x := 0; x < thumb.Bounds().Dx(); x++ {
					canvas.Set(pasteAt.X+x, pasteAt.Y+y, thumb.At(x, y))
				}
			}
		}
		drawScreenBorder(canvas, left, top, screenTileWidth, screenTileHeight+screenTileLabelHeight, borderColor)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		return DesktopScreenshot{}, err
	}
	return newDesktopScreenshot(buffer.Bytes(), frames[len(frames)-1].CapturedAt, canvas.Bounds().Dx(), canvas.Bounds().Dy()), nil
}

func screenLabelFace() (font.Face, error) {
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: 18, DPI: 72, Hinting: font.HintingFull})
}

func fillScreenColor(dst *image.RGBA, fill color.RGBA) {
	for y := dst.Bounds().Min.Y; y < dst.Bounds().Max.Y; y++ {
		for x := dst.Bounds().Min.X; x < dst.Bounds().Max.X; x++ {
			dst.SetRGBA(x, y, fill)
		}
	}
}

func drawScreenBorder(dst *image.RGBA, left, top, width, height int, border color.RGBA) {
	for x := left; x < left+width; x++ {
		dst.SetRGBA(x, top, border)
		dst.SetRGBA(x, top+height-1, border)
	}
	for y := top; y < top+height; y++ {
		dst.SetRGBA(left, y, border)
		dst.SetRGBA(left+width-1, y, border)
	}
}

func colorRGB(r, g, b uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: 255} }

// DecodeRemoteScreenshot 校验远端截图并重算摘要与尺寸
func DecodeRemoteScreenshot(payload map[string]any) (DesktopScreenshot, error) {
	encoded := asString(payload["base64"])
	if encoded == "" || len(encoded) > screenRemoteMaxEncodedLen {
		return DesktopScreenshot{}, errors.New("远端截图超过大小限制")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > screenRemoteMaxBytes {
		return DesktopScreenshot{}, errors.New("远端截图不能超过 16 MiB")
	}
	capturedAt := asString(payload["captured_at"])
	if capturedAt == "" || len(capturedAt) > 64 {
		return DesktopScreenshot{}, errors.New("远端截图时间无效")
	}
	if _, err := parseScreenshotTime(capturedAt); err != nil {
		return DesktopScreenshot{}, errors.New("远端截图时间必须包含时区")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !strings.EqualFold(format, "png") || max(config.Width, config.Height) > screenThumbnailSide {
		return DesktopScreenshot{}, errors.New("远端截图必须为最长边不超过 1920 像素的 PNG")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return DesktopScreenshot{}, errors.New("远端截图数据无效")
	}
	return newDesktopScreenshot(data, capturedAt, config.Width, config.Height), nil
}

func parseScreenshotTime(value string) (time.Time, error) {
	if !strings.Contains(value, "T") {
		return time.Time{}, errors.New("invalid")
	}
	if !strings.HasSuffix(value, "Z") && !strings.Contains(value[10:], "+") && !strings.Contains(value[10:], "-") {
		return time.Time{}, errors.New("missing timezone")
	}
	return time.Parse(time.RFC3339Nano, value)
}

// screenCapturedNow 返回带 UTC 偏移的 ISO 时间，对齐 Python datetime.isoformat
func screenCapturedNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.999999+00:00")
}
