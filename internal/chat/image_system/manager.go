package imagesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ManagerOptions 图片管理器依赖
type ManagerOptions struct {
	Store     Store
	Root      string
	Dir       string
	Describer Describer
	Memory    MemorySync
	Listener  RecognitionListener
	Now       func() time.Time
	// SkipLegacyCleanup 为 true 时不执行启动时的历史注册字段清理，测试用
	SkipLegacyCleanup bool
}

// Manager 图片描述与缓存管理器，对应 Python ImageManager
type Manager struct {
	store     Store
	root      string
	dir       string
	describer Describer
	memory    MemorySync
	listener  RecognitionListener
	now       func() time.Time

	mu           sync.Mutex
	pending      map[string]struct{}
	shuttingDown bool
	wg           sync.WaitGroup
}

// NewManager 创建图片管理器并确保缓存目录存在
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil {
		return nil, errors.New("图片记录存储未配置")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = instanceRoot()
	}
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		dir = filepath.Join(root, "data", "images")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建图片缓存目录失败: %w", err)
	}
	manager := &Manager{
		store:     opts.Store,
		root:      root,
		dir:       dir,
		describer: opts.Describer,
		memory:    opts.Memory,
		listener:  opts.Listener,
		now:       opts.Now,
		pending:   map[string]struct{}{},
	}
	if !opts.SkipLegacyCleanup {
		manager.CleanupLegacyImageRegistrationRecords(context.Background())
	}
	return manager, nil
}

// Root 实例根目录
func (m *Manager) Root() string { return m.root }

// Dir 图片缓存目录
func (m *Manager) Dir() string { return m.dir }

// Shutdown 等待描述构建与派生的记忆同步任务完成
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shuttingDown = true
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetCachedImageDescription 读取已完成的 VLM 描述，不触发新的模型调用
func (m *Manager) GetCachedImageDescription(ctx context.Context, imageHash string) string {
	record, err := m.store.GetByHash(ctx, strings.TrimSpace(imageHash), ImageTypeImage)
	if err != nil {
		zap.L().Error("查询图片描述时发生错误", zap.String("hash", imageHash), zap.Error(err))
		return ""
	}
	if record == nil || !record.VLMProcessed {
		return ""
	}
	return strings.TrimSpace(record.Description)
}

// GetCachedEmojiDescription 读取表情描述，不要求 VLM 处理完成
func (m *Manager) GetCachedEmojiDescription(ctx context.Context, emojiHash string) string {
	record, err := m.store.GetByHash(ctx, strings.TrimSpace(emojiHash), ImageTypeEmoji)
	if err != nil {
		zap.L().Error("查询表情描述时发生错误", zap.String("hash", emojiHash), zap.Error(err))
		return ""
	}
	if record == nil || record.NoFileFlag {
		return ""
	}
	return strings.TrimSpace(record.Description)
}

// IsImageDescriptionPending 判断图片记录是否仍在等待识别；记录不存在或文件缺失视为无需等待
func (m *Manager) IsImageDescriptionPending(ctx context.Context, imageHash string) bool {
	record, err := m.store.GetByHash(ctx, strings.TrimSpace(imageHash), ImageTypeImage)
	if err != nil {
		zap.L().Error("查询图片识别状态时发生错误", zap.String("hash", imageHash), zap.Error(err))
		return false
	}
	if record == nil || record.NoFileFlag {
		return false
	}
	return !record.VLMProcessed
}

// VLMAvailable 是否配置了可用的视觉模型
func (m *Manager) VLMAvailable() bool {
	return m != nil && m.describer != nil && m.describer.Available()
}

