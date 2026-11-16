package display

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Prompt 预览落盘常量
const (
	DefaultMaxPreviewGroupsPerChat = 256
	PreviewBaseDirName             = "maisaka_prompt"
	PromptImageDirName             = "prompt_imgs"
)

var cacheImageNamePattern = regexp.MustCompile("^[0-9a-f]{64}\\.[A-Za-z0-9]+$")
var imageNamePattern = regexp.MustCompile("[0-9a-f]{64}\\.[A-Za-z0-9]+")

// PreviewImageAsset 一张需要随预览一起落盘的图片
type PreviewImageAsset struct {
	FileName string
	Content  []byte
}

// PromptPreviewLogger 负责保存 Prompt 预览文件并控制目录容量
type PromptPreviewLogger struct {
	BaseDir                 string
	ImageDir                string
	MaxPreviewGroupsPerChat int

	mu       sync.Mutex
	lastStem map[string]int64
}

// NewPromptPreviewLogger 创建预览落盘器
func NewPromptPreviewLogger(baseDir string) *PromptPreviewLogger {
	root := RepoRoot()
	if baseDir == "" {
		baseDir = filepath.Join(root, "logs", PreviewBaseDirName)
	}
	return &PromptPreviewLogger{
		BaseDir:                 baseDir,
		ImageDir:                filepath.Join(root, "data", PromptImageDirName),
		MaxPreviewGroupsPerChat: DefaultMaxPreviewGroupsPerChat,
		lastStem:                map[string]int64{},
	}
}

var defaultPromptPreviewLogger = NewPromptPreviewLogger("")

// DefaultPromptPreviewLogger 返回默认预览落盘器
func DefaultPromptPreviewLogger() *PromptPreviewLogger { return defaultPromptPreviewLogger }

// ConfigureDefaultPreviewLimit 按实例配置设置默认预览保留数
// 对齐 Python prompt_preview_logger.py:595-604：max(1, configured or 256)
func ConfigureDefaultPreviewLimit(limit int) {
	if limit < 1 {
		limit = DefaultMaxPreviewGroupsPerChat
	}
	defaultPromptPreviewLogger.MaxPreviewGroupsPerChat = limit
}

// SavePreviewFile 登记一次预览落盘并立即返回该预览的文件路径
func (l *PromptPreviewLogger) SavePreviewFile(chatID, category, content string, images []PreviewImageAsset, lookup PreviewSessionLookup) (string, error) {
	return l.SavePreviewFileWithTrace(chatID, category, content, images, lookup, "")
}

// SavePreviewFileWithTrace 保存预览并登记到本轮推理 trace
func (l *PromptPreviewLogger) SavePreviewFileWithTrace(
	chatID, category, content string, images []PreviewImageAsset, lookup PreviewSessionLookup, traceID string,
) (string, error) {
	categoryDir := NormalizePreviewName(category)
	chatDir := filepath.Join(l.BaseDir, categoryDir, BuildPreviewChatDirName(chatID, lookup))
	stem := l.allocateStem(chatDir)
	filePath := filepath.Join(chatDir, fmt.Sprintf("%d.json", stem))
	if err := l.WriteRecordFileWithTrace(filePath, content, images, traceID); err != nil {
		return "", err
	}
	l.trimOverflow(chatDir)
	return filePath, nil
}

// WriteRecordFile 原子写入一条预览记录
func (l *PromptPreviewLogger) WriteRecordFile(filePath, content string, images []PreviewImageAsset) error {
	return l.WriteRecordFileWithTrace(filePath, content, images, "")
}

// WriteRecordFileWithTrace 原子写入一条预览记录，并同步推理轮次索引
func (l *PromptPreviewLogger) WriteRecordFileWithTrace(filePath, content string, images []PreviewImageAsset, traceID string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	for _, image := range images {
		imagePath := filepath.Join(l.ImageDir, image.FileName)
		if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(imagePath); err == nil {
			continue
		}
		if err := os.WriteFile(imagePath, image.Content, 0o644); err != nil {
			return err
		}
	}
	temporary := filepath.Join(filepath.Dir(filePath), "."+filepath.Base(filePath)+".tmp")
	if err := os.WriteFile(temporary, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, filePath); err != nil {
		return err
	}
	return RegisterReasoningStep(l.BaseDir, filePath, traceID)
}

