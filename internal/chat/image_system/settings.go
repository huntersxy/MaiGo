package imagesystem

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

const (
	defaultMaxImageSizeMB  = 16.0
	defaultCleanupHours    = 6.0
	defaultFileRetention   = 14
	defaultNoFileRetention = 30
)

// ImageCacheCleanupSettings 对齐 Python ImageCacheCleanupConfig
type ImageCacheCleanupSettings struct {
	Enabled                   bool
	CheckIntervalHours        float64
	ImageFileRetentionDays    int
	NoFileResultRetentionDays int
}

// ImageSettings 图片系统运行配置，默认值与 official_configs.py 一致
type ImageSettings struct {
	// Root 实例根目录
	Root string
	// Dir 图片缓存目录，通常为 <Root>/data/images
	Dir                        string
	HandleOversizedImages      bool
	MaxImageSizeMB             float64
	OversizedImageHandleMethod string
	CacheCleanup               ImageCacheCleanupSettings
}

// DefaultImageSettings 返回 Python official_configs 的图片默认配置
func DefaultImageSettings(root string) ImageSettings {
	if strings.TrimSpace(root) == "" {
		root = instanceRoot()
	}
	return ImageSettings{
		Root:                       root,
		Dir:                        filepath.Join(root, "data", "images"),
		HandleOversizedImages:      true,
		MaxImageSizeMB:             defaultMaxImageSizeMB,
		OversizedImageHandleMethod: "compress",
		CacheCleanup: ImageCacheCleanupSettings{
			Enabled:                   true,
			CheckIntervalHours:        defaultCleanupHours,
			ImageFileRetentionDays:    defaultFileRetention,
			NoFileResultRetentionDays: defaultNoFileRetention,
		},
	}
}

// LoadImageSettings 从 MAIBOT_INSTANCE_DIR/config/bot_config.toml 读取图片配置
func LoadImageSettings() ImageSettings {
	root := instanceRoot()
	return LoadImageSettingsFromRoot(root)
}

// LoadImageSettingsFromRoot 从指定实例目录读取图片配置，缺失项回退 Python 默认值
func LoadImageSettingsFromRoot(root string) ImageSettings {
	settings := DefaultImageSettings(root)
	path := filepath.Join(settings.Root, "config", "bot_config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return normalizeImageSettings(settings)
	}
	var probe imageSettingsProbe
	if err := toml.Unmarshal(raw, &probe); err != nil {
		return normalizeImageSettings(settings)
	}
	visual := probe.Visual
	if visual.HandleOversizedImages != nil {
		settings.HandleOversizedImages = *visual.HandleOversizedImages
	}
	if visual.MaxImageSizeMB != nil {
		settings.MaxImageSizeMB = *visual.MaxImageSizeMB
	}
	if visual.OversizedImageHandleMethod != nil {
		settings.OversizedImageHandleMethod = *visual.OversizedImageHandleMethod
	}
	if visual.ImageCacheCleanup.Enabled != nil {
		settings.CacheCleanup.Enabled = *visual.ImageCacheCleanup.Enabled
	}
	if visual.ImageCacheCleanup.CheckIntervalHours != nil {
		settings.CacheCleanup.CheckIntervalHours = *visual.ImageCacheCleanup.CheckIntervalHours
	}
	if visual.ImageCacheCleanup.ImageFileRetentionDays != nil {
		settings.CacheCleanup.ImageFileRetentionDays = *visual.ImageCacheCleanup.ImageFileRetentionDays
	}
	if visual.ImageCacheCleanup.NoFileResultRetentionDays != nil {
		settings.CacheCleanup.NoFileResultRetentionDays = *visual.ImageCacheCleanup.NoFileResultRetentionDays
	}
	return normalizeImageSettings(settings)
}

// Normalize 修正非法配置，保持与 Python 字段约束一致
func NormalizeImageSettings(settings ImageSettings) ImageSettings {
	return normalizeImageSettings(settings)
}

func normalizeImageSettings(settings ImageSettings) ImageSettings {
	settings.Root = strings.TrimSpace(settings.Root)
	if settings.Root == "" {
		settings.Root = instanceRoot()
	}
	if strings.TrimSpace(settings.Dir) == "" {
		settings.Dir = filepath.Join(settings.Root, "data", "images")
	}
	if settings.MaxImageSizeMB < 0 {
		settings.MaxImageSizeMB = 0
	}
	if strings.TrimSpace(settings.OversizedImageHandleMethod) == "" {
		settings.OversizedImageHandleMethod = "compress"
	}
	if settings.CacheCleanup.CheckIntervalHours <= 0 {
		settings.CacheCleanup.CheckIntervalHours = defaultCleanupHours
	}
	if settings.CacheCleanup.ImageFileRetentionDays < 1 {
		settings.CacheCleanup.ImageFileRetentionDays = 1
	}
	if settings.CacheCleanup.NoFileResultRetentionDays < 1 {
		settings.CacheCleanup.NoFileResultRetentionDays = 1
	}
	return settings
}

// MaxImageSizeBytes 返回入站图片大小上限，0 表示不限
func (s ImageSettings) MaxImageSizeBytes() int64 {
	if s.MaxImageSizeMB <= 0 {
		return 0
	}
	return int64(s.MaxImageSizeMB * 1024 * 1024)
}

// CleanupInterval 返回清理周期，最短 60 秒
func (s ImageCacheCleanupSettings) CleanupInterval() time.Duration {
	seconds := s.CheckIntervalHours * 3600.0
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds * float64(time.Second))
}

type imageSettingsProbe struct {
	Visual struct {
		HandleOversizedImages      *bool    `toml:"handle_oversized_images"`
		MaxImageSizeMB             *float64 `toml:"max_image_size_mb"`
		OversizedImageHandleMethod *string  `toml:"oversized_image_handle_method"`
		ImageCacheCleanup          struct {
			Enabled                   *bool    `toml:"enabled"`
			CheckIntervalHours        *float64 `toml:"check_interval_hours"`
			ImageFileRetentionDays    *int     `toml:"image_file_retention_days"`
			NoFileResultRetentionDays *int     `toml:"no_file_result_retention_days"`
		} `toml:"image_cache_cleanup"`
	} `toml:"visual"`
}

func instanceRoot() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		return value
	}
	if working, err := os.Getwd(); err == nil {
		return working
	}
	return "."
}
