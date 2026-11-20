package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/common/utils"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// chatImagePreprocessor 在 Agent 消费消息前缓存图片并处理过大图片，描述在后台生成。
type chatImagePreprocessor struct {
	manager  *imagesystem.Manager
	settings func() imagesystem.ImageSettings
}

func newChatImagePreprocessor(manager *imagesystem.Manager) *chatImagePreprocessor {
	return &chatImagePreprocessor{
		manager: manager,
		settings: func() imagesystem.ImageSettings {
			if manager == nil {
				return imagesystem.DefaultImageSettings("")
			}
			return imagesystem.LoadImageSettingsFromRoot(manager.Root())
		},
	}
}

// Process 预处理消息中的图片，失败时保留原图交给 Agent 的既有媒体链路
func (p *chatImagePreprocessor) Process(ctx context.Context, msg *onebot.ConversationMessage, sessionID string) {
	if p == nil || p.manager == nil || msg == nil || len(msg.Images) == 0 {
		return
	}
	settings := p.settings()
	kept := make([]onebot.ImageInfo, 0, len(msg.Images))
	remap := make([]int, len(msg.Images))
	for index := range remap {
		remap[index] = -1
	}
	for index, image := range msg.Images {
		ref := strings.TrimSpace(image.URL)
		if ref == "" {
			ref = strings.TrimSpace(image.File)
		}
		if !preprocessableImageRef(ref) {
			kept = append(kept, image)
			remap[index] = len(kept) - 1
			continue
		}
		updated, keep, err := p.processOne(ctx, ref, image, settings, sessionID)
		if err != nil {
			zap.L().Warn("图片预处理失败，保留原图交给 Agent",
				zap.Int("image_index", index), zap.String("url", ref), zap.Error(err))
			kept = append(kept, image)
			remap[index] = len(kept) - 1
			continue
		}
		if !keep {
			continue
		}
		kept = append(kept, updated)
		remap[index] = len(kept) - 1
	}
	msg.Images = kept
	if len(msg.MessageParts) == 0 {
		return
	}
	parts := make([]onebot.MessagePart, 0, len(msg.MessageParts))
	for _, part := range msg.MessageParts {
		if part.Kind == "image" {
			if part.Index < 0 || part.Index >= len(remap) || remap[part.Index] < 0 {
				continue
			}
			part.Index = remap[part.Index]
		}
		parts = append(parts, part)
	}
	msg.MessageParts = parts
}

func (p *chatImagePreprocessor) processOne(ctx context.Context, ref string, image onebot.ImageInfo, settings imagesystem.ImageSettings, sessionID string) (onebot.ImageInfo, bool, error) {
	dir, err := os.MkdirTemp(p.manager.Dir(), "inbound-image-")
	if err != nil {
		return image, true, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	downloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	downloaded, err := utils.DownloadImage(downloadCtx, ref, dir, 0)
	if err != nil {
		return image, true, err
	}
	data, err := os.ReadFile(downloaded.FilePath)
	if err != nil {
		return image, true, err
	}
	if len(data) == 0 {
		return image, true, fmt.Errorf("图片内容为空")
	}
	kind := imagesystem.ReceiveKindImage
	if image.SubType == 1 {
		kind = imagesystem.ReceiveKindEmoji
	}
	sum := sha256.Sum256(data)
	components := []imagesystem.ReceiveComponent{{
		Kind: kind,
		Image: &imagesystem.ReceiveImage{
			Data:  data,
			Hash:  hex.EncodeToString(sum[:]),
			Emoji: image.SubType == 1,
		},
	}}
	retained, report, err := imagesystem.ProcessReceivedImages(components, imagesystem.OversizedImageOptions{
		HandleOversizedImages: settings.HandleOversizedImages,
		MaxImageSizeBytes:     settings.MaxImageSizeBytes(),
		HandleMethod:          imagesystem.OversizedImageHandleMethod(settings.OversizedImageHandleMethod),
	})
	if err != nil {
		return image, true, err
	}
	if len(retained) == 0 {
		zap.L().Info("入站图片按配置丢弃",
			zap.String("url", ref), zap.Int64("discarded_bytes", report.DiscardedBytes))
		return image, false, nil
	}
	if report.CompressedCount > 0 && retained[0].Image != nil {
		data = retained[0].Image.Data
		image.File = ""
	}
	// 复用已取得的图片内容，避免 Agent 再向适配器下载同一张图。
	if report.CompressedCount > 0 || image.URL == "" || !strings.HasPrefix(strings.ToLower(ref), "data:image/") {
		image.URL = "data:" + onebot.SniffImageMime(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	// 表情包走表情系统缓存与描述，才能入库并参与收藏与维护；描述异步构建不阻塞入站
	if image.SubType == 1 {
		description, _, emojiErr := emojisystem.DefaultRegistry().GetEmojiDescriptionForSession(ctx, data, "", false, sessionID)
		if emojiErr != nil {
			zap.L().Warn("表情包缓存失败", zap.String("url", ref), zap.Error(emojiErr))
		} else if text := strings.TrimSpace(description.Description); text != "" {
			image.Desc = text
		}
		return image, true, nil
	}
	image.RecognitionHash = retained[0].Image.Hash
	description, descErr := p.manager.GetImageDescription(ctx, image.RecognitionHash, data, false)
	if descErr != nil {
		zap.L().Warn("图片缓存或描述调度失败，保留空描述", zap.String("url", ref), zap.Error(descErr))
	} else if strings.TrimSpace(description) != "" {
		image.Desc = description
	}
	return image, true, nil
}

func preprocessableImageRef(ref string) bool {
	lower := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:image/")
}
