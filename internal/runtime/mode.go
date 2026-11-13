package runtime

import (
	"os"
	"strings"
)

const (
	// EnvRuntimeMode 显式指定 runner 或 worker；未设置时默认单进程 worker
	EnvRuntimeMode = "MAIBOT_RUNTIME_MODE"
	// EnvWorkerProcess Runner 拉起子进程时设置，对齐 Python MAIBOT_WORKER_PROCESS
	EnvWorkerProcess = "MAIBOT_WORKER_PROCESS"
)

const (
	modeRunner = "runner"
	modeWorker = "worker"
)

// Mode 进程角色
type Mode string

const (
	ModeRunner Mode = modeRunner
	ModeWorker Mode = modeWorker
)

// DetectMode 根据环境变量判断当前进程应扮演的角色
func DetectMode() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvRuntimeMode))) {
	case modeRunner:
		return ModeRunner
	case modeWorker:
		return ModeWorker
	default:
		return ModeWorker
	}
}

// IsRunner 是否以 Runner 监督模式运行
func IsRunner() bool {
	return DetectMode() == ModeRunner
}
