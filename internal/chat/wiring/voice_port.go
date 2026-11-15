package chatwiring

// 语音转写端口
// 对齐 MaiBot src/common/utils/utils_voice.py 与 src/services/llm_service.py：
//   - VoicePort 只负责解析语音字节、选择 MIME、编码 base64 并调用 AudioTranscriber
//   - 可注入 VoiceResolver 替换默认的 data URI/base64/file/URL/本地路径解析
//   - 默认大小上限防止把超大文件读入内存；超限返回真实错误，不静默截断
//   - 空转写文本按失败返回，不伪造 "[语音: ...]" 内容

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"MaiGo/internal/chat/message_receive"
	"MaiGo/internal/llm_models"
)

const (
	// DefaultVoiceMaxBytes 默认语音大小上限 20 MiB
	DefaultVoiceMaxBytes int64 = 20 << 20
	// defaultVoiceMIMEType 对齐 MaiBot Gemini 音频默认 MIME
	defaultVoiceMIMEType = "audio/wav"
	// defaultVoiceHTTPTimeout 默认下载超时
	defaultVoiceHTTPTimeout = 30 * time.Second
)

var (
	// ErrVoiceTooLarge 语音数据超过上限
	ErrVoiceTooLarge = errors.New("语音数据超过大小限制")
	// ErrVoiceUnsupported 无法识别的语音内容
	ErrVoiceUnsupported = errors.New("无法识别的语音内容")
	// ErrVoiceHashUnavailable 只提供 hash 时缺少宿主解析器
	ErrVoiceHashUnavailable = errors.New("语音 hash 无法解析")
)

// VoiceMedia 解析后的语音字节与 MIME
type VoiceMedia struct {
	Bytes    []byte
	MIMEType string
	// Source 解析来源：data-uri/base64/file/url/path
	Source string
}

// VoiceResolver 把 VoiceRef 的 hash/content 解析成语音字节
// 默认实现见 VoiceContentResolver；宿主可以为消息存储注入自己的 hash 解析
type VoiceResolver interface {
	ResolveVoice(ctx context.Context, hash, content string) (VoiceMedia, error)
}

// VoiceSourceResolver 可选扩展：解析 VoiceRef.Source 中的平台媒体引用
// 实现该接口的宿主可处理 OneBot get_record 等平台内部文件标识
type VoiceSourceResolver interface {
	ResolveVoiceSource(ctx context.Context, source string) (VoiceMedia, error)
}

// NewVoiceMedia 用已加载字节构造语音媒体，并做 MIME 归一化
func NewVoiceMedia(data []byte, mimeHint, pathHint string) VoiceMedia {
	return VoiceMedia{
		Bytes:    append([]byte(nil), data...),
		MIMEType: detectVoiceMIMEType(mimeHint, pathHint, data),
		Source:   "bytes",
	}
}

// VoicePort 实现 messagereceive.VoiceTranscriber
type VoicePort struct {
	Transcriber llm.AudioTranscriber
	Resolver    VoiceResolver
	// MaxBytes 语音大小上限；<=0 使用 DefaultVoiceMaxBytes
	MaxBytes int64
}

