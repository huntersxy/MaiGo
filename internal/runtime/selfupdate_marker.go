package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// ErrRestartRequiresWorker 非 Worker 子进程时不能通过退出码 42 请求 Runner 重启
var ErrRestartRequiresWorker = errors.New("runtime: 仅 Worker 子进程可请求 Runner 重启（需 MAIBOT_RUNTIME_MODE=runner）")

type pendingBinaryRecord struct {
	BinaryPath  string  `json:"binary_path"`
	RequestedAt float64 `json:"requested_at"`
}

// SetPendingUpdate 写入自更新标记，供 ApplyPendingUpdate 在下次 42 重启时消费
func SetPendingUpdate(binaryPath string) error {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return errors.New("runtime: 待安装二进制路径为空")
	}
	marker, ok := pendingMarkerPath()
	if !ok {
		return errors.New("runtime: 无法解析自更新标记路径（请设置 MAIBOT_INSTANCE_DIR）")
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return err
	}
	payload, err := sonic.ConfigStd.Marshal(pendingBinaryRecord{
		BinaryPath:  binaryPath,
		RequestedAt: float64(time.Now().Unix()),
	})
	if err != nil {
		return err
	}
	tmp := marker + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, marker)
}

func readPendingBinaryFromMarker() (string, error) {
	marker, ok := pendingMarkerPath()
	if !ok {
		return "", nil
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var rec pendingBinaryRecord
	if err := sonic.ConfigStd.Unmarshal(raw, &rec); err != nil {
		return "", err
	}
	return strings.TrimSpace(rec.BinaryPath), nil
}

func resolvePendingBinary() (string, error) {
	fromMarker, err := readPendingBinaryFromMarker()
	if err != nil {
		return "", err
	}
	if fromMarker != "" {
		return fromMarker, nil
	}
	return strings.TrimSpace(os.Getenv(EnvUpdatePendingBinary)), nil
}

func consumePendingMarker() error {
	marker, ok := pendingMarkerPath()
	if !ok {
		return nil
	}
	return os.Remove(marker)
}

func moveAsidePendingMarker() error {
	marker, ok := pendingMarkerPath()
	if !ok {
		return nil
	}
	if _, err := os.Stat(marker); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	aside := marker + ".discarded." + time.Now().Format("20060102-150405")
	return os.Rename(marker, aside)
}
