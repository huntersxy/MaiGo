package maisaka

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
)

// DeferredMediaMarkerPrefix 惰性图片占位前缀，与 runtime deferred-media 主链路一致
const DeferredMediaMarkerPrefix = "deferred-media:"

// ImageComponentPayload 解析图片组件的可发送内容；deferred-media 路径在此读盘编码，URL/哈希等无法内联的引用返回 false
func ImageComponentPayload(component ImageComponent) (string, string, bool) {
	ref := strings.TrimSpace(component.MediaRef)
	if ref == "" {
		return "", "", false
	}
	if strings.HasPrefix(ref, DeferredMediaMarkerPrefix) {
		path := strings.TrimPrefix(ref, DeferredMediaMarkerPrefix)
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 || len(raw) > 12<<20 {
			return "", "", false
		}
		mime := normalizeImageMIME(strings.TrimSpace(component.Format))
		if !strings.HasPrefix(mime, "image/") {
			mime = strings.TrimSpace(http.DetectContentType(raw))
		}
		if !strings.HasPrefix(mime, "image/") {
			mime = "image/png"
		}
		return base64.StdEncoding.EncodeToString(raw), mime, true
	}
	mimeHint := ""
	if strings.HasPrefix(ref, "data:") {
		comma := strings.Index(ref, ",")
		if comma < 0 {
			return "", "", false
		}
		metadata := ref[len("data:"):comma]
		if !strings.Contains(strings.ToLower(metadata), ";base64") {
			return "", "", false
		}
		mimeHint = strings.TrimSpace(strings.Split(metadata, ";")[0])
		ref = ref[comma+1:]
	}
	raw, ok := decodeImageBase64(ref)
	if !ok {
		return "", "", false
	}
	mime := strings.TrimSpace(http.DetectContentType(raw))
	if !strings.HasPrefix(mime, "image/") {
		mime = normalizeImageMIME(mimeHint)
	}
	if !strings.HasPrefix(mime, "image/") {
		return "", "", false
	}
	return base64.StdEncoding.EncodeToString(raw), mime, true
}

func decodeImageBase64(value string) ([]byte, bool) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		raw, err := encoding.DecodeString(value)
		if err == nil && len(raw) > 0 {
			return raw, true
		}
	}
	return nil, false
}

func normalizeImageMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "/") {
		if strings.HasPrefix(value, "image/") {
			return value
		}
		return ""
	}
	switch value {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	case "tiff", "tif":
		return "image/tiff"
	default:
		return ""
	}
}