// AppendRecordEvent 追加一行事件，旧事件保持原样
func (l *PromptPreviewLogger) AppendRecordEvent(filePath, content string, images []PreviewImageAsset) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	for _, image := range images {
		imagePath := filepath.Join(l.ImageDir, image.FileName)
		if _, err := os.Stat(imagePath); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(imagePath, image.Content, 0o644); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(content + "\n")
	return err
}

// DeleteRecordFile 删除日志
func (l *PromptPreviewLogger) DeleteRecordFile(filePath string) error {
	RemoveReasoningStep(l.BaseDir, filePath)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CleanupOrphanImages 清理没有被任何预览引用的专用图片缓存
func (l *PromptPreviewLogger) CleanupOrphanImages() (int, error) {
	entries, err := os.ReadDir(l.ImageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	referenced := map[string]struct{}{}
	records := make([]string, 0)
	_ = filepath.WalkDir(l.BaseDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension == ".json" || extension == ".jsonl" || extension == ".html" || extension == ".txt" {
			records = append(records, path)
		}
		return nil
	})
	for _, record := range records {
		content, readErr := os.ReadFile(record)
		if readErr != nil {
			continue
		}
		for _, name := range imageNamePattern.FindAllString(string(content), -1) {
			referenced[name] = struct{}{}
		}
	}
	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() || !cacheImageNamePattern.MatchString(entry.Name()) {
			continue
		}
		if _, ok := referenced[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(l.ImageDir, entry.Name())); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

// PromptOrphanImageCheckInterval 孤立图片巡检周期，对齐 Python _ORPHAN_IMAGE_CHECK_INTERVAL_SECONDS
const PromptOrphanImageCheckInterval = time.Hour

// Start 启动后台巡检（启动时立即一次，之后每小时一次，ctx 取消后退出）
func (l *PromptPreviewLogger) Start(ctx context.Context) {
	go l.RunPeriodicCleanup(ctx)
}

// RunPeriodicCleanup 阻塞执行启动巡检与周期巡检
func (l *PromptPreviewLogger) RunPeriodicCleanup(ctx context.Context) {
	l.runOrphanImageCleanupLoop(ctx, PromptOrphanImageCheckInterval)
}

func (l *PromptPreviewLogger) runOrphanImageCleanupLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = PromptOrphanImageCheckInterval
	}
	if _, err := l.CleanupOrphanImages(); err != nil {
		zap.L().Error("Prompt 预览孤立图片巡检失败", zap.Error(err))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := l.CleanupOrphanImages(); err != nil {
				zap.L().Error("Prompt 预览孤立图片巡检失败", zap.Error(err))
			}
		}
	}
}

// BuildImageCachePath 计算图片缓存路径
func (l *PromptPreviewLogger) BuildImageCachePath(imageFormat string, content []byte) string {
	imageFormat = NormalizeImageFormat(imageFormat)
	if imageFormat == "" {
		imageFormat = "bin"
	}
	digest := sha256.Sum256(content)
	return filepath.Join(l.ImageDir, hex.EncodeToString(digest[:])+"."+imageFormat)
}

// NormalizeImageFormat 归一化图片扩展名
func NormalizeImageFormat(imageFormat string) string {
	normalized := strings.ToLower(strings.TrimSpace(imageFormat))
	if normalized == "jpg" {
		return "jpeg"
	}
	return normalized
}

func (l *PromptPreviewLogger) allocateStem(chatDir string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	stem := time.Now().UnixMilli()
	if previous, ok := l.lastStem[chatDir]; ok && stem <= previous {
		stem = previous + 1
	}
	l.lastStem[chatDir] = stem
	return stem
}

func (l *PromptPreviewLogger) trimOverflow(chatDir string) {
	entries, err := os.ReadDir(chatDir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	limit := l.MaxPreviewGroupsPerChat
	if limit <= 0 {
		limit = DefaultMaxPreviewGroupsPerChat
	}
	if len(names) <= limit {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-limit] {
		_ = os.Remove(filepath.Join(chatDir, name))
	}
}
