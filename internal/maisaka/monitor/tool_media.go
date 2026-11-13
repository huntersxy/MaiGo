package monitor

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"strings"

	"github.com/disintegration/imaging"
	genwebp "github.com/gen2brain/webp"
)

const (
	// ThumbnailSize 监控缩略图边长，对齐 THUMBNAIL_SIZE
	ThumbnailSize = 192
	// ThumbnailQuality 缩略图 WebP 质量，对齐 quality=65
	ThumbnailQuality = 65
)

// BuildToolResultImages 为工具返回的图片生成小体积缩略图，避免原始 Base64 进入快照与账本
//
// 每个条目固定带 label；成功时填 thumbnail_url（data URL），失败时填 error，
// 与 Python build_tool_result_images 的字段面一致。
func BuildToolResultImages(items []map[string]any) []map[string]any {
	images := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemType := strings.ToLower(strings.TrimSpace(pickString(item, "type")))
		mimeType := strings.ToLower(strings.TrimSpace(pickString(item, "mime_type")))
		isImage := itemType == "image" ||
			((itemType == "binary" || itemType == "resource" || itemType == "resource_link") && strings.HasPrefix(mimeType, "image/"))
		if !isImage {
			continue
		}
		label := pickString(item, "name")
		if label == "" {
			label = pickString(item, "description")
		}
		if label == "" {
			label = "工具返回图片"
		}
		entry := map[string]any{"label": label, "thumbnail_url": ""}
		images = append(images, entry)

		rawData := pickString(item, "data")
		uri := pickString(item, "uri")
		if rawData == "" && strings.HasPrefix(uri, "data:") {
			rawData = uri
		}
		if rawData == "" {
			// URL 型图片由浏览器懒加载，后端不代为访问
			if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
				entry["thumbnail_url"] = uri
			} else {
				entry["error"] = "图片缺少可显示的数据或链接"
			}
			continue
		}
		if strings.HasPrefix(rawData, "data:") {
			index := strings.Index(rawData, ",")
			if index < 0 || !strings.HasSuffix(rawData[:index], ";base64") {
				entry["error"] = "图片数据无效，无法生成缩略图"
				continue
			}
			rawData = rawData[index+1:]
		}
		thumbnail, err := buildToolThumbnail(rawData)
		if err != nil {
			entry["error"] = "图片数据无效，无法生成缩略图"
			continue
		}
		entry["thumbnail_url"] = "data:image/webp;base64," + base64.StdEncoding.EncodeToString(thumbnail)
	}
	return images
}

// buildToolThumbnail 解码并等比缩放到缩略图边长，统一输出 WebP
func buildToolThumbnail(rawData string) ([]byte, error) {
	normalized := strings.Join(strings.Fields(rawData), "")
	decoded, err := base64.StdEncoding.DecodeString(padBase64(normalized))
	if err != nil {
		return nil, err
	}
	source, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		return nil, err
	}
	resized := imaging.Fit(source, ThumbnailSize, ThumbnailSize, imaging.Lanczos)
	if resized == nil {
		return nil, errors.New("缩略图缩放失败")
	}
	// 统一转 RGBA，避免调色板/灰度图在 WebP 编码时行为不一致
	rgba := image.NewNRGBA(resized.Bounds())
	draw.Draw(rgba, rgba.Bounds(), resized, resized.Bounds().Min, draw.Src)
	var buffer bytes.Buffer
	if err := genwebp.Encode(&buffer, rgba, genwebp.Options{Quality: ThumbnailQuality}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// padBase64 补齐 base64 填充位，容忍前端裁掉末尾 '='
func padBase64(value string) string {
	if remainder := len(value) % 4; remainder != 0 {
		return value + strings.Repeat("=", 4-remainder)
	}
	return value
}

func pickString(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	value, ok := record[key]
	if !ok {
		return ""
	}
	text := strings.TrimSpace(toText(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func toText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if data, ok := value.([]byte); ok {
		return string(data)
	}
	return fmt.Sprint(value)
}
