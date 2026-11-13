package webuiapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"
)

var (
	dataTransferImportGuardsMu sync.Mutex
	dataTransferImportGuards   = map[string]struct{}{}
)

// reserveDataTransferImport 以 instanceDir 为粒度对导入全流程做原子占位
// 解压、文件覆盖与待恢复 marker 写入期间必须独占，避免并发任务互写
func reserveDataTransferImport(instanceDir string) (func(), error) {
	key := dataTransferImportGuardKey(instanceDir)
	dataTransferImportGuardsMu.Lock()
	defer dataTransferImportGuardsMu.Unlock()
	if _, exists := dataTransferImportGuards[key]; exists {
		return nil, newTransferError(http.StatusConflict, "已有导入任务正在执行，请稍后重试")
	}
	dataTransferImportGuards[key] = struct{}{}
	var once sync.Once
	release := func() {
		once.Do(func() {
			dataTransferImportGuardsMu.Lock()
			delete(dataTransferImportGuards, key)
			dataTransferImportGuardsMu.Unlock()
		})
	}
	return release, nil
}

func dataTransferImportGuardKey(instanceDir string) string {
	trimmed := strings.TrimSpace(instanceDir)
	if absolute, err := filepath.Abs(trimmed); err == nil {
		return absolute
	}
	return filepath.Clean(trimmed)
}
