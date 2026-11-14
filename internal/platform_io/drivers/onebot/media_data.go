package onebot

import (
	"encoding/base64"
	"strings"
)

// ImageDataURL 把 base64 图片正文转换为带 MIME 的 data URL
// 入站媒体链路只接受可下载引用，data URL 由 DownloadImage 直接落盘
func ImageDataURL(encoded string) string {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return ""
	}
	if strings.HasPrefix(encoded, "data:image/") {
		return encoded
	}
	mimeType := "image/jpeg"
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		mimeType = SniffImageMime(decoded)
	}
	return "data:" + mimeType + ";base64," + encoded
}

// SniffImageMime 按魔数判断图片类型，无法识别时按 JPEG 处理
func SniffImageMime(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 2 && string(data[:2]) == "BM":
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}
