package emojisystem

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"maibot-go/internal/common/database"

	"github.com/disintegration/imaging"
	genwebp "github.com/gen2brain/webp"
)

const (
	thumbnailSize    = 200
	thumbnailQuality = 80
	thumbnailExt     = ".webp"
)

var (
	thumbGenMu      sync.Mutex
	thumbGenerating = map[string]struct{}{}
)

// ThumbnailCacheDir 缩略图缓存目录，优先实例目录下的 data/emoji_thumbnails
func ThumbnailCacheDir() string {
	if inst := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); inst != "" {
		return filepath.Join(inst, "data", "emoji_thumbnails")
	}
	return filepath.Join("data", "emoji_thumbnails")
}

// EnsureThumbnailCacheDir 确保缓存目录存在
func EnsureThumbnailCacheDir() (string, error) {
	dir := ThumbnailCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, err
	}
	return dir, nil
}

// ThumbnailCachePath 指定哈希的缩略图路径
func ThumbnailCachePath(hash string) string {
	return filepath.Join(ThumbnailCacheDir(), strings.TrimSpace(hash)+thumbnailExt)
}

// GenerateThumbnail 生成 200x200 以内的 webp 缩略图并返回缓存路径
func GenerateThumbnail(sourcePath, hash string) (string, error) {
	dir, err := EnsureThumbnailCacheDir()
	if err != nil {
		return "", err
	}
	cachePath := filepath.Join(dir, strings.TrimSpace(hash)+thumbnailExt)
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}
	img, err := decodeStickerImage(sourcePath)
	if err != nil {
		return "", err
	}
	resized := fitWithin(img, thumbnailSize, thumbnailSize)
	tmp, err := os.CreateTemp(dir, ".thumb-*"+thumbnailExt)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if err := genwebp.Encode(tmp, resized, genwebp.Options{Quality: thumbnailQuality}); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		return "", err
	}
	return cachePath, nil
}

func decodeStickerImage(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, err := imaging.Decode(file, imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// fitWithin 只在超过上限时缩小，对齐 PIL Image.thumbnail 行为
func fitWithin(img image.Image, maxW, maxH int) image.Image {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxW && height <= maxH {
		return img
	}
	return imaging.Fit(img, maxW, maxH, imaging.Lanczos)
}

// StartBackgroundThumbnail 异步生成缩略图，同一哈希只启动一次
func StartBackgroundThumbnail(sourcePath, hash string) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return
	}
	thumbGenMu.Lock()
	if _, exists := thumbGenerating[hash]; exists {
		thumbGenMu.Unlock()
		return
	}
	thumbGenerating[hash] = struct{}{}
	thumbGenMu.Unlock()
	go func() {
		defer func() {
			thumbGenMu.Lock()
			delete(thumbGenerating, hash)
			thumbGenMu.Unlock()
		}()
		_, _ = GenerateThumbnail(sourcePath, hash)
	}()
}

// ThumbnailGenerating 查询是否正在生成缩略图
func ThumbnailGenerating(hash string) bool {
	thumbGenMu.Lock()
	defer thumbGenMu.Unlock()
	_, ok := thumbGenerating[strings.TrimSpace(hash)]
	return ok
}

// ThumbnailCacheStats 缓存统计，emojiCount 由 images 表（image_type=EMOJI）总数提供
func ThumbnailCacheStats(emojiCount int) (dir string, count int, sizeMB float64, coverage float64, err error) {
	dir = ThumbnailCacheDir()
	abs, absErr := filepath.Abs(dir)
	if absErr == nil {
		dir = abs
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return dir, 0, 0, 0, nil
		}
		return dir, 0, 0, 0, err
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), thumbnailExt) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		count++
		total += info.Size()
	}
	sizeMB = roundFloat(float64(total)/(1024*1024), 2)
	if emojiCount > 0 {
		coverage = roundFloat(float64(count)/float64(emojiCount)*100, 1)
	}
	return dir, count, sizeMB, coverage, nil
}

// CleanupOrphanedThumbnails 清理孤立缓存，返回删除与保留数量
func (s *WebUIService) CleanupOrphanedThumbnails(ctx context.Context) (int, int, error) {
	dir := ThumbnailCacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	var hashes []string
	if err := s.db(ctx).Model(&memory.EmojiRecord{}).
		Where("image_type = ?", schemaImageTypeEmoji).Pluck("image_hash", &hashes).Error; err != nil {
		return 0, 0, err
	}
	valid := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		valid[hash] = struct{}{}
	}
	cleaned, kept := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), thumbnailExt) {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, ok := valid[stem]; ok {
			kept++
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			continue
		}
		cleaned++
	}
	return cleaned, kept, nil
}

// ClearThumbnailCache 清空缓存，返回删除数量
func ClearThumbnailCache() (int, error) {
	dir := ThumbnailCacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), thumbnailExt) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			continue
		}
		cleaned++
	}
	return cleaned, nil
}

// PreheatingCandidates 预热候选：未封禁、使用次数倒序，最多 limit 个
func (s *WebUIService) PreheatingCandidates(ctx context.Context, limit int) ([]memory.EmojiRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []memory.EmojiRecord
	if err := s.db(ctx).Model(&memory.EmojiRecord{}).
		Where("image_type = ? AND is_banned = ?", schemaImageTypeEmoji, false).
		Order("query_count DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func roundFloat(value float64, digits int) float64 {
	if digits < 0 {
		return value
	}
	factor := 1.0
	for i := 0; i < digits; i++ {
		factor *= 10
	}
	return float64(int64(value*factor+0.5)) / factor
}

// ThumbnailContentType 缩略图 MIME
func ThumbnailContentType() string { return "image/webp" }

// MimeTypeByFormat 原图 MIME，对齐 Python mime_types 表
func MimeTypeByFormat(format string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	default:
		return "application/octet-stream"
	}
}

// ClearThumbnailForHash 删除指定哈希的缩略图缓存
func ClearThumbnailForHash(hash string) error {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil
	}
	err := os.Remove(ThumbnailCachePath(hash))
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
