package imagesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// EnsureImageSaved 先保存图片记录与文件，确保后续可以按哈希回填图片内容
func (m *Manager) EnsureImageSaved(ctx context.Context, imageBytes []byte) (*ManagedImage, error) {
	hash := sha256Hex(imageBytes)
	record, err := m.store.GetByHash(ctx, hash, ImageTypeImage)
	if err != nil {
		return nil, err
	}
	if record != nil {
		m.normalizeImageRegistrationFields(record)
		path, pathErr := ResolveStoredImagePath(m.root, record.FullPath)
		if pathErr != nil {
			return nil, pathErr
		}
		if !record.NoFileFlag && fileExists(path) {
			zap.L().Info("图片已存在于数据库中", zap.String("hash", hash))
			now := m.now()
			record.LastUsedTime = &now
			record.QueryCount++
			if err := m.store.Save(ctx, record); err != nil {
				return nil, err
			}
			return m.managedFromRecord(record)
		}
		zap.L().Info("图片记录存在但文件缺失，准备重新保存图片文件", zap.String("hash", hash))
	}

	tmpPath := filepath.Join(m.dir, hash+".tmp")
	if err := os.WriteFile(tmpPath, imageBytes, 0o644); err != nil {
		return nil, fmt.Errorf("写入图片临时文件失败: %w", err)
	}
	image := &ManagedImage{FullPath: tmpPath, FileName: filepath.Base(tmpPath), ImageBytes: imageBytes, FileHash: hash}
	if err := m.calculateHashFormat(image); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if !m.upsertSavedImageRecord(ctx, image) {
		return nil, fmt.Errorf("保存图片记录到数据库失败: %s", hash)
	}
	return image, nil
}

func (m *Manager) upsertSavedImageRecord(ctx context.Context, image *ManagedImage) bool {
	if image == nil {
		return false
	}
	record, err := m.store.GetByHash(ctx, image.FileHash, ImageTypeImage)
	if err != nil {
		zap.L().Error("查询图片记录时发生错误", zap.String("hash", image.FileHash), zap.Error(err))
		return false
	}
	storedPath, err := SerializeStoredImagePath(m.root, image.FullPath)
	if err != nil {
		zap.L().Error("序列化图片路径失败", zap.String("path", image.FullPath), zap.Error(err))
		return false
	}
	now := m.now()
	if record == nil {
		record = &ImageRecord{
			ImageHash:   image.FileHash,
			Description: image.Description,
			ImageType:   ImageTypeImage,
			QueryCount:  0,
		}
	} else {
		m.normalizeImageRegistrationFields(record)
		// 对齐 Python：命中已有记录时无条件回填 description/vlm_processed
		image.Description = record.Description
		image.VLMProcessed = record.VLMProcessed
	}
	record.FullPath = storedPath
	record.NoFileFlag = false
	record.LastUsedTime = &now
	record.VLMProcessed = image.VLMProcessed
	if record.RecordTime.IsZero() {
		record.RecordTime = now
	}
	if record.ID == 0 {
		err = m.store.Create(ctx, record)
	} else {
		err = m.store.Save(ctx, record)
	}
	if err != nil {
		zap.L().Error("保存图片记录到数据库时发生错误", zap.String("hash", image.FileHash), zap.Error(err))
		return false
	}
	return true
}

// calculateHashFormat 计算哈希与实际格式，并在扩展名不符时改名
func (m *Manager) calculateHashFormat(image *ManagedImage) error {
	data := image.ImageBytes
	if len(data) == 0 {
		var err error
		data, err = os.ReadFile(image.FullPath)
		if err != nil {
			return err
		}
	}
	image.ImageBytes = data
	image.FileHash = sha256Hex(data)
	format, err := detectImageFormat(data)
	if err != nil {
		return fmt.Errorf("读取图片格式失败: %w", err)
	}
	image.ImageFormat = format
	return m.renameToMatchFormat(image)
}

func (m *Manager) renameToMatchFormat(image *ManagedImage) error {
	extension := "." + image.ImageFormat
	currentExtension := strings.ToLower(filepath.Ext(image.FullPath))
	if currentExtension == extension {
		image.FileName = filepath.Base(image.FullPath)
		return nil
	}
	target := strings.TrimSuffix(image.FullPath, filepath.Ext(image.FullPath)) + extension
	if target == image.FullPath {
		return nil
	}
	if fileExists(target) {
		existingHash, err := sha256File(target)
		if err == nil && existingHash == image.FileHash {
			zap.L().Info(target + " 已存在且内容一致，复用已有文件")
			if err := os.Remove(image.FullPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			image.FullPath = target
			image.FileName = filepath.Base(target)
			return nil
		}
		stem := strings.TrimSuffix(filepath.Base(target), extension)
		for index := 1; ; index++ {
			candidate := filepath.Join(filepath.Dir(target), fmt.Sprintf("%s_%s_%d%s", stem, shortHash(image.FileHash), index, extension))
			if !fileExists(candidate) {
				target = candidate
				break
			}
		}
		zap.L().Warn("目标图片文件已存在且内容不同，改为保存到 " + filepath.Base(target))
	}
	if err := os.Rename(image.FullPath, target); err != nil {
		return err
	}
	image.FullPath = target
	image.FileName = filepath.Base(target)
	return nil
}

func detectImageFormat(data []byte) (string, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil && format != "" {
		return strings.ToLower(format), nil
	}
	// DecodeConfig 未覆盖的常见格式：对齐 PIL 可用的 tiff/ico 识别面（至少拿到格式名用于落库扩展名）
	if sniffed := sniffImageFormat(data); sniffed != "" {
		return sniffed, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("无法识别图片格式")
}

func sniffImageFormat(data []byte) string {
	if len(data) >= 4 {
		// ICO: 00 00 01 00
		if data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 {
			return "ico"
		}
		// TIFF little/big endian
		if (data[0] == 'I' && data[1] == 'I' && data[2] == 42 && data[3] == 0) ||
			(data[0] == 'M' && data[1] == 'M' && data[2] == 0 && data[3] == 42) {
			return "tiff"
		}
	}
	return ""
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
