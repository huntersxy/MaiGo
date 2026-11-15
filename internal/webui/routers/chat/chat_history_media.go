package chat

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/core/internal_chat"
)

// chatHistoryBinaryLoader 为落库还原的图片/表情段回读二进制
// 对齐 src/webui/routers/chat/serializers.py:_load_cached_binary_data：
// 按 hash 与 image_type 查 images 表，再解析实例内的存储路径读取字节
func (d Deps) HistoryBinaryLoader(ctx context.Context) internalchat.BinaryLoader {
	store := d.resolveMonitorMediaStore()
	root := strings.TrimSpace(d.InstanceDir)
	if store == nil || root == "" {
		return nil
	}
	return func(segmentType, hash string) ([]byte, string) {
		imageType := imagesystem.ImageTypeImage
		if segmentType == "emoji" {
			imageType = imagesystem.ImageTypeEmoji
		}
		record, err := store.GetByHash(ctx, hash, imageType)
		if err != nil || record == nil || record.NoFileFlag {
			return nil, ""
		}
		filePath, err := imagesystem.ResolveStoredImagePath(root, record.FullPath)
		if err != nil {
			return nil, ""
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, ""
		}
		return data, chatHistoryMediaMIME(filePath)
	}
}

// chatHistoryMediaMIME 对齐 mimetypes.guess_type(path)[0]
// 未知扩展返回空串，由序列化器回退该段类型的默认 MIME
func chatHistoryMediaMIME(filePath string) string {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	if contentType == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType
	}
	return mediaType
}
