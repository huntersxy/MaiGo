package monitor

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// MonitorMediaKinds 监控面板可切换展示的媒体类型
const (
	MonitorMediaImage = "image"
	MonitorMediaEmoji = "emoji"
	MonitorMediaVoice = "voice"
)

// PendingImagePlaceholder 与视觉占位保持一致的文本
const PendingImagePlaceholder = "[图片，识别中.....]"

// MaxInlineMonitorMediaBytes 可内联为 data URL 的媒体上限，对齐 MAX_INLINE_MONITOR_MEDIA_BYTES
const MaxInlineMonitorMediaBytes = 2 * 1024 * 1024

// BuildMonitorMessageContent 生成监控面板使用的消息可见文本，媒体内容由 media payload 单独承载
func BuildMonitorMessageContent(content string, refreshVisual func() string, logPrefix string) string {
	_ = logPrefix
	if refreshVisual != nil {
		content = refreshVisual()
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == PendingImagePlaceholder {
		return "[图片]"
	}
	return content
}

// HasMonitorMessageMedia 判断监控面板是否需要为消息单独展示图片或表情
func HasMonitorMessageMedia(media []map[string]any) bool {
	return len(media) > 0
}

// BuildMonitorMessageMedia 构造监控面板可切换展示的原始图片或表情
//
// 每个条目带 hash、可见文本、原始 URL、可内联 data URL 与在消息中的位置，
// 与 Python build_monitor_message_media 的字段面一致。
func BuildMonitorMessageMedia(media []map[string]any, defaultOriginal bool, logPrefix string) []map[string]any {
	_ = logPrefix
	payloads := make([]map[string]any, 0, len(media))
	for index, item := range media {
		kind := strings.TrimSpace(fmt.Sprint(item["kind"]))
		if kind == "" || kind == "<nil>" {
			kind = MonitorMediaImage
		}
		hash := strings.TrimSpace(fmt.Sprint(item["hash"]))
		if hash == "" || hash == "<nil>" {
			continue
		}
		label := "[表情包]"
		switch kind {
		case MonitorMediaImage:
			label = PendingImagePlaceholder
		case MonitorMediaVoice:
			label = "[语音消息]"
		}
		text := strings.TrimSpace(fmt.Sprint(item["text"]))
		if text == "" || text == "<nil>" {
			text = label
		}
		position := index
		if rawIndex, ok := item["index"]; ok {
			if parsed, ok := payloadInt64Value(rawIndex); ok {
				position = int(parsed)
			}
		}
		mediaURL := fmt.Sprintf("/api/webui/system/maisaka-monitor/media/%s/%s", kind, hash)
		if rawURL := strings.TrimSpace(fmt.Sprint(item["url"])); strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
			mediaURL = rawURL
		}
		entry := map[string]any{
			"kind":             kind,
			"hash":             hash,
			"text":             text,
			"url":              mediaURL,
			"data_url":         "",
			"default_original": defaultOriginal,
			"index":            position,
		}
		if dataURL := strings.TrimSpace(fmt.Sprint(item["data_url"])); dataURL != "" && dataURL != "<nil>" {
			entry["data_url"] = dataURL
		}
		if data, ok := item["data"].(string); ok && data != "" {
			format := strings.TrimSpace(fmt.Sprint(item["format"]))
			if format == "" || format == "<nil>" {
				format = "png"
			}
			if dataURL := BuildMonitorMediaDataURL(kind, data, format); dataURL != "" {
				entry["data_url"] = dataURL
			}
		}
		payloads = append(payloads, entry)
	}
	return payloads
}

// BuildMonitorMediaDataURL 为未落库的小体积图片/表情生成观察面板可直接展示的 data URL
func BuildMonitorMediaDataURL(mediaKind, base64Data, format string) string {
	if strings.TrimSpace(base64Data) == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return ""
	}
	if len(decoded) == 0 || len(decoded) > MaxInlineMonitorMediaBytes {
		return ""
	}
	mimeType := GuessMonitorMediaMimeType(decoded)
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/" + strings.TrimPrefix(strings.TrimSpace(format), ".")
	}
	return "data:" + mimeType + ";base64," + base64Data
}

// GuessMonitorMediaMimeType 根据文件头判断常见图片 MIME 类型，未知回退 image/png
func GuessMonitorMediaMimeType(binaryData []byte) string {
	switch {
	case len(binaryData) >= 3 && binaryData[0] == 0xFF && binaryData[1] == 0xD8 && binaryData[2] == 0xFF:
		return "image/jpeg"
	case len(binaryData) >= 4 && string(binaryData[:4]) == "GIF8":
		return "image/gif"
	case len(binaryData) >= 12 && string(binaryData[:4]) == "RIFF" && strings.Contains(string(binaryData[:16]), "WEBP"):
		return "image/webp"
	case len(binaryData) >= 2 && string(binaryData[:2]) == "BM":
		return "image/bmp"
	default:
		return "image/png"
	}
}

// BuildMonitorReplyPreview 构造监控面板中的回复引用预览
func BuildMonitorReplyPreview(target map[string]any, findSourceMessage func(string) map[string]any, buildSourceContent func(map[string]any) string) map[string]any {
	if target == nil {
		return nil
	}
	replyToID := monitorContentText(target["reply_to"])
	if replyToID == "" {
		return nil
	}
	preview := map[string]any{"message_id": replyToID}
	senderName := firstMonitorText(target["reply_sender"], target["reply_sender_cardname"], target["reply_sender_nickname"])
	content := monitorContentText(target["reply_preview"])
	if content == "" && replyToID != "" && findSourceMessage != nil {
		if source := findSourceMessage(replyToID); source != nil {
			if buildSourceContent != nil {
				content = strings.TrimSpace(buildSourceContent(source))
			}
			if name := firstMonitorText(source["speaker_name"], source["sender_name"], source["user_cardname"], source["nickname"]); name != "" {
				senderName = name
			}
		}
	}
	if senderName == "" {
		senderName = "未知用户"
	}
	if content == "" {
		content = "原消息已无法访问"
	}
	preview["sender_name"] = senderName
	preview["content"] = content
	return preview
}

func firstMonitorText(values ...any) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		text := monitorContentText(value)
		if text != "" {
			return text
		}
	}
	return ""
}

// monitorContentText 只接受真正的文本载体；结构化载荷不能格式化成正文
func monitorContentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case map[string]any:
		return monitorContentText(typed["text"])
	default:
		return ""
	}
}
