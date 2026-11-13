package capabilities

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

const (
	// MaxUploadBytes WebUI 上传单文件上限
	MaxUploadBytes = 20 * 1024 * 1024
	// MaxUploadPixels 上传图片像素上限
	MaxUploadPixels = 40_000_000
	// MaxPendingUploadsPerPlugin 单插件未领取上传上限
	MaxPendingUploadsPerPlugin = 8
	// MaxPendingUploadsTotal 全局未领取上传上限
	MaxPendingUploadsTotal = 32
	// MaxUploadsPerPlugin 单插件同时处理上传上限
	MaxUploadsPerPlugin = 2
	// MaxUploadsTotal 全局同时处理上传上限
	MaxUploadsTotal = 4
	// UploadTTL 暂存文件有效期
	UploadTTL = time.Hour
)

// UploadCapacityError 上传接收或暂存预算已用尽，调用者应返回 429
type UploadCapacityError struct{ Message string }

func (e *UploadCapacityError) Error() string { return e.Message }

type uploadEntry struct {
	Owner   string  `json:"owner"`
	Suffix  string  `json:"suffix"`
	Expires float64 `json:"expires"`
	Claimed bool    `json:"claimed"`
	SHA256  string  `json:"sha256"`
}

type uploadIndex struct {
	Version int                    `json:"version"`
	Items   map[string]uploadEntry `json:"items"`
}

// UploadStore WebUI 单次上传暂存；文件不经过 JSON/RPC
type UploadStore struct {
	root  string
	mu    sync.Mutex
	items map[string]uploadEntry
}

// NewUploadStore 创建或加载上传暂存库
func NewUploadStore(root string) (*UploadStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("上传暂存目录不能为空")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	store := &UploadStore{root: root, items: map[string]uploadEntry{}}
	raw, err := os.ReadFile(store.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	var index uploadIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("上传暂存索引格式错误: %w", err)
	}
	if index.Version != 1 || index.Items == nil {
		return nil, fmt.Errorf("上传暂存索引格式错误")
	}
	store.items = index.Items
	return store, nil
}

func (s *UploadStore) indexPath() string { return filepath.Join(s.root, "uploads.json") }

func (s *UploadStore) writeIndexLocked() error {
	index := uploadIndex{Version: 1, Items: s.items}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.root, "uploads.json.*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, s.indexPath()); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

func (s *UploadStore) cleanupLocked(now time.Time) {
	for token, entry := range s.items {
		if !entry.Claimed && entry.Expires < float64(now.Unix()) {
			os.Remove(filepath.Join(s.root, token))
			delete(s.items, token)
		}
	}
}

// Cleanup 后台定期回收过期暂存
func (s *UploadStore) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now())
	return s.writeIndexLocked()
}

// ValidateUploadImage 校验图片并返回扩展名
func ValidateUploadImage(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > MaxUploadBytes {
		return "", fmt.Errorf("文件为空或超过 20 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("无法解析图片")
	}
	if cfg.Width*cfg.Height > MaxUploadPixels {
		return "", fmt.Errorf("图片超过 4000 万像素")
	}
	switch format {
	case "jpeg":
		return ".jpg", nil
	case "png":
		return ".png", nil
	case "webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("仅支持 JPEG、PNG、静态 WebP")
	}
}

// Stage 暂存上传，返回一次性 token
func (s *UploadStore) Stage(owner string, raw []byte) (string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", fmt.Errorf("插件标识不能为空")
	}
	suffix, err := ValidateUploadImage(raw)
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupLocked(now)
	total, owned := 0, 0
	for _, entry := range s.items {
		if entry.Claimed {
			continue
		}
		total++
		if entry.Owner == owner {
			owned++
		}
	}
	if total >= MaxPendingUploadsTotal || owned >= MaxPendingUploadsPerPlugin {
		return "", &UploadCapacityError{Message: "未领取图片过多，请领取或等待暂存文件过期"}
	}
	if err := os.WriteFile(filepath.Join(s.root, token), raw, 0o644); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	s.items[token] = uploadEntry{
		Owner:   owner,
		Suffix:  suffix,
		Expires: float64(now.Add(UploadTTL).Unix()),
		SHA256:  hex.EncodeToString(sum[:]),
	}
	if err := s.writeIndexLocked(); err != nil {
		os.Remove(filepath.Join(s.root, token))
		delete(s.items, token)
		return "", err
	}
	return token, nil
}

// Claim 领取上传并把文件移入插件数据目录
func (s *UploadStore) Claim(owner, token string, dataDir string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[token]
	if !ok || entry.Owner != strings.TrimSpace(owner) || entry.Claimed || entry.Expires < float64(time.Now().Unix()) {
		return nil, fmt.Errorf("上传凭证不存在、已过期或已领取")
	}
	destination := filepath.Join(dataDir, "uploads", token+entry.Suffix)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(filepath.Join(s.root, token), destination); err != nil {
		return nil, err
	}
	entry.Claimed = true
	s.items[token] = entry
	if err := s.writeIndexLocked(); err != nil {
		return nil, err
	}
	return map[string]any{"path": destination, "sha256": entry.SHA256, "upload_id": token}, nil
}

// StageCount 返回未领取暂存数量，供健康检查使用
func (s *UploadStore) StageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, entry := range s.items {
		if !entry.Claimed {
			count++
		}
	}
	return count
}