// GetImageDescription 获取图片描述，缺少缓存时按 waitForBuild 决定同步构建或后台构建
func (m *Manager) GetImageDescription(ctx context.Context, imageHash string, imageBytes []byte, waitForBuild bool) (string, error) {
	hash := strings.TrimSpace(imageHash)
	if hash == "" {
		if len(imageBytes) == 0 {
			return "", errors.New("必须提供图片哈希值或图片字节数据")
		}
		hash = sha256Hex(imageBytes)
	}
	var saved *ManagedImage
	if len(imageBytes) > 0 {
		image, err := m.EnsureImageSaved(ctx, imageBytes)
		if err != nil {
			return "", err
		}
		saved = image
	}
	record, err := m.store.GetByHash(ctx, hash, ImageTypeImage)
	if err != nil {
		zap.L().Error("查询图片描述时发生错误", zap.String("hash", hash), zap.Error(err))
	} else if record != nil && record.VLMProcessed && record.Description != "" {
		return record.Description, nil
	}
	if len(imageBytes) == 0 {
		zap.L().Warn("图片哈希值未找到，且未提供图片字节数据，返回无描述", zap.String("hash", hash))
		return "", nil
	}
	if m.describer == nil || !m.describer.Available() {
		zap.L().Info("未配置 VLM 模型，跳过图片识别", zap.String("hash", hash))
		return "", nil
	}
	if !waitForBuild {
		m.scheduleDescriptionBuild(hash, imageBytes, saved)
		return "", nil
	}
	zap.L().Info("图片描述未找到，准备生成新描述", zap.String("hash", hash))
	image, err := m.BuildImageDescription(ctx, imageBytes, saved)
	if err != nil {
		zap.L().Error("生成图片描述时发生错误", zap.String("hash", hash), zap.Error(err))
		return "", nil
	}
	return image.Description, nil
}

func (m *Manager) scheduleDescriptionBuild(imageHash string, imageBytes []byte, saved *ManagedImage) {
	if m.describer == nil || !m.describer.Available() {
		zap.L().Info("未配置 VLM 模型，跳过图片后台识别任务", zap.String("hash", imageHash))
		return
	}
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return
	}
	if _, exists := m.pending[imageHash]; exists {
		m.mu.Unlock()
		return
	}
	m.pending[imageHash] = struct{}{}
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.pending, imageHash)
			m.mu.Unlock()
			m.wg.Done()
		}()
		zap.L().Debug("图片描述后台构建已开始", zap.String("hash", imageHash))
		image, err := m.BuildImageDescription(context.Background(), imageBytes, saved)
		if err != nil {
			zap.L().Warn("图片描述后台构建失败", zap.String("hash", imageHash), zap.Error(err))
			return
		}
		zap.L().Debug("图片描述后台构建完成", zap.String("hash", imageHash))
		m.finalizeDescriptionBuild(image)
	}()
}

func (m *Manager) finalizeDescriptionBuild(image *ManagedImage) {
	if image == nil || strings.TrimSpace(image.Description) == "" {
		return
	}
	hash := image.FileHash
	if m.memory != nil {
		if err := m.memory.SyncImageDescription(context.Background(), hash, "[图片："+image.Description+"]"); err != nil {
			zap.L().Warn("同步图片描述到记忆失败", zap.String("hash", hash), zap.Error(err))
		}
	}
	if m.listener != nil {
		m.listener(hash)
	}
}

// GetImageFromDB 按哈希读取图片，no_file_flag 记录返回 nil
func (m *Manager) GetImageFromDB(ctx context.Context, imageHash string) (*ManagedImage, error) {
	return m.getFromDB(ctx, imageHash, ImageTypeImage)
}

// GetEmojiFromDB 按哈希读取表情图片，no_file_flag 记录返回 nil
func (m *Manager) GetEmojiFromDB(ctx context.Context, imageHash string) (*ManagedImage, error) {
	return m.getFromDB(ctx, imageHash, ImageTypeEmoji)
}

func (m *Manager) getFromDB(ctx context.Context, imageHash string, imageType string) (*ManagedImage, error) {
	record, err := m.store.GetByHash(ctx, strings.TrimSpace(imageHash), imageType)
	if err != nil {
		return nil, err
	}
	if record == nil {
		zap.L().Info("未找到哈希值为 " + imageHash + " 的图片记录")
		return nil, nil
	}
	if record.NoFileFlag {
		zap.L().Warn("数据库记录标记为文件不存在", zap.String("hash", imageHash))
		return nil, nil
	}
	return m.managedFromRecord(record)
}

