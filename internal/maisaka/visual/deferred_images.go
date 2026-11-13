package visual

import (
	"strings"

	"github.com/google/uuid"

	maisaka "maibot-go/internal/maisaka"
)

// DeferredImageMarkerPrefix 延迟图片占位前缀
const DeferredImageMarkerPrefix = "deferred-image:"

// ImagePart 编码后的上下文图片片段
type ImagePart struct {
	Format string
	Base64 string
}

// ImageEncoder 延迟执行的图片编码任务
type ImageEncoder func() ImagePart

// DeferredImageScope 请求构建期间暂存图片编码任务，只编码最终保留的图片
type DeferredImageScope struct {
	pending map[string]ImageEncoder
}

// NewDeferredImageScope 创建延迟编码作用域
func NewDeferredImageScope() *DeferredImageScope {
	return &DeferredImageScope{pending: map[string]ImageEncoder{}}
}

// Active 判断当前是否处于延迟编码作用域
func (s *DeferredImageScope) Active() bool {
	return s != nil
}

// AppendContextImage 无构建作用域时立即编码；作用域内仅登记一次图片出现位置
func (s *DeferredImageScope) AppendContextImage(item *maisaka.ContextItem, encode ImageEncoder) {
	if s == nil {
		part := encode()
		item.Images = append(item.Images, maisaka.ImageComponent{MediaRef: part.Base64, Format: part.Format, HasBinary: true})
		return
	}
	marker := DeferredImageMarkerPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.pending[marker] = encode
	item.Images = append(item.Images, maisaka.ImageComponent{MediaRef: marker, Format: "png", HasBinary: true})
}

// TransformContextImage 对已编码图片立即变换；延迟图片将变换组合进编码任务
func (s *DeferredImageScope) TransformContextImage(image maisaka.ImageComponent, transform func(ImagePart) ImagePart) maisaka.ImageComponent {
	if s == nil {
		part := transform(ImagePart{Format: image.Format, Base64: image.MediaRef})
		image.Format = part.Format
		image.MediaRef = part.Base64
		return image
	}
	encode, ok := s.pending[image.MediaRef]
	if !ok {
		part := transform(ImagePart{Format: image.Format, Base64: image.MediaRef})
		image.Format = part.Format
		image.MediaRef = part.Base64
		return image
	}
	marker := DeferredImageMarkerPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.pending[marker] = func() ImagePart {
		return transform(encode())
	}
	image.MediaRef = marker
	return image
}

// ResolveImages 解析最终保留的图片，丢弃被裁掉的任务
func (s *DeferredImageScope) ResolveImages(items []maisaka.ContextItem) []maisaka.ContextItem {
	if s == nil {
		return items
	}
	for index, item := range items {
		changed := false
		for imageIndex, image := range item.Images {
			encode, ok := s.pending[image.MediaRef]
			if !ok {
				continue
			}
			part := encode()
			item.Images[imageIndex] = maisaka.ImageComponent{MediaRef: part.Base64, Format: part.Format, HasBinary: true}
			changed = true
		}
		if changed {
			items[index] = item
		}
	}
	s.pending = map[string]ImageEncoder{}
	return items
}

// DeferContextImageEncoding 在构建逻辑返回后解析保留图片，对齐 Python defer_context_image_encoding。
func DeferContextImageEncoding[T any](selectItems func(T) []maisaka.ContextItem, build func(scope *DeferredImageScope) T) T {
	scope := NewDeferredImageScope()
	result := build(scope)
	items := selectItems(result)
	_ = scope.ResolveImages(items)
	return result
}
