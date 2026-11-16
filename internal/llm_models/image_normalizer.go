package llm

// 模型请求图片治理，对齐 MaiBot src/llm_models/image_normalizer.py：
//   - 按模型边长上下限缩放，长图按短边*3 与总段数预算分段，24px 重叠，最多 16 段
//   - 超过图片数量上限时保留最近的图片，较早的图片替换为文本占位
//   - 超限动图取首帧；PNG 等保留透明通道，JPEG 统一转 RGB 后 quality=95
//
// 归一化发生在 ContextItem 投影之后、Provider 请求构造之前，因此 chat/gemini/plugin
// 与失败快照看到的是同一份图片内容。
// Responses 客户端把请求交给 eino，只能用消息级入口在投影前改写图片，见 normalizeAgenticMessageImages。

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/disintegration/imaging"
	"go.uber.org/zap"

	contract "MaiGo/internal/llm_models/contract"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	modelImageMaxSide       = contract.DefaultModelImageMaxSide
	modelImageMinSide       = contract.DefaultModelImageMinSide
	modelImageSplitRatio    = 3
	modelImageOverlap       = 24
	modelImageMaxSegments   = 16
	modelImageOmittedNotice = "[图片因模型数量限制已省略]"
)

// imageLimits 单次请求的图片限制；零值按默认边长与不限数量处理
type imageLimits struct {
	maxImages int
	maxSide   int
	minSide   int
}

func (l imageLimits) normalized() imageLimits {
	if l.maxSide <= 0 {
		l.maxSide = modelImageMaxSide
	}
	if l.minSide <= 0 {
		l.minSide = modelImageMinSide
	}
	if l.maxImages < 0 {
		l.maxImages = 0
	}
	// Python 对 min_side > max_side 抛 ValueError；Go 侧配置校验已拒绝该组合，
	// 这里兜底成默认限制，避免直接调用时放大模型未允许的边长。
	if l.minSide > l.maxSide {
		l.maxSide = modelImageMaxSide
		l.minSide = modelImageMinSide
	}
	return l
}

// normalizeContextImages 使用默认限制规范化请求图片
func normalizeContextImages(items []ContextItem) []ContextItem {
	return normalizeContextImagesWithLimits(items, imageLimits{})
}

// normalizeContextImagesForModel 按模型配置的图片数量与边长限制规范化
func normalizeContextImagesForModel(items []ContextItem, maxImages, maxSide, minSide int) []ContextItem {
	return normalizeContextImagesWithLimits(items, imageLimits{maxImages: maxImages, maxSide: maxSide, minSide: minSide})
}

// normalizeContextImagesWithLimits 规范化全部图片并保留最近的限额内图片，不修改共享的历史对象。
func normalizeContextImagesWithLimits(items []ContextItem, limits imageLimits) []ContextItem {
	limits = limits.normalized()
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
			images := normalizeContextImagePart(part, limits)
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
	if limits.maxImages == 0 {
		return normalized
	}
	imageCount := 0
	for _, item := range normalized {
		if imageContentItem(item.Type) {
			for _, part := range item.Parts {
				if part.Type == ContextPartImage {
					imageCount++
				}
			}
		}
	}
	excess := imageCount - limits.maxImages
	if excess <= 0 {
		return normalized
	}
	zap.L().Info("请求图片数量超过模型上限，保留最近图片",
		zap.Int("image_count", imageCount),
		zap.Int("max_images", limits.maxImages))
	limited := make([]ContextItem, 0, len(normalized))
	for _, item := range normalized {
		if excess <= 0 || !imageContentItem(item.Type) {
			limited = append(limited, item)
			continue
		}
		parts := make([]ContextContentPart, 0, len(item.Parts))
		changed := false
		for _, part := range item.Parts {
			if part.Type == ContextPartImage && excess > 0 {
				parts = append(parts, ContextContentPart{Type: ContextPartText, Text: modelImageOmittedNotice})
				excess--
				changed = true
				continue
			}
			parts = append(parts, part)
		}
		if !changed {
			limited = append(limited, item)
			continue
		}
		copied := item
		copied.Parts = parts
		if item.Type == ContextItemAssistantMessage {
			copied.Replay = nil
		}
		limited = append(limited, copied)
	}
	return limited
}

func imageContentItem(itemType ContextItemType) bool {
	return itemType == ContextItemSystemMessage || itemType == ContextItemUserMessage || itemType == ContextItemAssistantMessage
}

