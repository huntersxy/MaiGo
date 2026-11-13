package platformio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// AvatarStatus 头像可用状态，对齐 MaiBot AvatarStatus
type AvatarStatus string

const (
	AvatarStatusAvailable   AvatarStatus = "available"
	AvatarStatusUnsupported AvatarStatus = "unsupported"
	AvatarStatusMissing     AvatarStatus = "missing"
)

const (
	maxAvatarBytes    = 5 * 1024 * 1024
	avatarCacheTTL    = 24 * time.Hour
	avatarNegativeTTL = 5 * time.Minute
	avatarHTTPTimeout = 10 * time.Second
)

// AvatarResult 是一个目标的头像解析结果；Path 仅供宿主读取，不进入消息载荷
type AvatarResult struct {
	Status    AvatarStatus `json:"status"`
	URL       string       `json:"url,omitempty"`
	ExpiresAt float64      `json:"expires_at"`
	Path      string       `json:"-"`
}

// ToMap 对齐 Python AvatarResult.to_dict，不暴露本地缓存路径
func (r AvatarResult) ToMap() map[string]any {
	result := map[string]any{"status": string(r.Status), "expires_at": r.ExpiresAt}
	if r.URL != "" {
		result["url"] = r.URL
	}
	return result
}

// ValidateAvatarTarget 校验平台、目标 ID 与目标类型
func ValidateAvatarTarget(platform, targetID, targetType string) (string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	targetID = strings.TrimSpace(targetID)
	if platform == "" || targetID == "" || len(platform) > 128 || len(targetID) > 512 {
		return "", "", errors.New("头像平台和目标 ID 必须非空，且不能超过长度限制")
	}
	if targetType != "user" && targetType != "group" {
		return "", "", errors.New("头像 target_type 必须为 user 或 group")
	}
	return platform, targetID, nil
}

// AvatarProvider 由插件适配器提供头像来源；返回结构对齐 avatar_provider 的 result
type AvatarProvider interface {
	QueryAvatar(ctx context.Context, platform, targetID, targetType, accountID, scope string) (map[string]any, error)
}

type avatarCacheMetadata struct {
	Status    string  `json:"status"`
	URL       string  `json:"url"`
	ExpiresAt float64 `json:"expires_at"`
	Suffix    *string `json:"suffix"`
}

type avatarCall struct {
	done   chan struct{}
	result AvatarResult
	err    error
}

// AvatarService 合并同一路由目标的并发请求并维护磁盘头像缓存
type AvatarService struct {
	CacheRoot  string
	HTTPClient *http.Client
	Provider   AvatarProvider

	mu      sync.Mutex
	pending map[string]*avatarCall
}

