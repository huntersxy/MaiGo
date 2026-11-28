package voicesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	memory "MaiGo/internal/common/database"
	"MaiGo/internal/common/utils"
)

const voicesRelativeDir = "data/voices"

// StoreVoice 按内容哈希落盘语音原件并写 binary_data 索引
func StoreVoice(ctx context.Context, db *gorm.DB, instanceRoot string, data []byte) (string, error) {
	if db == nil {
		return "", errors.New("数据库未初始化")
	}
	var digest string
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var storeErr error
		digest, storeErr = storeVoiceTx(tx, instanceRoot, data)
		return storeErr
	})
	return digest, err
}

// StoreVoiceTx 在已有事务内保存语音原件，与引用消息同一事务提交
func StoreVoiceTx(tx *gorm.DB, instanceRoot string, data []byte) (string, error) {
	return storeVoiceTx(tx, instanceRoot, data)
}

func storeVoiceTx(tx *gorm.DB, instanceRoot string, data []byte) (string, error) {
	if tx == nil {
		return "", errors.New("数据库事务未初始化")
	}
	if len(data) == 0 {
		return "", errors.New("语音内容为空")
	}
	root := strings.TrimSpace(instanceRoot)
	if root == "" {
		root = memory.InstanceRoot()
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	var existing memory.BinaryData
	err := tx.Where("data_hash = ?", digest).Limit(1).Take(&existing).Error
	if err == nil {
		if path, perr := ResolveVoicePath(root, existing.FullPath); perr == nil {
			if info, serr := os.Stat(path); serr == nil && !info.IsDir() {
				return digest, nil
			}
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("查询语音索引失败: %w", err)
	}

	extension, _ := utils.DetectAudioFormat(data)
	if extension == "" {
		extension = "bin"
	}
	relative := filepath.ToSlash(filepath.Join(voicesRelativeDir, digest+"."+extension))
	abs := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("创建语音目录失败: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			tmp, err := os.CreateTemp(filepath.Dir(abs), "voice-*")
			if err != nil {
				return "", fmt.Errorf("暂存语音失败: %w", err)
			}
			tmpPath := tmp.Name()
			if _, werr := tmp.Write(data); werr != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				return "", fmt.Errorf("写入语音失败: %w", werr)
			}
			if cerr := tmp.Close(); cerr != nil {
				_ = os.Remove(tmpPath)
				return "", fmt.Errorf("关闭语音暂存文件失败: %w", cerr)
			}
			if rerr := os.Rename(tmpPath, abs); rerr != nil {
				_ = os.Remove(tmpPath)
				return "", fmt.Errorf("提交语音文件失败: %w", rerr)
			}
		} else {
			return "", fmt.Errorf("检查语音文件失败: %w", err)
		}
	}

	record := memory.BinaryData{DataHash: digest, FullPath: relative}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "data_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"full_path"}),
	}).Create(&record).Error; err != nil {
		return "", fmt.Errorf("写入语音索引失败: %w", err)
	}
	return digest, nil
}

// ReadVoice 读取指定哈希对应的语音原件
func ReadVoice(instanceRoot, dataHash string) ([]byte, error) {
	path, err := VoicePathByHash(instanceRoot, dataHash)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.TrimSpace(dataHash) {
		return nil, fmt.Errorf("语音文件内容与记录的哈希不一致: %s", path)
	}
	return data, nil
}

// VoicePathByHash 按 binary_data 索引或 voices 目录解析语音绝对路径
func VoicePathByHash(instanceRoot, dataHash string) (string, error) {
	root := strings.TrimSpace(instanceRoot)
	if root == "" {
		root = memory.InstanceRoot()
	}
	normalized := strings.TrimSpace(dataHash)
	if normalized == "" {
		return "", errors.New("语音 hash 不能为空")
	}
	dir := filepath.Join(root, filepath.FromSlash(voicesRelativeDir))
	matches, err := filepath.Glob(filepath.Join(dir, normalized+".*"))
	if err != nil {
		return "", err
	}
	for _, match := range matches {
		if info, serr := os.Stat(match); serr == nil && !info.IsDir() {
			return match, nil
		}
	}
	return "", fmt.Errorf("未找到语音原件: %s", normalized)
}

// ResolveVoicePath 将存储相对路径解析为实例内绝对路径
func ResolveVoicePath(instanceRoot, storedPath string) (string, error) {
	root := strings.TrimSpace(instanceRoot)
	if root == "" {
		root = memory.InstanceRoot()
	}
	raw := strings.TrimSpace(storedPath)
	if raw == "" {
		return "", errors.New("语音路径不能为空")
	}
	target := raw
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, filepath.FromSlash(raw))
	}
	target = filepath.Clean(target)
	rootClean := filepath.Clean(root)
	rel, err := filepath.Rel(rootClean, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("语音路径不在实例目录内: %s", raw)
	}
	return target, nil
}