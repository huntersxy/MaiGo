package visual

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"
)

// errEmojiCacheResult singleflight 返回值类型异常，属内部错误
var errEmojiCacheResult = errors.New("visual: 表情缓存返回值类型异常")

// 表情缩略图缓存常量
const (
	EmojiMaxSide         = 256
	EmojiCacheMaxEntries = 512
	EmojiCacheMaxBytes   = 32 << 20
)

// PrepareEmojiImage 小图保持原样；大图取首帧并等比例缩小，保留透明度
func PrepareEmojiImage(imageBytes []byte) (ImagePart, error) {
	decoded, _, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return ImagePart{}, err
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= EmojiMaxSide && height <= EmojiMaxSide {
		return ImagePart{Format: "png", Base64: base64.StdEncoding.EncodeToString(imageBytes)}, nil
	}
	scaled := scaleToFit(decoded, EmojiMaxSide)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, scaled); err != nil {
		return ImagePart{}, err
	}
	return ImagePart{Format: "png", Base64: base64.StdEncoding.EncodeToString(buffer.Bytes())}, nil
}

// scaleToFit 等比例缩放到最长边不超过 maxSide
func scaleToFit(source image.Image, maxSide int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxSide && height <= maxSide {
		return source
	}
	targetWidth, targetHeight := width, height
	if width >= height {
		targetHeight = max(1, height*maxSide/width)
		targetWidth = maxSide
	} else {
		targetWidth = max(1, width*maxSide/height)
		targetHeight = maxSide
	}
	// 最近邻采样保留透明度与像素边界，避免额外图像依赖
	scaled := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sourceY := bounds.Min.Y + y*height/targetHeight
		for x := 0; x < targetWidth; x++ {
			sourceX := bounds.Min.X + x*width/targetWidth
			scaled.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	return scaled
}

// EmojiImageCache 线程间共享的 LRU，同时约束缓存字节数与条目数。
//
// ttlcache 负责容量与字节预算淘汰（WithMaxCost 按 Base64 长度计费），
// singleflight 合并同一图片的并发转换，对齐 Python emoji_image_cache
// 的 pending Future 共享。
type EmojiImageCache struct {
	items *ttlcache.Cache[string, ImagePart]
	group singleflight.Group
}

// NewEmojiImageCache 创建表情图片缓存
func NewEmojiImageCache(maxBytes, maxEntries int) *EmojiImageCache {
	if maxBytes <= 0 {
		maxBytes = EmojiCacheMaxBytes
	}
	if maxEntries <= 0 {
		maxEntries = EmojiCacheMaxEntries
	}
	return &EmojiImageCache{items: ttlcache.New(
		ttlcache.WithCapacity[string, ImagePart](uint64(maxEntries)),
		ttlcache.WithMaxCost[string, ImagePart](uint64(maxBytes), emojiImageCost),
	)}
}

// Get 返回最终编码结果
func (c *EmojiImageCache) Get(binaryHash string, imageBytes []byte) (ImagePart, error) {
	if c == nil {
		return PrepareEmojiImage(imageBytes)
	}
	if item := c.items.Get(binaryHash); item != nil {
		return item.Value(), nil
	}
	result, err, _ := c.group.Do(binaryHash, func() (any, error) {
		if item := c.items.Get(binaryHash); item != nil {
			return item.Value(), nil
		}
		part, prepareErr := PrepareEmojiImage(imageBytes)
		if prepareErr != nil {
			return ImagePart{}, prepareErr
		}
		c.items.Set(binaryHash, part, ttlcache.NoTTL)
		return part, nil
	})
	if err != nil {
		return ImagePart{}, err
	}
	part, ok := result.(ImagePart)
	if !ok {
		return ImagePart{}, errEmojiCacheResult
	}
	return part, nil
}

// Len 当前缓存条目数
func (c *EmojiImageCache) Len() int {
	if c == nil {
		return 0
	}
	return c.items.Len()
}

// emojiImageCost 以 Base64 长度作为内存占用近似值，对齐 Python 的 getsizeof 口径
func emojiImageCost(item ttlcache.CostItem[string, ImagePart]) uint64 {
	return uint64(len(item.Value.Base64) + len(item.Value.Format) + len(item.Key))
}
