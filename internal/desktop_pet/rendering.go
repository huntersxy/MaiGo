package desktoppet

// 桌宠气泡消息转换，对齐 src/desktop_pet/rendering.py

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// RenderParts 把消息段转成桌宠气泡可显示的文本与图片，气泡放不下的类型记入 skipped
func RenderParts(segments []map[string]any, skipped *[]string) ([]map[string]any, error) {
	parts := make([]map[string]any, 0, len(segments))
	for _, segment := range segments {
		if segment == nil {
			continue
		}
		kind, _ := segment["type"].(string)
		switch kind {
		case "text":
			text, _ := segment["data"].(string)
			parts = append(parts, map[string]any{"kind": "text", "text": text})
		case "at":
			parts = append(parts, map[string]any{"kind": "text", "text": "@" + atLabel(segment["data"]) + " "})
		case "image", "emoji":
			src, err := imageSrc(segment)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{"kind": "image", "src": src})
		case "reply":
			// 桌宠只有一个对话对象，引用关系不需要展示
		case "forward":
			nodes, _ := segment["data"].([]any)
			for _, item := range nodes {
				node, _ := item.(map[string]any)
				content, _ := node["content"].([]any)
				nested, err := RenderParts(segmentMaps(content), skipped)
				if err != nil {
					return nil, err
				}
				parts = append(parts, nested...)
			}
		default:
			if skipped != nil {
				*skipped = append(*skipped, kind)
			}
		}
	}
	return parts, nil
}

// atLabel 对齐 target_user_cardname or target_user_nickname or target_user_id
func atLabel(data any) string {
	info, _ := data.(map[string]any)
	for _, key := range []string{"target_user_cardname", "target_user_nickname", "target_user_id"} {
		if value, ok := info[key]; ok && value != nil {
			if text := fmt.Sprint(value); text != "" {
				return text
			}
		}
	}
	return ""
}

// imageSrc 把图片/表情消息段转成页面可直接显示的 data URI
func imageSrc(segment map[string]any) (string, error) {
	encoded, _ := segment["binary_data_base64"].(string)
	if encoded == "" {
		// WebUI 富文本段已带 data URI，可直接交给气泡展示
		if dataURL, _ := segment["data"].(string); strings.HasPrefix(dataURL, "data:image/") {
			return dataURL, nil
		}
		return "", fmt.Errorf("图片缺少 base64 数据")
	}
	prefix := encoded
	if len(prefix) > 24 {
		prefix = prefix[:24]
	}
	prefix = prefix[:len(prefix)-len(prefix)%4]
	head, err := base64.StdEncoding.DecodeString(prefix)
	if err != nil {
		return "", fmt.Errorf("图片 base64 无效")
	}
	mime := imageMIMEFromHead(head)
	if mime == "" {
		return "", fmt.Errorf("无法识别的图片格式")
	}
	return "data:" + mime + ";base64," + encoded, nil
}

// imageMIMEFromHead 按文件头识别图片 MIME
func imageMIMEFromHead(head []byte) string {
	switch {
	case len(head) >= 4 && head[0] == 0x89 && string(head[1:4]) == "PNG":
		return "image/png"
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return "image/jpeg"
	case len(head) >= 4 && string(head[:4]) == "GIF8":
		return "image/gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}

// segmentMaps 把 []any 里的消息段转成 []map[string]any
func segmentMaps(items []any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if segment, ok := item.(map[string]any); ok {
			out = append(out, segment)
		}
	}
	return out
}

// ImageSrc 对齐 desktop_pet/rendering.image_src：按魔数嗅探 MIME 并转 data URI
func ImageSrc(segment map[string]any) (string, error) {
	return imageSrc(segment)
}
