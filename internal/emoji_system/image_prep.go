package emojisystem

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"path/filepath"
	"strings"

	"maibot-go/internal/common/database"
)

func stickerFilePath(stickerDir, fileName string) string {
	fileName = strings.TrimSpace(fileName)
	stickerDir = strings.TrimSpace(stickerDir)
	if fileName == "" || stickerDir == "" {
		return ""
	}
	return filepath.Join(stickerDir, filepath.Base(fileName))
}

// emojiFileName 从记录里取文件名，对齐 Python emoji.file_name（full_path 的 basename）
func emojiFileName(row memory.EmojiRecord) string {
	if name := strings.TrimSpace(row.FullPath); name != "" {
		return filepath.Base(filepath.FromSlash(name))
	}
	return ""
}

func mimeFromFileName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}

func isGIFBytes(data []byte, fileName string) bool {
	if strings.EqualFold(filepath.Ext(fileName), ".gif") {
		return true
	}
	return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
}

// prepareVLMPayload 对齐 MaiBot build_emoji_description：GIF 取首帧转 JPEG 再送 VLM
func prepareVLMPayload(raw []byte, fileName string) (b64 string, mime string, imageType string, err error) {
	if len(raw) == 0 {
		return "", "", "", fmt.Errorf("emojisystem: 表情图片为空")
	}
	imageType = "STATIC"
	payload := raw
	mime = mimeFromFileName(fileName)
	if isGIFBytes(raw, fileName) {
		imageType = "GIF"
		jpg, convErr := gifFirstFrameJPEG(raw)
		if convErr != nil {
			return "", "", "", fmt.Errorf("emojisystem: GIF 转静态图失败: %w", convErr)
		}
		payload = jpg
		mime = "image/jpeg"
	}
	b64 = base64.StdEncoding.EncodeToString(payload)
	return b64, mime, imageType, nil
}

// GIF 转静态图参数，对齐 Python utils_image.py:78-80
const (
	// gifFrameSimilarityThreshold 帧相似度阈值（MSE），越小要求差异越大
	gifFrameSimilarityThreshold = 1000.0
	// gifMaxFrames 最大抽取帧数
	gifMaxFrames = 15
	// gifTargetHeight 拼接后固定高度
	gifTargetHeight = 200
	// gifJPEGQuality 输出 JPEG 质量
	gifJPEGQuality = 85
)

// gifFirstFrameJPEG 把 GIF 多帧水平拼接为静态 JPEG
// 对齐 Python utils_image.py:78-150 gif_2_static_image：
// 逐帧与上一选中帧比较 MSE，超过阈值才选中，最多 gifMaxFrames 帧，等比缩放到固定高度后水平拼接
func gifFirstFrameJPEG(data []byte) ([]byte, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(g.Image) == 0 {
		return nil, fmt.Errorf("GIF 无帧")
	}
	selected := make([]*image.RGBA, 0, gifMaxFrames)
	var lastSelected []uint8
	for index, frame := range g.Image {
		rgba := imageToRGBA(frame)
		pixels := rgbaToGray(rgba)
		if index == 0 {
			selected = append(selected, rgba)
			lastSelected = pixels
			continue
		}
		if meanSquaredError(pixels, lastSelected) > gifFrameSimilarityThreshold {
			selected = append(selected, rgba)
			lastSelected = pixels
			if len(selected) >= gifMaxFrames {
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("未能抽取到任何有效帧")
	}
	frameWidth := selected[0].Bounds().Dx()
	frameHeight := selected[0].Bounds().Dy()
	if frameHeight == 0 {
		return nil, fmt.Errorf("帧高度为0，无法计算缩放尺寸")
	}
	targetWidth := int(float64(gifTargetHeight) / float64(frameHeight) * float64(frameWidth))
	if targetWidth == 0 {
		targetWidth = 1
	}
	combined := image.NewRGBA(image.Rect(0, 0, targetWidth*len(selected), gifTargetHeight))
	for index, frame := range selected {
		resized := resizeBilinear(frame, targetWidth, gifTargetHeight)
		draw.Draw(combined, image.Rect(index*targetWidth, 0, (index+1)*targetWidth, gifTargetHeight),
			resized, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, combined, &jpeg.Options{Quality: gifJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// imageToRGBA 把任意图像转为 RGBA
func imageToRGBA(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, bounds.Min, draw.Src)
	return rgba
}

// rgbaToGray 提取灰度像素，用于 MSE 比较
func rgbaToGray(rgba *image.RGBA) []uint8 {
	bounds := rgba.Bounds()
	gray := make([]uint8, 0, bounds.Dx()*bounds.Dy())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := rgba.At(x, y).RGBA()
			// 对齐 PIL 的 convert("RGB") 后按通道均值近似
			gray = append(gray, uint8((r+g+b)/3>>8))
		}
	}
	return gray
}

// meanSquaredError 计算两帧灰度像素的均方误差
func meanSquaredError(left, right []uint8) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var sum float64
	for index := range left {
		diff := float64(left[index]) - float64(right[index])
		sum += diff * diff
	}
	return sum / float64(len(left))
}

// resizeBilinear 双线性缩放，近似 PIL 的 LANCZOS
func resizeBilinear(src *image.RGBA, width, height int) *image.RGBA {
	if width <= 0 || height <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	srcBounds := src.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()
	if srcWidth == 0 || srcHeight == 0 {
		return dst
	}
	for y := 0; y < height; y++ {
		srcY := float64(y) * float64(srcHeight) / float64(height)
		for x := 0; x < width; x++ {
			srcX := float64(x) * float64(srcWidth) / float64(width)
			dst.Set(x, y, src.At(srcBounds.Min.X+int(srcX), srcBounds.Min.Y+int(srcY)))
		}
	}
	return dst
}
