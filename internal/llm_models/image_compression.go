package llm

// 请求体过大时的图片压缩与 data URI 上限重试
// 对齐 MaiBot src/llm_models/utils.py 的 compress_messages 与
// src/llm_models/utils_model.py 的 413 / data URI 单项超限兜底：
//   - 静态 JPEG/PNG/WEBP 先转 JPEG quality=95
//   - 仍超过目标大小时按 base64 长度比例缩放
//   - data URI 错误中带单项上限时，目标大小 = max(512KB, limit-128KB)
//   - 普通 413 使用 1MB 默认目标
// 压缩本身不消耗 Provider 的 max_retry 次数：Chat Completions 客户端内置重发，
// Gemini/插件等其它客户端由失败切换层压缩后重发一次。

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"regexp"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
)

const (
	defaultCompressedImageTargetSize = 1 << 20
	minCompressedImageTargetSize     = 512 << 10
	dataURIRetryMarginBytes          = 128 << 10
)

var dataURILimitPattern = regexp.MustCompile("(?i)Exceeded limit on max bytes per data-uri item\\s*:\\s*(\\d+)")

// compressContextImages 压缩消息 Item 中的全部图片；Assistant 图片被修改后清除 replay。
func compressContextImages(items []ContextItem, targetSize int) []ContextItem {
	if targetSize <= 0 {
		targetSize = defaultCompressedImageTargetSize
	}
	compressed := make([]ContextItem, 0, len(items))
	for _, item := range items {
		if item.Type != ContextItemSystemMessage && item.Type != ContextItemUserMessage && item.Type != ContextItemAssistantMessage {
			compressed = append(compressed, item)
			continue
		}
		hasImage := false
		parts := make([]ContextContentPart, len(item.Parts))
		copy(parts, item.Parts)
		for index, part := range item.Parts {
			if part.Type != ContextPartImage {
				continue
			}
			hasImage = true
			parts[index].ImageBase64 = compressBase64Image(part.ImageBase64, targetSize)
		}
		if !hasImage {
			compressed = append(compressed, item)
			continue
		}
		copied := item
		copied.Parts = parts
		if item.Type == ContextItemAssistantMessage {
			copied.Replay = nil
		}
		compressed = append(compressed, copied)
	}
	return compressed
}

// compressBase64Image 对单张 base64 图片执行格式转换与缩放；失败时保留原值。
func compressBase64Image(encoded string, targetSize int) string {
	if targetSize <= 0 {
		targetSize = defaultCompressedImageTargetSize
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}
	reformatted := reformatStaticImage(raw)
	encoded = base64.StdEncoding.EncodeToString(reformatted)
	if len(encoded) <= targetSize {
		return encoded
	}
	scale := float64(targetSize) / float64(len(encoded))
	rescaled, ok := rescaleImage(reformatted, scale)
	if !ok {
		return encoded
	}
	return base64.StdEncoding.EncodeToString(rescaled)
}

// reformatStaticImage 将非动图 JPEG/PNG/WEBP 转成 JPEG quality=95；失败或动图原样返回。
func reformatStaticImage(raw []byte) []byte {
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	if isAnimatedGIF(raw) {
		return raw
	}
	switch strings.ToLower(format) {
	case "jpeg", "jpg", "png", "webp":
	default:
		return raw
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, imageForJPEG(decoded), &jpeg.Options{Quality: 95}); err != nil {
		return raw
	}
	return buffer.Bytes()
}

// rescaleImage 按比例缩放图片；静态图输出 JPEG，动图保留 GIF 动画。
func rescaleImage(raw []byte, scale float64) ([]byte, bool) {
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	if format == "gif" && isAnimatedGIF(raw) {
		return rescaleAnimatedGIF(raw, scale)
	}
	bounds := decoded.Bounds()
	width := max(1, int(float64(bounds.Dx())*scale))
	height := max(1, int(float64(bounds.Dy())*scale))
	resized := imaging.Resize(decoded, width, height, imaging.Lanczos)
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, imageForJPEG(resized), &jpeg.Options{Quality: 95}); err != nil {
		return nil, false
	}
	return buffer.Bytes(), true
}

func rescaleAnimatedGIF(raw []byte, scale float64) ([]byte, bool) {
	animation, err := gif.DecodeAll(bytes.NewReader(raw))
	if err != nil || len(animation.Image) == 0 {
		return nil, false
	}
	width := max(1, int(float64(animation.Config.Width)*scale))
	height := max(1, int(float64(animation.Config.Height)*scale))
	// 与 Python 一致：动图在通用缩放比例上再打五折，避免帧数与体积失控
	width = max(1, width/2)
	height = max(1, height/2)
	frames := make([]*image.Paletted, 0, len(animation.Image))
	for _, frame := range animation.Image {
		resized := imaging.Resize(frame, width, height, imaging.Lanczos)
		paletted := image.NewPaletted(image.Rect(0, 0, width, height), palette.Plan9)
		draw.FloydSteinberg.Draw(paletted, paletted.Bounds(), resized, resized.Bounds().Min)
		frames = append(frames, paletted)
	}
	delay := 10
	if len(animation.Delay) > 0 && animation.Delay[0] > 0 {
		delay = animation.Delay[0]
	}
	delays := make([]int, len(frames))
	for index := range delays {
		delays[index] = delay
	}
	loopCount := animation.LoopCount
	if loopCount < 0 {
		loopCount = 0
	}
	var buffer bytes.Buffer
	if err := gif.EncodeAll(&buffer, &gif.GIF{Image: frames, Delay: delays, LoopCount: loopCount}); err != nil {
		return nil, false
	}
	return buffer.Bytes(), true
}

func isAnimatedGIF(raw []byte) bool {
	animation, err := gif.DecodeAll(bytes.NewReader(raw))
	return err == nil && len(animation.Image) > 1
}

// imageCompressionTargetForError 判断 HTTP 错误是否应按图片过大处理，并返回压缩目标。
func imageCompressionTargetForError(err error) (int, bool) {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	// 对齐 Python：429/5xx 先按普通可重试 HTTP 错误处理，不进入图片压缩分支
	if statusErr.StatusCode == 429 || statusErr.StatusCode >= 500 {
		return 0, false
	}
	if limit, ok := extractDataURILimitBytes(statusErr); ok {
		return buildDataURIRetryTargetSize(limit), true
	}
	if statusErr.StatusCode == 413 {
		return defaultCompressedImageTargetSize, true
	}
	return 0, false
}

func extractDataURILimitBytes(statusErr *HTTPStatusError) (int, bool) {
	if statusErr == nil {
		return 0, false
	}
	candidates := []string{statusErr.Message, statusErr.Error()}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		match := dataURILimitPattern.FindStringSubmatch(candidate)
		if len(match) != 2 {
			continue
		}
		limit, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, false
		}
		return limit, true
	}
	return 0, false
}

func buildDataURIRetryTargetSize(limitBytes int) int {
	target := limitBytes - dataURIRetryMarginBytes
	if target < minCompressedImageTargetSize {
		target = minCompressedImageTargetSize
	}
	return target
}
