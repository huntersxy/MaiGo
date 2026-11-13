package visual

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"sync"
)

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

type emojiCacheEntry struct {
	key  string
	part ImagePart
	size int
}

// EmojiImageCache 线程间共享的 LRU，同时约束缓存字节数与条目数
type EmojiImageCache struct {
	mu         sync.Mutex
	maxBytes   int
	maxEntries int
	bytes      int
	items      map[string]*list.Element
	order      *list.List
}

// NewEmojiImageCache 创建表情图片缓存
func NewEmojiImageCache(maxBytes, maxEntries int) *EmojiImageCache {
	if maxBytes <= 0 {
		maxBytes = EmojiCacheMaxBytes
	}
	if maxEntries <= 0 {
		maxEntries = EmojiCacheMaxEntries
	}
	return &EmojiImageCache{
		maxBytes:   maxBytes,
		maxEntries: maxEntries,
		items:      map[string]*list.Element{},
		order:      list.New(),
	}
}

// Get 返回最终编码结果
func (c *EmojiImageCache) Get(binaryHash string, imageBytes []byte) (ImagePart, error) {
	if c == nil {
		return PrepareEmojiImage(imageBytes)
	}
	c.mu.Lock()
	if element, ok := c.items[binaryHash]; ok {
		c.order.MoveToFront(element)
		entry := element.Value.(*emojiCacheEntry)
		c.mu.Unlock()
		return entry.part, nil
	}
	c.mu.Unlock()
	part, err := PrepareEmojiImage(imageBytes)
	if err != nil {
		return ImagePart{}, err
	}
	size := len(part.Base64)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[binaryHash]; exists {
		return part, nil
	}
	element := c.order.PushFront(&emojiCacheEntry{key: binaryHash, part: part, size: size})
	c.items[binaryHash] = element
	c.bytes += size
	c.pruneLocked()
	return part, nil
}

func (c *EmojiImageCache) pruneLocked() {
	for c.order.Len() > 0 && (c.bytes > c.maxBytes || c.order.Len() > c.maxEntries) {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*emojiCacheEntry)
		c.order.Remove(oldest)
		delete(c.items, entry.key)
		c.bytes -= entry.size
	}
}

// Len 当前缓存条目数
func (c *EmojiImageCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