// RegisterImageToDB 将图片对象注册到数据库
func (m *Manager) RegisterImageToDB(ctx context.Context, image *ManagedImage) bool {
	if image == nil {
		zap.L().Error("无效的图片对象，无法注册到数据库")
		return false
	}
	if !fileExists(image.FullPath) {
		zap.L().Error("图片文件不存在，无法注册到数据库: " + image.FullPath)
		return false
	}
	storedPath, err := SerializeStoredImagePath(m.root, image.FullPath)
	if err != nil {
		zap.L().Error("序列化图片路径失败", zap.Error(err))
		return false
	}
	now := m.now()
	record := &ImageRecord{
		ImageHash:    image.FileHash,
		Description:  image.Description,
		FullPath:     storedPath,
		ImageType:    ImageTypeImage,
		IsRegistered: false,
		RecordTime:   now,
		LastUsedTime: &now,
		VLMProcessed: image.VLMProcessed,
	}
	if err := m.store.Create(ctx, record); err != nil {
		zap.L().Error("保存图片记录到数据库时发生错误", zap.Error(err))
		return false
	}
	zap.L().Debug("保存图片", zap.Uint("id", record.ID), zap.String("path", record.FullPath))
	return true
}

// UpdateImageDescription 更新图片描述
func (m *Manager) UpdateImageDescription(ctx context.Context, image *ManagedImage) bool {
	if image == nil {
		return false
	}
	record, err := m.store.GetByHash(ctx, image.FileHash, ImageTypeImage)
	if err != nil {
		zap.L().Error("更新图片描述时发生错误", zap.Error(err))
		return false
	}
	if record == nil {
		zap.L().Error("未找到哈希值为 " + image.FileHash + " 的图片记录，无法更新描述")
		return false
	}
	m.normalizeImageRegistrationFields(record)
	now := m.now()
	record.Description = image.Description
	record.LastUsedTime = &now
	record.VLMProcessed = image.VLMProcessed
	if err := m.store.Save(ctx, record); err != nil {
		zap.L().Error("更新图片描述时发生错误", zap.Error(err))
		return false
	}
	zap.L().Info("理解图片: " + image.Description)
	return true
}

// DeleteImage 删除图片记录和对应文件
func (m *Manager) DeleteImage(ctx context.Context, image *ManagedImage) bool {
	if image == nil {
		return false
	}
	record, err := m.store.GetByHash(ctx, image.FileHash, ImageTypeImage)
	if err != nil {
		logDeleteImageError(image, err)
		return false
	}
	if record == nil {
		zap.L().Error("未找到哈希值为 " + image.FileHash + " 的图片记录，无法删除")
		return false
	}
	if err := m.store.Delete(ctx, record.ID); err != nil {
		logDeleteImageError(image, err)
		return false
	}
	zap.L().Info("成功删除图片记录: " + image.FileHash)
	if fileExists(image.FullPath) {
		if err := os.Remove(image.FullPath); err != nil {
			logDeleteImageError(image, err)
			return false
		}
		zap.L().Info("成功删除图片文件: " + image.FullPath)
	} else {
		zap.L().Warn("图片文件不存在，无法删除: " + image.FullPath)
	}
	return true
}

// logDeleteImageError 对齐 Python delete_image 的 except 分支：错误 + 残留文件告警
func logDeleteImageError(image *ManagedImage, err error) {
	zap.L().Error("删除图片时发生错误", zap.Error(err))
	if image != nil && fileExists(image.FullPath) {
		zap.L().Warn("图片文件未被删除: " + image.FullPath)
	}
}

// BuildImageDescription 在图片已保存的前提下生成或补齐图片描述
func (m *Manager) BuildImageDescription(ctx context.Context, imageBytes []byte, saved *ManagedImage) (*ManagedImage, error) {
	image := saved
	var err error
	if image == nil {
		image, err = m.EnsureImageSaved(ctx, imageBytes)
		if err != nil {
			return nil, err
		}
	}
	if image.ImageFormat == "" {
		if err := m.calculateHashFormat(image); err != nil {
			return nil, err
		}
	}
	if image.VLMProcessed && image.Description != "" {
		return image, nil
	}
	if m.describer == nil || !m.describer.Available() {
		zap.L().Info("未配置 VLM 模型，跳过图片识别", zap.String("hash", image.FileHash))
		return image, nil
	}
	description, err := m.describer.Describe(ctx, imageBytes, image.ImageFormat)
	if err != nil {
		return nil, err
	}
	if description == "" {
		zap.L().Warn("VLM未能生成图片描述")
	}
	image.Description = description
	image.VLMProcessed = true
	if !m.UpdateImageDescription(ctx, image) {
		return nil, fmt.Errorf("更新图片描述失败: %s", image.FileHash)
	}
	return image, nil
}

