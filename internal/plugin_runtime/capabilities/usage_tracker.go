package capabilities

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// pluginUsageFileRecord 磁盘记录，字段对齐 Python {"version":1,"usage_seconds":{...}}
type pluginUsageFileRecord struct {
	Version int                `json:"version"`
	Usage   map[string]float64 `json:"usage_seconds"`
}

// PluginUsageTracker 按实际运行区间累计插件使用时长；持久化文件不保存跨进程计时起点
type PluginUsageTracker struct {
	path        string
	clock       func() float64
	mu          sync.Mutex
	totals      map[string]float64
	activeSince map[string]float64
}

// NewPluginUsageTracker 创建使用时长记录器
func NewPluginUsageTracker(path string) *PluginUsageTracker {
	return &PluginUsageTracker{
		path:        path,
		clock:       func() float64 { return float64(time.Now().UnixNano()) / float64(time.Second) },
		totals:      map[string]float64{},
		activeSince: map[string]float64{},
	}
}

// SetClock 注入时钟（测试用）
func (t *PluginUsageTracker) SetClock(clock func() float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if clock != nil {
		t.clock = clock
	}
}

// quarantineUsageFile 保留损坏记录原文件，并把正式路径释放给下一次写入
func (t *PluginUsageTracker) quarantineUsageFile(cause error) error {
	backup := t.path + ".corrupt-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.Rename(t.path, backup); err != nil {
		return err
	}
	t.totals = map[string]float64{}
	t.activeSince = map[string]float64{}
	zap.L().Warn("插件累计使用时长记录损坏，已保留原文件并重新计时",
		zap.String("backup", backup), zap.Error(cause))
	return nil
}

// Load 从磁盘读取累计时长；损坏记录隔离后重新计时，不阻止启动
func (t *PluginUsageTracker) Load() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	raw, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			t.totals = map[string]float64{}
			t.activeSince = map[string]float64{}
			return nil
		}
		return err
	}
	var record pluginUsageFileRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return t.quarantineUsageFile(err)
	}
	if record.Version != 1 || record.Usage == nil {
		return t.quarantineUsageFile(fmt.Errorf("插件使用时长记录格式错误: %s", t.path))
	}
	totals := make(map[string]float64, len(record.Usage))
	for pluginID, seconds := range record.Usage {
		if strings.TrimSpace(pluginID) == "" || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			return t.quarantineUsageFile(fmt.Errorf("插件 %q 的累计使用时长无效: %s", pluginID, t.path))
		}
		totals[pluginID] = seconds
	}
	t.totals = totals
	t.activeSince = map[string]float64{}
	return nil
}

// Reconcile 只结算离开运行集合的插件；重复通知不会重置仍在运行的起点
func (t *PluginUsageTracker) Reconcile(activePluginIDs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	active := make(map[string]struct{}, len(activePluginIDs))
	for _, pluginID := range activePluginIDs {
		active[pluginID] = struct{}{}
	}
	for pluginID, startedAt := range t.activeSince {
		if _, stillActive := active[pluginID]; !stillActive {
			t.totals[pluginID] += now - startedAt
			delete(t.activeSince, pluginID)
		}
	}
	for pluginID := range active {
		if _, started := t.activeSince[pluginID]; !started {
			t.activeSince[pluginID] = now
		}
	}
}

// Snapshot 返回含当前运行区间的累计时长
func (t *PluginUsageTracker) Snapshot() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	totals := make(map[string]float64, len(t.totals)+len(t.activeSince))
	for pluginID, seconds := range t.totals {
		totals[pluginID] = seconds
	}
	for pluginID, startedAt := range t.activeSince {
		totals[pluginID] += now - startedAt
	}
	return totals
}

// Save 原子写入当前快照
func (t *PluginUsageTracker) Save() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	totals := make(map[string]float64, len(t.totals)+len(t.activeSince))
	for pluginID, seconds := range t.totals {
		totals[pluginID] = seconds
	}
	now := t.clock()
	for pluginID, startedAt := range t.activeSince {
		totals[pluginID] += now - startedAt
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(pluginUsageFileRecord{Version: 1, Usage: totals})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(t.path), filepath.Base(t.path)+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, t.path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

// DefaultPluginUsageCheckpointInterval 周期保存间隔，对齐 integration._checkpoint_plugin_usage 的 60 秒
const DefaultPluginUsageCheckpointInterval = 60 * time.Second

// DefaultPluginUsagePath 返回实例目录下的插件使用时长文件，路径对齐 Python get_instance_root()
func DefaultPluginUsagePath() string {
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		root, _ = os.Getwd()
	}
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Join(root, "data", "plugin_runtime", "plugin_usage.json")
}

// Path 返回使用时长记录的落盘路径
//
// 父包在保存失败时要把路径写进告警日志；字段保持私有不对外暴露可写，
// 只提供只读访问器。
func (t *PluginUsageTracker) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}