// TranscribeVoice 解析语音并调用模型转写
func (p *VoicePort) TranscribeVoice(ctx context.Context, ref messagereceive.VoiceRef) (string, bool, error) {
	if p == nil || p.Transcriber == nil {
		return "", false, errors.New("语音转录后端未配置")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = NewVoiceContentResolver(p.MaxBytes)
	}
	var media VoiceMedia
	var err error
	switch {
	case len(ref.Binary) > 0:
		media = NewVoiceMedia(ref.Binary, "", "")
	case strings.TrimSpace(ref.Source) != "":
		source := strings.TrimSpace(ref.Source)
		if sourceResolver, ok := resolver.(VoiceSourceResolver); ok {
			media, err = sourceResolver.ResolveVoiceSource(ctx, source)
		} else {
			media, err = resolver.ResolveVoice(ctx, "", source)
		}
	default:
		media, err = resolver.ResolveVoice(ctx, ref.Hash, ref.Content)
	}
	if err != nil {
		return "", false, fmt.Errorf("解析语音内容失败: %w", err)
	}
	limit := voiceLimit(p.MaxBytes)
	if int64(len(media.Bytes)) > limit {
		return "", false, fmt.Errorf("%w: %d > %d", ErrVoiceTooLarge, len(media.Bytes), limit)
	}
	if len(media.Bytes) == 0 {
		return "", false, errors.New("语音内容为空")
	}
	result, err := p.Transcriber.Transcribe(ctx, llm.AudioTranscriptionRequest{
		AudioBase64: base64.StdEncoding.EncodeToString(media.Bytes),
		MIMEType:    strings.TrimSpace(media.MIMEType),
	})
	if err != nil {
		return "", false, fmt.Errorf("语音转录失败: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		// Python get_voice_text 对空文本返回 None；这里保持“未得到文本”而不是伪造内容
		return "", false, nil
	}
	return text, true, nil
}

// VoiceContentResolver 默认语音解析器
type VoiceContentResolver struct {
	MaxBytes   int64
	HTTPClient *http.Client
}

// NewVoiceContentResolver 创建默认解析器；maxBytes<=0 使用 DefaultVoiceMaxBytes
func NewVoiceContentResolver(maxBytes int64) *VoiceContentResolver {
	return &VoiceContentResolver{MaxBytes: maxBytes}
}

// ResolveVoiceSource 把 source 当作媒体引用解析；默认实现直接复用 ResolveVoice
func (r *VoiceContentResolver) ResolveVoiceSource(ctx context.Context, source string) (VoiceMedia, error) {
	return r.ResolveVoice(ctx, "", source)
}

// ResolveVoice 按 data URI、base64、file://、http(s) URL、本地路径依次解析
func (r *VoiceContentResolver) ResolveVoice(ctx context.Context, hash, content string) (VoiceMedia, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	content = strings.TrimSpace(content)
	if content == "" {
		if strings.TrimSpace(hash) != "" {
			return VoiceMedia{}, fmt.Errorf("%w: hash=%s（未注入 VoiceResolver）", ErrVoiceHashUnavailable, strings.TrimSpace(hash))
		}
		return VoiceMedia{}, errors.New("语音缺少可读取内容")
	}
	lower := strings.ToLower(content)
	switch {
	case strings.HasPrefix(lower, "data:"):
		return r.resolveDataURI(content)
	case strings.HasPrefix(lower, "base64://"):
		payload := content[len("base64://"):]
		data, err := r.decodeBase64(payload)
		if err != nil {
			return VoiceMedia{}, err
		}
		return VoiceMedia{Bytes: data, MIMEType: detectVoiceMIMEType("", "", data), Source: "base64"}, nil
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return r.resolveHTTP(ctx, content)
	case strings.HasPrefix(lower, "file://"):
		path, err := voiceFilePath(content)
		if err != nil {
			return VoiceMedia{}, err
		}
		data, err := r.readFile(path)
		if err != nil {
			return VoiceMedia{}, err
		}
		return VoiceMedia{Bytes: data, MIMEType: detectVoiceMIMEType("", path, data), Source: "file"}, nil
	}
	if isVoiceLocalPath(content) {
		data, err := r.readFile(content)
		if err != nil {
			return VoiceMedia{}, err
		}
		return VoiceMedia{Bytes: data, MIMEType: detectVoiceMIMEType("", content, data), Source: "path"}, nil
	}
	data, err := r.decodeBase64(content)
	if err != nil {
		if errors.Is(err, ErrVoiceTooLarge) {
			return VoiceMedia{}, err
		}
		return VoiceMedia{}, fmt.Errorf("%w: 内容既不是 data URI/URL/文件路径，也不是合法 base64: %v", ErrVoiceUnsupported, err)
	}
	return VoiceMedia{Bytes: data, MIMEType: detectVoiceMIMEType("", "", data), Source: "base64"}, nil
}

func (r *VoiceContentResolver) resolveDataURI(content string) (VoiceMedia, error) {
	comma := strings.IndexByte(content, ',')
	if comma < 0 {
		return VoiceMedia{}, fmt.Errorf("%w: data URI 缺少逗号分隔", ErrVoiceUnsupported)
	}
	header := content[len("data:"):comma]
	if !strings.Contains(strings.ToLower(header), ";base64") {
		return VoiceMedia{}, fmt.Errorf("%w: 仅支持 base64 data URI", ErrVoiceUnsupported)
	}
	mimeType := strings.TrimSpace(strings.SplitN(header, ";", 2)[0])
	payload := content[comma+1:]
	data, err := r.decodeBase64(payload)
	if err != nil {
		return VoiceMedia{}, err
	}
	mimeType = detectVoiceMIMEType(mimeType, "", data)
	return VoiceMedia{Bytes: data, MIMEType: mimeType, Source: "data-uri"}, nil
}

func (r *VoiceContentResolver) resolveHTTP(ctx context.Context, rawURL string) (VoiceMedia, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return VoiceMedia{}, fmt.Errorf("创建语音下载请求失败: %w", err)
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultVoiceHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return VoiceMedia{}, fmt.Errorf("下载语音失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VoiceMedia{}, fmt.Errorf("下载语音失败: HTTP %d", resp.StatusCode)
	}
	limit := voiceLimit(r.MaxBytes)
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return VoiceMedia{}, fmt.Errorf("读取语音响应失败: %w", err)
	}
	if int64(len(data)) > limit {
		return VoiceMedia{}, fmt.Errorf("%w: 响应超过 %d 字节", ErrVoiceTooLarge, limit)
	}
	pathHint := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		pathHint = parsed.Path
	}
	mimeType := detectVoiceMIMEType(resp.Header.Get("Content-Type"), pathHint, data)
	return VoiceMedia{Bytes: data, MIMEType: mimeType, Source: "url"}, nil
}

