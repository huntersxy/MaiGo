package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvUpdatePendingBinary 指向待安装的新可执行文件；Runner 在 Worker 退出码 42 后读取并替换当前二进制
	EnvUpdatePendingBinary = "MAIBOT_UPDATE_PENDING_PATH"
	// EnvRunnerSupervised 外层 Runner 守护内层 Runner 时设置，对齐 Python MAIBOT_RUNNER_SUPERVISED
	EnvRunnerSupervised = "MAIBOT_RUNNER_SUPERVISED"
)

const (
	pendingMarkerDir  = "data/self_update"
	pendingMarkerName = "pending_binary.json"
)

// IsWorkerProcess 当前进程是否为 Runner 拉起的 Worker 子进程
func IsWorkerProcess() bool {
	return strings.TrimSpace(os.Getenv(EnvWorkerProcess)) == "1"
}

func pendingMarkerPath() (string, bool) {
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false
	}
	return filepath.Join(root, pendingMarkerDir, pendingMarkerName), true
}
