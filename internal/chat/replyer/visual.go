package replyer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/jpeg"
	"image/png"
	"strconv"
	"strings"
	"sync"

	"github.com/disintegration/imaging"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

// 对齐 MaiBot maisaka/context/messages.py
const (
	imageQualityNormal = "normal"
	imageQualityHigh   = "high"
	imageMaxSideNormal = 512
	imageMaxSideHigh   = 1024
	emojiMaxSide       = 256
	defaultMaxImages   = 64
	transcodedCacheCap = 16
)

var supportedImageFormats = map[string]bool{
	"jpeg": true,
	"png":  true,
	"webp": true,
	"gif":  true,
}

// imageMaxSideForQuality 对齐 IMAGE_QUALITY_MAX_SIDE
func imageMaxSideForQuality(quality string) int {
	if quality == imageQualityHigh {
		return imageMaxSideHigh
	}
	return imageMaxSideNormal
}

// resolveImageMaxSide 请求显式值优先，否则按 image_quality 选档
func resolveImageMaxSide(quality string, requested int) int {
	if requested > 0 {
		return requested
	}
	return imageMaxSideForQuality(quality)
}

// resolveMaxImageNum 对齐 visual.max_image_num 默认值 64
func resolveMaxImageNum(requested int) int {
	if requested > 0 {
		return requested
	}
	return defaultMaxImages
}

// encodeImageForContext 对齐 _encode_image_for_context：
// 受支持格式且不需要缩放时原样返回；否则转码首帧，透明图用 PNG，其余 JPEG quality=95
func encodeImageForContext(raw []byte, maxSide int) (RequestImage, bool) {
	if len(raw) == 0 {
		return RequestImage{}, false
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return RequestImage{}, false
	}
	normalized := normalizeImageFormat(format)
	// 受支持格式且无需缩放时原样返回；其余格式（含 bmp/tiff）按 Python 转码
	if supportedImageFormats[normalized] && (maxSide <= 0 || (config.Width <= maxSide && config.Height <= maxSide)) {
		return RequestImage{Format: normalized, Base64: base64.StdEncoding.EncodeToString(raw)}, true
	}
	key := cacheKey(raw, maxSide)
	if cached, ok := loadTranscodedImage(key); ok {
		return cached, true
	}
	decoded, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return RequestImage{}, false
	}
	resized := resizeWithin(decoded, maxSide)
	encoded, encodedFormat, err := encodeContextImage(resized)
	if err != nil {
		return RequestImage{}, false
	}
	result := RequestImage{Format: encodedFormat, Base64: base64.StdEncoding.EncodeToString(encoded)}
	storeTranscodedImage(key, result)
	return result, true
}

func normalizeImageFormat(format string) string {
	switch format {
	case "jpg":
		return "jpeg"
	default:
		return format
	}
}

func resizeWithin(source image.Image, maxSide int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || maxSide <= 0 {
		return source
	}
	largest := width
	if height > largest {
		largest = height
	}
	if largest <= maxSide {
		return source
	}
	scale := float64(maxSide) / float64(largest)
	targetWidth := int(float64(width) * scale)
	targetHeight := int(float64(height) * scale)
	if targetWidth < 1 {
		targetWidth = 1
	}
	if targetHeight < 1 {
		targetHeight = 1
	}
	return imaging.Resize(source, targetWidth, targetHeight, imaging.Lanczos)
}

// encodeContextImage 对齐 _transcode_first_frame_for_context 的输出格式选择
func encodeContextImage(img image.Image) ([]byte, string, error) {
	var buffer bytes.Buffer
	if hasTransparency(img) {
		if err := png.Encode(&buffer, img); err != nil {
			return nil, "", err
		}
		return buffer.Bytes(), "png", nil
	}
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 95}); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), "jpeg", nil
}

func hasTransparency(img image.Image) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := img.At(x, y).RGBA()
			if alpha != 0xffff {
				return true
			}
		}
	}
	return false
}

// ContextImageMaxSide 按 visual.image_quality 返回上下文图片最大边
func ContextImageMaxSide(quality string) int {
	return imageMaxSideForQuality(quality)
}

// TranscodeContextImage 按 base64 内容缩放/转码上下文图片，返回 base64 与 MIME
func TranscodeContextImage(base64Data string, maxSide int) (string, string, bool) {
	encoded, ok := transcodeImage(base64Data, maxSide)
	if !ok {
		return "", "", false
	}
	return encoded.Base64, mimeTypeForImageFormat(encoded.Format), true
}

// transcodeImage 按 base64 内容编码单张图片
func transcodeImage(base64Data string, maxSide int) (RequestImage, bool) {
	raw, err := decodeBase64Image(base64Data)
	if err != nil {
		return RequestImage{}, false
	}
	return encodeImageForContext(raw, maxSide)
}

func decodeBase64Image(raw string) ([]byte, error) {
	trimmed := trimBase64Prefix(raw)
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(trimmed)
}

func trimBase64Prefix(raw string) string {
	lowered := strings.ToLower(raw)
	if !strings.HasPrefix(lowered, "data:") {
		return raw
	}
	if index := strings.IndexByte(raw, ','); index >= 0 {
		return raw[index+1:]
	}
	return raw
}

func cacheKey(raw []byte, maxSide int) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) + ":" + strconv.Itoa(maxSide)
}

type transcodedImageCache struct {
	mu      sync.Mutex
	order   []string
	entries map[string]RequestImage
}

var contextImageCache = &transcodedImageCache{entries: map[string]RequestImage{}}

func loadTranscodedImage(key string) (RequestImage, bool) {
	contextImageCache.mu.Lock()
	defer contextImageCache.mu.Unlock()
	value, ok := contextImageCache.entries[key]
	return value, ok
}

func storeTranscodedImage(key string, value RequestImage) {
	contextImageCache.mu.Lock()
	defer contextImageCache.mu.Unlock()
	if _, exists := contextImageCache.entries[key]; !exists {
		contextImageCache.order = append(contextImageCache.order, key)
	}
	contextImageCache.entries[key] = value
	for len(contextImageCache.order) > transcodedCacheCap {
		oldest := contextImageCache.order[0]
		contextImageCache.order = contextImageCache.order[1:]
		delete(contextImageCache.entries, oldest)
	}
}

// mimeTypeForImageFormat 把图片格式转成 eino 图片块需要的 MIME 类型
func mimeTypeForImageFormat(format string) string {
	switch normalizeImageFormat(format) {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
