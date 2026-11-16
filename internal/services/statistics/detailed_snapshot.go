package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// DetailedSnapshotPath 返回详细统计快照的持久化路径
func DetailedSnapshotPath(instanceDir string) string {
	return statisticsCachePath(instanceDir, detailedCacheFilename)
}

// LoadDetailedStatisticsSnapshot 从实例目录读取最近一次报告发布的快照
func LoadDetailedStatisticsSnapshot(instanceDir string) (DetailedStatisticsData, bool) {
	path := DetailedSnapshotPath(instanceDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			zap.L().Warn("读取详细统计快照失败", zap.String("path", path), zap.Error(err))
		}
		return DetailedStatisticsData{}, false
	}
	var snapshot DetailedStatisticsData
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		zap.L().Warn("解析详细统计快照失败", zap.String("path", path), zap.Error(err))
		return DetailedStatisticsData{}, false
	}
	if snapshot.Periods == nil {
		snapshot.Periods = []DetailedStatisticsPeriod{}
	}
	if snapshot.Trends == nil {
		snapshot.Trends = map[string]DetailedStatisticsTrendData{}
	}
	if snapshot.Metrics == nil {
		snapshot.Metrics = map[string]DetailedStatisticsMetricsData{}
	}
	return snapshot, true
}

// StoreDetailedStatisticsSnapshot 原子写入详细统计快照
func StoreDetailedStatisticsSnapshot(instanceDir string, snapshot DetailedStatisticsData) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("序列化详细统计快照失败: %w", err)
	}
	if err := writeAtomicFile(DetailedSnapshotPath(instanceDir), raw, 0o644); err != nil {
		return fmt.Errorf("写入详细统计快照失败: %w", err)
	}
	return nil
}

// writeAtomicFile 先写同目录临时文件再重命名，避免读到半截文件
func writeAtomicFile(path string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("写入路径为空")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, perm); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// cleanupStaleDetailedTemps 清理异常退出遗留的原子写临时文件
func cleanupStaleDetailedTemps(dir string, now time.Time, maxAge time.Duration) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".") || !strings.Contains(name, ".tmp-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}
