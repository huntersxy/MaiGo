package host

// 宿主退出后的更新阶段：与 Python launcher/updater 相同，先应用待执行更新，再以新二进制恢复运行。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"MaiGo/internal/common/self_update"
)

func applyHostPendingUpdate(workspace Workspace, releaseLock func()) error {
	root := maintenanceRoot()
	if _, err := os.Stat(selfupdate.PendingPath(root)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	result, err := selfupdate.ApplyPending(ctx, root)
	if err != nil {
		if result != nil && result.RolledBack {
			fmt.Fprintf(os.Stderr, "更新失败，已回滚：%s\n", err)
			return nil
		}
		return fmt.Errorf("更新失败：%w", err)
	}
	if result == nil {
		return nil
	}
	markerPath := filepath.Join(selfupdate.StateDir(root), "pending_binary.json")
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		return nil
	}
	var marker struct {
		BinaryPath string `json:"binary_path"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil || marker.BinaryPath == "" {
		return nil
	}
	info, err := os.Stat(marker.BinaryPath)
	if err != nil || info.IsDir() {
		return nil
	}
	_ = os.Remove(markerPath)
	if releaseLock != nil {
		releaseLock()
	}
	command := exec.Command(marker.BinaryPath, os.Args[1:]...)
	if working, err := os.Getwd(); err == nil {
		command.Dir = working
	}
	command.Env = os.Environ()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动更新后的宿主失败：%w", err)
	}
	fmt.Printf("宿主已更新，新进程 PID %d 正在启动\n", command.Process.Pid)
	return nil
}

// updatePending 判断是否有等待执行的更新请求
func updatePending() bool {
	_, err := os.Stat(selfupdate.PendingPath(maintenanceRoot()))
	return err == nil
}