func (r *VoiceContentResolver) readFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("读取语音文件失败: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("语音路径是目录: %s", path)
	}
	limit := voiceLimit(r.MaxBytes)
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: 文件 %d > %d", ErrVoiceTooLarge, info.Size(), limit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取语音文件失败: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: 文件 %d > %d", ErrVoiceTooLarge, len(data), limit)
	}
	return data, nil
}

func (r *VoiceContentResolver) decodeBase64(value string) ([]byte, error) {
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	if compact == "" {
		return nil, errors.New("语音 base64 为空")
	}
	limit := voiceLimit(r.MaxBytes)
	if int64(len(compact)) > limit*4/3+8 {
		return nil, fmt.Errorf("%w: base64 长度 %d 超过上限 %d", ErrVoiceTooLarge, len(compact), limit)
	}
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	var lastErr error
	for _, encoding := range encodings {
		data, err := encoding.DecodeString(compact)
		if err != nil {
			lastErr = err
			continue
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("%w: %d > %d", ErrVoiceTooLarge, len(data), limit)
		}
		return data, nil
	}
	if lastErr == nil {
		lastErr = errors.New("未知 base64 错误")
	}
	return nil, fmt.Errorf("%w: %v", ErrVoiceUnsupported, lastErr)
}

func voiceLimit(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return DefaultVoiceMaxBytes
	}
	return maxBytes
}

// isVoiceLocalPath 仅把短且存在的路径当作本地文件，避免把 base64 误判成路径
func isVoiceLocalPath(value string) bool {
	if len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	info, err := os.Stat(value)
	return err == nil && info.Mode().IsRegular()
}

func voiceFilePath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("解析 file URL 失败: %w", err)
	}
	host := parsed.Host
	pathPart := parsed.Path
	// Windows 下 file://C:/x 会解析成 Host=C:、Path=/x
	if len(host) == 2 && isASCIIAlpha(host[0]) && host[1] == ':' {
		pathPart = "/" + host + pathPart
		host = ""
	}
	if host != "" && !strings.EqualFold(host, "localhost") {
		return "", fmt.Errorf("%w: 不支持远程 file URL 主机 %q", ErrVoiceUnsupported, host)
	}
	path, err := url.PathUnescape(pathPart)
	if err != nil {
		return "", fmt.Errorf("解析 file URL 路径失败: %w", err)
	}
	// Windows 下 file:///C:/x 会解析出 /C:/x
	if len(path) >= 3 && path[0] == '/' && isASCIIAlpha(path[1]) && path[2] == ':' {
		path = path[1:]
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: file URL 缺少路径", ErrVoiceUnsupported)
	}
	return filepath.FromSlash(path), nil
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func detectVoiceMIMEType(explicit, pathHint string, data []byte) string {
	explicit = normalizeVoiceMIMEType(explicit)
	if explicit != "" {
		return explicit
	}
	if mimeType := voiceMIMETypeFromPath(pathHint); mimeType != "" {
		return mimeType
	}
	if mimeType := sniffVoiceMIMEType(data); mimeType != "" {
		return mimeType
	}
	return defaultVoiceMIMEType
}

func normalizeVoiceMIMEType(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	switch value {
	case "", "application/octet-stream", "binary/octet-stream":
		return ""
	}
	if strings.HasPrefix(value, "audio/") || value == "application/ogg" {
		return value
	}
	return ""
}

func voiceMIMETypeFromPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".flac":
		return "audio/flac"
	case ".amr":
		return "audio/amr"
	case ".silk":
		return "audio/silk"
	case ".webm":
		return "audio/webm"
	}
	if mimeType := mime.TypeByExtension(ext); strings.HasPrefix(mimeType, "audio/") {
		return mimeType
	}
	return ""
}

func sniffVoiceMIMEType(data []byte) string {
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		return "audio/wav"
	}
	if len(data) >= 4 && string(data[0:4]) == "OggS" {
		return "audio/ogg"
	}
	if len(data) >= 4 && string(data[0:4]) == "fLaC" {
		return "audio/flac"
	}
	if len(data) >= 5 && string(data[0:5]) == "#!AMR" {
		return "audio/amr"
	}
	if len(data) >= 5 && string(data[0:5]) == "#!SIL" {
		return "audio/silk"
	}
	if len(data) >= 3 && (string(data[0:3]) == "ID3" || (data[0] == 0xff && data[1]&0xe0 == 0xe0)) {
		return "audio/mpeg"
	}
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		return "audio/mp4"
	}
	if len(data) >= 4 && data[0] == 0x1a && data[1] == 0x45 && data[2] == 0xdf && data[3] == 0xa3 {
		return "audio/webm"
	}
	return ""
}
