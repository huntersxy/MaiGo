package storage

// A_memorix 旧格式 vectors.npy 迁移
// 对齐 Python 的 migrate_legacy_npy / _migrate_from_npy_unlocked
//
// 与 Python 的差异：Python 用 numpy.load 读取 .npy（可读任意 dtype 与布局）；
// Go 版本只支持 C 序、非 pickle 的 <f2/<f4/<f8/>f2/>f4/>f8 二维数组，
// 其它格式显式返回错误，不做伪造迁移

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
)

// MigrateLegacyNpy 把旧版 vectors.npy 迁移为 vNext 成对文件
// dataDir 为空时使用初始化时绑定的目录，返回结构与 Python 一致
func (s *VectorStore) MigrateLegacyNpy(dataDir string) (map[string]any, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return nil, err
	}
	targetDir := dataDir
	if targetDir == "" {
		targetDir = s.dataDir
	}
	if targetDir == "" {
		return nil, fmt.Errorf("No data_dir")
	}
	npyPath := filepath.Join(targetDir, "vectors.npy")
	indexPath := filepath.Join(targetDir, "vectors.index")
	binPath := filepath.Join(targetDir, "vectors.bin")
	idsBinPath := filepath.Join(targetDir, "vectors_ids.bin")
	metaPath := filepath.Join(targetDir, "vectors_metadata.json")

	if _, exists, err := statSize(npyPath); err != nil {
		return nil, err
	} else if !exists {
		return map[string]any{"migrated": false, "reason": "npy_missing"}, nil
	}
	if _, exists, err := statSize(metaPath); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("legacy vectors.npy migration requires vectors_metadata.json")
	}
	binExists, err := fileExists(binPath)
	if err != nil {
		return nil, err
	}
	idsExists, err := fileExists(idsBinPath)
	if err != nil {
		return nil, err
	}
	if binExists && idsExists {
		return map[string]any{"migrated": false, "reason": "bin_exists"}, nil
	}

	// 重置内存状态，避免继续向旧运行时缓冲区追加数据
	s.knownHashes = map[string]struct{}{}
	s.deletedIDs = map[int64]struct{}{}
	s.invalidateIDMap()
	s.invalidateIDOffsets()
	s.writeBufferVecs = nil
	s.writeBufferIDs = nil
	s.entries = map[int64][]float32{}
	s.entryOrder = nil
	s.trained = false
	s.binCount = 0

	if err := s.migrateFromNpyLocked(npyPath, indexPath, targetDir); err != nil {
		return nil, err
	}
	if err := s.saveLocked(targetDir, nil); err != nil {
		return nil, err
	}
	return map[string]any{"migrated": true, "reason": "ok"}, nil
}

// migrateFromNpyLocked 按块读取 npy 并写入成对文件，对齐 _migrate_from_npy_unlocked
func (s *VectorStore) migrateFromNpyLocked(npyPath string, indexPath string, targetDir string) error {
	array, err := readNpyFile(npyPath)
	if err != nil {
		return err
	}
	values, err := npyFloat32(array)
	if err != nil {
		return err
	}
	if len(array.Shape) != 2 {
		return fmt.Errorf("vectors must have shape (N, D), got %s", pyShapeRepr(array.Shape))
	}
	rows := array.Shape[0]
	width := array.Shape[1]
	if rows*width != len(values) {
		return fmt.Errorf("npy 数据长度与 shape 不一致")
	}

	metaPath := filepath.Join(targetDir, "vectors_metadata.json")
	oldIDs := []string{}
	if _, exists, err := statSize(metaPath); err != nil {
		return err
	} else if exists {
		meta, err := readJSONObject(metaPath)
		if err != nil {
			return err
		}
		if raw, ok := meta["ids"]; ok && raw != nil {
			parsed, parseErr := jsonStringSlice(raw)
			if parseErr != nil {
				return fmt.Errorf("Vector IDs must be non-empty strings")
			}
			oldIDs = parsed
		}
	}
	if rows != len(oldIDs) {
		zap.L().Error(
			"迁移中止：npy 行数与元数据 ids 数量不一致",
			zap.Int("array_rows", rows),
			zap.Int("id_count", len(oldIDs)),
		)
		return nil
	}

	zap.L().Info("开始迁移旧版向量", zap.Int("count", rows))
	const chunk = 1000
	for start := 0; start < rows; start += chunk {
		end := start + chunk
		if end > rows {
			end = rows
		}
		vectors := make([][]float32, 0, end-start)
		for row := start; row < end; row++ {
			vector := append([]float32(nil), values[row*width:(row+1)*width]...)
			vectors = append(vectors, vector)
		}
		if _, err := s.addLocked(vectors, oldIDs[start:end]); err != nil {
			return err
		}
	}
	if !s.trained {
		if err := s.forceTrainSmallDataLocked(); err != nil {
			return err
		}
	}
	if err := os.Rename(npyPath, npyPath+".bak"); err != nil {
		return err
	}
	if exists, err := fileExists(indexPath); err != nil {
		return err
	} else if exists {
		if err := os.Rename(indexPath, indexPath+".bak"); err != nil {
			return err
		}
	}
	zap.L().Info("旧版向量迁移完成")
	return nil
}

// fileExists 判断普通文件是否存在
func fileExists(path string) (bool, error) {
	_, exists, err := statSize(path)
	return exists, err
}

// pyShapeRepr 近似 Python 的 shape 文本，用于对齐错误文案
func pyShapeRepr(shape []int) string {
	if len(shape) == 0 {
		return "()"
	}
	parts := ""
	for index, dimension := range shape {
		if index != 0 {
			parts += ", "
		}
		parts += fmt.Sprintf("%d", dimension)
	}
	if len(shape) == 1 {
		return "(" + parts + ",)"
	}
	return "(" + parts + ")"
}
