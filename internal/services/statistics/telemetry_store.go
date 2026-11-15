package stats

import (
	"path/filepath"
	"strings"
	"time"

	"MaiGo/internal/manager/local_store"

	"github.com/bytedance/sonic"
)

// localStoreRelativePath 与 MaiBot 的 data/local_store.json 一致
var localStoreRelativePath = filepath.Join("data", "local_store.json")

const (
	localStoreUUIDKey   = "mmc_uuid"
	localStoreDeployKey = "deploy_time"
	localStoreCursorKey = "telemetry_stats_cursor"
)

// telemetryLocal 遥测在本机记事本里的三个键，字段名与 MaiBot 保持一致
type telemetryLocal struct {
	MmcUUID     string
	DeployTime  float64
	StatsCursor *telemetryStatsCursor
}

func resolveInstanceRoot(instanceDir string) (string, error) {
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		cwd, err := filepath.Abs(".")
		if err != nil {
			return "", err
		}
		return cwd, nil
	}
	return filepath.Clean(root), nil
}

// telemetryLocalPath 遥测与 MaiBot 共用 data/local_store.json，方便两边交叉读取
func telemetryLocalPath(instanceDir string) (string, error) {
	root, err := resolveInstanceRoot(instanceDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, localStoreRelativePath), nil
}

func openTelemetryStore(instanceDir string) (*localstore.Store, error) {
	path, err := telemetryLocalPath(instanceDir)
	if err != nil {
		return nil, err
	}
	return localstore.Open(path)
}

func readTelemetryLocal(instanceDir string) (telemetryLocal, error) {
	store, err := openTelemetryStore(instanceDir)
	if err != nil {
		return telemetryLocal{}, err
	}
	data := telemetryLocal{
		MmcUUID:    stringValue(store.Get(localStoreUUIDKey)),
		DeployTime: floatValue(store.Get(localStoreDeployKey)),
	}
	raw := store.Get(localStoreCursorKey)
	if raw == nil {
		return data, nil
	}
	encoded, err := sonic.Marshal(raw)
	if err != nil {
		return telemetryLocal{}, err
	}
	var cursor telemetryStatsCursor
	if err := sonic.Unmarshal(encoded, &cursor); err != nil {
		return telemetryLocal{}, err
	}
	data.StatsCursor = &cursor
	return data, nil
}

// writeTelemetryLocal 只改遥测自己的键，保留 local_store.json 里的其他内容
func writeTelemetryLocal(instanceDir string, data telemetryLocal) error {
	store, err := openTelemetryStore(instanceDir)
	if err != nil {
		return err
	}
	changes := map[string]any{}
	if strings.TrimSpace(data.MmcUUID) == "" {
		changes[localStoreUUIDKey] = nil
	} else {
		changes[localStoreUUIDKey] = strings.TrimSpace(data.MmcUUID)
	}
	if data.DeployTime > 0 {
		changes[localStoreDeployKey] = data.DeployTime
	}
	if data.StatsCursor != nil {
		changes[localStoreCursorKey] = data.StatsCursor
	}
	if len(changes) == 0 {
		return nil
	}
	return store.Apply(changes)
}

// ensureTelemetryDeployTime 返回 deploy_time；缺失时写入当前时间（对齐 MaiBot StatManager 首次记录）
func ensureTelemetryDeployTime(instanceDir string) (float64, error) {
	data, err := readTelemetryLocal(instanceDir)
	if err != nil {
		return 0, err
	}
	if data.DeployTime > 0 {
		return data.DeployTime, nil
	}
	data.DeployTime = float64(time.Now().UnixNano()) / 1e9
	if err := writeTelemetryLocal(instanceDir, data); err != nil {
		return 0, err
	}
	return data.DeployTime, nil
}

func saveTelemetryClientUUID(instanceDir, uuid string) error {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return nil
	}
	data, err := readTelemetryLocal(instanceDir)
	if err != nil {
		return err
	}
	if data.MmcUUID == uuid {
		if data.DeployTime > 0 {
			return nil
		}
		_, err := ensureTelemetryDeployTime(instanceDir)
		return err
	}
	data.MmcUUID = uuid
	if data.DeployTime <= 0 {
		data.DeployTime = float64(time.Now().UnixNano()) / 1e9
	}
	return writeTelemetryLocal(instanceDir, data)
}

func clearTelemetryClientUUID(instanceDir string) error {
	data, err := readTelemetryLocal(instanceDir)
	if err != nil {
		return err
	}
	if data.MmcUUID == "" {
		return nil
	}
	data.MmcUUID = ""
	return writeTelemetryLocal(instanceDir, data)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}
