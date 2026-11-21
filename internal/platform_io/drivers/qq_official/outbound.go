package qq_official

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"strings"

	"MaiGo/internal/platform_io"
)

const maxFileSize = 100 << 20

type outboundPart struct {
	body     map[string]any
	fileType int
	binary   []byte
	name     string
	fileRef  string
	size     int
}

func outboundSceneTarget(outbound platformio.OutboundMessageEnvelope) (string, string) {
	info := outbound.MessageInfo
	config := info.AdditionalConfig
	group := ""
	if info.GroupInfo != nil {
		group = info.GroupInfo.GroupID
	}
	group = firstNonEmpty(group, asString(config["platform_io_target_group_id"]))
	target, scene := firstNonEmpty(group, asString(config["platform_io_target_user_id"]), outbound.TargetID), "private"
	if group != "" || outbound.ChatType == platformio.ChatTypeGroup {
		scene = "group"
	}
	return scene, target
}

func encodeOutbound(ctx context.Context, outbound platformio.OutboundMessageEnvelope, resolveUser func(string) (string, error)) ([]outboundPart, string, error) {
	parts := make([]outboundPart, 0)
	var text strings.Builder
	markdown := false
	quote := ""
	flush := func() {
		if body := strings.TrimSpace(text.String()); body != "" {
			fields := map[string]any{"msg_type": 0, "content": body}
			if markdown {
				fields = map[string]any{"msg_type": 2, "markdown": map[string]any{"content": body}}
			}
			parts = append(parts, outboundPart{body: fields})
		}
		text.Reset()
		markdown = false
	}
	for _, seg := range outbound.Segments {
		data, _ := asMap(seg.Data)
		if seg.Kind == platformio.SegmentDict && data["type"] == "video" {
			seg.Kind = platformio.SegmentVideo
			data, _ = asMap(data["data"])
		}
		switch seg.Kind {
		case platformio.SegmentText:
			text.WriteString(seg.Text)
		case platformio.SegmentAt:
			id := firstNonEmpty(seg.AtUserID, asString(data["target_user_id"]), seg.Extra["target_user_id"])
			name := firstNonEmpty(asString(data["target_user_cardname"]), seg.Extra["target_user_cardname"], seg.Extra["cardname"], seg.Extra["card"], asString(data["target_user_nickname"]), seg.Extra["target_user_nickname"], seg.Extra["nickname"], seg.Extra["name"], seg.Text)
			openid, err := resolveUser(id)
			if err == nil && openid != "" {
				text.WriteString(fmt.Sprintf(`<qqbot-at-user id="%s" /> `, html.EscapeString(openid)))
				markdown = true
			} else if name != "" {
				text.WriteString("@" + name + " ")
			}
		case platformio.SegmentReply:
			raw, _ := seg.Data.(string)
			quote = firstNonEmpty(asString(data["target_message_id"]), asString(data["message_id"]), seg.Extra["target_message_id"], seg.Extra["id"], seg.Extra["message_id"], seg.Text, raw)
		case platformio.SegmentImage, platformio.SegmentEmoji, platformio.SegmentVoice, platformio.SegmentRecord, platformio.SegmentFile, platformio.SegmentVideo:
			part, err := encodeMedia(ctx, seg, data)
			if err != nil {
				return nil, "", err
			}
			flush()
			parts = append(parts, part)
		default:
			return nil, "", fmt.Errorf("出站暂不支持 %s 消息段", seg.Kind)
		}
	}
	flush()
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("出站消息为空")
	}
	return parts, quote, nil
}

func encodeMedia(ctx context.Context, seg platformio.Segment, data map[string]any) (outboundPart, error) {
	part := outboundPart{}
	limit := maxFileSize
	switch seg.Kind {
	case platformio.SegmentFile, platformio.SegmentVideo:
		part.fileType = 4
		if seg.Kind == platformio.SegmentVideo {
			part.fileType = 2
		}
		part.name = firstNonEmpty(asString(data["name"]), seg.Extra["name"], seg.Extra["file_name"], seg.Extra["filename"], seg.Text)
		if part.name == "" || part.name == "." || part.name == ".." || strings.ContainsAny(part.name, "/\\") || strings.IndexFunc(part.name, func(r rune) bool { return r < 32 }) >= 0 {
			return part, fmt.Errorf("出站文件名不能包含路径或控制字符")
		}
		if seg.Kind == platformio.SegmentFile {
			part.fileRef = firstNonEmpty(asString(data["file_ref"]), seg.Extra["file_ref"])
			if part.fileRef != "" {
				part.size, _ = asInt(firstNonEmpty(asString(data["size"]), seg.Extra["size"]))
				if part.size <= 0 || part.size > limit {
					return part, fmt.Errorf("出站文件大小必须在 1 字节至 100 MiB 之间")
				}
				return part, nil
			}
		}
	case platformio.SegmentVoice, platformio.SegmentRecord:
		part.fileType, limit = 3, 20<<20
	default:
		part.fileType = 1
	}
	b64 := firstNonEmpty(seg.Extra["binary_data_base64"], seg.Extra["base64"], asString(data["base64"]))
	if b64 == "" && part.fileType != 2 && part.fileType != 4 {
		value, err := platformio.SegmentMediaBase64(ctx, seg)
		if err != nil {
			return part, err
		}
		b64 = value
	}
	if b64 == "" || len(b64) > base64.StdEncoding.EncodedLen(limit) || strings.ContainsAny(b64, "\r\n") {
		return part, fmt.Errorf("QQ 出站媒体缺少有效 Base64 或超过大小限制")
	}
	binary, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(binary) == 0 || len(binary) > limit {
		return part, fmt.Errorf("QQ 出站媒体缺少有效 Base64 或超过大小限制")
	}
	part.binary = binary
	switch part.fileType {
	case 1:
		extension := imageExtension(binary)
		if extension == "" {
			return part, fmt.Errorf("QQ 出站图片格式无法识别")
		}
		hash := firstNonEmpty(seg.Extra["hash"], seg.Extra["binary_hash"])
		if hash == "" {
			hash = fmt.Sprintf("%x", sha256.Sum256(binary))
		}
		part.name = hash + "." + extension
	case 2:
		if len(binary) < 8 || string(binary[4:8]) != "ftyp" {
			return part, fmt.Errorf("出站视频需为 MP4 格式")
		}
	case 3:
		extension := voiceExtension(binary)
		if extension == "" {
			return part, fmt.Errorf("出站语音格式不支持，请提供 SILK、MP3、WAV 或 OGG 音频")
		}
		part.name = fmt.Sprintf("%x.%s", sha256.Sum256(binary), extension)
	}
	return part, nil
}

func imageExtension(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "jpg"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(data, []byte("BM")):
		return "bmp"
	}
	return ""
}

func voiceExtension(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x02#!SILK_V3")) || bytes.HasPrefix(data, []byte("#!SILK_V3")):
		return "silk"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "wav"
	case bytes.HasPrefix(data, []byte("OggS")):
		return "ogg"
	case bytes.HasPrefix(data, []byte("ID3")) || len(data) >= 4 &&
		data[0] == 0xff && data[1]&0xe0 == 0xe0 && data[1]&0x18 != 0x08 && data[1]&0x06 == 0x02 &&
		data[2]>>4 != 0 && data[2]>>4 != 15 && data[2]&0x0c != 0x0c:
		return "mp3"
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
