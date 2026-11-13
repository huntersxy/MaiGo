package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	envTelemetryURL      = "MAIBOT_TELEMETRY_URL"
	envTelemetryInterval = "MAIBOT_TELEMETRY_INTERVAL_SECONDS"
	envTelemetryUUID     = "MAIBOT_TELEMETRY_CLIENT_UUID"
	defaultHeartbeatSec  = 600
	maxRegisterAttempts  = 4
)

// AdapterSnapshot 遥测允许的接入类型快照
type AdapterSnapshot struct {
	Platform string `json:"platform"`
	Protocol string `json:"protocol"`
}

// BotAccount 机器人账号，用于遥测区分 bot_sent 与 received
type BotAccount struct {
	Platform string
	UserID   string
}

// TelemetryOptions 遥测启动参数
type TelemetryOptions struct {
	// InstanceDir 为 MAIBOT_INSTANCE_DIR；空则使用进程 cwd 下的 .maibot/
	InstanceDir string
	// DB 统计上传读取的数据库；为空时只上报内存计数
	DB *gorm.DB
	// StatsEnabled 对应 MaiBot telemetry.enable；为空视为启用
	StatsEnabled func() bool
	// ModelAssignments 提供 model_task_config 的任务到模型映射
	ModelAssignments func() map[string][]string
	// BotAccounts 提供当前机器人账号；为空时退回 message_traffic 方向统计
	BotAccounts func() []BotAccount
}

// Telemetry 可选远程心跳；URL 为空时为 no-op
type Telemetry struct {
	baseURL     string
	interval    time.Duration
	clientUUID  string
	uuidFromEnv bool
	instanceDir string
	version     string
	adapters    []AdapterSnapshot
	botAccounts func() []BotAccount
	client      *http.Client
	stop        chan struct{}
	done        chan struct{}
	once        sync.Once
	uuidMu      sync.Mutex
	stats       *statsUploader
	started     bool
}

// NewTelemetry 从环境变量读取 MAIBOT_TELEMETRY_URL；未设置则 Enabled 为 false
func NewTelemetry(version string, adapters []AdapterSnapshot, opts TelemetryOptions) *Telemetry {
	base := strings.TrimSpace(os.Getenv(envTelemetryURL))
	if base == "" {
		return &Telemetry{}
	}
	base = strings.TrimRight(base, "/")
	interval := time.Duration(defaultHeartbeatSec) * time.Second
	if raw := strings.TrimSpace(os.Getenv(envTelemetryInterval)); raw != "" {
		if sec, err := strconv.Atoi(raw); err == nil && sec > 0 {
			interval = time.Duration(sec) * time.Second
		}
	}
	t := &Telemetry{
		baseURL:     base,
		interval:    interval,
		instanceDir: strings.TrimSpace(opts.InstanceDir),
		version:     version,
		adapters:    append([]AdapterSnapshot(nil), adapters...),
		botAccounts: opts.BotAccounts,
		client:      &http.Client{Timeout: 8 * time.Second},
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	if envUUID := strings.TrimSpace(os.Getenv(envTelemetryUUID)); envUUID != "" {
		t.clientUUID = envUUID
		t.uuidFromEnv = true
		if err := saveTelemetryClientUUID(t.instanceDir, envUUID); err != nil {
			zap.L().Debug("遥测 Client-UUID 写入实例文件失败", zap.Error(err))
		}
	} else if data, err := readTelemetryLocal(t.instanceDir); err != nil {
		zap.L().Debug("读取实例遥测本地存储失败", zap.Error(err))
	} else if id := strings.TrimSpace(data.MmcUUID); id != "" {
		t.clientUUID = id
	}
	t.stats = newStatsUploader(t, opts)
	return t
}

// Enabled 是否配置了遥测 URL
func (t *Telemetry) Enabled() bool {
	return t != nil && t.baseURL != ""
}

// Start 启动后台心跳与统计上传；未启用时立即返回
func (t *Telemetry) Start(ctx context.Context) {
	if !t.Enabled() {
		return
	}
	t.started = true
	go func() {
		defer close(t.done)
		t.sendOnce(ctx)
		ticker := time.NewTicker(t.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				t.sendOnce(context.Background())
				return
			case <-t.stop:
				t.sendOnce(context.Background())
				return
			case <-ticker.C:
				t.sendOnce(ctx)
			}
		}
	}()
	if t.stats != nil {
		go t.stats.loop(ctx)
	}
	zap.L().Info("远程遥测心跳已启用",
		zap.String("url", t.baseURL),
		zap.Duration("interval", t.interval))
}

// Close 停止心跳与统计上传 goroutine
func (t *Telemetry) Close() {
	if t == nil || !t.Enabled() {
		return
	}
	t.once.Do(func() { close(t.stop) })
	if !t.started {
		return
	}
	<-t.done
	if t.stats != nil {
		t.stats.shutdown()
	}
}

// UploadStatsOnce 立即执行一次统计上传，供测试与手动触发
func (t *Telemetry) UploadStatsOnce(ctx context.Context) error {
	if t == nil || t.stats == nil {
		return nil
	}
	return t.stats.upload(ctx)
}

