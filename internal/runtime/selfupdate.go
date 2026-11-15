package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"MaiGo/internal/common/self_update"
)

// ClearPendingUpdate Runner 启动时丢弃遗留的更新请求，对齐 runtime.run_runner_process 中的 clear_pending_update
func ClearPendingUpdate() {
	_ = selfupdate.ClearPendingUpdate("")
	if err := moveAsidePendingMarker(); err != nil {
		// Runner 不加载 zap 以外的重依赖；标记清理失败不阻断启动
		_, _ = fmt.Fprintf(os.Stderr, "runtime: 清理遗留自更新标记失败: %v\n", err)
	}
}

// ApplyPendingUpdate 在 Worker 以退出码 42 退出后、拉起新 Worker 前调用
//
// 对齐 MaiBot apply_pending_update 的消费语义：先移除 pending 标记再执行，避免失败后重复安装。
// 当前实现 MAIBOT_UPDATE_PENDING_PATH（或 pending_binary.json）指向的新二进制替换。
func ApplyPendingUpdate() (applied bool, err error) {
	staged, err := resolvePendingBinary()
	if err != nil {
		return false, err
	}
	staged = strings.TrimSpace(staged)
	if staged == "" {
		result, applyErr := selfupdate.ApplyPending(context.Background(), "")
		if applyErr != nil {
			return false, fmt.Errorf("runtime: 执行源码自更新失败: %w", applyErr)
		}
		if result != nil {
			staged, err = resolvePendingBinary()
			if err != nil {
				return false, err
			}
			staged = strings.TrimSpace(staged)
		}
	}
	if staged == "" {
		return false, nil
	}

	if err := consumePendingMarker(); err != nil && !os.IsNotExist(err) {
		return false, err
	}

	info, statErr := os.Stat(staged)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, fmt.Errorf("runtime: 待安装二进制不存在: %s", staged)
		}
		return false, statErr
	}
	if info.IsDir() {
		return false, fmt.Errorf("runtime: 待安装路径是目录: %s", staged)
	}

	current, err := os.Executable()
	if err != nil {
		return false, err
	}
	current, err = filepathEvalSymlinks(current)
	if err != nil {
		return false, err
	}

	if samePath(staged, current) {
		_ = os.Remove(staged)
		return false, nil
	}

	if err := replaceExecutable(staged, current); err != nil {
		return false, err
	}
	_ = os.Remove(staged)
	return true, nil
}

func filepathEvalSymlinks(path string) (string, error) {
	return filepathCleanEval(path)
}
