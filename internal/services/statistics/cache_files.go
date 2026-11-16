package stats

// 统计缓存文件，对齐 services/statistics_cache_files.py
//
// 统计缓存体积大且每轮更新，单独放在实例目录的 data/statistics/ 下；
// 写入先落临时文件再原子替换，读到的总是一份完整文件。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

const statisticsCacheRelativeDir = "data/statistics"

const (
	dashboardCacheFilename = "dashboard_cache.json"
	detailedCacheFilename  = "detailed_snapshot.json"
)

func statisticsCachePath(instanceDir, filename string) string {
	if strings.TrimSpace(instanceDir) == "" {
		instanceDir = "."
	}
	return filepath.Join(instanceDir, filepath.FromSlash(statisticsCacheRelativeDir), filename)
}

func loadStatisticsCacheFile(instanceDir, filename string) (any, bool) {
	path := statisticsCachePath(instanceDir, filename)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logStatisticsCacheFailure("读取统计缓存失败", path, err)
		}
		return nil, false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		logStatisticsCacheFailure("解析统计缓存失败", path, err)
		return nil, false
	}
	return value, true
}

func saveStatisticsCacheFile(instanceDir, filename string, value any) error {
	raw, err := sonic.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomicFile(statisticsCachePath(instanceDir, filename), raw, 0o644)
}

func logStatisticsCacheFailure(message, path string, err error) {
	zap.L().Warn(message, zap.String("path", path), zap.Error(err))
}
