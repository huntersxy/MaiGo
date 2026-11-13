package desktoppet

// 桌宠图文请求文件，对齐 src/desktop_pet/attachments.py：大附件不经过按行读取的进程管道

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

const (
	// desktopPetImageBytes 单张图片解码后的体积上限
	desktopPetImageBytes = 8 * 1024 * 1024
	// desktopPetRequestBytes 图文请求文件的体积上限
	desktopPetRequestBytes = 48 * 1024 * 1024
	// desktopPetRequestText 正文的字符上限
	desktopPetRequestText = 10000
	// desktopPetRequestImages 图片数量上限
	desktopPetRequestImages = 4
	// desktopPetRequestPixels 单张图片的像素上限
	desktopPetRequestPixels = 25_000_000
)

var desktopPetRequestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ChatRequestImage 桌宠图文请求里的图片，Base64 为消息原文
type ChatRequestImage struct {
	Base64   string `json:"base64"`
	MimeType string `json:"mime_type"`
}

// ChatRequest 桌宠图文请求
type ChatRequest struct {
	Text   string             `json:"text"`
	Images []ChatRequestImage `json:"images"`
}

// ReadChatRequest 读取并校验图文请求，读完即删除临时文件
func ReadChatRequest(directory, requestID string) (ChatRequest, error) {
	if !desktopPetRequestIDPattern.MatchString(requestID) {
		return ChatRequest{}, fmt.Errorf("无效的桌宠请求 ID")
	}
	path := filepath.Join(directory, requestID+".json")
	defer func() { _ = os.Remove(path) }()
	stat, err := os.Stat(path)
	if err != nil {
		return ChatRequest{}, err
	}
	if stat.Size() > desktopPetRequestBytes {
		return ChatRequest{}, fmt.Errorf("图文消息过大")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ChatRequest{}, err
	}
	var request ChatRequest
	if err := sonic.Unmarshal(raw, &request); err != nil {
		return ChatRequest{}, fmt.Errorf("图文消息格式无效")
	}
	text := strings.TrimSpace(request.Text)
	if utf8.RuneCountInString(text) > desktopPetRequestText || len(request.Images) > desktopPetRequestImages || (text == "" && len(request.Images) == 0) {
		return ChatRequest{}, fmt.Errorf("最多 10000 字、4 张图片，消息不能为空")
	}
	for i := range request.Images {
		payload, decodeErr := strictBase64Decode(request.Images[i].Base64)
		if decodeErr != nil {
			return ChatRequest{}, fmt.Errorf("图片 base64 无效")
		}
		if len(payload) > desktopPetImageBytes {
			return ChatRequest{}, fmt.Errorf("每张图片不能超过 8 MiB")
		}
		info, infoErr := parseImageInfo(payload)
		if infoErr != nil {
			return ChatRequest{}, fmt.Errorf("无法识别的图片格式")
		}
		if info.Width*info.Height > desktopPetRequestPixels {
			return ChatRequest{}, fmt.Errorf("图片不能超过 2500 万像素")
		}
		request.Images[i].MimeType = info.MIME
	}
	return ChatRequest{Text: text, Images: request.Images}, nil
}

// strictBase64Decode 严格解码，拒绝 Python validate=True 会报错的换行与非法字符
func strictBase64Decode(encoded string) ([]byte, error) {
	if strings.ContainsAny(encoded, "\r\n") {
		return nil, fmt.Errorf("base64 含换行")
	}
	return base64.StdEncoding.Strict().DecodeString(encoded)
}