// NewAvatarService 使用实例根目录创建头像服务，缓存落在 <instance>/data/avatar
func NewAvatarService(instanceRoot string) *AvatarService {
	root := strings.TrimSpace(instanceRoot)
	if root == "" {
		root = "data"
	}
	return &AvatarService{
		CacheRoot: filepath.Join(root, "data", "avatar"),
		HTTPClient: &http.Client{
			Timeout: avatarHTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		pending: map[string]*avatarCall{},
	}
}

func avatarCacheKey(platform, accountID, scope, targetType, targetID string) string {
	encoded := "[" + strconv.Quote(platform) + ", " + strconv.Quote(accountID) + ", " +
		strconv.Quote(scope) + ", " + strconv.Quote(targetType) + ", " + strconv.Quote(targetID) + "]"
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}

// GetAvatar 返回头像；forceRefresh 仍复用同一目标的进行中请求
func (s *AvatarService) GetAvatar(ctx context.Context, platform, targetID, targetType, accountID, scope string, forceRefresh bool) (AvatarResult, error) {
	if s == nil {
		return AvatarResult{}, errors.New("头像服务未初始化")
	}
	normalizedPlatform, normalizedTarget, err := ValidateAvatarTarget(platform, targetID, targetType)
	if err != nil {
		return AvatarResult{}, err
	}
	accountID = strings.TrimSpace(accountID)
	scope = strings.TrimSpace(scope)
	key := avatarCacheKey(normalizedPlatform, accountID, scope, targetType, normalizedTarget)

	if !forceRefresh {
		cached, err := s.readCache(key)
		if err != nil {
			return AvatarResult{}, err
		}
		if cached != nil {
			return *cached, nil
		}
	}

	s.mu.Lock()
	if s.pending == nil {
		s.pending = map[string]*avatarCall{}
	}
	call := s.pending[key]
	if call == nil {
		call = &avatarCall{done: make(chan struct{})}
		s.pending[key] = call
		go func() {
			result, resolveErr := s.resolve(key, normalizedPlatform, normalizedTarget, targetType, accountID, scope)
			call.result, call.err = result, resolveErr
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
			close(call.done)
		}()
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return AvatarResult{}, ctx.Err()
	case <-call.done:
		return call.result, call.err
	}
}

func (s *AvatarService) cacheDir() string { return filepath.Join(s.CacheRoot, "remote") }

func (s *AvatarService) readCache(key string) (*AvatarResult, error) {
	metadataPath := filepath.Join(s.cacheDir(), key+".json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取头像缓存失败: %w", err)
	}
	var data avatarCacheMetadata
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("读取头像缓存失败: %w", err)
	}
	if data.ExpiresAt <= avatarNow() {
		return nil, nil
	}
	status := AvatarStatus(data.Status)
	if status != AvatarStatusAvailable && status != AvatarStatusUnsupported && status != AvatarStatusMissing {
		return nil, errors.New("头像缓存状态不合法")
	}
	result := AvatarResult{Status: status, URL: data.URL, ExpiresAt: data.ExpiresAt}
	if status == AvatarStatusAvailable {
		if data.Suffix == nil || !validAvatarSuffix(*data.Suffix) {
			return nil, errors.New("头像缓存图片格式不合法")
		}
		result.Path = filepath.Join(s.cacheDir(), key+*data.Suffix)
		if _, err := os.Stat(result.Path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("读取头像缓存失败: %w", err)
		}
	}
	return &result, nil
}

func validAvatarSuffix(suffix string) bool {
	switch suffix {
	case ".jpg", ".png", ".webp", ".gif", ".bmp":
		return true
	default:
		return false
	}
}

