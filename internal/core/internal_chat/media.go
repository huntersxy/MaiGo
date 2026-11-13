package internalchat

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// imageSegment 读取图片文件并生成 WebUI 富文本图片段
func imageSegment(filePath string, isSticker bool) (map[string]any, error) {
	if strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("图片路径为空")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取图片失败: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("图片内容为空")
	}
	mimeType := detectImageMIME(data)
	if mimeType == "" {
		mimeType = mimeForExt(filepath.Ext(filePath))
	}
	if mimeType == "" {
		return nil, fmt.Errorf("无法识别的图片格式")
	}
	segmentType := "image"
	if isSticker {
		segmentType = "emoji"
	}
	return map[string]any{
		"type": segmentType,
		"data": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

// detectImageMIME 按文件头识别图片类型，对齐 serializers._detect_binary_mime_type
func detectImageMIME(data []byte) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 8 && data[0] == 0x89 && string(data[1:4]) == "PNG" {
		return "image/png"
	}
	if len(data) >= 6 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return "image/bmp"
	}
	if detected := http.DetectContentType(data); strings.HasPrefix(detected, "image/") {
		return detected
	}
	return ""
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}
