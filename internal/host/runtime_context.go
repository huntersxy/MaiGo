package host

// 宿主与 Worker 之间的运行契约，对齐 src/common/runtime_context.py。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// RuntimeContextEnv Worker 读取的运行契约环境变量
	RuntimeContextEnv = "MAIBOT_RUNTIME_CONTEXT"
	// HostControlEndpointEnv Worker 读取的宿主控制通道环境变量
	HostControlEndpointEnv = "MAIBOT_HOST_CONTROL_ENDPOINT"
	// RestartExitCode Worker 以此退出码请求宿主重新拉起自己
	RestartExitCode = 42
	// WorkerShutdownTimeout Worker 自身的关闭预算；宿主多留 15 秒余量
	WorkerShutdownTimeout = 60 * time.Second
)

// 运行阶段
const (
	PhaseStarting  = "starting"
	PhaseMigrating = "migrating"
	PhaseReady     = "ready"
	PhaseStopping  = "stopping"
)

// RuntimeContext 宿主为一次 Worker 运行下发的全部参数
type RuntimeContext struct {
	BotID         string `json:"bot_id"`
	HTTPEndpoint  string `json:"http_endpoint"`
	PublicBaseURL string `json:"public_base_url"`
	StatusPath    string `json:"status_path"`
}

// ToEnv 序列化为环境变量值
func (c RuntimeContext) ToEnv() string {
	encoded, _ := json.Marshal(map[string]string{
		"bot_id":          c.BotID,
		"http_endpoint":   c.HTTPEndpoint,
		"public_base_url": c.PublicBaseURL,
		"status_path":     c.StatusPath,
	})
	return string(encoded)
}

// ParseRuntimeContext 校验宿主下发的运行契约
func ParseRuntimeContext(value string) (RuntimeContext, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return RuntimeContext{}, fmt.Errorf("运行契约必须是 JSON 对象")
	}
	fields := map[string]string{}
	for _, name := range []string{"bot_id", "http_endpoint", "public_base_url", "status_path"} {
		text, ok := payload[name].(string)
		if !ok || text == "" {
			return RuntimeContext{}, fmt.Errorf("运行契约缺少 %s", name)
		}
		fields[name] = text
	}
	if !botIDPattern.MatchString(fields["bot_id"]) {
		return RuntimeContext{}, errors.New("运行契约的 bot_id 无效")
	}
	if !strings.HasPrefix(fields["public_base_url"], "http://") && !strings.HasPrefix(fields["public_base_url"], "https://") {
		return RuntimeContext{}, errors.New("运行契约的 public_base_url 必须是 HTTP(S) 地址")
	}
	return RuntimeContext{
		BotID:         fields["bot_id"],
		HTTPEndpoint:  fields["http_endpoint"],
		PublicBaseURL: strings.TrimRight(fields["public_base_url"], "/"),
		StatusPath:    fields["status_path"],
	}, nil
}

var (
	runtimeContextMu     sync.Mutex
	runtimeContextCached *RuntimeContext
	statusMu             sync.Mutex
)

// CurrentRuntimeContext 读取并缓存当前进程的运行契约；缺失时返回错误而不是猜测参数
func CurrentRuntimeContext() (RuntimeContext, error) {
	runtimeContextMu.Lock()
	defer runtimeContextMu.Unlock()
	if runtimeContextCached != nil {
		return *runtimeContextCached, nil
	}
	value, exists := os.LookupEnv(RuntimeContextEnv)
	if !exists {
		return RuntimeContext{}, errors.New("MaiGo 只能由宿主启动：请执行 maigo run")
	}
	parsed, err := ParseRuntimeContext(value)
	if err != nil {
		return RuntimeContext{}, err
	}
	runtimeContextCached = &parsed
	return parsed, nil
}

// HasRuntimeContext 当前进程是否由宿主启动
func HasRuntimeContext() bool {
	_, exists := os.LookupEnv(RuntimeContextEnv)
	return exists
}

// ReportPhase 原子写入当前阶段供宿主读取；未在宿主下运行时静默跳过
func ReportPhase(phase, detail string) {
	if !HasRuntimeContext() {
		return
	}
	current, err := CurrentRuntimeContext()
	if err != nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"phase": phase, "detail": detail, "pid": os.Getpid(), "at": float64(time.Now().UnixNano()) / 1e9,
	})
	if err != nil {
		return
	}
	statusMu.Lock()
	defer statusMu.Unlock()
	directory := filepath.Dir(current.StatusPath)
	_ = os.MkdirAll(directory, 0o755)
	temporary := filepath.Join(directory, "."+filepath.Base(current.StatusPath)+"."+fmt.Sprint(time.Now().UnixNano())+".tmp")
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return
	}
	if err := os.Rename(temporary, current.StatusPath); err != nil {
		_ = os.Remove(temporary)
	}
}

// WorkerStatus 宿主读取的 Worker 阶段快照
type WorkerStatus struct {
	Phase  string  `json:"phase"`
	Detail string  `json:"detail"`
	PID    int     `json:"pid"`
	At     float64 `json:"at"`
}

// ReadWorkerStatus 读取 Worker 上报的阶段
func ReadWorkerStatus(path string) (WorkerStatus, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return WorkerStatus{}, false
	}
	var status WorkerStatus
	if err := json.Unmarshal(raw, &status); err != nil || status.Phase == "" {
		return WorkerStatus{}, false
	}
	return status, true
}