// SaveImageAndProcess 保存图片并生成描述
func (m *Manager) SaveImageAndProcess(ctx context.Context, imageBytes []byte) (*ManagedImage, error) {
	return m.BuildImageDescription(ctx, imageBytes, nil)
}

// CleanupInvalidDescriptionsInDB 清理描述为空或文件路径不存在的图片记录
func (m *Manager) CleanupInvalidDescriptionsInDB(ctx context.Context) (int, int) {
	invalidCount := 0
	nullPathCount := 0
	zap.L().Info("开始清理数据库中无效的图片记录...")
	records, err := m.store.List(ctx, "")
	if err != nil {
		zap.L().Error("清理数据库中无效图片记录时发生错误", zap.Error(err))
		return 0, 0
	}
	for index := range records {
		record := records[index]
		if record.Description == "" {
			if record.FullPath != "" {
				path, resolveErr := ResolveStoredImagePath(m.root, record.FullPath)
				if resolveErr == nil && fileExists(path) {
					if err := os.Remove(path); err != nil {
						zap.L().Error("删除无效描述的图片文件时发生错误", zap.String("path", path), zap.Error(err))
					} else {
						zap.L().Info("已删除无效描述的图片文件: " + record.FullPath)
					}
				}
			}
			if err := m.store.Delete(ctx, record.ID); err != nil {
				// 对齐 Python：一次 DB 异常中止整轮清理
				zap.L().Error("清理数据库中无效图片记录时发生错误", zap.Error(err))
				break
			}
			invalidCount++
			continue
		}
		if record.FullPath != "" {
			path, resolveErr := ResolveStoredImagePath(m.root, record.FullPath)
			if resolveErr == nil && !fileExists(path) {
				if err := m.store.Delete(ctx, record.ID); err != nil {
					zap.L().Error("清理数据库中无效图片记录时发生错误", zap.Error(err))
					break
				}
				nullPathCount++
			}
		}
	}
	zap.L().Info(fmt.Sprintf("清理完成: %d 条无效描述记录，%d 条文件路径不存在记录", invalidCount, nullPathCount))
	return invalidCount, nullPathCount
}

// CleanupLegacyImageRegistrationRecords 修正历史图片记录误用的表情注册字段
func (m *Manager) CleanupLegacyImageRegistrationRecords(ctx context.Context) int {
	fixed := 0
	records, err := m.store.List(ctx, ImageTypeImage)
	if err != nil {
		zap.L().Error("清理图片注册状态失败", zap.Error(err))
		return 0
	}
	for index := range records {
		record := records[index]
		if !m.normalizeImageRegistrationFields(&record) {
			continue
		}
		if err := m.store.Save(ctx, &record); err != nil {
			zap.L().Error("清理图片注册状态失败", zap.Error(err))
			continue
		}
		fixed++
	}
	if fixed > 0 {
		zap.L().Info(fmt.Sprintf("已清理 %d 条图片记录的误注册状态", fixed))
	}
	return fixed
}

func (m *Manager) normalizeImageRegistrationFields(record *ImageRecord) bool {
	if record == nil || record.ImageType != ImageTypeImage {
		return false
	}
	if !record.IsRegistered && record.RegisterTime == nil {
		return false
	}
	record.IsRegistered = false
	record.RegisterTime = nil
	return true
}

func (m *Manager) managedFromRecord(record *ImageRecord) (*ManagedImage, error) {
	path, err := ResolveStoredImagePath(m.root, record.FullPath)
	if err != nil {
		return nil, err
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	return &ManagedImage{
		FullPath:     path,
		FileName:     filepath.Base(path),
		FileHash:     record.ImageHash,
		ImageFormat:  format,
		Description:  record.Description,
		VLMProcessed: record.VLMProcessed,
	}, nil
}
