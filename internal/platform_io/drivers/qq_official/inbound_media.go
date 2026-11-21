package qq_official

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxInboundAttachments = 10
const maxInboundMediaBytes = 20 << 20

func (c *Client) inboundAttachments(ctx context.Context, payload map[string]any) (segments []map[string]any, labels []string, picture, emoji bool) {
	attachments, _ := payload["attachments"].([]any)
	attachments = attachments[:min(len(attachments), maxInboundAttachments)]
	hint := strings.TrimSpace(asString(payload["content"]))
	inline := len(attachments) == 1 && qqFaceTagPattern.MatchString(hint)
	appendAttachment := func(attachment map[string]any, inlineEmoji bool) {
		mime := strings.ToLower(firstNonEmpty(asString(attachment["content_type"]), asString(attachment["type"])))
		kind := strings.ToLower(strings.TrimSpace(asString(attachment["type"])))
		filename := strings.TrimSpace(asString(attachment["filename"]))
		address := firstNonEmpty(asString(attachment["voice_wav_url"]), asString(attachment["url"]))
		if strings.HasPrefix(address, "//") {
			address = "https:" + address
		}
		if isImageAttachment(mime, kind) {
			picture = true
			label, segmentType := "[图片]", "image"
			if isEmojiAttachment(kind) || inlineEmoji || asBool(attachment["is_emoji"]) {
				emoji = true
				label, segmentType = "[表情]", "emoji"
			}
			binary := c.downloadInboundMedia(ctx, address)
			if len(binary) == 0 {
				segments = append(segments, map[string]any{"type": "text", "data": label})
			} else {
				segments = append(segments, binaryAttachment(segmentType, binary))
			}
			labels = append(labels, label)
			return
		}
		file := map[string]any{"type": "file", "data": map[string]any{
			"name": filename, "size": strings.TrimSpace(asString(attachment["size"])),
			"url": address, "file_id": strings.TrimSpace(asString(attachment["file_uuid"])), "mime_type": mime,
		}}
		if mime == "voice" || strings.HasPrefix(mime, "audio/") || kind == "voice" {
			transcript, _ := attachment["asr_refer_text"].(string)
			if transcript = strings.TrimSpace(transcript); transcript != "" {
				label := "[语音: " + transcript + "]"
				segments = append(segments, map[string]any{"type": "text", "data": label})
				labels = append(labels, label)
				return
			}
			if binary := c.downloadInboundMedia(ctx, address); len(binary) > 0 {
				segments = append(segments, binaryAttachment("voice", binary))
			} else {
				segments = append(segments, file)
			}
			labels = append(labels, "[语音]")
		} else if address != "" || filename != "" {
			segments = append(segments, file)
			label := "[文件：" + firstNonEmpty(filename, "未命名") + "]"
			if strings.HasPrefix(mime, "video/") {
				label = "[视频]"
			}
			labels = append(labels, label)
		}
	}
	for _, raw := range attachments {
		if attachment, ok := asMap(raw); ok {
			appendAttachment(attachment, inline || hint == "[表情]" || hint == "[动画表情]")
		}
	}
	if quoted := quotedImages(payload, maxInboundAttachments-len(attachments)); len(quoted) > 0 {
		labelCount := len(labels)
		segments = append(segments, map[string]any{"type": "text", "data": "[以下图片来自引用消息]"})
		for _, attachment := range quoted {
			appendAttachment(attachment, false)
		}
		segments = append(segments, map[string]any{"type": "text", "data": "[引用图片结束]"})
		labels = append(labels[:labelCount], "[引用消息中的图片]")
	}
	return
}

func binaryAttachment(kind string, binary []byte) map[string]any {
	return map[string]any{"type": kind, "data": "", "binary_data_base64": base64.StdEncoding.EncodeToString(binary), "hash": fmt.Sprintf("%x", sha256.Sum256(binary))}
}

func isEmojiAttachment(kind string) bool {
	return kind == "emoji" || kind == "face" || kind == "sticker"
}

func isImageAttachment(mime, kind string) bool {
	return strings.HasPrefix(mime, "image/") || kind == "image" || isEmojiAttachment(kind)
}

func quotedImages(payload map[string]any, limit int) []map[string]any {
	var images []map[string]any
	remaining := 64
	seen := make(map[string]bool)
	var visit func(any, int, bool)
	visit = func(raw any, depth int, quoted bool) {
		elements, _ := raw.([]any)
		if depth > 4 {
			return
		}
		for _, entry := range elements {
			if remaining <= 0 || len(images) >= limit {
				return
			}
			remaining--
			element, ok := asMap(entry)
			if !ok {
				continue
			}
			value, exists := element["message_type"]
			if !exists {
				value = element["type"]
			}
			kind := asString(value)
			if kind == "102" {
				continue
			}
			isQuote := quoted || kind == "103"
			if isQuote {
				attachments, _ := element["attachments"].([]any)
				for _, rawAttachment := range attachments {
					if remaining <= 0 || len(images) >= limit {
						return
					}
					remaining--
					attachment, _ := asMap(rawAttachment)
					address := strings.TrimSpace(asString(attachment["url"]))
					if address != "" && !seen[address] && isImageAttachment(strings.ToLower(asString(attachment["content_type"])), strings.ToLower(asString(attachment["type"]))) {
						seen[address] = true
						images = append(images, attachment)
					}
				}
			}
			visit(element["msg_elements"], depth+1, isQuote)
		}
	}
	if limit > 0 {
		visit(payload["msg_elements"], 0, asString(payload["message_type"]) == "103")
	}
	return images
}

func (c *Client) downloadInboundMedia(ctx context.Context, address string) []byte {
	if len(address) > 4096 || strings.IndexFunc(address, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return nil
	}
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "https" || target.User != nil || (target.Port() != "" && target.Port() != "443") {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	trusted := host == "grouppro.grouppro.qq.com"
	for _, suffix := range []string{"nt.qq.com", "nt.qq.com.cn", "ugcimg.cn"} {
		trusted = trusted || host == suffix || strings.HasSuffix(host, "."+suffix)
	}
	if !trusted {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 || res.ContentLength > maxInboundMediaBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxInboundMediaBytes+1))
	if err != nil || len(data) > maxInboundMediaBytes {
		return nil
	}
	return data
}