func (t *Telemetry) sendOnce(ctx context.Context) {
	if t == nil || !t.Enabled() {
		return
	}
	if !t.ensureClientUUID(ctx) {
		return
	}
	payload := t.buildHeartbeatPayload()
	body, err := json.Marshal(payload)
	if err != nil {
		zap.L().Debug("遥测心跳序列化失败", zap.Error(err))
		return
	}
	uuid := t.currentClientUUID()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/stat/client_heartbeat", bytes.NewReader(body))
	if err != nil {
		zap.L().Debug("遥测心跳请求构建失败", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if uuid != "" {
		req.Header.Set("Client-UUID", uuid)
		req.Header.Set("User-Agent", "HeartbeatClient/"+uuid[:min(8, len(uuid))])
	}
	resp, err := t.client.Do(req)
	if err != nil {
		zap.L().Debug("遥测心跳发送失败", zap.Error(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return
	}
	if resp.StatusCode == http.StatusForbidden {
		zap.L().Warn("遥测心跳 403，将清除本地 UUID 并在下次尝试重新注册")
		t.invalidateClientUUID()
		return
	}
	zap.L().Debug("遥测心跳非成功状态", zap.Int("status", resp.StatusCode))
}

func (t *Telemetry) currentClientUUID() string {
	t.uuidMu.Lock()
	defer t.uuidMu.Unlock()
	return t.clientUUID
}

func (t *Telemetry) setClientUUID(uuid string) {
	t.uuidMu.Lock()
	t.clientUUID = strings.TrimSpace(uuid)
	t.uuidMu.Unlock()
}

func (t *Telemetry) ensureClientUUID(ctx context.Context) bool {
	if uuid := t.currentClientUUID(); uuid != "" {
		return true
	}
	if envUUID := strings.TrimSpace(os.Getenv(envTelemetryUUID)); envUUID != "" {
		t.uuidFromEnv = true
		t.setClientUUID(envUUID)
		if err := saveTelemetryClientUUID(t.instanceDir, envUUID); err != nil {
			zap.L().Debug("遥测 Client-UUID 写入实例文件失败", zap.Error(err))
		}
		return true
	}
	if data, err := readTelemetryLocal(t.instanceDir); err == nil {
		if id := strings.TrimSpace(data.MmcUUID); id != "" {
			t.setClientUUID(id)
			return true
		}
	}
	return t.registerClient(ctx)
}

func (t *Telemetry) invalidateClientUUID() {
	if t.uuidFromEnv {
		return
	}
	t.setClientUUID("")
	if err := clearTelemetryClientUUID(t.instanceDir); err != nil {
		zap.L().Debug("清除实例遥测 UUID 失败", zap.Error(err))
	}
}

func (t *Telemetry) registerClient(ctx context.Context) bool {
	deployTime, err := ensureTelemetryDeployTime(t.instanceDir)
	if err != nil {
		zap.L().Warn("遥测缺少 deploy_time，无法注册 Client-UUID", zap.Error(err))
		return false
	}
	body, err := sonic.Marshal(map[string]float64{"deploy_time": deployTime})
	if err != nil {
		zap.L().Debug("遥测注册请求序列化失败", zap.Error(err))
		return false
	}
	url := t.baseURL + "/stat/reg_client"
	for attempt := 1; attempt <= maxRegisterAttempts; attempt++ {
		if attempt > 1 {
			delaySec := int64(1)
			for i := 1; i < attempt; i++ {
				delaySec *= 4
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Duration(delaySec) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			zap.L().Debug("遥测注册请求构建失败", zap.Error(err))
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := t.client.Do(req)
		if err != nil {
			zap.L().Warn("遥测注册 Client-UUID 失败，不影响正常使用", zap.Error(err))
			continue
		}
		ok, clientID := parseRegisterResponse(resp)
		resp.Body.Close()
		if ok && clientID != "" {
			t.setClientUUID(clientID)
			if err := saveTelemetryClientUUID(t.instanceDir, clientID); err != nil {
				zap.L().Debug("遥测 Client-UUID 写入实例文件失败", zap.Error(err))
			}
			zap.L().Info("遥测 Client-UUID 注册成功")
			return true
		}
		if attempt < maxRegisterAttempts {
			zap.L().Info("遥测注册 Client-UUID 失败，将重试", zap.Int("attempt", attempt))
		}
	}
	zap.L().Warn("遥测注册 Client-UUID 失败，跳过本次心跳")
	return false
}

func parseRegisterResponse(resp *http.Response) (bool, string) {
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		zap.L().Warn("遥测注册 Client-UUID 非 200",
			zap.Int("status", resp.StatusCode),
			zap.ByteString("body", raw))
		return false, ""
	}
	var data struct {
		MmcUUID string `json:"mmc_uuid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		zap.L().Warn("遥测注册响应解析失败", zap.Error(err))
		return false, ""
	}
	clientID := strings.TrimSpace(data.MmcUUID)
	if clientID == "" {
		zap.L().Warn("遥测注册响应缺少 mmc_uuid")
		return false, ""
	}
	return true, clientID
}

func (t *Telemetry) buildHeartbeatPayload() map[string]any {
	snap := ProcessSnapshot(t.version)
	inbound, outbound := int64(0), int64(0)
	if c := defaultMessages.Load(); c != nil {
		inbound, outbound = c.Totals()
	}
	adapters := t.adapters
	if adapters == nil {
		adapters = []AdapterSnapshot{}
	}
	return map[string]any{
		"kind":           "heartbeat",
		"backend":        "maibot-go",
		"version":        snap.Version,
		"os_type":        osTypeLabel(),
		"go_version":     runtime.Version(),
		"uptime_seconds": snap.UptimeSec,
		"messages": map[string]int64{
			"inbound":  inbound,
			"outbound": outbound,
		},
		"adapters": adapters,
	}
}

func osTypeLabel() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "darwin":
		return "macOS"
	default:
		return "Unknown"
	}
}
