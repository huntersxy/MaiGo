package llm

// 模型请求图片尺寸治理
// 对齐 MaiBot src/llm_models/image_normalizer.py：
//   - 单边不超过 8000 的图片原样保留
//   - 长宽比不超过 3 的超限图片等比缩小到长边 8000
//   - 长图按短边*3 与总段数预算计算段长，24px 重叠，最多 16 段
//   - 超限动图取首帧；PNG 等保留透明通道，JPEG 统一转 RGB 后 quality=95
//
// 归一化发生在 ContextItem 投影之后、Provider 请求构造之前，因此 chat/gemini/plugin
// 与失败快照看到的是同一份图片内容。

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	modelImageMaxSide     = 8000
	modelImageSplitRatio  = 3
	modelImageOverlap     = 24
	modelImageMaxSegments = 16
)

// normalizeContextImages 规范化请求中的全部图片，保留消息顺序、元信息与未修改的历史对象。
func normalizeContextImages(items []ContextItem) []ContextItem {
	normalized := make([]ContextItem, 0, len(items))
	for _, item := range items {
		if item.Type != ContextItemSystemMessage && item.Type != ContextItemUserMessage && item.Type != ContextItemAssistantMessage {
			normalized = append(normalized, item)
			continue
		}
		parts := make([]ContextContentPart, 0, len(item.Parts))
		changed := false
		for _, part := range item.Parts {
			if part.Type != ContextPartImage {
				parts = append(parts, part)
				continue
			}
			images := normalizeContextImagePart(part)
			parts = append(parts, images...)
			if len(images) != 1 || images[0].ImageFormat != part.ImageFormat || images[0].ImageBase64 != part.ImageBase64 {
				changed = true
			}
		}
		if !changed {
			normalized = append(normalized, item)
			continue
		}
		copied := item
		copied.Parts = parts
		if item.Type == ContextItemAssistantMessage {
			// 修改正文后原始 Provider 回放片段已失效，不能绕过尺寸限制重发原图
			copied.Replay = nil
		}
		normalized = append(normalized, copied)
	}
	return normalized
}

// normalizeContextImagePart 仅处理单边超过 8000 的图片，返回一个或多个图片片段。
func normalizeContextImagePart(part ContextContentPart) []ContextContentPart {
	if part.Type != ContextPartImage || part.ImageBase64 == "" {
		return []ContextContentPart{part}
	}
	raw, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		return []ContextContentPart{part}
	}
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return []ContextContentPart{part}
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	longSide, shortSide := width, height
	if shortSide > longSide {
		longSide, shortSide = shortSide, longSide
	}
	if longSide <= modelImageMaxSide {
		return []ContextContentPart{part}
	}
	if shortSide <= 0 {
		return []ContextContentPart{part}
	}

	frame := decoded
	segments := make([]image.Image, 0, 1)
	if float64(longSide)/float64(shortSide) <= modelImageSplitRatio {
		frame = resizeLongSide(frame, modelImageMaxSide, longSide)
		segments = append(segments, frame)
	} else {
		maxLength := modelImageMaxSide*modelImageMaxSegments - modelImageOverlap*(modelImageMaxSegments-1)
		scale := math.Min(1, math.Min(float64(modelImageMaxSide)/float64(shortSide), float64(maxLength)/float64(longSide)))
		if scale < 1 {
			frame = resizeByScale(frame, scale)
		}
		bounds = frame.Bounds()
		width, height = bounds.Dx(), bounds.Dy()
		longSide, shortSide = width, height
		if shortSide > longSide {
			longSide, shortSide = shortSide, longSide
		}
		segmentLength := modelImageMaxSide
		budgetLength := int(math.Ceil(float64(longSide+modelImageOverlap*(modelImageMaxSegments-1)) / float64(modelImageMaxSegments)))
		if shortSide*modelImageSplitRatio > budgetLength {
			budgetLength = shortSide * modelImageSplitRatio
		}
		if budgetLength < segmentLength {
			segmentLength = budgetLength
		}
		if segmentLength <= modelImageOverlap {
			return []ContextContentPart{part}
		}
		for start := 0; start < longSide; {
			end := start + segmentLength
			if end > longSide {
				end = longSide
			}
			var rect image.Rectangle
			if height >= width {
				rect = image.Rect(0, start, width, end)
			} else {
				rect = image.Rect(start, 0, end, height)
			}
			segments = append(segments, imaging.Crop(frame, rect))
			if end == longSide {
				break
			}
			start = end - modelImageOverlap
		}
	}

	outputFormat := "png"
	if strings.EqualFold(strings.TrimSpace(format), "jpeg") || strings.EqualFold(strings.TrimSpace(format), "jpg") {
		outputFormat = "jpeg"
	}
	normalized := make([]ContextContentPart, 0, len(segments))
	for _, segment := range segments {
		encoded, encodeErr := encodeContextImageSegment(segment, outputFormat)
		if encodeErr != nil {
			return []ContextContentPart{part}
		}
		normalized = append(normalized, encoded)
	}
	if len(normalized) == 0 {
		return []ContextContentPart{part}
	}
	return normalized
}

func resizeLongSide(img image.Image, targetLongSide, currentLongSide int) image.Image {
	scale := float64(targetLongSide) / float64(currentLongSide)
	return resizeByScale(img, scale)
}

func resizeByScale(img image.Image, scale float64) image.Image {
	bounds := img.Bounds()
	width := max(1, int(float64(bounds.Dx())*scale))
	height := max(1, int(float64(bounds.Dy())*scale))
	return imaging.Resize(img, width, height, imaging.Lanczos)
}

func encodeContextImageSegment(segment image.Image, format string) (ContextContentPart, error) {
	var buffer bytes.Buffer
	if format == "jpeg" {
		if err := jpeg.Encode(&buffer, imageForJPEG(segment), &jpeg.Options{Quality: 95}); err != nil {
			return ContextContentPart{}, err
		}
	} else {
		if err := png.Encode(&buffer, segment); err != nil {
			return ContextContentPart{}, err
		}
	}
	return ContextContentPart{
		Type:        ContextPartImage,
		ImageFormat: format,
		ImageBase64: base64.StdEncoding.EncodeToString(buffer.Bytes()),
	}, nil
}

// imageForJPEG 按 Python convert("RGB") 的语义丢弃 alpha，保留原始 RGB 分量。
func imageForJPEG(src image.Image) image.Image {
	if opaque, ok := src.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		return src
	}
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			pixel := color.NRGBAModel.Convert(src.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			dst.SetRGBA(x, y, color.RGBA{R: pixel.R, G: pixel.G, B: pixel.B, A: 255})
		}
	}
	return dst
}