func (s *AvatarService) writeCache(key string, result AvatarResult, imageBytes []byte) error {
	dir := s.cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建头像缓存目录失败: %w", err)
	}
	if result.Path != "" && len(imageBytes) > 0 {
		temporary, err := os.CreateTemp(dir, "."+key+".*.tmp")
		if err != nil {
			return fmt.Errorf("写入头像缓存失败: %w", err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err := temporary.Write(imageBytes); err != nil {
			temporary.Close()
			return fmt.Errorf("写入头像缓存失败: %w", err)
		}
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("写入头像缓存失败: %w", err)
		}
		if err := os.Rename(temporaryPath, result.Path); err != nil {
			return fmt.Errorf("写入头像缓存失败: %w", err)
		}
	}
	var suffix *string
	if result.Path != "" {
		value := filepath.Ext(result.Path)
		suffix = &value
	}
	payload, err := json.Marshal(avatarCacheMetadata{
		Status: string(result.Status), URL: result.URL, ExpiresAt: result.ExpiresAt, Suffix: suffix,
	})
	if err != nil {
		return fmt.Errorf("编码头像缓存失败: %w", err)
	}
	temporary, err := os.CreateTemp(dir, "."+key+".*.tmp")
	if err != nil {
		return fmt.Errorf("写入头像缓存失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("写入头像缓存失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("写入头像缓存失败: %w", err)
	}
	metadataPath := filepath.Join(dir, key+".json")
	if err := os.Rename(temporaryPath, metadataPath); err != nil {
		return fmt.Errorf("写入头像缓存失败: %w", err)
	}
	for _, oldSuffix := range []string{".jpg", ".png", ".webp", ".gif", ".bmp"} {
		oldPath := filepath.Join(dir, key+oldSuffix)
		if oldPath != result.Path {
			_ = os.Remove(oldPath)
		}
	}
	return nil
}

func (s *AvatarService) resolve(key, platform, targetID, targetType, accountID, scope string) (AvatarResult, error) {
	if s.Provider == nil {
		return s.finishNegative(key, AvatarStatusUnsupported, "")
	}
	source, err := s.Provider.QueryAvatar(context.Background(), platform, targetID, targetType, accountID, scope)
	if err != nil {
		return AvatarResult{}, fmt.Errorf("适配器头像查询失败: %w", err)
	}
	status := AvatarStatus(strings.TrimSpace(fmt.Sprint(source["status"])))
	if status != AvatarStatusAvailable && status != AvatarStatusUnsupported && status != AvatarStatusMissing {
		return AvatarResult{}, errors.New("适配器必须返回 available、unsupported 或 missing 头像状态")
	}
	if status != AvatarStatusAvailable {
		return s.finishNegative(key, status, "")
	}
	urlText, ok := source["url"].(string)
	if !ok || strings.TrimSpace(urlText) == "" {
		return AvatarResult{}, errors.New("可用头像必须提供 URL")
	}
	ttl := avatarCacheTTL
	if raw, exists := source["expires_in"]; exists && raw != nil {
		value, valid := avatarExpiresIn(raw)
		if !valid {
			return AvatarResult{}, errors.New("头像 expires_in 必须大于 0")
		}
		if value < ttl {
			ttl = value
		}
	}
	imageBytes, err := s.download(urlText)
	if err != nil {
		return AvatarResult{}, err
	}
	suffix, err := avatarImageSuffix(imageBytes)
	if err != nil {
		return AvatarResult{}, err
	}
	result := AvatarResult{Status: status, URL: strings.TrimSpace(urlText), ExpiresAt: avatarNow() + ttl.Seconds(), Path: filepath.Join(s.cacheDir(), key+suffix)}
	if err := s.writeCache(key, result, imageBytes); err != nil {
		return AvatarResult{}, err
	}
	return result, nil
}

func (s *AvatarService) finishNegative(key string, status AvatarStatus, urlText string) (AvatarResult, error) {
	result := AvatarResult{Status: status, URL: urlText, ExpiresAt: avatarNow() + avatarNegativeTTL.Seconds()}
	if err := s.writeCache(key, result, nil); err != nil {
		return AvatarResult{}, err
	}
	return result, nil
}

func avatarNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func avatarExpiresIn(raw any) (time.Duration, bool) {
	var value float64
	switch typed := raw.(type) {
	case float64:
		value = typed
	case float32:
		value = float64(typed)
	case int:
		value = float64(typed)
	case int64:
		value = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		value = parsed
	default:
		return 0, false
	}
	if value <= 0 {
		return 0, false
	}
	if value >= avatarCacheTTL.Seconds() {
		return avatarCacheTTL, true
	}
	return time.Duration(value * float64(time.Second)), true
}

func (s *AvatarService) download(rawURL string) ([]byte, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("适配器头像 URL 必须为不含凭据的 HTTP(S) 地址")
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("请求头像失败: %w", err)
	}
	request.Header.Set("User-Agent", "MaiBot-Avatar/1.0")
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: avatarHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("下载头像失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("下载头像失败: HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxAvatarBytes+1)
	imageBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("下载头像失败: %w", err)
	}
	if len(imageBytes) > maxAvatarBytes {
		return nil, errors.New("头像图片不能超过 5 MB")
	}
	if len(imageBytes) == 0 {
		return nil, errors.New("头像图片为空")
	}
	return imageBytes, nil
}

func avatarImageSuffix(imageBytes []byte) (string, error) {
	_, format, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return "", errors.New("头像仅支持 JPG、PNG、WebP、GIF 或 BMP")
	}
	switch strings.ToLower(format) {
	case "jpeg":
		return ".jpg", nil
	case "png":
		return ".png", nil
	case "webp":
		return ".webp", nil
	case "gif":
		return ".gif", nil
	case "bmp":
		return ".bmp", nil
	default:
		return "", errors.New("头像仅支持 JPG、PNG、WebP、GIF 或 BMP")
	}
}