// normalizeContextImagePart 按模型限制处理单张图片，返回一个或多个图片片段。
func normalizeContextImagePart(part ContextContentPart, limits imageLimits) []ContextContentPart {
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
	if longSide <= limits.maxSide && shortSide >= limits.minSide {
		return []ContextContentPart{part}
	}
	if shortSide <= 0 {
		return []ContextContentPart{part}
	}
	overlap := min(modelImageOverlap, limits.maxSide-1)
	if overlap < 0 {
		overlap = 0
	}

	frame := decoded
	segments := make([]image.Image, 0, 1)
	if longSide <= limits.maxSide || float64(longSide)/float64(shortSide) <= modelImageSplitRatio {
		scale := math.Min(math.Max(1, float64(limits.minSide)/float64(shortSide)), float64(limits.maxSide)/float64(longSide))
		frame = resizeByScale(frame, scale)
		segments = append(segments, frame)
	} else {
		// 先约束短边和最大总长度，再裁剪，避免极长图片生成无界数量的图片
		maxLength := limits.maxSide*modelImageMaxSegments - overlap*(modelImageMaxSegments-1)
		scale := math.Min(1, math.Min(float64(limits.maxSide)/float64(shortSide), float64(maxLength)/float64(longSide)))
		if scale < 1 {
			frame = resizeByScale(frame, scale)
		}
		bounds = frame.Bounds()
		width, height = bounds.Dx(), bounds.Dy()
		longSide, shortSide = width, height
		if shortSide > longSide {
			longSide, shortSide = shortSide, longSide
		}
		segmentLength := limits.maxSide
		budgetLength := int(math.Ceil(float64(longSide+overlap*(modelImageMaxSegments-1)) / float64(modelImageMaxSegments)))
		if shortSide*modelImageSplitRatio > budgetLength {
			budgetLength = shortSide * modelImageSplitRatio
		}
		if budgetLength < segmentLength {
			segmentLength = budgetLength
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
			start = end - overlap
		}
	}

	outputFormat := "png"
	if strings.EqualFold(strings.TrimSpace(format), "jpeg") || strings.EqualFold(strings.TrimSpace(format), "jpg") {
		outputFormat = "jpeg"
	}
	normalized := make([]ContextContentPart, 0, len(segments))
	for _, segment := range segments {
		if min(segment.Bounds().Dx(), segment.Bounds().Dy()) < limits.minSide {
			scale := math.Min(float64(limits.minSide)/float64(min(segment.Bounds().Dx(), segment.Bounds().Dy())), float64(limits.maxSide)/float64(max(segment.Bounds().Dx(), segment.Bounds().Dy())))
			segment = resizeByScale(segment, scale)
			// 极端长宽比无法同时满足两端限制时，补边以保留完整内容
			segment = expandToMinSide(segment, limits.minSide, outputFormat)
		}
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

// agenticImageKind 区分 eino 消息里的用户图片与助手图片
type agenticImageKind int

const (
	agenticImageUser agenticImageKind = iota
	agenticImageAssistant
)

// agenticImageInfo 记录改写图片时需要保留的块级信息
type agenticImageInfo struct {
	kind   agenticImageKind
	detail schema.ImageURLDetail
}

// normalizeAgenticMessageImages 按模型限制改写 eino 消息里的 base64 图片。
// Responses 请求由 eino 直接消费消息，无法像 chat/gemini/plugin 那样按 ContextItem 组装，
// 因此需要在投影前做与 normalize_context_images 等价的尺寸和数量治理。
func normalizeAgenticMessageImages(messages []*schema.AgenticMessage, maxImages, maxSide, minSide int) []*schema.AgenticMessage {
	if len(messages) == 0 {
		return messages
	}
	limits := imageLimits{maxImages: maxImages, maxSide: maxSide, minSide: minSide}.normalized()
	normalized := make([]*schema.AgenticMessage, 0, len(messages))
	imageCount := 0
	for _, message := range messages {
		if message == nil || !agenticImageMessageRole(message.Role) {
			normalized = append(normalized, message)
			continue
		}
		blocks := make([]*schema.ContentBlock, 0, len(message.ContentBlocks))
		changed := false
		for _, block := range message.ContentBlocks {
			part, info, ok := agenticImageContentPart(block)
			if !ok {
				blocks = append(blocks, block)
				continue
			}
			images := normalizeContextImagePart(part, limits)
			imageCount += len(images)
			if len(images) == 1 && images[0].ImageBase64 == part.ImageBase64 && images[0].ImageFormat == part.ImageFormat {
				blocks = append(blocks, block)
				continue
			}
			changed = true
			for _, image := range images {
				blocks = append(blocks, agenticImageBlock(image, info))
			}
		}
		if !changed {
			normalized = append(normalized, message)
			continue
		}
		copied := *message
		copied.ContentBlocks = blocks
		normalized = append(normalized, &copied)
	}
	if limits.maxImages == 0 {
		return normalized
	}
	excess := imageCount - limits.maxImages
	if excess <= 0 {
		return normalized
	}
	zap.L().Info("请求图片数量超过模型上限，保留最近图片",
		zap.Int("image_count", imageCount),
		zap.Int("max_images", limits.maxImages))
	for index, message := range normalized {
		if excess <= 0 {
			break
		}
		if message == nil || !agenticImageMessageRole(message.Role) {
			continue
		}
		blocks := make([]*schema.ContentBlock, 0, len(message.ContentBlocks))
		changed := false
		for _, block := range message.ContentBlocks {
			if excess > 0 {
				if _, _, ok := agenticImageContentPart(block); ok {
					blocks = append(blocks, agenticTextBlock(message.Role))
					excess--
					changed = true
					continue
				}
			}
			blocks = append(blocks, block)
		}
		if !changed {
			continue
		}
		copied := *message
		copied.ContentBlocks = blocks
		normalized[index] = &copied
	}
	return normalized
}

func agenticImageMessageRole(role schema.AgenticRoleType) bool {
	return role == schema.AgenticRoleTypeSystem || role == schema.AgenticRoleTypeUser || role == schema.AgenticRoleTypeAssistant
}

func agenticImageContentPart(block *schema.ContentBlock) (ContextContentPart, agenticImageInfo, bool) {
	if block == nil {
		return ContextContentPart{}, agenticImageInfo{}, false
	}
	if image := block.UserInputImage; image != nil && image.Base64Data != "" {
		return ContextContentPart{
			Type:        ContextPartImage,
			ImageFormat: snapshotImageExtension(image.MIMEType),
			ImageBase64: image.Base64Data,
		}, agenticImageInfo{kind: agenticImageUser, detail: image.Detail}, true
	}
	if image := block.AssistantGenImage; image != nil && image.Base64Data != "" {
		return ContextContentPart{
			Type:        ContextPartImage,
			ImageFormat: snapshotImageExtension(image.MIMEType),
			ImageBase64: image.Base64Data,
		}, agenticImageInfo{kind: agenticImageAssistant}, true
	}
	return ContextContentPart{}, agenticImageInfo{}, false
}

func agenticImageBlock(part ContextContentPart, info agenticImageInfo) *schema.ContentBlock {
	mimeType := contextImageMIME(part.ImageFormat)
	if info.kind == agenticImageAssistant {
		return schema.NewContentBlock(&schema.AssistantGenImage{Base64Data: part.ImageBase64, MIMEType: mimeType})
	}
	return schema.NewContentBlock(&schema.UserInputImage{
		Base64Data: part.ImageBase64,
		MIMEType:   mimeType,
		Detail:     info.detail,
	})
}

func agenticTextBlock(role schema.AgenticRoleType) *schema.ContentBlock {
	if role == schema.AgenticRoleTypeAssistant {
		return schema.NewContentBlock(&schema.AssistantGenText{Text: modelImageOmittedNotice})
	}
	return schema.NewContentBlock(&schema.UserInputText{Text: modelImageOmittedNotice})
}

func resizeByScale(img image.Image, scale float64) image.Image {
	bounds := img.Bounds()
	width := max(1, int(float64(bounds.Dx())*scale))
	height := max(1, int(float64(bounds.Dy())*scale))
	return imaging.Resize(img, width, height, imaging.Lanczos)
}

// expandToMinSide 对齐 ImageOps.expand(border=(..., min_side - width, min_side - height))。
// PIL 的填充色 0 在 RGB/L 上是不透明黑色，在 RGBA 上是透明色，按源图是否含透明像素区分。
func expandToMinSide(img image.Image, minSide int, format string) image.Image {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	extraX := max(0, minSide-width)
	extraY := max(0, minSide-height)
	if extraX == 0 && extraY == 0 {
		return img
	}
	content := image.Rect(0, 0, width, height)
	if format == "jpeg" {
		canvas := image.NewRGBA(image.Rect(0, 0, width+extraX, height+extraY))
		draw.Draw(canvas, content, img, bounds.Min, draw.Src)
		return canvas
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, width+extraX, height+extraY))
	if opaque, ok := img.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		for index := 3; index < len(canvas.Pix); index += 4 {
			canvas.Pix[index] = 255
		}
	}
	draw.Draw(canvas, content, img, bounds.Min, draw.Src)
	return canvas
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
