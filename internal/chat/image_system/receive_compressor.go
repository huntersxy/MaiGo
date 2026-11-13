package imagesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"go.uber.org/zap"
)

// OversizedImageHandleMethod 过大图片处理方式，对齐 Python Literal["compress", "discard"]
type OversizedImageHandleMethod string

const (
	OversizedImageCompress OversizedImageHandleMethod = "compress"
	OversizedImageDiscard  OversizedImageHandleMethod = "discard"
)

// ImageReceiveProcessReport 入站图片处理统计，字段与 Python 一致
type ImageReceiveProcessReport struct {
	CompressedCount int   `json:"compressed_count"`
	DiscardedCount  int   `json:"discarded_count"`
	OriginalBytes   int64 `json:"original_bytes"`
	CompressedBytes int64 `json:"compressed_bytes"`
	DiscardedBytes  int64 `json:"discarded_bytes"`
}

// ReceiveComponentKind 入站组件类型
type ReceiveComponentKind string

const (
	ReceiveKindImage   ReceiveComponentKind = "image"
	ReceiveKindEmoji   ReceiveComponentKind = "emoji"
	ReceiveKindForward ReceiveComponentKind = "forward"
	ReceiveKindOther   ReceiveComponentKind = "other"
)

// ReceiveImage 图片/表情的二进制数据与哈希
type ReceiveImage struct {
	Data  []byte
	Hash  string
	Emoji bool
}

// ReceiveForwardNode 转发节点，对应 Python ForwardComponent
type ReceiveForwardNode struct {
	UserNickname string
	MessageID    string
	UserID       string
	UserCardname string
	Content      []ReceiveComponent
}

// ReceiveComponent 入站组件树，Forward 对应 Python ForwardNodeComponent
type ReceiveComponent struct {
	Kind    ReceiveComponentKind
	Image   *ReceiveImage
	Forward []ReceiveForwardNode
}

// OversizedImageOptions 入站图片处理配置，来自 visual.handle_oversized_images 等字段
type OversizedImageOptions struct {
	HandleOversizedImages bool
	MaxImageSizeBytes     int64
	HandleMethod          OversizedImageHandleMethod
}

// ProcessReceivedImages 按配置处理入站消息中的过大图片组件
// 返回保留下来的组件与统计；未知处理方法在遇到过大图片时返回真实错误
func ProcessReceivedImages(components []ReceiveComponent, options OversizedImageOptions) ([]ReceiveComponent, ImageReceiveProcessReport, error) {
	report := ImageReceiveProcessReport{}
	if !options.HandleOversizedImages || options.MaxImageSizeBytes <= 0 {
		return append([]ReceiveComponent(nil), components...), report, nil
	}
	retained := make([]ReceiveComponent, 0, len(components))
	for _, component := range components {
		switch component.Kind {
		case ReceiveKindImage, ReceiveKindEmoji:
			keep, childReport, err := processImageComponent(component, options)
			if err != nil {
				return nil, report, err
			}
			mergeProcessReport(&report, childReport)
			if keep {
				retained = append(retained, component)
			}
		case ReceiveKindForward:
			for index := range component.Forward {
				children, childReport, err := ProcessReceivedImages(component.Forward[index].Content, options)
				if err != nil {
					return nil, report, err
				}
				mergeProcessReport(&report, childReport)
				component.Forward[index].Content = children
			}
			retained = append(retained, component)
		default:
			retained = append(retained, component)
		}
	}
	return retained, report, nil
}

func processImageComponent(component ReceiveComponent, options OversizedImageOptions) (bool, ImageReceiveProcessReport, error) {
	report := ImageReceiveProcessReport{}
	if component.Image == nil || len(component.Image.Data) == 0 || int64(len(component.Image.Data)) <= options.MaxImageSizeBytes {
		return true, report, nil
	}
	name := "图片"
	if component.Kind == ReceiveKindEmoji || component.Image.Emoji {
		name = "表情"
	}
	switch options.HandleMethod {
	case OversizedImageDiscard:
		report.DiscardedCount++
		report.DiscardedBytes += int64(len(component.Image.Data))
		zap.L().Info(fmt.Sprintf("接收%s过大，已丢弃: %.1fKB, image_hash=%s", name, float64(len(component.Image.Data))/1024, component.Image.Hash))
		return false, report, nil
	case OversizedImageCompress:
		original := component.Image.Data
		compressed, err := CompressImageToSize(original, int(options.MaxImageSizeBytes))
		if err != nil {
			zap.L().Warn("接收"+name+"压缩失败，保持原图", zap.Error(err))
			return true, report, nil
		}
		if len(compressed) >= len(original) {
			return true, report, nil
		}
		sum := sha256.Sum256(compressed)
		component.Image.Data = compressed
		component.Image.Hash = hex.EncodeToString(sum[:])
		report.CompressedCount++
		report.OriginalBytes += int64(len(original))
		report.CompressedBytes += int64(len(compressed))
		zap.L().Info(fmt.Sprintf("接收%s已压缩: %.1fKB -> %.1fKB, image_hash=%s", name, float64(len(original))/1024, float64(len(compressed))/1024, component.Image.Hash))
		return true, report, nil
	default:
		return false, report, fmt.Errorf("未知的过大图片处理方法: %s", options.HandleMethod)
	}
}

func mergeProcessReport(target *ImageReceiveProcessReport, source ImageReceiveProcessReport) {
	target.CompressedCount += source.CompressedCount
	target.DiscardedCount += source.DiscardedCount
	target.OriginalBytes += source.OriginalBytes
	target.CompressedBytes += source.CompressedBytes
	target.DiscardedBytes += source.DiscardedBytes
}
