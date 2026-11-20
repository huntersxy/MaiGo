package snowluma

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/common/utils"
)

func (r *Router) resolveInboundImages(ctx context.Context, message map[string]any) {
	segments := segmentItems(message["raw_message"])
	for _, item := range segments {
		segment := asMap(item)
		kind := asString(segment["type"])
		if kind != "image" && kind != "emoji" {
			continue
		}
		data := asMap(segment["data"])
		if data == nil {
			continue
		}
		image := inboundImageBytes(ctx, data, asString(segment["binary_data_base64"]))
		if len(image) == 0 {
			file := firstNonEmpty(asString(data["file"]), asString(data["file_id"]))
			if file != "" {
				detail, err := r.query.GetImage(ctx, file)
				if err == nil {
					image = inboundImageBytes(ctx, detail, "")
				}
			}
		}
		if len(image) == 0 {
			zap.L().Warn("SnowLuma 入站图片无法取到字节", zap.String("message_id", asString(message["message_id"])), zap.String("kind", kind))
			continue
		}
		sum := sha256.Sum256(image)
		segment["data"] = ""
		segment["hash"] = hex.EncodeToString(sum[:])
		segment["binary_data_base64"] = base64.StdEncoding.EncodeToString(image)
	}
}

func inboundImageBytes(ctx context.Context, data map[string]any, inline string) []byte {
	for _, encoded := range []string{inline, asString(data["binary_data_base64"]), asString(data["base64"])} {
		if image := decodeInboundImage(encoded); len(image) > 0 {
			return image
		}
	}
	for _, ref := range []string{asString(data["file"]), asString(data["url"])} {
		if image := readInboundImage(ctx, ref); len(image) > 0 {
			return image
		}
	}
	return nil
}

func decodeInboundImage(encoded string) []byte {
	encoded = strings.TrimPrefix(encoded, "base64://")
	if strings.HasPrefix(encoded, "data:image/") {
		_, encoded, _ = strings.Cut(encoded, ";base64,")
	}
	image, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !strings.HasPrefix(http.DetectContentType(image), "image/") {
		return nil
	}
	return image
}

func readInboundImage(ctx context.Context, ref string) []byte {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "base64://") || strings.HasPrefix(ref, "data:image/") {
		return decodeInboundImage(ref)
	}
	var source io.ReadCloser
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		downloadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		response, err := utils.OpenPublicHTTP(downloadCtx, ref)
		if err != nil {
			return nil
		}
		source = response.Body
	} else {
		info, err := os.Stat(ref)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(ref)
		if err != nil {
			return nil
		}
		source = file
	}
	defer source.Close()
	image, err := io.ReadAll(source)
	if err != nil || !strings.HasPrefix(http.DetectContentType(image), "image/") {
		return nil
	}
	return image
}
